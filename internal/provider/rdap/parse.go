package rdap

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider"
)

// response is the subset of RFC 9083 we rely on.
type response struct {
	ObjectClassName string   `json:"objectClassName"`
	LDHName         string   `json:"ldhName"`
	UnicodeName     string   `json:"unicodeName"`
	Handle          string   `json:"handle"`
	Status          []string `json:"status"`
	Events          []struct {
		Action string `json:"eventAction"`
		Date   string `json:"eventDate"`
		Actor  string `json:"eventActor"`
	} `json:"events"`
	Nameservers []struct {
		LDHName string `json:"ldhName"`
	} `json:"nameservers"`
	SecureDNS *struct {
		DelegationSigned *bool `json:"delegationSigned"`
	} `json:"secureDNS"`
	Entities []entity `json:"entities"`
}

type entity struct {
	Handle     string     `json:"handle"`
	Roles      []string   `json:"roles"`
	PublicIDs  []publicID `json:"publicIds"`
	VCardArray []any      `json:"vcardArray"`
}

type publicID struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
}

// statusMapping translates EPP/RDAP statuses into our detailed Status. Order
// matters: the first match wins, most-specific first.
var statusMapping = []struct {
	rdapStatus string
	status     core.Status
}{
	{"pending delete", core.StatusPendingDelete},
	{"redemption period", core.StatusRedemption},
	{"pending restore", core.StatusRedemption},
	{"reserved", core.StatusReserved},
	{"administrative reserved", core.StatusReserved},
	{"blocked", core.StatusBlocked},
}

// parseResponse maps a 200 RDAP payload onto a ProviderResult. A successful
// response always means a registration object exists.
func parseResponse(body []byte, name domainname.Name, source string) (core.ProviderResult, error) {
	var resp response
	if err := json.Unmarshal(body, &resp); err != nil {
		return core.ProviderResult{}, fmt.Errorf("rdap: malformed response for %s: %w", name.ASCII, err)
	}

	status := core.StatusRegistered
	evidence := []string{provider.Evidence(ProviderName, "registration_exists")}
	for _, raw := range resp.Status {
		normalized := strings.ToLower(strings.TrimSpace(raw))
		for _, mapping := range statusMapping {
			if strings.Contains(normalized, mapping.rdapStatus) {
				status = mapping.status
				evidence = append(evidence, provider.Evidence(ProviderName, strings.ReplaceAll(normalized, " ", "_")))
				break
			}
		}
		if status != core.StatusRegistered {
			break
		}
	}

	var raw any
	_ = json.Unmarshal(body, &raw)

	return core.ProviderResult{
		Provider:           ProviderName,
		RegistrationStatus: core.RegistrationRegistered,
		Status:             status,
		Evidence:           evidence,
		Source:             source,
		Raw:                raw,
		Info:               buildInfo(&resp, name, source),
	}, nil
}

func buildInfo(resp *response, name domainname.Name, source string) *core.DomainInfo {
	info := &core.DomainInfo{
		Domain:           name.Input,
		NormalizedDomain: name.ASCII,
		Registered:       true,
		Statuses:         resp.Status,
		Source:           source,
		Provider:         ProviderName,
	}

	for _, ns := range resp.Nameservers {
		if ns.LDHName != "" {
			info.Nameservers = append(info.Nameservers, strings.ToLower(ns.LDHName))
		}
	}
	if resp.SecureDNS != nil && resp.SecureDNS.DelegationSigned != nil {
		info.DNSSEC = resp.SecureDNS.DelegationSigned
	}

	for _, ev := range resp.Events {
		date, err := parseRDAPTime(ev.Date)
		if err != nil {
			continue
		}
		action := strings.ToLower(ev.Action)
		info.Events = append(info.Events, core.Event{Action: action, Date: date, Actor: ev.Actor})

		switch action {
		case "registration":
			info.RegistrationDate = &date
		case "expiration":
			info.ExpirationDate = &date
		case "last changed":
			info.LastChangedDate = &date
		}
	}

	if registrar, ianaID := findRegistrar(resp.Entities); registrar != "" || ianaID != "" {
		info.Registrar = registrar
		info.RegistrarIANAID = ianaID
	}
	return info
}

func findRegistrar(entities []entity) (name, ianaID string) {
	for _, e := range entities {
		if !hasRole(e.Roles, "registrar") {
			continue
		}
		for _, id := range e.PublicIDs {
			if strings.EqualFold(id.Type, "IANA Registrar ID") {
				ianaID = id.Identifier
			}
		}
		if fn := vcardValue(e.VCardArray, "fn"); fn != "" {
			return fn, ianaID
		}
		return e.Handle, ianaID
	}
	return "", ""
}

func hasRole(roles []string, want string) bool {
	for _, role := range roles {
		if strings.EqualFold(role, want) {
			return true
		}
	}
	return false
}

// vcardValue extracts a property from the jCard structure
// ["vcard", [["fn", {}, "text", "Example Registrar"], ...]].
func vcardValue(vcard []any, property string) string {
	if len(vcard) < 2 {
		return ""
	}
	entries, ok := vcard[1].([]any)
	if !ok {
		return ""
	}
	for _, entry := range entries {
		fields, ok := entry.([]any)
		if !ok || len(fields) < 4 {
			continue
		}
		key, ok := fields[0].(string)
		if !ok || !strings.EqualFold(key, property) {
			continue
		}
		if value, ok := fields[3].(string); ok {
			return value
		}
	}
	return ""
}

// rdapTimeLayouts covers the RFC 3339 variants registries actually emit.
var rdapTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05Z0700",
	"2006-01-02",
}

func parseRDAPTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	for _, layout := range rdapTimeLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q", value)
}
