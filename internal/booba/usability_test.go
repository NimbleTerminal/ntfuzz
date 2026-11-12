package booba

import (
	"strings"
	"testing"
)

func TestDashboardAccessibilityAndCrashNavigation(t *testing.T) {
	page, err := RenderIndexHTML(Branding{})
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, want := range []string{
		`href="/crashes"`,
		`aria-label="Crash reports"`,
		`role="status" aria-live="polite" aria-atomic="true"`,
		`aria-hidden="true"`,
		`<main id="dashboard-content">`,
		`aria-label="Fleet summary"`,
		`id="accessible-summary"`,
		`aria-describedby="accessible-summary"`,
		`id="terminal-actions"`,
		`parent: document.getElementById('terminal-actions')`,
		`accessibleSummary.textContent = report.message`,
		`#action-controls .booba-action--toggle { position: absolute;`,
		`#action-controls .booba-action--keyboard { position: absolute;`,
		`.brand-sub { display: none; }`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if strings.Contains(html, `.brand-sub, #status-text { display: none; }`) {
		t.Fatal("mobile CSS hides the only textual connection status")
	}
}
