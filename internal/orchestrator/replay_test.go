package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"nimbleterminal.dev/ntfuzz/internal/store"
)

func TestReplayRequiresExecutedSavedInput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module replay.test\n\ngo 1.24\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input := []byte("go test fuzz v1\nstring(\"bad\")\n")
	saved := filepath.Join(t.TempDir(), "saved")
	if err := os.WriteFile(saved, input, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(input)
	crash := store.Crash{ID: 1, InputHash: hex.EncodeToString(hash[:16]), MinimizedInput: saved}
	job := JobSpec{RepoDir: dir, PkgRelDir: ".", TargetName: "FuzzX"}
	for _, tc := range []struct{ name, source, want string }{
		{"reproduces", `package replay
import "testing"
func FuzzX(f *testing.F){ f.Fuzz(func(t *testing.T,s string){if s=="bad"{t.Fatal("bug")}}) }`, "failed"},
		{"fixed", `package replay
import "testing"
func FuzzX(f *testing.F){ f.Fuzz(func(t *testing.T,s string){}) }`, "passed"},
		{"missing target", `package replay
import "testing"
func TestOther(t *testing.T){}`, "blocked"},
		{"compile failure", `package replay
invalid syntax`, "blocked"},
		{"skipped input", `package replay
import "testing"
func FuzzX(f *testing.F){ f.Fuzz(func(t *testing.T,s string){t.Skip("skip")}) }`, "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "replay_test.go"), []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			got, diagnostic := replayCrash(context.Background(), job, crash)
			if got != tc.want {
				t.Fatalf("got %s want %s: %s", got, tc.want, diagnostic)
			}
			if _, err := os.Stat(filepath.Join(dir, "testdata")); !os.IsNotExist(err) {
				t.Fatalf("checkout modified: %v", err)
			}
		})
	}
	crash.InputHash = "wrong"
	if got, _ := replayCrash(context.Background(), job, crash); got != "blocked" {
		t.Fatal("hash mismatch accepted")
	}
	crash.MinimizedInput = "missing"
	if got, _ := replayCrash(context.Background(), job, crash); got != "blocked" {
		t.Fatal("missing input accepted")
	}

	// Stored InputBytes are preferred even when MinimizedInput file is missing on disk.
	if err := os.WriteFile(filepath.Join(dir, "replay_test.go"), []byte(`package replay
import "testing"
func FuzzX(f *testing.F){ f.Fuzz(func(t *testing.T,s string){}) }`), 0600); err != nil {
		t.Fatal(err)
	}
	crashWithBytes := store.Crash{
		ID:             2,
		InputHash:      hex.EncodeToString(hash[:16]),
		MinimizedInput: filepath.Join(t.TempDir(), "deleted-from-disk"),
		InputBytes:     input,
		InputTruncated: false,
	}
	if got, diag := replayCrash(context.Background(), job, crashWithBytes); got != "passed" {
		t.Fatalf("expected stored InputBytes replay to pass without disk file, got %s: %s", got, diag)
	}
}

func TestPendingReplayBypassesFuzzBackoff(t *testing.T) {
	o := &Orchestrator{flatStreaks: map[int64]int{1: 10, 2: 0}, skipCounts: map[int64]int{}}
	selected := o.selectDueTargets([]scheduledTarget{{targetID: 2, priority: 0}, {targetID: 1, priority: -1}})
	if len(selected) != 2 || selected[0].targetID != 1 {
		t.Fatalf("pending replay delayed: %+v", selected)
	}
}
