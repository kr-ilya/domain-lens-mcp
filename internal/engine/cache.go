package engine

import (
	"sync"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
)

// CacheTTL holds per-outcome lifetimes. Availability is volatile, so an
// "available" answer expires quickly; a registration is stickier.
type CacheTTL struct {
	Available   time.Duration
	Unavailable time.Duration
	Unknown     time.Duration
}

func (t CacheTTL) lifetime(availability core.Availability) time.Duration {
	switch availability {
	case core.AvailabilityAvailable:
		return t.Available
	case core.AvailabilityUnavailable:
		return t.Unavailable
	default:
		return t.Unknown
	}
}

type cacheEntry struct {
	result    core.CheckResult
	expiresAt time.Time
}

// resultCache memoizes normalized check results by ASCII domain.
type resultCache struct {
	ttl CacheTTL

	mu      sync.Mutex
	entries map[string]cacheEntry
	// clock is injectable so tests do not have to sleep.
	clock func() time.Time
}

func newResultCache(ttl CacheTTL) *resultCache {
	return &resultCache{ttl: ttl, entries: make(map[string]cacheEntry), clock: time.Now}
}

// Get returns a cached result, marked as cached, when it is still fresh.
func (c *resultCache) Get(key string) (core.CheckResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return core.CheckResult{}, false
	}
	if !c.clock().Before(entry.expiresAt) {
		delete(c.entries, key)
		return core.CheckResult{}, false
	}
	result := entry.result
	result.Cached = true
	return result, true
}

// Put stores a result under the TTL matching its availability.
func (c *resultCache) Put(key string, result core.CheckResult) {
	ttl := c.ttl.lifetime(result.Availability)
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{result: result, expiresAt: c.clock().Add(ttl)}
}

// Purge drops expired entries so a long-running server does not grow forever.
func (c *resultCache) Purge() {
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
}
