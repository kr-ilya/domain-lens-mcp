package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/dnsprobe"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/rdap"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/whois"
)

// fakeProvider answers from a fixed table so engine behaviour can be tested
// without touching the network.
type fakeProvider struct {
	name      string
	supports  func(domainname.Name) bool
	responses map[string]core.ProviderResult
	err       error
	calls     atomic.Int32
	delay     time.Duration
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Supports(_ context.Context, name domainname.Name) bool {
	if f.supports == nil {
		return true
	}
	return f.supports(name)
}

func (f *fakeProvider) Check(ctx context.Context, name domainname.Name) (core.ProviderResult, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return core.ProviderResult{}, ctx.Err()
		}
	}
	if f.err != nil {
		return core.ProviderResult{}, f.err
	}
	response, ok := f.responses[name.ASCII]
	if !ok {
		return core.ProviderResult{}, errors.New("no fixture for " + name.ASCII)
	}
	response.Provider = f.name
	return response, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestEngine(primary, corroborating []provider.Provider) *Engine {
	return New(primary, corroborating, Options{
		MaxConcurrency: 4,
		RequestTimeout: time.Second,
		CacheTTL:       CacheTTL{Available: time.Minute, Unavailable: time.Minute, Unknown: time.Minute},
	}, testLogger())
}

func TestCheckUsesRDAPAsAuthority(t *testing.T) {
	rdapProvider := &fakeProvider{
		name: rdap.ProviderName,
		responses: map[string]core.ProviderResult{
			"taken.com": {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered},
			"free.com":  {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider}, nil)

	if got := eng.Check(context.Background(), "taken.com", false); got.Availability != core.AvailabilityUnavailable {
		t.Errorf("taken.com: Availability = %q, want unavailable", got.Availability)
	}
	got := eng.Check(context.Background(), "free.com", false)
	if got.Availability != core.AvailabilityAvailable || got.Confidence != core.ConfidenceHigh {
		t.Errorf("free.com: Availability = %q / %q, want available / high", got.Availability, got.Confidence)
	}
	if got.NormalizedDomain != "free.com" || got.TLD != "com" {
		t.Errorf("normalization lost: %+v", got)
	}
}

func TestCheckFallsBackToWHOISWhenRDAPHasNoService(t *testing.T) {
	rdapProvider := &fakeProvider{
		name:     rdap.ProviderName,
		supports: func(name domainname.Name) bool { return name.TLD != "de" },
	}
	whoisProvider := &fakeProvider{
		name: whois.ProviderName,
		responses: map[string]core.ProviderResult{
			"free.de": {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider, whoisProvider}, nil)

	got := eng.Check(context.Background(), "free.de", false)
	if got.Availability != core.AvailabilityAvailable {
		t.Errorf("Availability = %q, want available", got.Availability)
	}
	// WHOIS text parsing is heuristic, so it must not claim high confidence.
	if got.Confidence != core.ConfidenceMedium {
		t.Errorf("Confidence = %q, want medium", got.Confidence)
	}
	if rdapProvider.calls.Load() != 0 {
		t.Errorf("RDAP was queried for a TLD it does not support")
	}
}

func TestCheckFallsBackWhenRDAPFails(t *testing.T) {
	rdapProvider := &fakeProvider{name: rdap.ProviderName, err: errors.New("registry down")}
	whoisProvider := &fakeProvider{
		name: whois.ProviderName,
		responses: map[string]core.ProviderResult{
			"taken.com": {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider, whoisProvider}, nil)

	got := eng.Check(context.Background(), "taken.com", false)
	if got.Availability != core.AvailabilityUnavailable {
		t.Errorf("Availability = %q, want unavailable", got.Availability)
	}
	if whoisProvider.calls.Load() != 1 {
		t.Errorf("WHOIS calls = %d, want 1", whoisProvider.calls.Load())
	}
}

func TestCheckReturnsUnknownWhenEverySourceFails(t *testing.T) {
	failing := &fakeProvider{name: rdap.ProviderName, err: errors.New("registry down")}
	eng := newTestEngine([]provider.Provider{failing}, nil)

	got := eng.Check(context.Background(), "mystery.com", false)
	if got.Availability != core.AvailabilityUnknown {
		t.Fatalf("Availability = %q, want unknown — a failed lookup must never look free", got.Availability)
	}
	if len(got.Sources) != 1 || got.Sources[0].Error == "" {
		t.Errorf("the failure should be reported in sources: %+v", got.Sources)
	}
}

func TestCheckInvalidDomain(t *testing.T) {
	eng := newTestEngine([]provider.Provider{&fakeProvider{name: rdap.ProviderName}}, nil)

	got := eng.Check(context.Background(), "not a domain", false)
	if got.Availability != core.AvailabilityUnknown || got.Error == "" {
		t.Errorf("invalid input should yield unknown with an error, got %+v", got)
	}
}

func TestCheckCachesAndDeduplicates(t *testing.T) {
	rdapProvider := &fakeProvider{
		name: rdap.ProviderName,
		responses: map[string]core.ProviderResult{
			"free.com": {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider}, nil)

	first := eng.Check(context.Background(), "free.com", false)
	if first.Cached {
		t.Errorf("first check should not be served from cache")
	}
	second := eng.Check(context.Background(), "FREE.com.", false)
	if !second.Cached {
		t.Errorf("a normalized repeat should hit the cache")
	}
	if rdapProvider.calls.Load() != 1 {
		t.Errorf("provider calls = %d, want 1", rdapProvider.calls.Load())
	}
}

func TestCheckManyKeepsOrderAndIsolatesFailures(t *testing.T) {
	rdapProvider := &fakeProvider{
		name: rdap.ProviderName,
		responses: map[string]core.ProviderResult{
			"free.com":  {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
			"taken.com": {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider}, nil)

	inputs := []string{"free.com", "not a domain", "taken.com", "nofixture.com"}
	results := eng.CheckMany(context.Background(), inputs, false)

	if len(results) != len(inputs) {
		t.Fatalf("results = %d, want %d", len(results), len(inputs))
	}
	for i, input := range inputs {
		if results[i].Domain != input {
			t.Errorf("results[%d].Domain = %q, want %q", i, results[i].Domain, input)
		}
	}
	if results[0].Availability != core.AvailabilityAvailable {
		t.Errorf("free.com = %q", results[0].Availability)
	}
	if results[2].Availability != core.AvailabilityUnavailable {
		t.Errorf("taken.com = %q", results[2].Availability)
	}
	if results[3].Availability != core.AvailabilityUnknown {
		t.Errorf("a per-domain failure should be unknown, got %q", results[3].Availability)
	}
}

func TestCheckIncludeRaw(t *testing.T) {
	rdapProvider := &fakeProvider{
		name: rdap.ProviderName,
		responses: map[string]core.ProviderResult{
			"taken.com": {RegistrationStatus: core.RegistrationRegistered, Status: core.StatusRegistered, Raw: map[string]any{"objectClassName": "domain"}},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider}, nil)

	if got := eng.Check(context.Background(), "taken.com", false); got.Sources[0].Raw != nil {
		t.Errorf("raw payload should be stripped by default")
	}
	if got := eng.Check(context.Background(), "taken.com", true); got.Sources[0].Raw == nil {
		t.Errorf("raw payload should be returned on request, even from cache")
	}
}

func TestInfo(t *testing.T) {
	registered := time.Date(2013, 5, 1, 0, 0, 0, 0, time.UTC)
	rdapProvider := &fakeProvider{
		name: rdap.ProviderName,
		responses: map[string]core.ProviderResult{
			"taken.com": {
				RegistrationStatus: core.RegistrationRegistered,
				Status:             core.StatusRegistered,
				Info: &core.DomainInfo{
					NormalizedDomain: "taken.com",
					Registered:       true,
					Registrar:        "Example Registrar",
					RegistrationDate: &registered,
				},
			},
			"free.com": {RegistrationStatus: core.RegistrationNotRegistered, Status: core.StatusAvailable},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider}, nil)

	info, err := eng.Info(context.Background(), "taken.com")
	if err != nil {
		t.Fatalf("Info(taken.com): %v", err)
	}
	if !info.Registered || info.Registrar != "Example Registrar" {
		t.Errorf("Info = %+v", info)
	}

	free, err := eng.Info(context.Background(), "free.com")
	if err != nil {
		t.Fatalf("Info(free.com): %v", err)
	}
	if free.Registered {
		t.Errorf("free.com should report Registered=false")
	}

	if _, err := eng.Info(context.Background(), "not a domain"); err == nil {
		t.Errorf("Info should reject invalid input")
	}
}

func TestDNSNeverMakesADomainAvailable(t *testing.T) {
	rdapProvider := &fakeProvider{name: rdap.ProviderName, err: errors.New("registry down")}
	dnsProvider := &fakeProvider{
		name: dnsprobe.ProviderName,
		responses: map[string]core.ProviderResult{
			"mystery.com": {RegistrationStatus: core.RegistrationUnknown, Status: core.StatusUnknown},
		},
	}
	eng := newTestEngine([]provider.Provider{rdapProvider}, []provider.Provider{dnsProvider})

	got := eng.Check(context.Background(), "mystery.com", false)
	if got.Availability != core.AvailabilityUnknown {
		t.Errorf("Availability = %q, want unknown", got.Availability)
	}
}
