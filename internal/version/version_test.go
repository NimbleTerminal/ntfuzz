package version

import "testing"

func TestCurrentVersion(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	Version = "dev"
	if got := Current(); got == "" {
		t.Fatal("expected non-empty default version")
	}

	Version = "v0.2.0"
	if got := Current(); got != "v0.2.0" {
		t.Fatalf("Current() = %q, want %q", got, "v0.2.0")
	}
}
