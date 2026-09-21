package rdap

import (
	"testing"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
)

const registeredPayload = `{
  "objectClassName": "domain",
  "ldhName": "EXAMPLE.COM",
  "status": ["client delete prohibited", "client transfer prohibited"],
  "events": [
    {"eventAction": "registration", "eventDate": "1995-08-14T04:00:00Z"},
    {"eventAction": "expiration", "eventDate": "2028-08-13T04:00:00Z"},
    {"eventAction": "last changed", "eventDate": "2025-08-14T07:01:38Z"}
  ],
  "nameservers": [
    {"ldhName": "A.IANA-SERVERS.NET"},
    {"ldhName": "B.IANA-SERVERS.NET"}
  ],
  "secureDNS": {"delegationSigned": true},
  "entities": [
    {
      "handle": "376",
      "roles": ["registrar"],
      "publicIds": [{"type": "IANA Registrar ID", "identifier": "376"}],
      "vcardArray": ["vcard", [["version", {}, "text", "4.0"], ["fn", {}, "text", "RESERVED-Internet Assigned Numbers Authority"]]]
    }
  ]
}`

func mustParse(t *testing.T, domain string) domainname.Name {
	t.Helper()
	name, err := domainname.Parse(domain)
	if err != nil {
		t.Fatalf("Parse(%q): %v", domain, err)
	}
	return name
}

func TestParseResponseRegistered(t *testing.T) {
	result, err := parseResponse([]byte(registeredPayload), mustParse(t, "example.com"), "https://rdap.verisign.com/com/v1/domain/example.com")
	if err != nil {
		t.Fatalf("parseResponse returned error: %v", err)
	}

	if result.RegistrationStatus != core.RegistrationRegistered {
		t.Errorf("RegistrationStatus = %q, want registered", result.RegistrationStatus)
	}
	if result.Status != core.StatusRegistered {
		t.Errorf("Status = %q, want registered", result.Status)
	}
	if result.Info == nil {
		t.Fatalf("Info is nil, want registration details")
	}
	if got := result.Info.Registrar; got != "RESERVED-Internet Assigned Numbers Authority" {
		t.Errorf("Registrar = %q", got)
	}
	if got := result.Info.RegistrarIANAID; got != "376" {
		t.Errorf("RegistrarIANAID = %q, want 376", got)
	}
	if result.Info.RegistrationDate == nil || result.Info.RegistrationDate.Year() != 1995 {
		t.Errorf("RegistrationDate = %v, want 1995", result.Info.RegistrationDate)
	}
	if result.Info.ExpirationDate == nil || result.Info.ExpirationDate.Year() != 2028 {
		t.Errorf("ExpirationDate = %v, want 2028", result.Info.ExpirationDate)
	}
	if result.Info.LastChangedDate == nil {
		t.Errorf("LastChangedDate is nil")
	}
	if len(result.Info.Nameservers) != 2 || result.Info.Nameservers[0] != "a.iana-servers.net" {
		t.Errorf("Nameservers = %v", result.Info.Nameservers)
	}
	if result.Info.DNSSEC == nil || !*result.Info.DNSSEC {
		t.Errorf("DNSSEC = %v, want true", result.Info.DNSSEC)
	}
}

func TestParseResponseLifecycleStatuses(t *testing.T) {
	tests := map[string]core.Status{
		`{"status":["pending delete"]}`:                           core.StatusPendingDelete,
		`{"status":["redemption period"]}`:                        core.StatusRedemption,
		`{"status":["client hold","server transfer prohibited"]}`: core.StatusRegistered,
		`{"status":[]}`: core.StatusRegistered,
	}
	for payload, want := range tests {
		result, err := parseResponse([]byte(payload), mustParse(t, "example.com"), "source")
		if err != nil {
			t.Fatalf("parseResponse(%s): %v", payload, err)
		}
		if result.Status != want {
			t.Errorf("payload %s: Status = %q, want %q", payload, result.Status, want)
		}
		if result.RegistrationStatus != core.RegistrationRegistered {
			t.Errorf("payload %s: a 200 response always means a registration exists", payload)
		}
	}
}

func TestParseResponseMalformed(t *testing.T) {
	if _, err := parseResponse([]byte("not json"), mustParse(t, "example.com"), "source"); err == nil {
		t.Fatalf("expected an error for malformed payload")
	}
}

func TestEmbeddedBootstrapIsUsable(t *testing.T) {
	services, publication, err := parseBootstrap(embeddedBootstrap)
	if err != nil {
		t.Fatalf("embedded bootstrap is unusable: %v", err)
	}
	if publication.IsZero() {
		t.Errorf("embedded bootstrap has no publication date")
	}
	for _, tld := range []string{"com", "net", "org", "ai", "dev", "app", "nl", "xyz"} {
		if len(services[tld]) == 0 {
			t.Errorf("embedded bootstrap has no RDAP base for .%s", tld)
		}
	}
	// These ccTLDs publish no RDAP service, which is exactly why the WHOIS
	// fallback exists. If IANA adds them, the fallback simply stops being used.
	for _, tld := range []string{"io", "de", "eu"} {
		if len(services[tld]) != 0 {
			t.Logf(".%s now has an RDAP service; WHOIS fallback is no longer needed for it", tld)
		}
	}
	if len(services) < 1000 {
		t.Errorf("embedded bootstrap covers only %d TLDs, expected 1000+", len(services))
	}
}

func TestSortHTTPSFirst(t *testing.T) {
	urls := []string{"http://plain.example", "https://secure.example"}
	sortHTTPSFirst(urls)
	if urls[0] != "https://secure.example" {
		t.Errorf("https base should come first, got %v", urls)
	}
}
