package whois

import (
	"testing"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
)

func mustParse(t *testing.T, domain string) domainname.Name {
	t.Helper()
	name, err := domainname.Parse(domain)
	if err != nil {
		t.Fatalf("Parse(%q): %v", domain, err)
	}
	return name
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name               string
		body               string
		registrationStatus core.RegistrationStatus
		status             core.Status
	}{
		{
			name:               "verisign no match",
			body:               "No match for \"SOMEFREE.COM\".\n>>> Last update of whois database: 2026-09-20 <<<",
			registrationStatus: core.RegistrationNotRegistered,
			status:             core.StatusAvailable,
		},
		{
			name:               "denic free",
			body:               "Domain: example-free.de\nStatus: free\n",
			registrationStatus: core.RegistrationNotRegistered,
			status:             core.StatusAvailable,
		},
		{
			name:               "denic connected",
			body:               "Domain: example.de\nStatus: connect\nChanged: 2024-01-02T10:00:00+01:00\n",
			registrationStatus: core.RegistrationRegistered,
			status:             core.StatusRegistered,
		},
		{
			name:               "registered record",
			body:               "Domain Name: EXAMPLE.IO\nRegistrar: Example Registrar\nCreation Date: 2013-05-01T12:00:00Z\n",
			registrationStatus: core.RegistrationRegistered,
			status:             core.StatusRegistered,
		},
		{
			name:               "reserved",
			body:               "Domain Name: RESERVED.IO\nRegistrar: Registry\nDomain Status: reserved\n",
			registrationStatus: core.RegistrationRegistered,
			status:             core.StatusReserved,
		},
		{
			name:               "unparsable",
			body:               "connection throttled, try again later",
			registrationStatus: core.RegistrationUnknown,
			status:             core.StatusUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := classify(tc.body, mustParse(t, "example.com"), "whois.example")
			if result.RegistrationStatus != tc.registrationStatus {
				t.Errorf("RegistrationStatus = %q, want %q", result.RegistrationStatus, tc.registrationStatus)
			}
			if result.Status != tc.status {
				t.Errorf("Status = %q, want %q", result.Status, tc.status)
			}
			if len(result.Evidence) == 0 {
				t.Errorf("Evidence is empty")
			}
		})
	}
}

func TestParseInfo(t *testing.T) {
	body := `Domain Name: EXAMPLE.IO
Registrar: Example Registrar, Inc.
Registrar IANA ID: 1234
Creation Date: 2013-05-01T12:00:00Z
Registry Expiry Date: 2027-05-01T12:00:00Z
Updated Date: 2026-04-02T09:30:00Z
Domain Status: clientTransferProhibited
Name Server: NS1.EXAMPLE.NET
Name Server: ns2.example.net 192.0.2.1
`

	info := parseInfo(body, mustParse(t, "example.io"), "whois.nic.io")
	if info.Registrar != "Example Registrar, Inc." {
		t.Errorf("Registrar = %q", info.Registrar)
	}
	if info.RegistrarIANAID != "1234" {
		t.Errorf("RegistrarIANAID = %q, want 1234", info.RegistrarIANAID)
	}
	if info.RegistrationDate == nil || info.RegistrationDate.Year() != 2013 {
		t.Errorf("RegistrationDate = %v", info.RegistrationDate)
	}
	if info.ExpirationDate == nil || info.ExpirationDate.Year() != 2027 {
		t.Errorf("ExpirationDate = %v", info.ExpirationDate)
	}
	if info.LastChangedDate == nil {
		t.Errorf("LastChangedDate is nil")
	}
	want := []string{"ns1.example.net", "ns2.example.net"}
	if len(info.Nameservers) != len(want) {
		t.Fatalf("Nameservers = %v, want %v", info.Nameservers, want)
	}
	for i, ns := range want {
		if info.Nameservers[i] != ns {
			t.Errorf("Nameservers[%d] = %q, want %q", i, info.Nameservers[i], ns)
		}
	}
}

func TestParseIANAServer(t *testing.T) {
	body := `% IANA WHOIS server
domain:       IO

organisation: Internet Computer Bureau Ltd
whois:        whois.nic.io

status:       ACTIVE
`
	if got := parseIANAServer(body); got != "whois.nic.io" {
		t.Errorf("parseIANAServer = %q, want whois.nic.io", got)
	}
	if got := parseIANAServer("domain: TEST\nstatus: ACTIVE\n"); got != "" {
		t.Errorf("parseIANAServer = %q, want empty for a TLD without WHOIS", got)
	}
}
