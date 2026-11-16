package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestDeduplicatedAndReopenedCrashReplayEndToEnd(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "replay_dedup.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	repoDir := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		if err := runGit(ctx, repoDir, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/supports\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	buggySrc := `package supports
import "testing"
func FuzzSupports(f *testing.F) {
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) >= 2 && s[:2] == "(:" {
			panic("slice bounds out of range")
		}
	})
}
`
	if err := os.WriteFile(filepath.Join(repoDir, "supports_test.go"), []byte(buggySrc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGit(ctx, repoDir, "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := runGit(ctx, repoDir, "commit", "-m", "buggy v1"); err != nil {
		t.Fatal(err)
	}
	shaBuggy, _ := inspectGitRevision(ctx, repoDir)

	fixedSrc := `package supports
import "testing"
func FuzzSupports(f *testing.F) {
	f.Fuzz(func(t *testing.T, s string) {})
}
`
	if err := os.WriteFile(filepath.Join(repoDir, "supports_test.go"), []byte(fixedSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGit(ctx, repoDir, "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := runGit(ctx, repoDir, "commit", "-m", "fixed v2"); err != nil {
		t.Fatal(err)
	}
	shaFixed, _ := inspectGitRevision(ctx, repoDir)

	now := time.Now().UTC().Add(-time.Hour)
	pID, err := st.SyncProject(ctx, store.ProjectSyncInput{
		Name:     "nthtml",
		Repo:     repoDir,
		GitSHA:   shaFixed,
		SyncedAt: &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "example.com/supports", "FuzzSupports")
	if err != nil {
		t.Fatal(err)
	}

	crashDir := filepath.Join(tmp, "corpus", "nthtml", "crashes", "example.com_supports", "FuzzSupports")
	if err := os.MkdirAll(crashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeInput := func(body string) (string, string, []byte) {
		raw := []byte("go test fuzz v1\n" + body + "\n")
		h := store.HashCrashInput(raw)
		p := filepath.Join(crashDir, h)
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return h, p, raw
	}

	h1, p1, b1 := writeInput(`string("(:\xf7\xf7vAr())")`)
	h2, p2, b2 := writeInput(`string("(:\xbc\xbcvAr())")`)
	h3, p3, b3 := writeInput(`string("(:0\x8a\x8avAr())")`)

	// Confirm both candidates fail on buggy revision and pass on fixed revision via VerifyCrashInputAtRevision.
	for _, b := range [][]byte{b1, b2, b3} {
		if out, diag := VerifyCrashInputAtRevision(ctx, repoDir, shaBuggy, ".", "FuzzSupports", 1, b); out != "failed" {
			t.Fatalf("expected candidate to fail on %s, got %s: %s", shaBuggy, out, diag)
		}
		if out, diag := VerifyCrashInputAtRevision(ctx, repoDir, shaFixed, ".", "FuzzSupports", 1, b); out != "passed" {
			t.Fatalf("expected candidate to pass on %s, got %s: %s", shaFixed, out, diag)
		}
	}

	fp := "fp-supports-var-bounds"
	cID, err := st.RecordCrash(ctx, store.Crash{
		TargetID:         tID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            "panic: slice bounds out of range",
		StackFingerprint: fp,
		InputHash:        h1,
		MinimizedInput:   p1,
		InputBytes:       b1,
		GitSHA:           shaBuggy,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Second crash with different input and same stack fingerprint deduplicates onto cID.
	if _, err := st.RecordCrash(ctx, store.Crash{
		TargetID:         tID,
		FirstSeen:        now.Add(time.Minute),
		LastSeen:         now.Add(time.Minute),
		Stack:            "panic: slice bounds out of range",
		StackFingerprint: fp,
		InputHash:        h2,
		MinimizedInput:   p2,
		InputBytes:       b2,
		GitSHA:           shaBuggy,
	}); err != nil {
		t.Fatal(err)
	}

	saved, err := st.GetCrash(ctx, cID)
	if err != nil {
		t.Fatal(err)
	}
	job := JobSpec{Project: "nthtml", RepoDir: repoDir, PkgImport: "example.com/supports", PkgRelDir: ".", TargetName: "FuzzSupports"}
	if outcome, diag := replayCrash(ctx, job, saved); outcome != "passed" {
		t.Fatalf("expected deduplicated crash replay to pass, got %s: %s", outcome, diag)
	}
	if err := st.RecordReplay(ctx, saved, store.CrashReplay{
		GitSHA:     shaFixed,
		CheckedAt:  now.Add(2 * time.Minute),
		Outcome:    "passed",
		Diagnostic: "Saved input executed successfully on this revision",
	}); err != nil {
		t.Fatal(err)
	}

	// Reopen the fixed crash with input 3 (same stack fingerprint) and verify replay accepts the reopened reproducer.
	if _, err := st.RecordCrash(ctx, store.Crash{
		TargetID:         tID,
		FirstSeen:        now.Add(3 * time.Minute),
		LastSeen:         now.Add(3 * time.Minute),
		Stack:            "panic: slice bounds out of range",
		StackFingerprint: fp,
		InputHash:        h3,
		MinimizedInput:   p3,
		InputBytes:       b3,
		GitSHA:           shaBuggy,
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := st.GetCrash(ctx, cID)
	if err != nil || reopened.FixedAt != nil || reopened.InputHash != h3 {
		t.Fatalf("expected reopened crash with hash %s, got %+v (err=%v)", h3, reopened, err)
	}
	if outcome, diag := replayCrash(ctx, job, reopened); outcome != "passed" {
		t.Fatalf("expected reopened crash replay to pass, got %s: %s", outcome, diag)
	}
}

func TestRepairedCrashReplaysOnCurrentRevisionWithoutNewCommit(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "repair_replay.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	repoDir := filepath.Join(tmp, "nthtml")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		if err := runGit(ctx, repoDir, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module nimbleterminal.dev/nthtml\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "supports_test.go"), []byte(`package nthtml
import "testing"
func FuzzSupports(f *testing.F) {
	f.Fuzz(func(t *testing.T, s string) {})
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runGit(ctx, repoDir, "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := runGit(ctx, repoDir, "commit", "-m", "fixed revision"); err != nil {
		t.Fatal(err)
	}
	currentSHA, _ := inspectGitRevision(ctx, repoDir)

	now := time.Now().UTC().Add(-10 * time.Minute)
	pID, err := st.SyncProject(ctx, store.ProjectSyncInput{
		Name:     "nthtml",
		Repo:     repoDir,
		GitSHA:   currentSHA,
		SyncedAt: &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "nimbleterminal.dev/nthtml", "FuzzSupports")
	if err != nil {
		t.Fatal(err)
	}

	corpusRoot := filepath.Join(tmp, "corpus")
	crashDir := filepath.Join(corpusRoot, "nthtml", "crashes", "nimbleterminal.dev_nthtml", "FuzzSupports")
	if err := os.MkdirAll(crashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origBytes := []byte("go test fuzz v1\nstring(\"(:\\xf7\\xf7vAr())\")\n")
	origHash := store.HashCrashInput(origBytes)
	origPath := filepath.Join(crashDir, origHash)
	if err := os.WriteFile(origPath, origBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	latestBytes := []byte("go test fuzz v1\nstring(\"(:\\xbc\\xbcvAr())\")\n")
	latestHash := store.HashCrashInput(latestBytes)
	latestPath := filepath.Join(crashDir, latestHash)
	if err := os.WriteFile(latestPath, latestBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// Create a crash with mismatched input_hash (origHash) vs minimized_input/input_bytes (latestHash)
	// simulating a pre-fix deduplicated record.
	cID, err := st.RecordCrash(ctx, store.Crash{
		TargetID:         tID,
		FirstSeen:        now,
		LastSeen:         now.Add(time.Minute),
		Stack:            "panic: slice bounds out of range [9:7]",
		StackFingerprint: "c6dc7ec1d16d48aaad5fbcb9",
		InputHash:        origHash,
		MinimizedInput:   latestPath,
		InputBytes:       latestBytes,
		GitSHA:           "c024ebde43e6",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordReplay(ctx, store.Crash{ID: cID, InputHash: origHash}, store.CrashReplay{
		GitSHA:     currentSHA,
		CheckedAt:  time.Now().UTC(),
		Outcome:    "blocked",
		Diagnostic: "saved input hash mismatch",
	}); err != nil {
		t.Fatal(err)
	}

	repReport, err := st.RepairCrashes(ctx, store.CrashRepairOptions{
		CrashIDs: []int64{cID},
		DryRun:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repReport.Actions) != 1 || repReport.Actions[0].Status != store.RepairStatusRepaired || repReport.Actions[0].NewInputHash != latestHash || !repReport.Actions[0].ReplayEligible {
		t.Fatalf("expected crash to be repaired to %s and replay_eligible on currentSHA, got %+v", latestHash, repReport.Actions)
	}

	// Repair alone must NOT close the crash.
	openBeforeReplay, err := st.ListCrashes(ctx, true, "")
	if err != nil || len(openBeforeReplay) != 1 {
		t.Fatalf("expected crash to remain open before replay execution, got %+v (err=%v)", openBeforeReplay, err)
	}

	// Run one orchestrator pass on the same revision (no new commit) with a no-op fuzz runner:
	// replayCrashes executes the real go test subtest and closes the crash on pass.
	cfg := Config{
		CorpusDir:       corpusRoot,
		WorkDir:         filepath.Join(tmp, "work"),
		DefaultFuzzTime: "1s",
		Projects: []ProjectConfig{
			{Name: "nthtml", Repo: repoDir, FuzzTime: "1s"},
		},
	}
	orch := New(cfg, st, Options{
		Workers: 1,
		Runner:  &concurrencyTrackingRunner{concurrent: new(int32), maxConcurrent: new(int32)},
	})
	orch.RunOnce(ctx)

	afterReplay, err := st.GetCrash(ctx, cID)
	if err != nil {
		t.Fatal(err)
	}
	if afterReplay.FixedAt == nil || afterReplay.ReplayStatus != "passed" || afterReplay.ReplayGitSHA != currentSHA {
		t.Fatalf("expected executed replay on %s to pass and close crash, got %+v", currentSHA, afterReplay)
	}
	replays, err := st.ListCrashReplays(ctx)
	if err != nil || len(replays) != 2 {
		t.Fatalf("expected both historical blocked replay and new passed replay preserved, got %+v (err=%v)", replays, err)
	}
}
