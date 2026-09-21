package whois

import (
	"strings"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider"
)

// notFoundMarkers are the phrases registries use to say "no registration".
// WHOIS has no schema, so this list is inherently heuristic — anything not
// matched here stays "unknown" rather than being guessed as available.
var notFoundMarkers = []string{
	"no match for",
	"no match",
	"not found",
	"domain not found",
	"no entries found",
	"no data found",
	"no object found",
	"nothing found",
	"not registered",
	"free for registration",
	"available for registration",
	"status: free",
	"status: available",
	"no information available about domain name",
	"domain status: no object found",
}

// registeredMarkers indicate that a registration record was returned.
var registeredMarkers = []string{
	"domain name:",
	"domain:",
	"creation date:",
	"created:",
	"registered on:",
	"registrar:",
	"status: connect",
	"status: active",
	"registry domain id:",
}

// reservedMarkers indicate the registry withholds the name from registration.
var reservedMarkers = []string{
	"reserved",
	"restricted",
	"blocked",
}

// classify turns a free-form WHOIS response into a normalized result.
func classify(body string, name domainname.Name, server string) core.ProviderResult {
	lower := strings.ToLower(body)
	result := core.ProviderResult{
		Provider: ProviderName,
		Source:   server,
		Raw:      body,
	}

	switch {
	case containsAny(lower, notFoundMarkers):
		result.RegistrationStatus = core.RegistrationNotRegistered
		result.Status = core.StatusAvailable
		result.Evidence = []string{provider.Evidence(ProviderName, "no_match")}

	case containsAny(lower, registeredMarkers):
		result.RegistrationStatus = core.RegistrationRegistered
		result.Status = core.StatusRegistered
		result.Evidence = []string{provider.Evidence(ProviderName, "record_found")}
		if containsAny(lower, reservedMarkers) {
			result.Status = core.StatusReserved
			result.Evidence = append(result.Evidence, provider.Evidence(ProviderName, "reserved"))
		}
		result.Info = parseInfo(body, name, server)

	default:
		result.RegistrationStatus = core.RegistrationUnknown
		result.Status = core.StatusUnknown
		result.Evidence = []string{provider.Evidence(ProviderName, "unparsable")}
	}
	return result
}

func containsAny(haystack string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// fieldAliases map normalized WHOIS keys onto the fields we expose.
var fieldAliases = map[string][]string{
	"registrar":   {"registrar", "sponsoring registrar"},
	"created":     {"creation date", "created", "created on", "registered on", "registration time", "domain record activated"},
	"expires":     {"registry expiry date", "expiration date", "expiry date", "paid-till", "renewal date", "expires on"},
	"updated":     {"updated date", "last updated", "last modified", "changed"},
	"status":      {"domain status", "status", "state"},
	"nameserver":  {"name server", "nserver", "nameserver", "domain nameservers"},
	"registrarid": {"registrar iana id"},
}

// parseInfo extracts the registration fields WHOIS servers commonly expose.
func parseInfo(body string, name domainname.Name, server string) *core.DomainInfo {
	info := &core.DomainInfo{
		Domain:           name.Input,
		NormalizedDomain: name.ASCII,
		Registered:       true,
		Source:           server,
		Provider:         ProviderName,
	}

	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "%") || strings.HasPrefix(line, "#") {
			continue
		}
		rawKey, rawValue, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(rawKey))
		value := strings.TrimSpace(rawValue)
		if value == "" {
			continue
		}

		switch {
		case matchesAlias(key, fieldAliases["registrarid"]):
			info.RegistrarIANAID = value
		case matchesAlias(key, fieldAliases["registrar"]) && info.Registrar == "":
			info.Registrar = value
		case matchesAlias(key, fieldAliases["created"]) && info.RegistrationDate == nil:
			info.RegistrationDate = parseWhoisTime(value)
		case matchesAlias(key, fieldAliases["expires"]) && info.ExpirationDate == nil:
			info.ExpirationDate = parseWhoisTime(value)
		case matchesAlias(key, fieldAliases["updated"]) && info.LastChangedDate == nil:
			info.LastChangedDate = parseWhoisTime(value)
		case matchesAlias(key, fieldAliases["status"]):
			info.Statuses = append(info.Statuses, value)
		case matchesAlias(key, fieldAliases["nameserver"]):
			// Some servers append glue addresses after the hostname.
			host := strings.ToLower(strings.Fields(value)[0])
			info.Nameservers = append(info.Nameservers, strings.TrimSuffix(host, "."))
		}
	}
	return info
}

func matchesAlias(key string, aliases []string) bool {
	for _, alias := range aliases {
		if key == alias {
			return true
		}
	}
	return false
}

// whoisTimeLayouts covers the date formats seen across common registries.
var whoisTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04:05 MST",
	"2006-01-02",
	"02.01.2006 15:04:05",
	"02-Jan-2006",
	"02/01/2006",
}

func parseWhoisTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range whoisTimeLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}
