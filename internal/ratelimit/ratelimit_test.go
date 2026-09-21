package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCircuitOpensAfterConsecutiveFailures(t *testing.T) {
	limiter := NewHostLimiter(Config{RPS: 100, Burst: 100, FailureThreshold: 3, OpenDuration: time.Minute})
	ctx := context.Background()

	for range 2 {
		limiter.ReportFailure("registry.example")
	}
	if err := limiter.Wait(ctx, "registry.example"); err != nil {
		t.Fatalf("circuit opened too early: %v", err)
	}

	limiter.ReportFailure("registry.example")

	err := limiter.Wait(ctx, "registry.example")
	var open *ErrCircuitOpen
	if !errors.As(err, &open) {
		t.Fatalf("Wait = %v, want ErrCircuitOpen", err)
	}
	if open.Host != "registry.example" {
		t.Errorf("Host = %q", open.Host)
	}
}

func TestSuccessClosesCircuit(t *testing.T) {
	limiter := NewHostLimiter(Config{RPS: 100, Burst: 100, FailureThreshold: 2, OpenDuration: time.Minute})
	ctx := context.Background()

	limiter.ReportFailure("registry.example")
	limiter.ReportFailure("registry.example")
	limiter.ReportSuccess("registry.example")

	if err := limiter.Wait(ctx, "registry.example"); err != nil {
		t.Fatalf("a success should close the circuit, got %v", err)
	}
}

func TestHostsAreIsolated(t *testing.T) {
	limiter := NewHostLimiter(Config{RPS: 100, Burst: 100, FailureThreshold: 1, OpenDuration: time.Minute})
	limiter.ReportFailure("broken.example")

	if err := limiter.Wait(context.Background(), "healthy.example"); err != nil {
		t.Fatalf("one failing host must not block another: %v", err)
	}
}

func TestBackoffHonoursRetryAfter(t *testing.T) {
	if got := Backoff(0, 3*time.Second); got != 3*time.Second {
		t.Errorf("Backoff with Retry-After = %v, want 3s", got)
	}
	if got := Backoff(0, time.Hour); got != 30*time.Second {
		t.Errorf("Backoff should cap Retry-After at 30s, got %v", got)
	}
	for attempt := range 6 {
		got := Backoff(attempt, 0)
		if got <= 0 || got > 8*time.Second {
			t.Errorf("Backoff(%d) = %v, want a positive delay under 8s", attempt, got)
		}
	}
}

func TestSleepRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep = %v, want context.Canceled", err)
	}
}

func TestWaitRespectsCancellation(t *testing.T) {
	limiter := NewHostLimiter(Config{RPS: 0.001, Burst: 1})
	ctx, cancel := context.WithCancel(context.Background())

	if err := limiter.Wait(ctx, "slow.example"); err != nil {
		t.Fatalf("the first token should be free: %v", err)
	}
	cancel()
	if err := limiter.Wait(ctx, "slow.example"); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait = %v, want context.Canceled", err)
	}
}
