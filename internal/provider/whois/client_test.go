package whois

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/ratelimit"
)

// fakeWhoisServer serves canned answers over a real TCP listener, keyed by the
// query line the client sends.
func fakeWhoisServer(t *testing.T, answers map[string]string) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				query, err := bufio.NewReader(conn).ReadString('\n')
				if err != nil {
					return
				}
				io.WriteString(conn, answers[strings.TrimSpace(query)])
			}()
		}
	}()
	return listener
}

// newTestProvider routes every WHOIS dial to the fake server.
func newTestProvider(t *testing.T, listener net.Listener) *Provider {
	t.Helper()
	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: 2 * time.Second}
		return dialer.DialContext(ctx, "tcp", listener.Addr().String())
	}
	return &Provider{
		registry: newServerRegistry(dial),
		limiter:  ratelimit.NewHostLimiter(ratelimit.Config{RPS: 1000, Burst: 1000}),
		dial:     dial,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestCheckDiscoversServerAndClassifies(t *testing.T) {
	listener := fakeWhoisServer(t, map[string]string{
		"de":          "domain: DE\nwhois: whois.denic.de\n",
		"example.de":  "Domain: example.de\nStatus: connect\n",
		"somefree.de": "Domain: somefree.de\nStatus: free\n",
	})
	provider := newTestProvider(t, listener)
	ctx := context.Background()

	if !provider.Supports(ctx, mustParse(t, "example.de")) {
		t.Fatalf("Supports = false, want true once IANA reports a WHOIS server")
	}

	taken, err := provider.Check(ctx, mustParse(t, "example.de"))
	if err != nil {
		t.Fatalf("Check(example.de): %v", err)
	}
	if taken.RegistrationStatus != core.RegistrationRegistered {
		t.Errorf("example.de = %q, want registered", taken.RegistrationStatus)
	}
	if taken.Source != "whois.denic.de" {
		t.Errorf("Source = %q, want the discovered server", taken.Source)
	}

	free, err := provider.Check(ctx, mustParse(t, "somefree.de"))
	if err != nil {
		t.Fatalf("Check(somefree.de): %v", err)
	}
	if free.RegistrationStatus != core.RegistrationNotRegistered {
		t.Errorf("somefree.de = %q, want not_registered", free.RegistrationStatus)
	}
}

func TestSupportsFalseWithoutWhoisServer(t *testing.T) {
	listener := fakeWhoisServer(t, map[string]string{"example": "domain: EXAMPLE\nstatus: ACTIVE\n"})
	provider := newTestProvider(t, listener)

	if provider.Supports(context.Background(), mustParse(t, "foo.example")) {
		t.Errorf("Supports = true, want false when IANA lists no WHOIS server")
	}
	if _, err := provider.Check(context.Background(), mustParse(t, "foo.example")); err == nil {
		t.Errorf("Check should fail when no WHOIS server exists")
	}
}

func TestServerDiscoveryIsCached(t *testing.T) {
	listener := fakeWhoisServer(t, map[string]string{
		"de":         "whois: whois.denic.de\n",
		"example.de": "Domain: example.de\nStatus: connect\n",
	})
	provider := newTestProvider(t, listener)
	ctx := context.Background()

	for range 3 {
		if _, err := provider.registry.Server(ctx, "de"); err != nil {
			t.Fatalf("Server(de): %v", err)
		}
	}
	if len(provider.registry.entries) != 1 {
		t.Errorf("registry entries = %d, want 1", len(provider.registry.entries))
	}
}
