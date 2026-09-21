package whois

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider"
	"github.com/kr-ilya/domain-lens-mcp/internal/ratelimit"
)

// ProviderName identifies this source in results and evidence tags.
const ProviderName = "whois"

// maxResponseBytes caps a WHOIS response; real answers are a few KB.
const maxResponseBytes = 512 << 10

// dialFunc lets tests substitute an in-process WHOIS server.
type dialFunc func(ctx context.Context, address string) (net.Conn, error)

// Provider implements provider.Provider on top of the port 43 WHOIS protocol.
type Provider struct {
	registry *serverRegistry
	limiter  *ratelimit.HostLimiter
	dial     dialFunc
	logger   *slog.Logger
}

// New builds a WHOIS provider using the default dialer.
func New(limiter *ratelimit.HostLimiter, timeout time.Duration, logger *slog.Logger) *Provider {
	dial := func(ctx context.Context, address string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: timeout}
		return dialer.DialContext(ctx, "tcp", address)
	}
	return &Provider{registry: newServerRegistry(dial), limiter: limiter, dial: dial, logger: logger}
}

func (p *Provider) Name() string { return ProviderName }

// Supports reports whether IANA knows a WHOIS server for the name's TLD.
func (p *Provider) Supports(ctx context.Context, name domainname.Name) bool {
	server, err := p.registry.Server(ctx, name.TLD)
	if err != nil {
		p.logger.Debug("whois server discovery failed", "tld", name.TLD, "error", err)
		return false
	}
	return server != ""
}

// Check queries the TLD's WHOIS server and classifies the free-form response.
func (p *Provider) Check(ctx context.Context, name domainname.Name) (core.ProviderResult, error) {
	started := time.Now()

	server, err := p.registry.Server(ctx, name.TLD)
	if err != nil {
		return core.ProviderResult{}, err
	}
	if server == "" {
		return core.ProviderResult{}, fmt.Errorf("whois: no server for .%s", name.TLD)
	}

	if err := p.limiter.Wait(ctx, server); err != nil {
		var open *ratelimit.ErrCircuitOpen
		if errors.As(err, &open) {
			return core.ProviderResult{
				Provider:           ProviderName,
				RegistrationStatus: core.RegistrationUnknown,
				Status:             core.StatusRateLimited,
				Evidence:           []string{provider.Evidence(ProviderName, "circuit_open")},
				LatencyMS:          time.Since(started).Milliseconds(),
				Error:              err.Error(),
			}, nil
		}
		return core.ProviderResult{}, err
	}

	body, err := query(ctx, p.dial, server, name.ASCII)
	if err != nil {
		p.limiter.ReportFailure(server)
		return core.ProviderResult{}, err
	}
	p.limiter.ReportSuccess(server)

	result := classify(body, name, server)
	result.LatencyMS = time.Since(started).Milliseconds()
	return result, nil
}

// query performs one WHOIS exchange: send the query line, read until EOF.
func query(ctx context.Context, dial dialFunc, server, request string) (string, error) {
	conn, err := dial(ctx, net.JoinHostPort(server, "43"))
	if err != nil {
		return "", fmt.Errorf("whois: dial %s: %w", server, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// Stop the read if the caller cancels without a deadline.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-done:
		}
	}()

	writer := bufio.NewWriter(conn)
	if _, err := writer.WriteString(request + "\r\n"); err != nil {
		return "", fmt.Errorf("whois: write to %s: %w", server, err)
	}
	if err := writer.Flush(); err != nil {
		return "", fmt.Errorf("whois: flush to %s: %w", server, err)
	}

	body, err := io.ReadAll(io.LimitReader(conn, maxResponseBytes))
	if err != nil && len(body) == 0 {
		return "", fmt.Errorf("whois: read from %s: %w", server, err)
	}
	return string(body), nil
}
