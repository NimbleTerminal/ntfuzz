package store

import (
	"context"
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
	defer st.Close()

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
