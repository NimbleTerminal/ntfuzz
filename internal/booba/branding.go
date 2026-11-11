package booba

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"strings"
	"unicode"

	"github.com/spf13/pflag"

	"nimbleterminal.dev/ntfuzz/internal/version"
)

const (
	// NTFuzzRepoURL is the canonical upstream repository URL linked from the NTFUZZ wordmark.
	NTFuzzRepoURL = "https://github.com/NimbleTerminal/ntfuzz"

	// DefaultAccentColor is the default header accent color when no override is configured.
	DefaultAccentColor = "#38bdf8"

	// Environment variable names for operator branding configuration.
	EnvBrandName   = "NTFUZZ_BRAND_NAME"
	EnvBrandURL    = "NTFUZZ_BRAND_URL"
	EnvBrandAccent = "NTFUZZ_BRAND_ACCENT"
)

// Branding configures optional operator identity and accent styling for the
// browser dashboard header. All fields are optional.
type Branding struct {
	Name        string
	URL         string
	AccentColor string
}

// AddBrandingFlags registers consistent CLI branding flags on fs for both
// "serve" and "view" commands.
func AddBrandingFlags(fs *pflag.FlagSet, dst *Branding) {
	if fs == nil || dst == nil {
		return
	}
	fs.StringVar(&dst.Name, "brand-name", "", "Optional operator or fleet display name shown beside NTFUZZ in the web header (or $"+EnvBrandName+")")
	fs.StringVar(&dst.URL, "brand-url", "", "Optional HTTP/HTTPS website URL for the operator display name (or $"+EnvBrandURL+")")
	fs.StringVar(&dst.AccentColor, "brand-accent", "", "Optional header accent color in #RRGGBB format (or $"+EnvBrandAccent+")")
}

// ResolveBranding resolves operator branding settings using precedence:
//  1. Explicit CLI flags (--brand-name, --brand-url, --brand-accent)
//  2. Environment variables (NTFUZZ_BRAND_NAME, NTFUZZ_BRAND_URL, NTFUZZ_BRAND_ACCENT)
//  3. Built-in defaults (unconfigured operator identity, default accent #38bdf8)
func ResolveBranding(flags Branding, getenv func(string) string) (Branding, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	resolved := Branding{
		Name:        firstNonEmpty(flags.Name, getenv(EnvBrandName)),
		URL:         firstNonEmpty(flags.URL, getenv(EnvBrandURL)),
		AccentColor: firstNonEmpty(flags.AccentColor, getenv(EnvBrandAccent)),
	}
	return ValidateBranding(resolved)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ValidateBranding validates and normalizes operator branding settings.
func ValidateBranding(b Branding) (Branding, error) {
	var out Branding

	name := strings.TrimSpace(b.Name)
	for _, r := range name {
		if unicode.IsControl(r) {
			return Branding{}, errors.New("invalid brand name: control characters are not allowed")
		}
	}
	out.Name = name

	rawURL := strings.TrimSpace(b.URL)
	if rawURL != "" {
		if err := validateHTTPURL(rawURL); err != nil {
			return Branding{}, err
		}
		out.URL = rawURL
	}

	rawAccent := strings.TrimSpace(b.AccentColor)
	if rawAccent != "" {
		normalized, err := validateHexColor(rawAccent)
		if err != nil {
			return Branding{}, err
		}
		out.AccentColor = normalized
	}

	return out, nil
}

func validateHTTPURL(raw string) error {
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("invalid brand URL %q: whitespace and control characters are not allowed", raw)
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid brand URL %q: %w", raw, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("invalid brand URL %q: scheme must be http or https", raw)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return fmt.Errorf("invalid brand URL %q: host is required", raw)
	}
	if parsed.User != nil {
		return fmt.Errorf("invalid brand URL %q: embedded credentials are not allowed", raw)
	}
	return nil
}

func validateHexColor(raw string) (string, error) {
	if len(raw) != 7 || raw[0] != '#' {
		return "", fmt.Errorf("invalid brand accent color %q: expected #RRGGBB hex format", raw)
	}
	for i := 1; i < 7; i++ {
		c := raw[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return "", fmt.Errorf("invalid brand accent color %q: expected #RRGGBB hex format", raw)
		}
	}
	return strings.ToLower(raw), nil
}

const (
	defaultTopbarTag  = `<header class="topbar">`
	defaultWordmark   = `<a class="brand-wordmark" href="https://github.com/NimbleTerminal/ntfuzz" target="_blank" rel="noopener noreferrer">NTFUZZ</a>`
	defaultVersionTag = `<span class="brand-version">dev</span>`
)

// RenderIndexHTML validates branding and returns the rendered dashboard HTML page.
// Operator text is always HTML-escaped as plain text; URLs are restricted to
// validated HTTP/HTTPS links and accent colors to #RRGGBB on a scoped CSS variable.
func RenderIndexHTML(b Branding) ([]byte, error) {
	clean, err := ValidateBranding(b)
	if err != nil {
		return nil, err
	}

	out := bytes.Replace(
		embeddedIndexHTML,
		[]byte(defaultVersionTag),
		[]byte(fmt.Sprintf(`<span class="brand-version">%s</span>`, html.EscapeString(version.Current()))),
		1,
	)

	if clean.AccentColor != "" {
		styledTopbar := fmt.Sprintf(`<header class="topbar" style="--ntfuzz-accent: %s;">`, html.EscapeString(clean.AccentColor))
		out = bytes.Replace(out, []byte(defaultTopbarTag), []byte(styledTopbar), 1)
	}

	if clean.Name != "" {
		escapedName := html.EscapeString(clean.Name)
		var operatorHTML string
		if clean.URL != "" {
			escapedURL := html.EscapeString(clean.URL)
			operatorHTML = fmt.Sprintf(
				"%s\n            <span class=\"brand-sep\" aria-hidden=\"true\">/</span>\n            <a class=\"brand-operator\" href=\"%s\" target=\"_blank\" rel=\"noopener noreferrer\" title=\"%s\">%s</a>",
				defaultWordmark,
				escapedURL,
				escapedName,
				escapedName,
			)
		} else {
			operatorHTML = fmt.Sprintf(
				"%s\n            <span class=\"brand-sep\" aria-hidden=\"true\">/</span>\n            <span class=\"brand-operator\" title=\"%s\">%s</span>",
				defaultWordmark,
				escapedName,
				escapedName,
			)
		}
		out = bytes.Replace(out, []byte(defaultWordmark), []byte(operatorHTML), 1)
	}

	return out, nil
}
