// Package provider defines the contract every availability data source implements.
package provider

import (
	"context"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
)

// Provider is a single source of domain registration information.
//
// Implementations must not decide availability policy: they report what their
// source says and leave conflict resolution to the engine's resolver.
type Provider interface {
	// Name is the stable identifier used in evidence tags and results.
	Name() string
	// Supports reports whether this provider can answer for the given name.
	Supports(ctx context.Context, name domainname.Name) bool
	// Check queries the source. A returned error means the query itself failed;
	// an inconclusive but successful query returns RegistrationUnknown instead.
	Check(ctx context.Context, name domainname.Name) (core.ProviderResult, error)
}

// Evidence builds a provider-scoped evidence tag, e.g. "rdap:no_registration".
func Evidence(provider, tag string) string {
	return provider + ":" + tag
}
