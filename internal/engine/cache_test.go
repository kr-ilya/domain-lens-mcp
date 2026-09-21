package engine

import (
	"testing"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
)

func TestCacheTTLPerAvailability(t *testing.T) {
	ttl := CacheTTL{Available: time.Minute, Unavailable: 5 * time.Minute, Unknown: 15 * time.Second}
	cache := newResultCache(ttl)

	now := time.Now()
	cache.clock = func() time.Time { return now }

	cache.Put("free.com", core.CheckResult{Availability: core.AvailabilityAvailable})
	cache.Put("taken.com", core.CheckResult{Availability: core.AvailabilityUnavailable})
	cache.Put("mystery.com", core.CheckResult{Availability: core.AvailabilityUnknown})

	// Volatile "available" answers expire first.
	now = now.Add(30 * time.Second)
	for _, key := range []string{"free.com", "taken.com", "mystery.com"} {
		if key == "mystery.com" {
			if _, ok := cache.Get(key); ok {
				t.Errorf("%s should have expired after 30s", key)
			}
			continue
		}
		if _, ok := cache.Get(key); !ok {
			t.Errorf("%s should still be cached after 30s", key)
		}
	}

	now = now.Add(time.Minute)
	if _, ok := cache.Get("free.com"); ok {
		t.Errorf("free.com should have expired after 90s")
	}
	if _, ok := cache.Get("taken.com"); !ok {
		t.Errorf("taken.com should still be cached after 90s")
	}
}

func TestCacheMarksHits(t *testing.T) {
	cache := newResultCache(CacheTTL{Available: time.Minute})
	cache.Put("free.com", core.CheckResult{Availability: core.AvailabilityAvailable})

	got, ok := cache.Get("free.com")
	if !ok {
		t.Fatalf("expected a cache hit")
	}
	if !got.Cached {
		t.Errorf("a cache hit must be marked as cached")
	}
}

func TestCacheSkipsZeroTTL(t *testing.T) {
	cache := newResultCache(CacheTTL{})
	cache.Put("free.com", core.CheckResult{Availability: core.AvailabilityAvailable})
	if _, ok := cache.Get("free.com"); ok {
		t.Errorf("a zero TTL must disable caching")
	}
}

func TestCachePurge(t *testing.T) {
	cache := newResultCache(CacheTTL{Available: time.Minute})
	now := time.Now()
	cache.clock = func() time.Time { return now }

	cache.Put("free.com", core.CheckResult{Availability: core.AvailabilityAvailable})
	now = now.Add(2 * time.Minute)
	cache.Purge()

	if len(cache.entries) != 0 {
		t.Errorf("expired entries should be dropped, got %d", len(cache.entries))
	}
}
