// Package engine orchestrates providers, reconciles their answers and shields
// upstream registries from agent-scale request bursts.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider"
)

// Options configures an Engine.
type Options struct {
	// MaxConcurrency bounds simultaneous upstream queries across all domains.
	MaxConcurrency int
	// RequestTimeout bounds a single provider call.
	RequestTimeout time.Duration
	// CacheTTL holds per-outcome cache lifetimes.
	CacheTTL CacheTTL
}

// Engine answers availability questions from a chain of providers.
type Engine struct {
	// primary is an ordered fallback chain of authoritative sources: the first
	// one that produces a conclusive answer wins.
	primary []provider.Provider
	// corroborating sources run alongside and only add evidence.
	corroborating []provider.Provider

	opts   Options
	cache  *resultCache
	group  singleflight.Group
	slots  chan struct{}
	logger *slog.Logger
}

// New builds an Engine. primary must be ordered by authority (RDAP first).
func New(primary, corroborating []provider.Provider, opts Options, logger *slog.Logger) *Engine {
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = 8
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 10 * time.Second
	}
	return &Engine{
		primary:       primary,
		corroborating: corroborating,
		opts:          opts,
		cache:         newResultCache(opts.CacheTTL),
		slots:         make(chan struct{}, opts.MaxConcurrency),
		logger:        logger,
	}
}

// Check resolves availability for a single domain.
func (e *Engine) Check(ctx context.Context, input string, includeRaw bool) core.CheckResult {
	name, err := domainname.Parse(input)
	if err != nil {
		return core.CheckResult{
			Domain:       input,
			Availability: core.AvailabilityUnknown,
			Status:       core.StatusUnknown,
			Confidence:   core.ConfidenceLow,
			Evidence:     []string{"input:invalid"},
			CheckedAt:    time.Now().UTC(),
			Error:        err.Error(),
		}
	}

	if cached, ok := e.cache.Get(name.ASCII); ok {
		cached.Domain = input
		return stripRaw(cached, includeRaw)
	}

	// Agents routinely ask for the same candidate twice in one batch.
	value, _, _ := e.group.Do(name.ASCII, func() (any, error) {
		result := e.check(ctx, name)
		e.cache.Put(name.ASCII, result)
		return result, nil
	})

	result := value.(core.CheckResult)
	result.Domain = input
	return stripRaw(result, includeRaw)
}

// CheckMany resolves a batch of domains in parallel under the shared
// concurrency budget. A failure on one domain never fails the batch.
func (e *Engine) CheckMany(ctx context.Context, inputs []string, includeRaw bool) []core.CheckResult {
	results := make([]core.CheckResult, len(inputs))
	var wg sync.WaitGroup
	for i, input := range inputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = e.Check(ctx, input, includeRaw)
		}()
	}
	wg.Wait()
	return results
}

// Info returns registration metadata, independent of the availability verdict.
func (e *Engine) Info(ctx context.Context, input string) (*core.DomainInfo, error) {
	name, err := domainname.Parse(input)
	if err != nil {
		return nil, err
	}

	result := e.check(ctx, name)
	for _, source := range result.Sources {
		if source.Info != nil {
			info := *source.Info
			info.Domain = input
			return &info, nil
		}
	}

	if result.Availability == core.AvailabilityAvailable {
		return &core.DomainInfo{
			Domain:           input,
			NormalizedDomain: name.ASCII,
			Registered:       false,
			Source:           firstSource(result.Sources),
			Provider:         firstProvider(result.Sources),
		}, nil
	}
	return nil, fmt.Errorf("no registration data available for %s (status %s)", name.ASCII, result.Status)
}

// check runs the providers for one name and reconciles their answers.
func (e *Engine) check(ctx context.Context, name domainname.Name) core.CheckResult {
	var (
		mu      sync.Mutex
		results []core.ProviderResult
	)
	appendResult := func(result core.ProviderResult) {
		mu.Lock()
		defer mu.Unlock()
		results = append(results, result)
	}

	group, groupCtx := errgroup.WithContext(ctx)

	for _, p := range e.corroborating {
		group.Go(func() error {
			if !p.Supports(groupCtx, name) {
				return nil
			}
			result, err := e.run(groupCtx, p, name)
			if err != nil {
				e.logger.Debug("corroborating provider failed",
					"provider", p.Name(), "domain", name.ASCII, "error", err)
				return nil
			}
			appendResult(result)
			return nil
		})
	}

	group.Go(func() error {
		for _, p := range e.primary {
			if !p.Supports(groupCtx, name) {
				continue
			}
			result, err := e.run(groupCtx, p, name)
			if err != nil {
				e.logger.Debug("primary provider failed",
					"provider", p.Name(), "domain", name.ASCII, "error", err)
				appendResult(core.ProviderResult{
					Provider:           p.Name(),
					RegistrationStatus: core.RegistrationUnknown,
					Status:             core.StatusUnknown,
					Evidence:           []string{provider.Evidence(p.Name(), "query_failed")},
					Error:              err.Error(),
				})
				continue
			}
			appendResult(result)
			// Move on to the next source only when this one was inconclusive.
			if result.RegistrationStatus != core.RegistrationUnknown {
				return nil
			}
		}
		return nil
	})

	_ = group.Wait()

	resolved := resolve(results)
	return core.CheckResult{
		Domain:           name.Input,
		NormalizedDomain: name.ASCII,
		TLD:              name.TLD,
		Availability:     resolved.Availability,
		Status:           resolved.Status,
		Confidence:       resolved.Confidence,
		Evidence:         resolved.Evidence,
		Conflict:         resolved.Conflict,
		Sources:          results,
		CheckedAt:        time.Now().UTC(),
	}
}

// run executes one provider call under the concurrency and timeout budget.
func (e *Engine) run(ctx context.Context, p provider.Provider, name domainname.Name) (core.ProviderResult, error) {
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-ctx.Done():
		return core.ProviderResult{}, ctx.Err()
	}

	callCtx, cancel := context.WithTimeout(ctx, e.opts.RequestTimeout)
	defer cancel()

	result, err := p.Check(callCtx, name)
	if err != nil {
		return core.ProviderResult{}, err
	}
	if result.Provider == "" {
		result.Provider = p.Name()
	}
	return result, nil
}

// PurgeCache drops expired cache entries; callers run it periodically.
func (e *Engine) PurgeCache() { e.cache.Purge() }

// stripRaw removes bulky provider payloads unless the caller asked for them.
func stripRaw(result core.CheckResult, includeRaw bool) core.CheckResult {
	sources := make([]core.ProviderResult, len(result.Sources))
	copy(sources, result.Sources)
	if !includeRaw {
		for i := range sources {
			sources[i].Raw = nil
		}
	}
	result.Sources = sources
	return result
}

func firstSource(sources []core.ProviderResult) string {
	for _, source := range sources {
		if source.Source != "" {
			return source.Source
		}
	}
	return ""
}

func firstProvider(sources []core.ProviderResult) string {
	if len(sources) > 0 {
		return sources[0].Provider
	}
	return ""
}
