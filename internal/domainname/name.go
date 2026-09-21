// Package domainname normalizes user-supplied domain names into the ASCII form
// registries expect, and extracts the registry suffix used to route lookups.
package domainname

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// ErrInvalid is returned for input that cannot be a registrable domain name.
var ErrInvalid = errors.New("invalid domain name")

// Name is a normalized domain ready to be queried.
type Name struct {
	// Input is the original string as supplied by the caller.
	Input string
	// ASCII is the lowercased punycode form, e.g. "xn--80ak6aa92e.com".
	ASCII string
	// Unicode is the display form, e.g. "аpple.com".
	Unicode string
	// TLD is the last label of ASCII, used for RDAP/WHOIS routing.
	TLD string
	// RegistrySuffix is the public suffix, e.g. "co.uk" for "foo.co.uk".
	RegistrySuffix string
	// SLD is the label registered under RegistrySuffix.
	SLD string
}

// Registrable reports whether the name is exactly one label under its public
// suffix — the only shape that can be registered directly.
func (n Name) Registrable() bool {
	return n.ASCII == n.SLD+"."+n.RegistrySuffix
}

// profile is UTS-46 with transitional processing disabled, matching how modern
// registries and browsers map Unicode domains to punycode.
var profile = idna.New(
	idna.MapForLookup(),
	idna.StrictDomainName(true),
	idna.Transitional(false),
	idna.BidiRule(),
)

// Parse normalizes a domain name: it trims scheme, path, whitespace and a
// trailing dot, maps Unicode to punycode, and derives the registry suffix.
func Parse(input string) (Name, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return Name{}, fmt.Errorf("%w: empty", ErrInvalid)
	}

	cleaned := stripURLParts(raw)
	cleaned = strings.TrimSuffix(cleaned, ".")
	if cleaned == "" {
		return Name{}, fmt.Errorf("%w: %q", ErrInvalid, input)
	}

	ascii, err := profile.ToASCII(cleaned)
	if err != nil {
		return Name{}, fmt.Errorf("%w: %q: %v", ErrInvalid, input, err)
	}
	ascii = strings.ToLower(ascii)

	if !strings.Contains(ascii, ".") {
		return Name{}, fmt.Errorf("%w: %q has no TLD", ErrInvalid, input)
	}
	if len(ascii) > 253 {
		return Name{}, fmt.Errorf("%w: %q is too long", ErrInvalid, input)
	}
	// idna accepts empty labels, which no registry does.
	for label := range strings.SplitSeq(ascii, ".") {
		if label == "" || len(label) > 63 {
			return Name{}, fmt.Errorf("%w: %q has an invalid label", ErrInvalid, input)
		}
	}

	unicode, err := profile.ToUnicode(ascii)
	if err != nil {
		unicode = ascii
	}

	suffix, _ := publicsuffix.PublicSuffix(ascii)
	if suffix == ascii {
		return Name{}, fmt.Errorf("%w: %q is a public suffix, not a domain", ErrInvalid, input)
	}

	sld := strings.TrimSuffix(ascii, "."+suffix)
	if i := strings.LastIndex(sld, "."); i >= 0 {
		sld = sld[i+1:]
	}

	tld := ascii[strings.LastIndex(ascii, ".")+1:]
	if tld == "" {
		return Name{}, fmt.Errorf("%w: %q", ErrInvalid, input)
	}

	return Name{
		Input:          input,
		ASCII:          ascii,
		Unicode:        unicode,
		TLD:            tld,
		RegistrySuffix: suffix,
		SLD:            sld,
	}, nil
}

// NormalizeTLD turns user input like ".COM", "com" or "рф" into an ASCII TLD label.
func NormalizeTLD(input string) (string, error) {
	tld := strings.ToLower(strings.TrimSpace(input))
	tld = strings.TrimPrefix(tld, ".")
	if tld == "" {
		return "", fmt.Errorf("%w: empty tld", ErrInvalid)
	}
	ascii, err := profile.ToASCII(tld)
	if err != nil {
		return "", fmt.Errorf("%w: tld %q: %v", ErrInvalid, input, err)
	}
	return strings.ToLower(ascii), nil
}

// stripURLParts removes anything around the host part so that pasted URLs and
// emails still resolve to the domain the user meant.
func stripURLParts(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	// Drop a port, but leave IPv6-looking input to fail validation later.
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s, "]") {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
