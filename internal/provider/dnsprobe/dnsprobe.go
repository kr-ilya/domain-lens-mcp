// Package dnsprobe adds a corroborating DNS signal. Delegation proves a domain
// is taken; the absence of delegation proves nothing, so this provider never
// reports "not registered".
package dnsprobe

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
	"github.com/kr-ilya/domain-lens-mcp/internal/provider"
)

// ProviderName identifies this source in results and evidence tags.
const ProviderName = "dns"

// Provider implements provider.Provider on top of NS lookups.
type Provider struct {
	resolver *net.Resolver
	timeout  time.Duration
}

// New builds a DNS probe using the system resolver.
func New(timeout time.Duration) *Provider {
	return &Provider{resolver: net.DefaultResolver, timeout: timeout}
}

func (p *Provider) Name() string { return ProviderName }

// Supports is true for every name: DNS works regardless of TLD.
func (p *Provider) Supports(context.Context, domainname.Name) bool { return true }

// Check looks up NS records for the domain.
func (p *Provider) Check(ctx context.Context, name domainname.Name) (core.ProviderResult, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	started := time.Now()
	result := core.ProviderResult{Provider: ProviderName, Source: "system-resolver"}

	records, err := p.resolver.LookupNS(ctx, name.ASCII)
	result.LatencyMS = time.Since(started).Milliseconds()

	switch {
	case err == nil && len(records) > 0:
		result.RegistrationStatus = core.RegistrationRegistered
		result.Status = core.StatusRegistered
		result.Evidence = []string{provider.Evidence(ProviderName, "delegated")}
		hosts := make([]string, 0, len(records))
		for _, ns := range records {
			hosts = append(hosts, ns.Host)
		}
		result.Raw = hosts

	case err == nil, isNotFound(err):
		// NXDOMAIN is weak evidence: parked, expired and never-registered names
		// all look the same here.
		result.RegistrationStatus = core.RegistrationUnknown
		result.Status = core.StatusUnknown
		result.Evidence = []string{provider.Evidence(ProviderName, "no_delegation")}

	default:
		result.RegistrationStatus = core.RegistrationUnknown
		result.Status = core.StatusUnknown
		result.Evidence = []string{provider.Evidence(ProviderName, "lookup_failed")}
		if err != nil {
			result.Error = err.Error()
		}
	}
	return result, nil
}

func isNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}
