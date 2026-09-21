// Package ratelimit protects upstream registries from agent-driven bursts:
// an LLM happily asks for two hundred domains in one turn.
package ratelimit

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ErrRateLimited reports that an upstream host refused or throttled a request.
type ErrRateLimited struct {
	Host       string
	Status     int
	RetryAfter time.Duration
}

func (e *ErrRateLimited) Error() string {
	return fmt.Sprintf("host %s rate limited (status %d)", e.Host, e.Status)
}

// ErrCircuitOpen reports that a host is being skipped after repeated failures.
type ErrCircuitOpen struct {
	Host  string
	Until time.Time
}

func (e *ErrCircuitOpen) Error() string {
	return fmt.Sprintf("host %s circuit open until %s", e.Host, e.Until.Format(time.RFC3339))
}

// Config tunes the per-host limiter.
type Config struct {
	// RPS is the sustained request rate allowed per upstream host.
	RPS float64
	// Burst is the bucket size for short spikes.
	Burst int
	// FailureThreshold is how many consecutive failures open the circuit.
	FailureThreshold int
	// OpenDuration is how long an open circuit stays open.
	OpenDuration time.Duration
}

// HostLimiter combines a token bucket and a circuit breaker per upstream host.
type HostLimiter struct {
	cfg Config

	mu    sync.Mutex
	hosts map[string]*hostState
}

type hostState struct {
	limiter   *rate.Limiter
	failures  int
	openUntil time.Time
}

// NewHostLimiter builds a limiter; zero-valued config fields get safe defaults.
func NewHostLimiter(cfg Config) *HostLimiter {
	if cfg.RPS <= 0 {
		cfg.RPS = 5
	}
	if cfg.Burst <= 0 {
		cfg.Burst = int(math.Max(1, cfg.RPS))
	}
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.OpenDuration <= 0 {
		cfg.OpenDuration = 30 * time.Second
	}
	return &HostLimiter{cfg: cfg, hosts: make(map[string]*hostState)}
}

// Wait blocks until the host has budget, or fails fast if its circuit is open.
func (l *HostLimiter) Wait(ctx context.Context, host string) error {
	state := l.state(host)

	l.mu.Lock()
	openUntil := state.openUntil
	l.mu.Unlock()
	if now := time.Now(); now.Before(openUntil) {
		return &ErrCircuitOpen{Host: host, Until: openUntil}
	}

	return state.limiter.Wait(ctx)
}

// ReportSuccess closes the circuit for a host after a healthy response.
func (l *HostLimiter) ReportSuccess(host string) {
	state := l.state(host)
	l.mu.Lock()
	defer l.mu.Unlock()
	state.failures = 0
	state.openUntil = time.Time{}
}

// ReportFailure records a failure and opens the circuit once the threshold is hit.
func (l *HostLimiter) ReportFailure(host string) {
	state := l.state(host)
	l.mu.Lock()
	defer l.mu.Unlock()
	state.failures++
	if state.failures >= l.cfg.FailureThreshold {
		state.openUntil = time.Now().Add(l.cfg.OpenDuration)
	}
}

func (l *HostLimiter) state(host string) *hostState {
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.hosts[host]
	if !ok {
		state = &hostState{limiter: rate.NewLimiter(rate.Limit(l.cfg.RPS), l.cfg.Burst)}
		l.hosts[host] = state
	}
	return state
}

// Backoff returns the delay before retry attempt n (0-based), honouring an
// upstream Retry-After hint when present.
func Backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, 30*time.Second)
	}
	base := time.Duration(math.Pow(2, float64(attempt))) * 500 * time.Millisecond
	base = min(base, 8*time.Second)
	// Jitter spreads retries when a whole batch hits the same limit at once.
	jitter := time.Duration(rand.Int64N(int64(base / 2)))
	return base/2 + jitter
}

// Sleep waits for d, returning early if the context is cancelled.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
