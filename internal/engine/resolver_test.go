package engine

import (
	"testing"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/dnsprobe"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/rdap"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/whois"
)

func result(provider string, registration core.RegistrationStatus, status core.Status) core.ProviderResult {
	return core.ProviderResult{
		Provider:           provider,
		RegistrationStatus: registration,
		Status:             status,
		Evidence:           []string{provider + ":test"},
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name         string
		sources      []core.ProviderResult
		availability core.Availability
		status       core.Status
		confidence   core.Confidence
		conflict     bool
	}{
		{
			name:         "rdap registered wins",
			sources:      []core.ProviderResult{result(rdap.ProviderName, core.RegistrationRegistered, core.StatusRegistered)},
			availability: core.AvailabilityUnavailable,
			status:       core.StatusRegistered,
			confidence:   core.ConfidenceHigh,
		},
		{
			name:         "rdap keeps lifecycle status",
			sources:      []core.ProviderResult{result(rdap.ProviderName, core.RegistrationRegistered, core.StatusPendingDelete)},
			availability: core.AvailabilityUnavailable,
			status:       core.StatusPendingDelete,
			confidence:   core.ConfidenceHigh,
		},
		{
			name: "rdap free and no delegation",
			sources: []core.ProviderResult{
				result(rdap.ProviderName, core.RegistrationNotRegistered, core.StatusAvailable),
				result(dnsprobe.ProviderName, core.RegistrationUnknown, core.StatusUnknown),
			},
			availability: core.AvailabilityAvailable,
			status:       core.StatusAvailable,
			confidence:   core.ConfidenceHigh,
		},
		{
			name: "rdap free but delegated is a conflict",
			sources: []core.ProviderResult{
				result(rdap.ProviderName, core.RegistrationNotRegistered, core.StatusAvailable),
				result(dnsprobe.ProviderName, core.RegistrationRegistered, core.StatusRegistered),
			},
			availability: core.AvailabilityAvailable,
			status:       core.StatusAvailable,
			confidence:   core.ConfidenceLow,
			conflict:     true,
		},
		{
			name: "rdap free but whois says registered is a conflict",
			sources: []core.ProviderResult{
				result(rdap.ProviderName, core.RegistrationNotRegistered, core.StatusAvailable),
				result(whois.ProviderName, core.RegistrationRegistered, core.StatusRegistered),
			},
			availability: core.AvailabilityAvailable,
			status:       core.StatusAvailable,
			confidence:   core.ConfidenceLow,
			conflict:     true,
		},
		{
			name: "whois answers when rdap is inconclusive",
			sources: []core.ProviderResult{
				result(rdap.ProviderName, core.RegistrationUnknown, core.StatusRateLimited),
				result(whois.ProviderName, core.RegistrationNotRegistered, core.StatusAvailable),
			},
			availability: core.AvailabilityAvailable,
			status:       core.StatusAvailable,
			confidence:   core.ConfidenceMedium,
		},
		{
			name:         "whois registered without rdap",
			sources:      []core.ProviderResult{result(whois.ProviderName, core.RegistrationRegistered, core.StatusRegistered)},
			availability: core.AvailabilityUnavailable,
			status:       core.StatusRegistered,
			confidence:   core.ConfidenceHigh,
		},
		{
			name: "delegation alone only proves the domain is taken",
			sources: []core.ProviderResult{
				result(dnsprobe.ProviderName, core.RegistrationRegistered, core.StatusRegistered),
			},
			availability: core.AvailabilityUnavailable,
			status:       core.StatusRegistered,
			confidence:   core.ConfidenceMedium,
		},
		{
			name: "no delegation alone never means available",
			sources: []core.ProviderResult{
				result(dnsprobe.ProviderName, core.RegistrationUnknown, core.StatusUnknown),
			},
			availability: core.AvailabilityUnknown,
			status:       core.StatusUnknown,
			confidence:   core.ConfidenceLow,
		},
		{
			name:         "no sources at all",
			sources:      nil,
			availability: core.AvailabilityUnknown,
			status:       core.StatusUnknown,
			confidence:   core.ConfidenceLow,
		},
		{
			name:         "rate limited rdap surfaces the reason",
			sources:      []core.ProviderResult{result(rdap.ProviderName, core.RegistrationUnknown, core.StatusRateLimited)},
			availability: core.AvailabilityUnknown,
			status:       core.StatusRateLimited,
			confidence:   core.ConfidenceLow,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolve(tc.sources)
			if got.Availability != tc.availability {
				t.Errorf("Availability = %q, want %q", got.Availability, tc.availability)
			}
			if got.Status != tc.status {
				t.Errorf("Status = %q, want %q", got.Status, tc.status)
			}
			if got.Confidence != tc.confidence {
				t.Errorf("Confidence = %q, want %q", got.Confidence, tc.confidence)
			}
			if got.Conflict != tc.conflict {
				t.Errorf("Conflict = %v, want %v", got.Conflict, tc.conflict)
			}
			if len(got.Evidence) != len(tc.sources) {
				t.Errorf("Evidence = %v, want one entry per source", got.Evidence)
			}
		})
	}
}
