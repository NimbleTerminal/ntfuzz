package booba

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
	"unicode"
)

func FuzzValidateAndRenderBranding(f *testing.F) {
	f.Add("", "", "")
	f.Add("Nimble Terminal", "https://nimbleterminal.com", "#10b981")
	f.Add("Acme & <Friends>", "https://example.org/fuzz?team=core", "#38BDF8")
	f.Add("<script>alert(1)</script>", "javascript:alert(1)", "#123456")
	f.Add("BadUser", "https://user:pass@example.com", "#abcdef")
	f.Add("BadColor", "https://example.com", "#12345g")
	f.Add("Control\x00Name", "https://example.com", "#00ff00")

	f.Fuzz(func(t *testing.T, name, rawURL, accent string) {
		if len(name)+len(rawURL)+len(accent) > 16*1024 {
			return
		}
		input := Branding{
			Name:        name,
			URL:         rawURL,
			AccentColor: accent,
		}

		clean, valErr := ValidateBranding(input)
		rendered, renderErr := RenderIndexHTML(input)
		if (valErr == nil) != (renderErr == nil) {
			t.Fatalf("ValidateBranding err=%v and RenderIndexHTML err=%v disagreed", valErr, renderErr)
		}
		if valErr != nil {
			return
		}

		// Idempotence: validating an already validated Branding must return the exact same struct.
		cleanAgain, err := ValidateBranding(clean)
		if err != nil || cleanAgain != clean {
			t.Fatalf("ValidateBranding not idempotent: %+v vs %+v (err=%v)", clean, cleanAgain, err)
		}

		for _, r := range clean.Name {
			if unicode.IsControl(r) {
				t.Fatalf("clean.Name contains control character: %q", clean.Name)
			}
		}

		if clean.URL != "" {
			u, err := url.Parse(clean.URL)
			if err != nil {
				t.Fatalf("clean.URL failed url.Parse: %v", err)
			}
			scheme := strings.ToLower(u.Scheme)
			if scheme != "http" && scheme != "https" {
				t.Fatalf("clean.URL allowed non-http(s) scheme %q", u.Scheme)
			}
			if u.Host == "" || u.Hostname() == "" || u.User != nil {
				t.Fatalf("clean.URL allowed invalid host or userinfo: %q", clean.URL)
			}
		}

		if clean.AccentColor != "" {
			if len(clean.AccentColor) != 7 || clean.AccentColor[0] != '#' || clean.AccentColor != strings.ToLower(clean.AccentColor) {
				t.Fatalf("clean.AccentColor not normalized #rrggbb: %q", clean.AccentColor)
			}
		}

		if !bytes.Contains(rendered, []byte(NTFuzzRepoURL)) {
			t.Fatalf("rendered HTML missing canonical NTFuzzRepoURL %q", NTFuzzRepoURL)
		}
		if !bytes.Contains(rendered, []byte("renderer: 'auto'")) {
			t.Fatalf("rendered HTML missing renderer: 'auto'")
		}
		if clean.Name != "" && strings.ContainsAny(clean.Name, "<>") {
			if bytes.Contains(rendered, []byte(clean.Name)) {
				t.Fatalf("rendered HTML contained unescaped angle brackets from Name %q", clean.Name)
			}
		}
	})
}
