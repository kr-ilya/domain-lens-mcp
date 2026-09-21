// Package rdap queries registry RDAP services, the authoritative source for
// whether a registration object exists for a domain.
package rdap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider"
	"github.com/kr-ilya/domain-lens-mcp/internal/ratelimit"
)

// ProviderName identifies this source in results and evidence tags.
const ProviderName = "rdap"

// maxBodyBytes caps RDAP responses; registry payloads are a few KB at most.
const maxBodyBytes = 2 << 20

// Provider implements provider.Provider on top of RDAP.
type Provider struct {
	bootstrap  *Bootstrap
	httpClient *http.Client
	limiter    *ratelimit.HostLimiter
	logger     *slog.Logger
	maxRetries int
}

// New builds an RDAP provider. The limiter is shared per RDAP host so that a
// burst of agent lookups cannot hammer a single registry.
func New(bootstrap *Bootstrap, httpClient *http.Client, limiter *ratelimit.HostLimiter, maxRetries int, logger *slog.Logger) *Provider {
	return &Provider{
		bootstrap:  bootstrap,
		httpClient: httpClient,
		limiter:    limiter,
		logger:     logger,
		maxRetries: maxRetries,
	}
}

func (p *Provider) Name() string { return ProviderName }

// Supports reports whether IANA lists an RDAP service for the name's TLD.
func (p *Provider) Supports(ctx context.Context, name domainname.Name) bool {
	return len(p.bootstrap.BaseURLs(ctx, name.TLD)) > 0
}

// Check asks the registry whether a registration object exists. A 404 is the
// authoritative "no registration"; a 200 is parsed for EPP statuses.
func (p *Provider) Check(ctx context.Context, name domainname.Name) (core.ProviderResult, error) {
	bases := p.bootstrap.BaseURLs(ctx, name.TLD)
	if len(bases) == 0 {
		return core.ProviderResult{}, fmt.Errorf("rdap: no service for .%s", name.TLD)
	}

	started := time.Now()
	var lastErr error
	for _, base := range bases {
		result, err := p.query(ctx, base, name)
		if err == nil {
			result.LatencyMS = time.Since(started).Milliseconds()
			return result, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		p.logger.Debug("rdap base failed, trying next", "base", base, "domain", name.ASCII, "error", err)
	}

	var limited *ratelimit.ErrRateLimited
	if errors.As(lastErr, &limited) {
		return core.ProviderResult{
			Provider:           ProviderName,
			RegistrationStatus: core.RegistrationUnknown,
			Status:             core.StatusRateLimited,
			Evidence:           []string{provider.Evidence(ProviderName, "rate_limited")},
			LatencyMS:          time.Since(started).Milliseconds(),
			Error:              lastErr.Error(),
		}, nil
	}
	return core.ProviderResult{}, lastErr
}

func (p *Provider) query(ctx context.Context, base string, name domainname.Name) (core.ProviderResult, error) {
	endpoint, err := url.JoinPath(base, "domain", name.ASCII)
	if err != nil {
		return core.ProviderResult{}, fmt.Errorf("rdap: bad base %q: %w", base, err)
	}
	host := hostOf(base)

	var lastErr error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if err := p.limiter.Wait(ctx, host); err != nil {
			return core.ProviderResult{}, err
		}

		result, retryAfter, err := p.do(ctx, endpoint, host, name)
		if err == nil {
			p.limiter.ReportSuccess(host)
			return result, nil
		}
		lastErr = err
		if !isRetryable(err) || attempt == p.maxRetries || ctx.Err() != nil {
			p.limiter.ReportFailure(host)
			break
		}
		if err := ratelimit.Sleep(ctx, ratelimit.Backoff(attempt, retryAfter)); err != nil {
			return core.ProviderResult{}, err
		}
	}
	return core.ProviderResult{}, lastErr
}

// do performs one RDAP request and maps the response onto a ProviderResult.
func (p *Provider) do(ctx context.Context, endpoint, host string, name domainname.Name) (core.ProviderResult, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return core.ProviderResult{}, 0, err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json;q=0.9")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return core.ProviderResult{}, 0, &retryableError{err: err}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		if err != nil {
			return core.ProviderResult{}, 0, &retryableError{err: err}
		}
		result, err := parseResponse(body, name, endpoint)
		if err != nil {
			return core.ProviderResult{}, 0, err
		}
		return result, 0, nil

	case http.StatusNotFound:
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		return core.ProviderResult{
			Provider:           ProviderName,
			RegistrationStatus: core.RegistrationNotRegistered,
			Status:             core.StatusAvailable,
			Evidence:           []string{provider.Evidence(ProviderName, "no_registration")},
			Source:             endpoint,
		}, 0, nil

	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		return core.ProviderResult{}, retryAfter, &retryableError{
			err: &ratelimit.ErrRateLimited{Host: host, Status: resp.StatusCode, RetryAfter: retryAfter},
		}

	default:
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		return core.ProviderResult{}, 0, fmt.Errorf("rdap: %s returned status %d", host, resp.StatusCode)
	}
}

func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	return u.Host
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func isRetryable(err error) bool {
	var r *retryableError
	return errors.As(err, &r)
}
