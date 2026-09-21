// Package whois queries port 43 WHOIS servers for TLDs that have no RDAP
// service, discovering the server through IANA rather than a hardcoded table.
package whois

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ianaServer answers "which WHOIS server is authoritative for this TLD?".
const ianaServer = "whois.iana.org"

// serverCacheTTL is long because TLD→WHOIS server mappings change rarely.
const serverCacheTTL = 24 * time.Hour

type serverEntry struct {
	server    string
	fetchedAt time.Time
}

// serverRegistry resolves and caches the WHOIS server for each TLD.
type serverRegistry struct {
	dial dialFunc

	mu      sync.Mutex
	entries map[string]serverEntry
}

func newServerRegistry(dial dialFunc) *serverRegistry {
	return &serverRegistry{dial: dial, entries: make(map[string]serverEntry)}
}

// Server returns the WHOIS host for a TLD, or "" when the TLD has none.
func (r *serverRegistry) Server(ctx context.Context, tld string) (string, error) {
	r.mu.Lock()
	entry, ok := r.entries[tld]
	r.mu.Unlock()
	if ok && time.Since(entry.fetchedAt) < serverCacheTTL {
		return entry.server, nil
	}

	raw, err := query(ctx, r.dial, ianaServer, tld)
	if err != nil {
		return "", fmt.Errorf("whois: iana lookup for .%s: %w", tld, err)
	}
	server := parseIANAServer(raw)

	r.mu.Lock()
	r.entries[tld] = serverEntry{server: server, fetchedAt: time.Now()}
	r.mu.Unlock()
	return server, nil
}

// parseIANAServer extracts the "whois:" line from an IANA TLD record.
func parseIANAServer(body string) string {
	for line := range strings.Lines(body) {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "whois") {
			continue
		}
		if server := strings.ToLower(strings.TrimSpace(value)); server != "" {
			return server
		}
	}
	return ""
}
