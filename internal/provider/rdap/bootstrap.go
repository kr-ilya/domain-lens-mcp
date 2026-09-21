package rdap

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// BootstrapURL is the IANA registry mapping TLDs to RDAP base URLs.
const BootstrapURL = "https://data.iana.org/rdap/dns.json"

// embeddedBootstrap keeps the server usable when IANA is unreachable at startup.
// Refreshed copies fetched at runtime always win over it.
//
//go:embed assets/dns.json
var embeddedBootstrap []byte

type bootstrapFile struct {
	Publication time.Time    `json:"publication"`
	Services    [][][]string `json:"services"`
}

// Bootstrap resolves a TLD to its RDAP base URLs, refreshing the IANA registry
// in the background and never falling back to a hardcoded TLD list.
type Bootstrap struct {
	httpClient *http.Client
	url        string
	ttl        time.Duration
	logger     *slog.Logger

	mu          sync.RWMutex
	services    map[string][]string
	publication time.Time
	fetchedAt   time.Time

	refresh sync.Mutex
}

// NewBootstrap returns a Bootstrap preloaded with the embedded snapshot.
func NewBootstrap(httpClient *http.Client, ttl time.Duration, logger *slog.Logger) *Bootstrap {
	b := &Bootstrap{
		httpClient: httpClient,
		url:        BootstrapURL,
		ttl:        ttl,
		logger:     logger,
	}
	services, publication, err := parseBootstrap(embeddedBootstrap)
	if err != nil {
		// The embedded file is validated by tests; treat a failure as fatal-ish
		// but recoverable via the first live fetch.
		logger.Error("embedded rdap bootstrap is unusable", "error", err)
		services = map[string][]string{}
	}
	b.services = services
	b.publication = publication
	return b
}

// BaseURLs returns the RDAP base URLs for a TLD, or nil when the TLD has no
// RDAP service registered with IANA.
func (b *Bootstrap) BaseURLs(ctx context.Context, tld string) []string {
	if b.stale() {
		b.tryRefresh(ctx)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.services[tld]
}

// Publication reports the publication timestamp of the loaded registry.
func (b *Bootstrap) Publication() time.Time {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.publication
}

func (b *Bootstrap) stale() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return time.Since(b.fetchedAt) > b.ttl
}

// tryRefresh fetches a fresh registry, keeping the current one on any failure.
func (b *Bootstrap) tryRefresh(ctx context.Context) {
	if !b.refresh.TryLock() {
		return
	}
	defer b.refresh.Unlock()
	if !b.stale() {
		return
	}

	services, publication, err := b.fetch(ctx)
	if err != nil {
		b.logger.Warn("rdap bootstrap refresh failed, keeping previous registry", "error", err)
		// Back off so a hard outage does not retry on every single lookup.
		b.mu.Lock()
		b.fetchedAt = time.Now().Add(-b.ttl).Add(time.Minute)
		b.mu.Unlock()
		return
	}

	b.mu.Lock()
	b.services = services
	b.publication = publication
	b.fetchedAt = time.Now()
	b.mu.Unlock()
	b.logger.Info("rdap bootstrap refreshed", "tlds", len(services), "publication", publication)
}

func (b *Bootstrap) fetch(ctx context.Context) (map[string][]string, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("rdap bootstrap: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, time.Time{}, err
	}
	return parseBootstrap(body)
}

func parseBootstrap(data []byte) (map[string][]string, time.Time, error) {
	var file bootstrapFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, time.Time{}, fmt.Errorf("rdap bootstrap: %w", err)
	}
	if len(file.Services) == 0 {
		return nil, time.Time{}, fmt.Errorf("rdap bootstrap: no services")
	}

	services := make(map[string][]string, 1500)
	for _, entry := range file.Services {
		if len(entry) < 2 {
			continue
		}
		urls := make([]string, 0, len(entry[1]))
		for _, u := range entry[1] {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			urls = append(urls, strings.TrimSuffix(u, "/"))
		}
		if len(urls) == 0 {
			continue
		}
		// Prefer https bases; several registries still advertise plain http.
		sortHTTPSFirst(urls)
		for _, tld := range entry[0] {
			tld = strings.ToLower(strings.TrimSpace(tld))
			if tld != "" {
				services[tld] = urls
			}
		}
	}
	return services, file.Publication, nil
}

func sortHTTPSFirst(urls []string) {
	secure := make([]string, 0, len(urls))
	plain := make([]string, 0, len(urls))
	for _, u := range urls {
		if strings.HasPrefix(u, "https://") {
			secure = append(secure, u)
		} else {
			plain = append(plain, u)
		}
	}
	copy(urls, append(secure, plain...))
}
