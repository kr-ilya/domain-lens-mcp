package engine

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/kr-ilya/domain-lens-mcp/internal/core"
	"github.com/kr-ilya/domain-lens-mcp/internal/domainname"
)

// SuggestRequest describes a deterministic candidate expansion. Inventing the
// base names is the agent's job; this engine only expands and verifies them.
type SuggestRequest struct {
	// Names are the base ideas, e.g. ["datalens", "analytics"].
	Names []string
	// TLDs are tried in the given order, which also drives ranking.
	TLDs []string
	// Prefixes and Suffixes are glued to each base name, e.g. "get" or "hq".
	Prefixes []string
	Suffixes []string
	// Hyphenate also tries prefix/suffix variants joined by a hyphen.
	Hyphenate bool
	// TLDHacks splits a base name so its tail becomes the TLD, e.g. analyt.ics.
	TLDHacks bool
	// MaxCandidates caps how many domains are actually queried upstream.
	MaxCandidates int
	// OnlyAvailable drops everything that is not confirmed available.
	OnlyAvailable bool
	// Limit caps how many results are returned after ranking.
	Limit int
}

// SuggestResult is the outcome of a suggestion run.
type SuggestResult struct {
	Results []core.CheckResult `json:"results"`
	// Generated is how many unique candidates the expansion produced.
	Generated int `json:"generated"`
	// Checked is how many of them were actually queried.
	Checked int `json:"checked"`
	// Truncated reports that MaxCandidates cut the candidate list short.
	Truncated bool `json:"truncated"`
}

// candidate carries the ranking inputs alongside the domain to check.
type candidate struct {
	domain string
	// tldRank is the position of the TLD in the request, lower is better.
	tldRank int
	// exactBase marks a candidate that uses a base name with no affixes.
	exactBase  bool
	hyphenated bool
}

// Suggest expands base names into domain candidates, checks them and returns
// the ranked survivors.
func (e *Engine) Suggest(ctx context.Context, req SuggestRequest) SuggestResult {
	candidates := expand(req)

	result := SuggestResult{Generated: len(candidates)}
	if req.MaxCandidates > 0 && len(candidates) > req.MaxCandidates {
		candidates = candidates[:req.MaxCandidates]
		result.Truncated = true
	}
	result.Checked = len(candidates)

	domains := make([]string, len(candidates))
	for i, c := range candidates {
		domains[i] = c.domain
	}
	checks := e.CheckMany(ctx, domains, false)

	kept := make([]core.CheckResult, 0, len(checks))
	keptCandidates := make([]candidate, 0, len(checks))
	for i, check := range checks {
		if req.OnlyAvailable && check.Availability != core.AvailabilityAvailable {
			continue
		}
		kept = append(kept, check)
		keptCandidates = append(keptCandidates, candidates[i])
	}

	order := make([]int, len(kept))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return less(keptCandidates[order[a]], keptCandidates[order[b]])
	})

	limit := req.Limit
	if limit <= 0 || limit > len(order) {
		limit = len(order)
	}
	result.Results = make([]core.CheckResult, 0, limit)
	for _, idx := range order[:limit] {
		result.Results = append(result.Results, kept[idx])
	}
	return result
}

// less ranks candidates: requested TLD order first, then plain base names,
// then shorter and un-hyphenated ones.
func less(a, b candidate) bool {
	if a.tldRank != b.tldRank {
		return a.tldRank < b.tldRank
	}
	if a.exactBase != b.exactBase {
		return a.exactBase
	}
	if a.hyphenated != b.hyphenated {
		return b.hyphenated
	}
	if len(a.domain) != len(b.domain) {
		return len(a.domain) < len(b.domain)
	}
	return a.domain < b.domain
}

// expand builds the deduplicated candidate list in a deterministic order.
func expand(req SuggestRequest) []candidate {
	tlds := make([]string, 0, len(req.TLDs))
	for _, raw := range req.TLDs {
		tld, err := domainname.NormalizeTLD(raw)
		if err != nil || slices.Contains(tlds, tld) {
			continue
		}
		tlds = append(tlds, tld)
	}

	seen := make(map[string]struct{})
	candidates := make([]candidate, 0, len(req.Names)*len(tlds))
	add := func(label, tld string, tldRank int, exactBase, hyphenated bool) {
		name, err := domainname.Parse(label + "." + tld)
		if err != nil || !name.Registrable() {
			return
		}
		if _, ok := seen[name.ASCII]; ok {
			return
		}
		seen[name.ASCII] = struct{}{}
		candidates = append(candidates, candidate{
			domain:     name.ASCII,
			tldRank:    tldRank,
			exactBase:  exactBase,
			hyphenated: hyphenated,
		})
	}

	for _, rawName := range req.Names {
		base := normalizeLabel(rawName)
		if base == "" {
			continue
		}
		for rank, tld := range tlds {
			add(base, tld, rank, true, false)
			for _, prefix := range req.Prefixes {
				prefix = normalizeLabel(prefix)
				if prefix == "" {
					continue
				}
				add(prefix+base, tld, rank, false, false)
				if req.Hyphenate {
					add(prefix+"-"+base, tld, rank, false, true)
				}
			}
			for _, suffix := range req.Suffixes {
				suffix = normalizeLabel(suffix)
				if suffix == "" {
					continue
				}
				add(base+suffix, tld, rank, false, false)
				if req.Hyphenate {
					add(base+"-"+suffix, tld, rank, false, true)
				}
			}
		}
		if req.TLDHacks {
			for _, hack := range tldHacks(base, tlds) {
				add(hack.label, hack.tld, len(tlds), false, false)
			}
		}
	}
	return candidates
}

// tldHacks yields splits of the base name whose tail is one of the requested
// TLDs, e.g. base "analytics" with TLD "ics" gives "analyt.ics".
func tldHacks(base string, tlds []string) []tldHack {
	hacks := make([]tldHack, 0, len(tlds))
	for _, tld := range tlds {
		if len(base) <= len(tld) || !strings.HasSuffix(base, tld) {
			continue
		}
		label := base[:len(base)-len(tld)]
		if strings.HasSuffix(label, "-") {
			continue
		}
		hacks = append(hacks, tldHack{label: label, tld: tld})
	}
	return hacks
}

// tldHack is a base name split into a label and the TLD it ends with.
type tldHack struct {
	label string
	tld   string
}

// normalizeLabel reduces a free-form idea to a usable DNS label body: lowercase,
// no spaces or underscores, no leading or trailing hyphens.
func normalizeLabel(input string) string {
	label := strings.ToLower(strings.TrimSpace(input))
	label = strings.NewReplacer(" ", "", "_", "-", ".", "").Replace(label)
	return strings.Trim(label, "-")
}
