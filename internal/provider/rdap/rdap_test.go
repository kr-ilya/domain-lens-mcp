package rdap

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/ratelimit"
)

// newTestProvider points a provider at a single fake registry, bypassing IANA.
func newTestProvider(t *testing.T, baseURL string, retries int) *Provider {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bootstrap := &Bootstrap{
		httpClient: http.DefaultClient,
		ttl:        time.Hour,
		logger:     logger,
		services:   map[string][]string{"com": {baseURL}},
		fetchedAt:  time.Now(),
	}
	limiter := ratelimit.NewHostLimiter(ratelimit.Config{RPS: 1000, Burst: 1000})
	return New(bootstrap, http.DefaultClient, limiter, retries, logger)
}

func TestCheckRegistered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/domain/example.com" {
			t.Errorf("path = %q, want /domain/example.com", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/rdap+json")
		io.WriteString(w, registeredPayload)
	}))
	defer server.Close()

	got, err := newTestProvider(t, server.URL, 0).Check(context.Background(), mustParse(t, "example.com"))
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if got.RegistrationStatus != core.RegistrationRegistered {
		t.Errorf("RegistrationStatus = %q, want registered", got.RegistrationStatus)
	}
	if got.Provider != ProviderName {
		t.Errorf("Provider = %q", got.Provider)
	}
}

func TestCheckNotFoundMeansNoRegistration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	got, err := newTestProvider(t, server.URL, 0).Check(context.Background(), mustParse(t, "example.com"))
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if got.RegistrationStatus != core.RegistrationNotRegistered || got.Status != core.StatusAvailable {
		t.Errorf("got %q/%q, want not_registered/available", got.RegistrationStatus, got.Status)
	}
}

func TestCheckRetriesOnThrottling(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, registeredPayload)
	}))
	defer server.Close()

	got, err := newTestProvider(t, server.URL, 2).Check(context.Background(), mustParse(t, "example.com"))
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if got.RegistrationStatus != core.RegistrationRegistered {
		t.Errorf("RegistrationStatus = %q, want registered after a retry", got.RegistrationStatus)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestCheckPersistentThrottlingIsNotAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	got, err := newTestProvider(t, server.URL, 1).Check(context.Background(), mustParse(t, "example.com"))
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if got.RegistrationStatus != core.RegistrationUnknown || got.Status != core.StatusRateLimited {
		t.Fatalf("got %q/%q, want unknown/rate_limited — throttling must never look free",
			got.RegistrationStatus, got.Status)
	}
}

func TestCheckFallsBackToSecondBase(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, registeredPayload)
	}))
	defer working.Close()

	provider := newTestProvider(t, broken.URL, 0)
	provider.bootstrap.services["com"] = []string{broken.URL, working.URL}

	got, err := provider.Check(context.Background(), mustParse(t, "example.com"))
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if got.RegistrationStatus != core.RegistrationRegistered {
		t.Errorf("RegistrationStatus = %q, want registered from the second base", got.RegistrationStatus)
	}
}

func TestSupports(t *testing.T) {
	provider := newTestProvider(t, "https://rdap.example", 0)
	if !provider.Supports(context.Background(), mustParse(t, "example.com")) {
		t.Errorf("Supports(.com) = false, want true")
	}
	if provider.Supports(context.Background(), mustParse(t, "example.de")) {
		t.Errorf("Supports(.de) = true, want false when no RDAP service is registered")
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("5"); got != 5*time.Second {
		t.Errorf("parseRetryAfter(\"5\") = %v, want 5s", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("parseRetryAfter(\"\") = %v, want 0", got)
	}
	if got := parseRetryAfter("garbage"); got != 0 {
		t.Errorf("parseRetryAfter(\"garbage\") = %v, want 0", got)
	}
}
