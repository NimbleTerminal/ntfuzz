package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.duckdb")

	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = st.Close() }()

	ver, err := st.SchemaVersion(ctx)
	if err != nil || ver != CurrentSchemaVersion {
		t.Fatalf("expected schema version %d, got %d (err=%v)", CurrentSchemaVersion, ver, err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	projID, err := st.SyncProject(ctx, ProjectSyncInput{
		Name:     "demo-parser",
		Repo:     "./examples/demo-parser",
		Branch:   "main",
		GitSHA:   "abc1234def56",
		Dirty:    false,
		SyncedAt: &now,
	})
	if err != nil {
		t.Fatalf("SyncProject insert failed: %v", err)
	}

	// Simulate a failed pull: SyncedAt is nil, SyncError is set -> last_synced must stay `now`
	projID2, err := st.SyncProject(ctx, ProjectSyncInput{
		Name:      "demo-parser",
		Repo:      "./examples/demo-parser",
		Branch:    "main",
		SyncedAt:  nil,
		SyncError: "git pull failed: network unreachable",
	})
	if err != nil || projID != projID2 {
		t.Fatalf("SyncProject failed pull update error: %v", err)
	}

	projs, err := st.ListProjects(ctx)
	if err != nil || len(projs) != 1 {
		t.Fatalf("ListProjects failed: %v", err)
	}
	if projs[0].LastSynced == nil || !projs[0].LastSynced.Equal(now) {
		t.Fatalf("expected LastSynced to remain %v after failed pull, got %v", now, projs[0].LastSynced)
	}
	if projs[0].SyncError == "" || projs[0].GitSHA != "abc1234def56" {
		t.Fatalf("expected SyncError and preserved GitSHA, got %+v", projs[0])
	}

	// Clear sync error on recovery
	later := now.Add(time.Minute)
	_, err = st.SyncProject(ctx, ProjectSyncInput{
		Name:     "demo-parser",
		Repo:     "./examples/demo-parser",
		Branch:   "main",
		GitSHA:   "fedcba987654",
		Dirty:    true,
		SyncedAt: &later,
	})
	if err != nil {
		t.Fatalf("SyncProject recovery failed: %v", err)
	}

	// Reconcile targets: start with two targets, then drop one
	reconciled, err := st.ReconcileProjectTargets(ctx, projID, []TargetKey{
		{Package: "example.com/demo/parser", Name: "FuzzParseExpr"},
		{Package: "example.com/demo/parser", Name: "FuzzDeletedLater"},
	})
	if err != nil || len(reconciled) != 2 {
		t.Fatalf("ReconcileProjectTargets initial failed: %v", err)
	}
	targetID := reconciled[TargetKey{Package: "example.com/demo/parser", Name: "FuzzParseExpr"}]

	// Now reconcile with only FuzzParseExpr -> FuzzDeletedLater should become inactive
	reconciled, err = st.ReconcileProjectTargets(ctx, projID, []TargetKey{
		{Package: "example.com/demo/parser", Name: "FuzzParseExpr"},
	})
	if err != nil || len(reconciled) != 1 {
		t.Fatalf("ReconcileProjectTargets second pass failed: %v", err)
	}

	// Insert two runs with revision metadata
	_, err = st.InsertRun(ctx, Run{
		TargetID:       targetID,
		Started:        now.Add(-5 * time.Minute),
		DurationSec:    5.0,
		Execs:          50000,
		ExecsPerSec:    10000,
		NewInteresting: 3,
		CorpusCount:    12,
		Crashes:        0,
		CoveragePct:    62.5,
		Status:         "ok",
		Worker:         "worker-1",
		GitSHA:         "abc1234def56",
		Dirty:          false,
		GoVersion:      "go1.27.3",
	})
	if err != nil {
		t.Fatalf("InsertRun 1 failed: %v", err)
	}

	_, err = st.InsertRun(ctx, Run{
		TargetID:       targetID,
		Started:        now,
		DurationSec:    5.0,
		Execs:          75000,
		ExecsPerSec:    15000,
		NewInteresting: 1,
		CorpusCount:    15,
		Crashes:        1,
		CoveragePct:    74.2,
		Status:         "crash",
		Worker:         "worker-1",
		GitSHA:         "fedcba987654",
		Dirty:          true,
		GoVersion:      "go1.27.3",
	})
	if err != nil {
		t.Fatalf("InsertRun 2 failed: %v", err)
	}

	crashID, err := st.RecordCrash(ctx, Crash{
		TargetID:         targetID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            "panic: index out of range [4] with length 4\nparser.go:42",
		StackFingerprint: "fp-index-out-of-range",
		InputHash:        "deadbeef12345678",
		MinimizedInput:   ".ntfuzz/corpus/demo-parser/crashes/FuzzParseExpr/deadbeef12345678",
		GitSHA:           "fedcba987654",
		Dirty:            true,
		GoVersion:        "go1.27.3",
	})
	if err != nil {
		t.Fatalf("RecordCrash failed: %v", err)
	}

	// Recording a crash with a different input hash but same stack_fingerprint deduplicates!
	crashID2, err := st.RecordCrash(ctx, Crash{
		TargetID:         targetID,
		FirstSeen:        now,
		LastSeen:         later,
		Stack:            "panic: index out of range [4] with length 4\nparser.go:49",
		StackFingerprint: "fp-index-out-of-range",
		InputHash:        "differentinputhash999",
		MinimizedInput:   ".ntfuzz/corpus/demo-parser/crashes/FuzzParseExpr/differentinputhash999",
		GitSHA:           "fedcba987654",
		Dirty:            true,
		GoVersion:        "go1.27.3",
	})
	if err != nil || crashID != crashID2 {
		t.Fatalf("expected stack_fingerprint dedup to return %d, got %d (err=%v)", crashID, crashID2, err)
	}

	targets, err := st.ListTargets(ctx, "demo-parser")
	if err != nil {
		t.Fatalf("ListTargets failed: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 active target after reconciliation, got %d", len(targets))
	}
	if targets[0].CoveragePct != 74.2 || len(targets[0].CoverageTrend) != 2 || targets[0].GitSHA != "fedcba987654" || targets[0].CorpusCount != 15 {
		t.Fatalf("unexpected target state: %+v", targets[0])
	}

	// Test MarkCrashFixed lifecycle
	if err := st.MarkCrashFixed(ctx, crashID, later, "https://github.com/example/repo/issues/12"); err != nil {
		t.Fatalf("MarkCrashFixed failed: %v", err)
	}
	openCrashes, err := st.ListCrashes(ctx, true, "")
	if err != nil || len(openCrashes) != 0 {
		t.Fatalf("expected 0 open crashes after MarkCrashFixed, got %d (err=%v)", len(openCrashes), err)
	}
	allCrashes, err := st.ListCrashes(ctx, false, "")
	if err != nil || len(allCrashes) != 1 || allCrashes[0].FixedAt == nil || allCrashes[0].IssueURL != "https://github.com/example/repo/issues/12" {
		t.Fatalf("unexpected all crashes state: %+v (err=%v)", allCrashes, err)
	}
}

func TestStorePruneOldRuns(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "prune.duckdb"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now().UTC()
	pID, err := st.UpsertProject(ctx, "prune-proj", "./repo", "main", &now)
	if err != nil {
		t.Fatal(err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "example.com/prune", "FuzzPrune")
	if err != nil {
		t.Fatal(err)
	}

	// Insert 5 old runs (30 days ago) and 2 recent runs (today)
	for i := 0; i < 5; i++ {
		_, err := st.InsertRun(ctx, Run{
			TargetID:    tID,
			Started:     now.Add(-30*24*time.Hour + time.Duration(i)*time.Minute),
			DurationSec: 1.0,
			Execs:       100,
			CoveragePct: float64(50 + i),
			Status:      "ok",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		_, err := st.InsertRun(ctx, Run{
			TargetID:    tID,
			Started:     now.Add(-time.Duration(i) * time.Minute),
			DurationSec: 1.0,
			Execs:       200,
			CoveragePct: float64(70 + i),
			Status:      "ok",
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Prune runs older than 14 days, keeping at least 3 runs per target -> should delete 4 old runs, leaving 3 total
	deleted, err := st.PruneOldRuns(ctx, 14*24*time.Hour, 3)
	if err != nil {
		t.Fatalf("PruneOldRuns: %v", err)
	}
	if deleted != 4 {
		t.Fatalf("expected 4 old runs pruned, got %d", deleted)
	}

	remaining, err := st.ListRuns(ctx, "FuzzPrune", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 3 {
		t.Fatalf("expected 3 runs remaining (keepPerTarget=3), got %d", len(remaining))
	}
}

func TestIncompleteRunIsNotHealthy(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	p, err := st.UpsertProject(ctx, "fixture", "local", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.UpsertTarget(ctx, p, "fixture/sub", "FuzzSub")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"missing_target", "incomplete", "error"} {
		if _, err := st.InsertRun(ctx, Run{TargetID: target, Status: status, Diagnostic: "detail"}); err != nil {
			t.Fatal(err)
		}
		stats, err := st.GetFleetStats(ctx)
		if err != nil || stats.FailedTargets != 1 {
			t.Fatalf("stats=%+v err=%v", stats, err)
		}
		projects, err := st.ListProjects(ctx)
		if err != nil || len(projects) != 1 || projects[0].LatestStatus != "error" {
			t.Fatalf("projects=%+v err=%v", projects, err)
		}
	}
}

func TestCanceledRunIsNotFailure(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	p, err := st.UpsertProject(ctx, "fixture", "local", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.UpsertTarget(ctx, p, "fixture/sub", "FuzzSub")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertRun(ctx, Run{TargetID: target, Status: "canceled"}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.GetFleetStats(ctx)
	if err != nil || stats.FailedTargets != 0 {
		t.Fatalf("canceled run must not count as failed: stats=%+v err=%v", stats, err)
	}
	projects, err := st.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].LatestStatus != "pending" {
		t.Fatalf("projects=%+v err=%v", projects, err)
	}
}

func TestStoreCannedStatsQueries(t *testing.T) {
	ctx := context.Background()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	base := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	pID, err := st.UpsertProject(ctx, "stats-proj", "./repo", "main", &base)
	if err != nil {
		t.Fatal(err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "example.com/stats", "FuzzStats")
	if err != nil {
		t.Fatal(err)
	}

	// Insert two runs in distinct 1-hour buckets within 24h, plus one run 3 days earlier.
	for _, r := range []Run{
		{
			TargetID:       tID,
			Started:        base.Add(-72 * time.Hour),
			DurationSec:    2.0,
			Execs:          10000,
			ExecsPerSec:    5000,
			NewInteresting: 1,
			CorpusCount:    5,
			CoveragePct:    40.0,
			Status:         "ok",
		},
		{
			TargetID:       tID,
			Started:        base.Add(-2 * time.Hour),
			DurationSec:    4.0,
			Execs:          40000,
			ExecsPerSec:    10000,
			NewInteresting: 2,
			CorpusCount:    10,
			CoveragePct:    60.0,
			Status:         "ok",
		},
		{
			TargetID:       tID,
			Started:        base,
			DurationSec:    5.0,
			Execs:          100000,
			ExecsPerSec:    20000,
			NewInteresting: 4,
			CorpusCount:    14,
			Crashes:        1,
			CoveragePct:    80.0,
			Status:         "crash",
		},
	} {
		if _, err := st.InsertRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	// 24h window should include the 2 recent buckets and exclude the 72h-old run.
	tp, err := st.GetStats(ctx, StatsQueryThroughput, StatsWindow24h, "stats-proj", "")
	if err != nil {
		t.Fatalf("GetStats throughput 24h: %v", err)
	}
	if tp.Query != StatsQueryThroughput || tp.Window != StatsWindow24h || len(tp.Buckets) != 2 || tp.TotalRuns != 2 || tp.TotalExecs != 140000 {
		t.Fatalf("unexpected 24h throughput stats: %+v", tp)
	}
	if tp.OKRuns != 1 || tp.CrashRuns != 1 || tp.MaxCoveragePct != 80.0 || tp.CorpusCount != 14 || tp.NewInteresting != 6 {
		t.Fatalf("unexpected 24h summary counts: %+v", tp)
	}

	// 7d window should include all 3 runs across 3 buckets.
	cov7d, err := st.GetStats(ctx, StatsQueryCoverage, StatsWindow7d, "", "FuzzStats")
	if err != nil {
		t.Fatalf("GetStats coverage 7d: %v", err)
	}
	if cov7d.Query != StatsQueryCoverage || cov7d.Window != StatsWindow7d || len(cov7d.Buckets) != 3 || cov7d.TotalRuns != 3 {
		t.Fatalf("unexpected 7d coverage stats: %+v", cov7d)
	}

	// Invalid query or window fails with ErrInvalidStatsParam.
	if _, err := st.GetStats(ctx, "unknown_query", StatsWindow24h, "", ""); err == nil {
		t.Fatal("expected error for unknown query")
	}
	if _, err := st.GetStats(ctx, StatsQueryOutcomes, "99y", "", ""); err == nil {
		t.Fatal("expected error for unknown window")
	}
}

func TestCrashSharePersistenceTruncationAndV5Compatibility(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "share.duckdb")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now().UTC().Truncate(time.Millisecond)
	pID, err := st.SyncProject(ctx, ProjectSyncInput{
		Name:         "nt3d",
		Repo:         "https://github.com/NimbleTerminal/nt3d",
		GitSHA:       "112233445566",
		PublicInputs: true,
		SyncedAt:     &now,
	})
	if err != nil {
		t.Fatalf("SyncProject: %v", err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "nimbleterminal.dev/nt3d/mesh/gltf", "FuzzDecode")
	if err != nil {
		t.Fatalf("UpsertTarget: %v", err)
	}

	corpusBytes := []byte("go test fuzz v1\n[]byte(\"glTF\\x02\\x00\\x00\\x00\")\n")
	cID, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            "panic: runtime error: slice bounds out of range [::-1]\ngithub.com/qmuntal/gltf@v0.29.0/decoder.go:151",
		StackFingerprint: "fp-gltf-151",
		InputHash:        "0123456789abcdef0123456789abcdef",
		MinimizedInput:   ".ntfuzz/corpus/nt3d/crashes/FuzzDecode/0123456789abcdef0123456789abcdef",
		InputBytes:       corpusBytes,
		GitSHA:           "112233445566",
		GoVersion:        "go1.25.0",
		BlamedModule:     "github.com/qmuntal/gltf",
		BlamedVersion:    "v0.29.0",
		BlamedFile:       "decoder.go",
		BlamedLine:       151,
	})
	if err != nil {
		t.Fatalf("RecordCrash: %v", err)
	}

	got, err := st.GetCrash(ctx, cID)
	if err != nil {
		t.Fatalf("GetCrash: %v", err)
	}
	if !got.PublicInputs || got.InputTruncated || string(got.InputBytes) != string(corpusBytes) {
		t.Fatalf("unexpected stored input fields: public=%v truncated=%v bytes=%q", got.PublicInputs, got.InputTruncated, got.InputBytes)
	}
	if got.BlamedModule != "github.com/qmuntal/gltf" || got.BlamedVersion != "v0.29.0" || got.BlamedFile != "decoder.go" || got.BlamedLine != 151 {
		t.Fatalf("unexpected blame fields: %+v", got)
	}

	// SetCrashIssue sets issue_url without marking the crash fixed.
	if err := st.SetCrashIssue(ctx, cID, "https://github.com/qmuntal/gltf/security/advisories/GHSA-xxxx"); err != nil {
		t.Fatalf("SetCrashIssue: %v", err)
	}
	gotAfterIssue, err := st.GetCrash(ctx, cID)
	if err != nil {
		t.Fatalf("GetCrash after SetCrashIssue: %v", err)
	}
	if gotAfterIssue.FixedAt != nil || gotAfterIssue.IssueURL != "https://github.com/qmuntal/gltf/security/advisories/GHSA-xxxx" {
		t.Fatalf("expected open-reported crash, got fixed_at=%v issue_url=%q", gotAfterIssue.FixedAt, gotAfterIssue.IssueURL)
	}

	// Oversized input (> 64 KiB) is truncated at 64 KiB and flagged InputTruncated = true.
	bigInput := make([]byte, MaxCrashInputBytes+1024)
	for i := range bigInput {
		bigInput[i] = byte('a' + (i % 26))
	}
	bigID, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            "panic: oversized",
		StackFingerprint: "fp-oversized",
		InputHash:        "fedcba9876543210fedcba9876543210",
		MinimizedInput:   "path/to/big",
		InputBytes:       bigInput,
	})
	if err != nil {
		t.Fatalf("RecordCrash oversized: %v", err)
	}
	gotBig, err := st.GetCrash(ctx, bigID)
	if err != nil {
		t.Fatalf("GetCrash oversized: %v", err)
	}
	if !gotBig.InputTruncated || len(gotBig.InputBytes) != MaxCrashInputBytes {
		t.Fatalf("expected truncated 64 KiB input, got truncated=%v len=%d", gotBig.InputTruncated, len(gotBig.InputBytes))
	}

	// Missing crash returns ErrNotFound.
	if _, err := st.GetCrash(ctx, 999999); err == nil {
		t.Fatal("expected ErrNotFound for missing crash ID")
	}

	// Verify legacy dependency crash fingerprint migration, stack scrubbing, & attribution backfill:
	// Simulate a pre-v6 row whose stack has testing.go:2078: panic: ... and raw /var/lib/ntfuzz/... paths with empty blamed_module.
	rawDepStack := "--- FAIL: FuzzDecode (0.04s)\n    --- FAIL: FuzzDecode (0.00s)\n        testing.go:2078: panic: runtime error: slice bounds out of range [::-1]\n        goroutine 22 [running]:\n        github.com/qmuntal/gltf.(*Decoder).decodeBuffer(0xc0001a2000, 0x0)\n        \t/var/lib/ntfuzz/.cache/go-mod/github.com/qmuntal/gltf@v0.29.0/decoder.go:151 +0x359"
	scrubbedDepStack := "--- FAIL: FuzzDecode (0.04s)\n    --- FAIL: FuzzDecode (0.00s)\n        testing.go:2078: panic: runtime error: slice bounds out of range [::-1]\n        goroutine 22 [running]:\n        github.com/qmuntal/gltf.(*Decoder).decodeBuffer(0xc0001a2000, 0x0)\n        \tgithub.com/qmuntal/gltf@v0.29.0/decoder.go:151 +0x359"
	oldFP := LegacyFingerprintStack(rawDepStack)
	newFP := FingerprintStack(scrubbedDepStack)
	if oldFP == newFP {
		t.Fatalf("expected legacy and new fingerprints to differ on @v0.29.0 path")
	}
	legacyCorpusDir := filepath.Join(t.TempDir(), "crashes", "FuzzDecode")
	if err := os.MkdirAll(legacyCorpusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyInputFile := filepath.Join(legacyCorpusDir, "legacy-input-hash-111")
	legacyInputBytes := []byte("go test fuzz v1\n[]byte(\"gltf-repro\")\n")
	if err := os.WriteFile(legacyInputFile, legacyInputBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	legacyCrashID, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            rawDepStack,
		StackFingerprint: oldFP,
		InputHash:        "legacy-input-hash-111",
		MinimizedInput:   legacyInputFile,
	})
	if err != nil {
		t.Fatalf("RecordCrash legacy: %v", err)
	}
	// Run backfillCrashes as Migrate does, then verify attribution, scrubbing, input_bytes backfill, and fingerprint deduplication.
	if err := st.backfillCrashes(ctx); err != nil {
		t.Fatalf("backfillCrashes: %v", err)
	}
	backfilled, err := st.GetCrash(ctx, legacyCrashID)
	if err != nil {
		t.Fatalf("GetCrash(legacyCrashID): %v", err)
	}
	if backfilled.BlamedModule != "github.com/qmuntal/gltf" ||
		backfilled.BlamedVersion != "v0.29.0" ||
		backfilled.BlamedFile != "decoder.go" ||
		backfilled.BlamedLine != 151 {
		t.Fatalf("expected backfillCrashes to populate gltf v0.29.0 attribution, got %+v", backfilled)
	}
	if backfilled.Stack != scrubbedDepStack {
		t.Fatalf("expected backfillCrashes to scrub stack, got:\n%s", backfilled.Stack)
	}
	if string(backfilled.InputBytes) != string(legacyInputBytes) {
		t.Fatalf("expected backfillCrashes to backfill InputBytes from disk, got %q", backfilled.InputBytes)
	}
	dedupCrashID, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            scrubbedDepStack,
		StackFingerprint: newFP,
		InputHash:        "new-input-hash-222",
		MinimizedInput:   "new-path",
	})
	if err != nil {
		t.Fatalf("RecordCrash after recompute: %v", err)
	}
	if dedupCrashID != legacyCrashID {
		t.Fatalf("expected scrubbed dependency crash to deduplicate onto existing crash %d, got new ID %d", legacyCrashID, dedupCrashID)
	}

	// Verify PublishSnapshot strips input_bytes for projects with public_inputs = false while keeping public_inputs = true.
	privPID, err := st.SyncProject(ctx, ProjectSyncInput{
		Name:         "priv-proj",
		Repo:         "https://github.com/example/priv",
		PublicInputs: false,
		SyncedAt:     &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	privTID, err := st.UpsertTarget(ctx, privPID, "example.com/priv", "FuzzPriv")
	if err != nil {
		t.Fatal(err)
	}
	privCID, err := st.RecordCrash(ctx, Crash{
		TargetID:         privTID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            "panic: private",
		StackFingerprint: "fp-priv",
		InputHash:        "priv-hash-999",
		MinimizedInput:   "priv-input",
		InputBytes:       []byte("go test fuzz v1\n[]byte(\"secret\")\n"),
	})
	if err != nil {
		t.Fatal(err)
	}

	snapDir := t.TempDir()
	snapMeta, err := st.PublishSnapshot(ctx, snapDir)
	if err != nil {
		t.Fatalf("PublishSnapshot: %v", err)
	}
	roStore, err := OpenReadOnly(filepath.Join(snapDir, snapMeta.File))
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = roStore.Close() }()

	pubSnapCrash, err := roStore.GetCrash(ctx, cID)
	if err != nil {
		t.Fatalf("roStore.GetCrash(pub): %v", err)
	}
	if string(pubSnapCrash.InputBytes) != string(corpusBytes) {
		t.Fatalf("expected public_inputs=true crash to retain InputBytes in snapshot, got %q", pubSnapCrash.InputBytes)
	}
	privSnapCrash, err := roStore.GetCrash(ctx, privCID)
	if err != nil {
		t.Fatalf("roStore.GetCrash(priv): %v", err)
	}
	if len(privSnapCrash.InputBytes) != 0 {
		t.Fatalf("expected public_inputs=false crash to have InputBytes stripped in snapshot, got %q", privSnapCrash.InputBytes)
	}
}

func TestRecordCrashSameFingerprintUpdatesReproducerAtomically(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "dedup.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Date(2026, 11, 14, 3, 46, 0, 0, time.UTC)
	pID, err := st.SyncProject(ctx, ProjectSyncInput{
		Name:         "nthtml",
		Repo:         "https://github.com/NimbleTerminal/nthtml.git",
		GitSHA:       "c024ebde43e6",
		PublicInputs: true,
		SyncedAt:     &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "nimbleterminal.dev/nthtml/document", "FuzzSupports")
	if err != nil {
		t.Fatal(err)
	}

	corpusDir := filepath.Join(t.TempDir(), "corpus", "nthtml", "crashes", "nimbleterminal.dev_nthtml_document", "FuzzSupports")
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		t.Fatal(err)
	}

	input1 := []byte("go test fuzz v1\nstring(\"(:\\xf7\\xf7vAr())\")\n")
	hash1 := HashCrashInput(input1)
	path1 := filepath.Join(corpusDir, hash1)
	if err := os.WriteFile(path1, input1, 0o600); err != nil {
		t.Fatal(err)
	}

	input2 := []byte("go test fuzz v1\nstring(\"(:\\xbc\\xbcvAr())\")\n")
	hash2 := HashCrashInput(input2)
	path2 := filepath.Join(corpusDir, hash2)
	if err := os.WriteFile(path2, input2, 0o600); err != nil {
		t.Fatal(err)
	}

	input3 := []byte("go test fuzz v1\nstring(\"(:0\\x8a\\x8avAr())\")\n")
	hash3 := HashCrashInput(input3)
	path3 := filepath.Join(corpusDir, hash3)
	if err := os.WriteFile(path3, input3, 0o600); err != nil {
		t.Fatal(err)
	}

	fp := "c6dc7ec1d16d48aaad5fbcb9"

	// 1. Initial crash record.
	cID, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            "panic: slice bounds out of range [9:7]\nsupports.go:178",
		StackFingerprint: fp,
		InputHash:        hash1,
		MinimizedInput:   path1,
		InputBytes:       input1,
		GitSHA:           "c024ebde43e6",
	})
	if err != nil {
		t.Fatalf("RecordCrash 1: %v", err)
	}

	// 2. Different input with the same stack_fingerprint while open updates hash, path, bytes, and truncation atomically.
	later := now.Add(time.Hour)
	cID2, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        later,
		LastSeen:         later,
		Stack:            "panic: slice bounds out of range [9:7]\nsupports.go:178",
		StackFingerprint: fp,
		InputHash:        hash2,
		MinimizedInput:   path2,
		InputBytes:       input2,
		GitSHA:           "c024ebde43e6",
	})
	if err != nil || cID2 != cID {
		t.Fatalf("RecordCrash 2: id=%d (want %d) err=%v", cID2, cID, err)
	}
	got, err := st.GetCrash(ctx, cID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputHash != hash2 || got.MinimizedInput != path2 || !bytes.Equal(got.InputBytes, input2) || got.InputTruncated {
		t.Fatalf("after open dedup: got hash=%s path=%s bytes=%q trunc=%t, want hash=%s path=%s",
			got.InputHash, got.MinimizedInput, got.InputBytes, got.InputTruncated, hash2, path2)
	}

	// 3. Missing bytes / inline-failure on same stack_fingerprint must NOT clobber the valid saved reproducer.
	inlineTime := later.Add(10 * time.Minute)
	cIDInline, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        inlineTime,
		LastSeen:         inlineTime,
		Stack:            "panic: slice bounds out of range [9:7]\nsupports.go:178",
		StackFingerprint: fp,
		InputHash:        "inline-synthetic-hash",
		MinimizedInput:   "inline-failure",
		InputBytes:       nil,
		GitSHA:           "c024ebde43e6",
	})
	if err != nil || cIDInline != cID {
		t.Fatalf("RecordCrash inline: id=%d err=%v", cIDInline, err)
	}
	gotAfterInline, err := st.GetCrash(ctx, cID)
	if err != nil {
		t.Fatal(err)
	}
	if gotAfterInline.InputHash != hash2 || gotAfterInline.MinimizedInput != path2 || !bytes.Equal(gotAfterInline.InputBytes, input2) {
		t.Fatalf("inline failure clobbered saved reproducer: %+v", gotAfterInline)
	}

	// 4. Mark crash fixed via replay pass, then reopen with a third distinct input via disk fallback (InputBytes omitted).
	fixTime := inlineTime.Add(10 * time.Minute)
	if err := st.RecordReplay(ctx, gotAfterInline, CrashReplay{
		GitSHA:     "141de7abe3fc",
		CheckedAt:  fixTime,
		Outcome:    "passed",
		Diagnostic: "Saved input executed successfully on this revision",
	}); err != nil {
		t.Fatalf("RecordReplay pass: %v", err)
	}
	fixedCrash, err := st.GetCrash(ctx, cID)
	if err != nil || fixedCrash.FixedAt == nil {
		t.Fatalf("expected crash to be marked fixed, got %+v (err=%v)", fixedCrash, err)
	}

	reopenTime := fixTime.Add(10 * time.Minute)
	cIDReopen, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        reopenTime,
		LastSeen:         reopenTime,
		Stack:            "panic: slice bounds out of range [9:7]\nsupports.go:178",
		StackFingerprint: fp,
		InputHash:        "", // exercises disk fallback hash + bytes resolution
		MinimizedInput:   path3,
		InputBytes:       nil,
		GitSHA:           "141de7abe3fc",
	})
	if err != nil || cIDReopen != cID {
		t.Fatalf("RecordCrash reopen: id=%d err=%v", cIDReopen, err)
	}
	reopened, err := st.GetCrash(ctx, cID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.FixedAt != nil {
		t.Fatalf("expected reopened crash FixedAt == nil, got %v", reopened.FixedAt)
	}
	if reopened.InputHash != hash3 || reopened.MinimizedInput != path3 || !bytes.Equal(reopened.InputBytes, input3) || reopened.InputTruncated {
		t.Fatalf("reopened crash reproducer mismatch: hash=%s (want %s) path=%s bytes=%q",
			reopened.InputHash, hash3, reopened.MinimizedInput, reopened.InputBytes)
	}
	// Stale replay status from hash2 must not be reported as the replay status for hash3.
	if reopened.ReplayStatus != "" {
		t.Fatalf("expected empty ReplayStatus for newly recorded input %s, got %q", hash3, reopened.ReplayStatus)
	}
	// Historical replay record for hash2 is still preserved in crash_replays.
	replays, err := st.ListCrashReplays(ctx)
	if err != nil || len(replays) != 1 || replays[0].InputHash != hash2 {
		t.Fatalf("expected historical replay for %s preserved, got %+v (err=%v)", hash2, replays, err)
	}

	// 5. Truncated input (> 64 KiB) backed by a disk file updates hash (full file digest), path, 64 KiB prefix, and truncated=true atomically;
	// and a subsequent non-truncated input resets truncated=false.
	bigPayload := append([]byte("go test fuzz v1\n[]byte(\""), bytes.Repeat([]byte("A"), MaxCrashInputBytes+512)...)
	bigPayload = append(bigPayload, []byte("\")\n")...)
	bigHash := HashCrashInput(bigPayload)
	bigPath := filepath.Join(corpusDir, bigHash)
	if err := os.WriteFile(bigPath, bigPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        reopenTime.Add(time.Minute),
		LastSeen:         reopenTime.Add(time.Minute),
		Stack:            "panic: slice bounds out of range [9:7]\nsupports.go:178",
		StackFingerprint: fp,
		MinimizedInput:   bigPath,
		InputBytes:       bigPayload[:MaxCrashInputBytes],
		InputTruncated:   true,
	}); err != nil {
		t.Fatalf("RecordCrash big truncated: %v", err)
	}
	gotBig, err := st.GetCrash(ctx, cID)
	if err != nil {
		t.Fatal(err)
	}
	if gotBig.InputHash != bigHash || gotBig.MinimizedInput != bigPath || !gotBig.InputTruncated || len(gotBig.InputBytes) != MaxCrashInputBytes {
		t.Fatalf("unexpected truncated dedup state: hash=%s (want %s) trunc=%t len=%d",
			gotBig.InputHash, bigHash, gotBig.InputTruncated, len(gotBig.InputBytes))
	}

	// Unbacked truncated input (no disk file) must NOT overwrite an existing valid reproducer.
	if _, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        reopenTime.Add(2 * time.Minute),
		LastSeen:         reopenTime.Add(2 * time.Minute),
		Stack:            "panic: slice bounds out of range [9:7]\nsupports.go:178",
		StackFingerprint: fp,
		InputHash:        "00000000000000000000000000000000",
		MinimizedInput:   filepath.Join(corpusDir, "nonexistent-big-file"),
		InputBytes:       bigPayload[:MaxCrashInputBytes],
		InputTruncated:   true,
	}); err != nil {
		t.Fatalf("RecordCrash unbacked truncated: %v", err)
	}
	gotStillBig, err := st.GetCrash(ctx, cID)
	if err != nil || gotStillBig.InputHash != bigHash || gotStillBig.MinimizedInput != bigPath {
		t.Fatalf("unbacked truncated input overwrote valid reproducer: %+v", gotStillBig)
	}

	// Subsequent small input resets InputTruncated back to false.
	if _, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        reopenTime.Add(3 * time.Minute),
		LastSeen:         reopenTime.Add(3 * time.Minute),
		Stack:            "panic: slice bounds out of range [9:7]\nsupports.go:178",
		StackFingerprint: fp,
		InputHash:        hash1,
		MinimizedInput:   path1,
		InputBytes:       input1,
	}); err != nil {
		t.Fatal(err)
	}
	gotSmallAgain, err := st.GetCrash(ctx, cID)
	if err != nil || gotSmallAgain.InputTruncated || gotSmallAgain.InputHash != hash1 || !bytes.Equal(gotSmallAgain.InputBytes, input1) {
		t.Fatalf("expected InputTruncated reset to false with hash1, got %+v", gotSmallAgain)
	}
}

func TestRepairCrashesProvenanceAndSafety(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "repair.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	firstSeen := time.Date(2026, 11, 14, 3, 46, 0, 0, time.UTC)
	lastSeen := time.Date(2026, 11, 15, 1, 12, 52, 0, time.UTC)
	syncTime := time.Now().UTC()

	pID, err := st.SyncProject(ctx, ProjectSyncInput{
		Name:     "nthtml",
		Repo:     "https://github.com/NimbleTerminal/nthtml.git",
		GitSHA:   "141de7abe3fc",
		SyncedAt: &syncTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "nimbleterminal.dev/nthtml/document", "FuzzSupports")
	if err != nil {
		t.Fatal(err)
	}

	corpusDir := filepath.Join(t.TempDir(), "corpus", "nthtml", "crashes", "nimbleterminal.dev_nthtml_document", "FuzzSupports")
	if err := os.MkdirAll(corpusDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeCorpusFile := func(dir, name string, data []byte, mtime time.Time) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if !mtime.IsZero() {
			if err := os.Chtimes(p, mtime, mtime); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}

	// Crash 1 (repairable): simulates historical deduplication bug (input_hash=origHash, minimized_input & input_bytes=latestHash).
	origBytes := []byte("go test fuzz v1\nstring(\"(:\\xf7\\xf7vAr())\")\n")
	origHash := HashCrashInput(origBytes)
	_ = writeCorpusFile(corpusDir, origHash, origBytes, firstSeen)

	latestBytes := []byte("go test fuzz v1\nstring(\"(:\\xbc\\xbcvAr())\")\n")
	latestHash := HashCrashInput(latestBytes)
	latestPath := writeCorpusFile(corpusDir, latestHash, latestBytes, lastSeen)

	repairableID, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        firstSeen,
		LastSeen:         lastSeen,
		Stack:            "panic: slice bounds out of range [9:7]",
		StackFingerprint: "fp-repairable",
		InputHash:        latestHash,
		MinimizedInput:   latestPath,
		InputBytes:       latestBytes,
		IssueURL:         "https://github.com/NimbleTerminal/nthtml/issues/1",
		GitSHA:           "c024ebde43e6",
		GoVersion:        "go1.27.3",
		BlamedModule:     "example.com/dep",
		BlamedVersion:    "v1.0.0",
		BlamedFile:       "dep.go",
		BlamedLine:       42,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Inject the pre-fix inconsistent state directly into crashes and record a blocked replay.
	if _, err := st.db.ExecContext(ctx, `UPDATE crashes SET input_hash = ? WHERE id = ?`, origHash, repairableID); err != nil {
		t.Fatal(err)
	}
	mismatchedCrash, err := st.GetCrash(ctx, repairableID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordReplay(ctx, mismatchedCrash, CrashReplay{
		GitSHA:     "141de7abe3fc",
		CheckedAt:  time.Now().UTC(),
		Outcome:    "blocked",
		Diagnostic: "saved input hash mismatch",
	}); err != nil {
		t.Fatal(err)
	}

	// Crash 2 (missing file on disk, even though DB has input_bytes): must NOT blindly overwrite input_hash.
	missingBytes := []byte("go test fuzz v1\nstring(\"missing-on-disk\")\n")
	missingHash := HashCrashInput(missingBytes)
	missingID, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        firstSeen,
		LastSeen:         lastSeen,
		Stack:            "panic: missing file",
		StackFingerprint: "fp-missing",
		InputHash:        "11111111111111111111111111111111",
		MinimizedInput:   filepath.Join(corpusDir, missingHash),
		InputBytes:       missingBytes,
		GitSHA:           "c024ebde43e6",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Crash 3 (corrupt corpus file on disk: invalid header).
	corruptBytes := []byte("not a valid go fuzz corpus file\n")
	corruptHash := HashCrashInput(corruptBytes)
	corruptPath := writeCorpusFile(corpusDir, corruptHash, corruptBytes, lastSeen)
	corruptID, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        firstSeen,
		LastSeen:         lastSeen,
		Stack:            "panic: corrupt file",
		StackFingerprint: "fp-corrupt",
		InputHash:        "22222222222222222222222222222222",
		MinimizedInput:   corruptPath,
		InputBytes:       corruptBytes,
		GitSHA:           "c024ebde43e6",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Crash 4 (ambiguous: DB input_bytes and disk minimized_input file disagree).
	ambigDiskBytes := []byte("go test fuzz v1\nstring(\"disk-variant\")\n")
	ambigDiskHash := HashCrashInput(ambigDiskBytes)
	ambigDiskPath := writeCorpusFile(corpusDir, ambigDiskHash, ambigDiskBytes, lastSeen)
	ambigDBBytes := []byte("go test fuzz v1\nstring(\"db-variant\")\n")
	ambigID, err := st.RecordCrash(ctx, Crash{
		TargetID:         tID,
		FirstSeen:        firstSeen,
		LastSeen:         lastSeen,
		Stack:            "panic: ambiguous",
		StackFingerprint: "fp-ambiguous",
		InputHash:        "33333333333333333333333333333333",
		MinimizedInput:   ambigDiskPath,
		InputBytes:       ambigDBBytes,
		GitSHA:           "c024ebde43e6",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Non-dry-run without crash IDs or All=true must be rejected.
	if _, err := st.RepairCrashes(ctx, CrashRepairOptions{DryRun: false}); !errors.Is(err, ErrRepairSelectionRequired) {
		t.Fatalf("expected ErrRepairSelectionRequired, got %v", err)
	}

	// Dry-run across all crashes inspects everything without mutating the database.
	dryReport, err := st.RepairCrashes(ctx, CrashRepairOptions{DryRun: true})
	if err != nil {
		t.Fatalf("RepairCrashes dry-run: %v", err)
	}
	if len(dryReport.Actions) != 4 {
		t.Fatalf("expected 4 actions in dry-run report, got %d", len(dryReport.Actions))
	}
	statusByID := map[int64]CrashRepairAction{}
	for _, a := range dryReport.Actions {
		statusByID[a.CrashID] = a
	}
	if statusByID[repairableID].Status != RepairStatusRepairable || statusByID[repairableID].NewInputHash != latestHash || !statusByID[repairableID].ReplayEligible {
		t.Fatalf("unexpected repairable dry-run action: %+v", statusByID[repairableID])
	}
	if statusByID[missingID].Status != RepairStatusMissing {
		t.Fatalf("expected missing status for crash %d, got %+v", missingID, statusByID[missingID])
	}
	if statusByID[corruptID].Status != RepairStatusCorrupt {
		t.Fatalf("expected corrupt status for crash %d, got %+v", corruptID, statusByID[corruptID])
	}
	if statusByID[ambigID].Status != RepairStatusAmbiguous {
		t.Fatalf("expected ambiguous status for crash %d, got %+v", ambigID, statusByID[ambigID])
	}

	// Verify dry-run did not modify crash 1 in the database.
	afterDry, err := st.GetCrash(ctx, repairableID)
	if err != nil || afterDry.InputHash != origHash {
		t.Fatalf("dry-run modified crash 1: %+v (err=%v)", afterDry, err)
	}

	// Apply repair with explicit crash ID selection.
	applyReport, err := st.RepairCrashes(ctx, CrashRepairOptions{
		CrashIDs: []int64{repairableID, missingID, corruptID, ambigID},
		DryRun:   false,
	})
	if err != nil {
		t.Fatalf("RepairCrashes apply: %v", err)
	}
	appliedByID := map[int64]CrashRepairAction{}
	for _, a := range applyReport.Actions {
		appliedByID[a.CrashID] = a
	}
	if appliedByID[repairableID].Status != RepairStatusRepaired {
		t.Fatalf("expected repaired status for crash %d, got %+v", repairableID, appliedByID[repairableID])
	}
	if appliedByID[missingID].Status != RepairStatusMissing || appliedByID[corruptID].Status != RepairStatusCorrupt || appliedByID[ambigID].Status != RepairStatusAmbiguous {
		t.Fatalf("unexpected non-repairable statuses on apply: %+v", applyReport.Actions)
	}

	// Verify crash 1 was updated atomically while preserving ID, timestamps, issue_url, blame, fixed_at=nil, and historical replays.
	repaired, err := st.GetCrash(ctx, repairableID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.ID != repairableID ||
		repaired.InputHash != latestHash ||
		repaired.MinimizedInput != latestPath ||
		!bytes.Equal(repaired.InputBytes, latestBytes) ||
		repaired.FixedAt != nil ||
		repaired.IssueURL != "https://github.com/NimbleTerminal/nthtml/issues/1" ||
		repaired.GitSHA != "c024ebde43e6" ||
		repaired.BlamedModule != "example.com/dep" ||
		!repaired.FirstSeen.Equal(firstSeen) ||
		!repaired.LastSeen.Equal(lastSeen) {
		t.Fatalf("repaired crash metadata mismatch: %+v", repaired)
	}

	// Verify missing, corrupt, and ambiguous records were NOT modified.
	for id, wantHash := range map[int64]string{
		missingID: "11111111111111111111111111111111",
		corruptID: "22222222222222222222222222222222",
		ambigID:   "33333333333333333333333333333333",
	} {
		unchanged, err := st.GetCrash(ctx, id)
		if err != nil || unchanged.InputHash != wantHash {
			t.Fatalf("crash %d should remain unmodified with hash %s, got %+v (err=%v)", id, wantHash, unchanged, err)
		}
	}

	// Verify historical replay record for origHash is preserved AND NeedsReplay is immediately true on current revision 141de7abe3fc.
	replays, err := st.ListCrashReplays(ctx)
	if err != nil || len(replays) != 1 || replays[0].InputHash != origHash || replays[0].Outcome != "blocked" {
		t.Fatalf("expected historical blocked replay for %s preserved, got %+v (err=%v)", origHash, replays, err)
	}
	needed, err := st.NeedsReplay(ctx, repaired, "141de7abe3fc")
	if err != nil || !needed {
		t.Fatalf("expected repaired crash to immediately need replay on 141de7abe3fc, got needed=%t err=%v", needed, err)
	}
}
