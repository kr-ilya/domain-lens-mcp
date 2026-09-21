package dnsprobe

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

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

// newUnreachableProvider points the resolver at a dialer that always fails, so
// the test never touches the network.
func newUnreachableProvider() *Provider {
	return &Provider{
		timeout: time.Second,
		resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("no network in tests")
			},
		},
	}
}

func TestCheckNeverReportsNotRegistered(t *testing.T) {
	got, err := newUnreachableProvider().Check(context.Background(), mustParse(t, "example.com"))
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	// DNS is corroboration only: a failed or empty lookup must stay unknown,
	// because an undelegated name can still be registered.
	if got.RegistrationStatus != core.RegistrationUnknown {
		t.Errorf("RegistrationStatus = %q, want unknown", got.RegistrationStatus)
	}
	if got.Status != core.StatusUnknown {
		t.Errorf("Status = %q, want unknown", got.Status)
	}
	if len(got.Evidence) == 0 {
		t.Errorf("Evidence is empty")
	}
}

func TestSupportsEveryTLD(t *testing.T) {
	provider := New(time.Second)
	for _, domain := range []string{"example.com", "example.de", "example.xn--p1ai"} {
		if !provider.Supports(context.Background(), mustParse(t, domain)) {
			t.Errorf("Supports(%s) = false, want true", domain)
		}
	}
}
