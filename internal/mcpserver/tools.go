package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/engine"
)

// maxBatchSize bounds one check_domains call so a single agent turn cannot
// queue an unbounded number of upstream queries.
const maxBatchSize = 100

// maxSuggestCandidates bounds how many generated candidates may be verified.
const maxSuggestCandidates = 250

// CheckDomainInput is the input of the check_domain tool.
type CheckDomainInput struct {
	Domain     string `json:"domain" jsonschema:"Domain name to check, e.g. example.com. Unicode and pasted URLs are accepted and normalized."`
	IncludeRaw bool   `json:"include_raw,omitempty" jsonschema:"Include the untouched provider payloads in sources[].raw. Useful for debugging disagreements between sources."`
}

// CheckDomainsInput is the input of the check_domains tool.
type CheckDomainsInput struct {
	Domains       []string `json:"domains" jsonschema:"Domain names to check, at most 100 per call."`
	OnlyAvailable bool     `json:"only_available,omitempty" jsonschema:"Return only domains resolved as available."`
	IncludeRaw    bool     `json:"include_raw,omitempty" jsonschema:"Include the untouched provider payloads in sources[].raw."`
}

// CheckDomainsOutput is the output of the check_domains tool.
type CheckDomainsOutput struct {
	Results []core.CheckResult `json:"results"`
	Summary BatchSummary       `json:"summary"`
}

// BatchSummary counts the outcomes of a batch so an agent can react without
// walking every result.
type BatchSummary struct {
	Total       int `json:"total"`
	Available   int `json:"available"`
	Unavailable int `json:"unavailable"`
	Unknown     int `json:"unknown"`
}

// GetDomainInfoInput is the input of the get_domain_info tool.
type GetDomainInfoInput struct {
	Domain string `json:"domain" jsonschema:"Domain name to look up."`
}

// SuggestDomainsInput is the input of the suggest_domains tool.
type SuggestDomainsInput struct {
	Names         []string `json:"names" jsonschema:"Base name ideas to expand, e.g. [\"datalens\",\"analytics\"]. Invent these yourself; this tool only expands and verifies them."`
	TLDs          []string `json:"tlds" jsonschema:"TLDs to try, in priority order, e.g. [\"com\",\"io\",\"dev\"]."`
	Prefixes      []string `json:"prefixes,omitempty" jsonschema:"Optional prefixes glued to each base name, e.g. [\"get\",\"try\"]."`
	Suffixes      []string `json:"suffixes,omitempty" jsonschema:"Optional suffixes glued to each base name, e.g. [\"hq\",\"app\"]."`
	Hyphenate     bool     `json:"hyphenate,omitempty" jsonschema:"Also try hyphenated affix variants, e.g. get-datalens.com."`
	TLDHacks      bool     `json:"tld_hacks,omitempty" jsonschema:"Also try splitting a name so its tail becomes the TLD, e.g. analyt.ics."`
	MaxCandidates int      `json:"max_candidates,omitempty" jsonschema:"Maximum number of generated candidates to actually check (default 100, hard cap 250)."`
	Limit         int      `json:"limit,omitempty" jsonschema:"Maximum number of results to return after ranking (default 20)."`
	IncludeTaken  bool     `json:"include_taken,omitempty" jsonschema:"Also return candidates that are unavailable or unknown. Off by default."`
}

// SuggestDomainsOutput is the output of the suggest_domains tool.
type SuggestDomainsOutput struct {
	Results   []core.CheckResult `json:"results"`
	Generated int                `json:"generated"`
	Checked   int                `json:"checked"`
	Truncated bool               `json:"truncated"`
}

// availabilityNote is repeated in every tool description because conflating
// "unknown" with "available" is the most damaging mistake an agent can make here.
const availabilityNote = "availability is one of: available (no registration found), " +
	"unavailable (cannot be registered as-is right now), unknown (no source gave a trustworthy answer — " +
	"never treat this as available). status adds detail (registered, reserved, pending_delete, redemption, " +
	"rate_limited, ...), confidence is high/medium/low, and evidence lists the per-source findings."

func (s *Server) registerTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "check_domain",
		Description: "Check whether a single domain can be registered right now. " +
			"Uses registry RDAP as the authoritative source, WHOIS where RDAP is absent, and DNS as corroboration. " +
			availabilityNote,
	}, s.checkDomain)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "check_domains",
		Description: "Check up to 100 domains in one call. Prefer this over repeated check_domain calls: " +
			"it shares the cache, deduplicates and rate-limits upstream registries. " +
			availabilityNote,
	}, s.checkDomains)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_domain_info",
		Description: "Get registration details for a domain: registrar, registration and expiration dates, " +
			"EPP statuses and nameservers. Use check_domain instead when you only need availability.",
	}, s.getDomainInfo)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "suggest_domains",
		Description: "Expand your own base name ideas into domain candidates (TLD matrix, prefixes, suffixes, " +
			"hyphens, TLD hacks), check them all and return the available ones, ranked. " +
			"This tool does not invent names: pass the creative candidates in names.",
	}, s.suggestDomains)
}

func (s *Server) checkDomain(ctx context.Context, _ *mcp.CallToolRequest, in CheckDomainInput) (*mcp.CallToolResult, core.CheckResult, error) {
	if in.Domain == "" {
		return nil, core.CheckResult{}, fmt.Errorf("domain is required")
	}
	result := s.engine.Check(ctx, in.Domain, in.IncludeRaw)
	return textResult(result), result, nil
}

func (s *Server) checkDomains(ctx context.Context, _ *mcp.CallToolRequest, in CheckDomainsInput) (*mcp.CallToolResult, CheckDomainsOutput, error) {
	if len(in.Domains) == 0 {
		return nil, CheckDomainsOutput{}, fmt.Errorf("domains is required")
	}
	if len(in.Domains) > maxBatchSize {
		return nil, CheckDomainsOutput{}, fmt.Errorf("too many domains: %d, maximum is %d", len(in.Domains), maxBatchSize)
	}

	results := s.engine.CheckMany(ctx, in.Domains, in.IncludeRaw)

	out := CheckDomainsOutput{Summary: BatchSummary{Total: len(results)}}
	out.Results = make([]core.CheckResult, 0, len(results))
	for _, result := range results {
		switch result.Availability {
		case core.AvailabilityAvailable:
			out.Summary.Available++
		case core.AvailabilityUnavailable:
			out.Summary.Unavailable++
		default:
			out.Summary.Unknown++
		}
		if in.OnlyAvailable && result.Availability != core.AvailabilityAvailable {
			continue
		}
		out.Results = append(out.Results, result)
	}
	return textResult(out), out, nil
}

func (s *Server) getDomainInfo(ctx context.Context, _ *mcp.CallToolRequest, in GetDomainInfoInput) (*mcp.CallToolResult, core.DomainInfo, error) {
	if in.Domain == "" {
		return nil, core.DomainInfo{}, fmt.Errorf("domain is required")
	}
	info, err := s.engine.Info(ctx, in.Domain)
	if err != nil {
		return nil, core.DomainInfo{}, err
	}
	return textResult(info), *info, nil
}

func (s *Server) suggestDomains(ctx context.Context, _ *mcp.CallToolRequest, in SuggestDomainsInput) (*mcp.CallToolResult, SuggestDomainsOutput, error) {
	if len(in.Names) == 0 {
		return nil, SuggestDomainsOutput{}, fmt.Errorf("names is required")
	}
	if len(in.TLDs) == 0 {
		return nil, SuggestDomainsOutput{}, fmt.Errorf("tlds is required")
	}

	maxCandidates := in.MaxCandidates
	if maxCandidates <= 0 {
		maxCandidates = 100
	}
	maxCandidates = min(maxCandidates, maxSuggestCandidates)

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}

	result := s.engine.Suggest(ctx, engine.SuggestRequest{
		Names:         in.Names,
		TLDs:          in.TLDs,
		Prefixes:      in.Prefixes,
		Suffixes:      in.Suffixes,
		Hyphenate:     in.Hyphenate,
		TLDHacks:      in.TLDHacks,
		MaxCandidates: maxCandidates,
		OnlyAvailable: !in.IncludeTaken,
		Limit:         limit,
	})

	out := SuggestDomainsOutput{
		Results:   result.Results,
		Generated: result.Generated,
		Checked:   result.Checked,
		Truncated: result.Truncated,
	}
	return textResult(out), out, nil
}

// textResult mirrors the structured output as JSON text, for clients that do
// not consume structuredContent.
func textResult(payload any) *mcp.CallToolResult {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("failed to encode result: %v", err)}},
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}
