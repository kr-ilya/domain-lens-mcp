package engine

import (
	"sort"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/dnsprobe"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/rdap"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider/whois"
)

// resolution is the outcome of reconciling every provider's finding.
type resolution struct {
	Availability core.Availability
	Status       core.Status
	Confidence   core.Confidence
	Evidence     []string
	Conflict     bool
}

// resolve turns per-provider findings into a single honest answer.
//
// Rules, in order of authority:
//  1. RDAP is authoritative — a registry answer decides both availability and status.
//  2. WHOIS decides only when RDAP produced nothing usable, at lower confidence.
//  3. DNS delegation can only confirm that a domain is taken. It never makes a
//     domain "available", because an undelegated name may still be registered.
//  4. Anything else is "unknown", which callers must not read as "available".
func resolve(results []core.ProviderResult) resolution {
	var (
		rdapResult  *core.ProviderResult
		whoisResult *core.ProviderResult
		dnsResult   *core.ProviderResult
		evidence    []string
	)

	for i := range results {
		result := &results[i]
		evidence = append(evidence, result.Evidence...)
		switch result.Provider {
		case rdap.ProviderName:
			rdapResult = result
		case whois.ProviderName:
			whoisResult = result
		case dnsprobe.ProviderName:
			dnsResult = result
		}
	}
	sort.Strings(evidence)

	delegated := dnsResult != nil && dnsResult.RegistrationStatus == core.RegistrationRegistered

	if rdapResult != nil {
		switch rdapResult.RegistrationStatus {
		case core.RegistrationRegistered:
			return resolution{Availability: core.AvailabilityUnavailable, Status: rdapResult.Status, Confidence: core.ConfidenceHigh, Evidence: evidence, Conflict: false}

		case core.RegistrationNotRegistered:
			// A delegated zone with no registry object is contradictory: trust
			// RDAP, but say so and lower the confidence.
			if delegated {
				return resolution{Availability: core.AvailabilityAvailable, Status: core.StatusAvailable, Confidence: core.ConfidenceLow, Evidence: evidence, Conflict: true}
			}
			if whoisResult != nil && whoisResult.RegistrationStatus == core.RegistrationRegistered {
				return resolution{Availability: core.AvailabilityAvailable, Status: core.StatusAvailable, Confidence: core.ConfidenceLow, Evidence: evidence, Conflict: true}
			}
			return resolution{Availability: core.AvailabilityAvailable, Status: core.StatusAvailable, Confidence: core.ConfidenceHigh, Evidence: evidence, Conflict: false}
		}
	}

	if whoisResult != nil {
		switch whoisResult.RegistrationStatus {
		case core.RegistrationRegistered:
			return resolution{Availability: core.AvailabilityUnavailable, Status: whoisResult.Status, Confidence: core.ConfidenceHigh, Evidence: evidence, Conflict: false}
		case core.RegistrationNotRegistered:
			if delegated {
				return resolution{Availability: core.AvailabilityUnknown, Status: core.StatusUnknown, Confidence: core.ConfidenceLow, Evidence: evidence, Conflict: true}
			}
			// WHOIS text parsing is heuristic, so this never reaches "high".
			return resolution{Availability: core.AvailabilityAvailable, Status: core.StatusAvailable, Confidence: core.ConfidenceMedium, Evidence: evidence, Conflict: false}
		}
	}

	if delegated {
		return resolution{Availability: core.AvailabilityUnavailable, Status: core.StatusRegistered, Confidence: core.ConfidenceMedium, Evidence: evidence, Conflict: false}
	}

	status := core.StatusUnknown
	if rdapResult != nil && rdapResult.Status == core.StatusRateLimited {
		status = core.StatusRateLimited
	}
	return resolution{Availability: core.AvailabilityUnknown, Status: status, Confidence: core.ConfidenceLow, Evidence: evidence, Conflict: false}
}
