package domainname

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		ascii  string
		tld    string
		suffix string
		sld    string
	}{
		{name: "plain", input: "Example.COM", ascii: "example.com", tld: "com", suffix: "com", sld: "example"},
		{name: "trailing dot", input: "example.com.", ascii: "example.com", tld: "com", suffix: "com", sld: "example"},
		{name: "url", input: "https://example.com/path?q=1", ascii: "example.com", tld: "com", suffix: "com", sld: "example"},
		{name: "port", input: "example.com:8443", ascii: "example.com", tld: "com", suffix: "com", sld: "example"},
		{name: "email", input: "user@example.com", ascii: "example.com", tld: "com", suffix: "com", sld: "example"},
		{name: "multi-label suffix", input: "shop.example.co.uk", ascii: "shop.example.co.uk", tld: "uk", suffix: "co.uk", sld: "example"},
		{name: "idn cyrillic", input: "пример.рф", ascii: "xn--e1afmkfd.xn--p1ai", tld: "xn--p1ai", suffix: "xn--p1ai", sld: "xn--e1afmkfd"},
		{name: "idn german", input: "Müller.de", ascii: "xn--mller-kva.de", tld: "de", suffix: "de", sld: "xn--mller-kva"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tc.input, err)
			}
			if got.ASCII != tc.ascii {
				t.Errorf("ASCII = %q, want %q", got.ASCII, tc.ascii)
			}
			if got.TLD != tc.tld {
				t.Errorf("TLD = %q, want %q", got.TLD, tc.tld)
			}
			if got.RegistrySuffix != tc.suffix {
				t.Errorf("RegistrySuffix = %q, want %q", got.RegistrySuffix, tc.suffix)
			}
			if got.SLD != tc.sld {
				t.Errorf("SLD = %q, want %q", got.SLD, tc.sld)
			}
		})
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	for _, input := range []string{"", "   ", "localhost", "com", "co.uk", "exa mple.com", "-bad.com", "example..com"} {
		t.Run(input, func(t *testing.T) {
			if got, err := Parse(input); err == nil {
				t.Fatalf("Parse(%q) = %+v, want error", input, got)
			}
		})
	}
}

func TestRegistrable(t *testing.T) {
	registrable, err := Parse("example.co.uk")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !registrable.Registrable() {
		t.Errorf("example.co.uk should be registrable")
	}

	sub, err := Parse("shop.example.co.uk")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub.Registrable() {
		t.Errorf("shop.example.co.uk is a subdomain, not registrable")
	}
}

func TestNormalizeTLD(t *testing.T) {
	tests := map[string]string{
		".COM": "com",
		"io":   "io",
		" dev": "dev",
		"рф":   "xn--p1ai",
	}
	for input, want := range tests {
		got, err := NormalizeTLD(input)
		if err != nil {
			t.Fatalf("NormalizeTLD(%q) returned error: %v", input, err)
		}
		if got != want {
			t.Errorf("NormalizeTLD(%q) = %q, want %q", input, got, want)
		}
	}

	if _, err := NormalizeTLD("."); err == nil {
		t.Errorf("NormalizeTLD(\".\") should fail")
	}
}
