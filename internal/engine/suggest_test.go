package engine

import (
	"context"
	"testing"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/rdap"
)

func domainsOf(candidates []candidate) []string {
	domains := make([]string, len(candidates))
	for i, c := range candidates {
		domains[i] = c.domain
	}
	return domains
}

func TestExpand(t *testing.T) {
	got := domainsOf(expand(SuggestRequest{
		Names:     []string{"Data Lens"},
		TLDs:      []string{"com", ".COM", "io"},
		Prefixes:  []string{"get"},
		Suffixes:  []string{"hq"},
		Hyphenate: true,
	}))

	want := []string{
		"datalens.com", "getdatalens.com", "get-datalens.com", "datalenshq.com", "datalens-hq.com",
		"datalens.io", "getdatalens.io", "get-datalens.io", "datalenshq.io", "datalens-hq.io",
	}
	if len(got) != len(want) {
		t.Fatalf("expand produced %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExpandNormalizesIDNAndSkipsJunk(t *testing.T) {
	got := domainsOf(expand(SuggestRequest{
		Names: []string{"пример", "", "  ", "--"},
		TLDs:  []string{"рф", "not a tld"},
	}))

	want := []string{"xn--e1afmkfd.xn--p1ai"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("expand = %v, want %v", got, want)
	}
}

func TestExpandTLDHacks(t *testing.T) {
	got := domainsOf(expand(SuggestRequest{
		Names:    []string{"analytics"},
		TLDs:     []string{"ics", "com"},
		TLDHacks: true,
	}))

	if len(got) != 3 {
		t.Fatalf("expand = %v, want 3 candidates", got)
	}
	if got[2] != "analyt.ics" {
		t.Errorf("TLD hack = %q, want analyt.ics", got[2])
	}
}

func TestSuggestRanksAndFilters(t *testing.T) {
	rdapProvider := &fakeProvider{
		name: rdap.ProviderName,
		responses: map[string]core.ProviderResult{
			"datalens.com":    {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered},
			"getdatalens.com": {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
			"datalenshq.com":  {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
			"datalens.io":     {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
			"getdatalens.io":  {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered},
			"datalenshq.io":   {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider}, nil)

	got := eng.Suggest(context.Background(), SuggestRequest{
		Names:         []string{"datalens"},
		TLDs:          []string{"com", "io"},
		Prefixes:      []string{"get"},
		Suffixes:      []string{"hq"},
		OnlyAvailable: true,
		Limit:         3,
	})

	if got.Generated != 6 || got.Checked != 6 || got.Truncated {
		t.Errorf("Generated/Checked/Truncated = %d/%d/%v, want 6/6/false", got.Generated, got.Checked, got.Truncated)
	}
	// .com outranks .io, and inside a TLD the shorter candidate wins.
	want := []string{"datalenshq.com", "getdatalens.com", "datalens.io"}
	if len(got.Results) != len(want) {
		t.Fatalf("Results = %d, want %d", len(got.Results), len(want))
	}
	for i, domain := range want {
		if got.Results[i].NormalizedDomain != domain {
			t.Errorf("Results[%d] = %q, want %q", i, got.Results[i].NormalizedDomain, domain)
		}
	}
}

func TestSuggestTruncatesCandidates(t *testing.T) {
	rdapProvider := &fakeProvider{
		name: rdap.ProviderName,
		responses: map[string]core.ProviderResult{
			"datalens.com": {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
			"datalens.io":  {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider}, nil)

	got := eng.Suggest(context.Background(), SuggestRequest{
		Names:         []string{"datalens"},
		TLDs:          []string{"com", "io", "dev"},
		MaxCandidates: 2,
		OnlyAvailable: true,
	})

	if !got.Truncated || got.Checked != 2 || got.Generated != 3 {
		t.Errorf("Generated/Checked/Truncated = %d/%d/%v, want 3/2/true", got.Generated, got.Checked, got.Truncated)
	}
}

func TestSuggestKeepsTakenWhenAsked(t *testing.T) {
	rdapProvider := &fakeProvider{
		name: rdap.ProviderName,
		responses: map[string]core.ProviderResult{
			"datalens.com": {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider}, nil)

	got := eng.Suggest(context.Background(), SuggestRequest{
		Names: []string{"datalens"},
		TLDs:  []string{"com"},
	})
	if len(got.Results) != 1 || got.Results[0].Availability != core.AvailabilityUnavailable {
		t.Errorf("Results = %+v, want the taken candidate included", got.Results)
	}
}
