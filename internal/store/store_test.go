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

	now := time.Now().UTC().Truncate(time.Millisecond)
	projID, err := st.UpsertProject(ctx, "demo-parser", "./examples/demo-parser", "main", &now)
	if err != nil {
		t.Fatalf("UpsertProject insert failed: %v", err)
	}
	if projID <= 0 {
		t.Fatalf("expected positive project ID, got %d", projID)
	}

	// Upsert again to test update branch
	later := now.Add(time.Minute)
	projID2, err := st.UpsertProject(ctx, "demo-parser", "./examples/demo-parser", "main", &later)
	if err != nil {
		t.Fatalf("UpsertProject update failed: %v", err)
	}
	if projID != projID2 {
		t.Fatalf("expected same project ID %d, got %d", projID, projID2)
	}

	targetID, err := st.UpsertTarget(ctx, projID, "example.com/demo/parser", "FuzzParseExpr")
	if err != nil {
		t.Fatalf("UpsertTarget failed: %v", err)
	}
	targetID2, err := st.UpsertTarget(ctx, projID, "example.com/demo/parser", "FuzzParseExpr")
	if err != nil {
		t.Fatalf("UpsertTarget duplicate failed: %v", err)
	}
	if targetID != targetID2 {
		t.Fatalf("expected same target ID %d, got %d", targetID, targetID2)
	}

	// Insert two runs to verify latest_target_status view and coverage trend
	_, err = st.InsertRun(ctx, Run{
		TargetID:       targetID,
		Started:        now.Add(-5 * time.Minute),
		DurationSec:    5.0,
		Execs:          50000,
		ExecsPerSec:    10000,
		NewInteresting: 3,
		Crashes:        0,
		CoveragePct:    62.5,
		Status:         "ok",
		Worker:         "worker-1",
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
		Crashes:        1,
		CoveragePct:    74.2,
		Status:         "crash",
		Worker:         "worker-1",
	})
	if err != nil {
		t.Fatalf("InsertRun 2 failed: %v", err)
	}

	crashID, err := st.RecordCrash(ctx, Crash{
		TargetID:       targetID,
		FirstSeen:      now,
		LastSeen:       now,
		Stack:          "panic: index out of range [4] with length 4",
		InputHash:      "deadbeef12345678",
		MinimizedInput: "testdata/fuzz/FuzzParseExpr/deadbeef12345678",
	})
	if err != nil {
		t.Fatalf("RecordCrash failed: %v", err)
	}
	if crashID <= 0 {
		t.Fatalf("expected positive crashID, got %d", crashID)
	}

	// Recording the same crash input hash updates last_seen
	crashID2, err := st.RecordCrash(ctx, Crash{
		TargetID:       targetID,
		FirstSeen:      now,
		LastSeen:       later,
		Stack:          "panic: index out of range [4] with length 4",
		InputHash:      "deadbeef12345678",
		MinimizedInput: "testdata/fuzz/FuzzParseExpr/deadbeef12345678",
	})
	if err != nil {
		t.Fatalf("RecordCrash duplicate failed: %v", err)
	}
	if crashID != crashID2 {
		t.Fatalf("expected same crashID %d, got %d", crashID, crashID2)
	}

	projects, err := st.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects failed: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].TargetCount != 1 || projects[0].TotalRuns != 2 || projects[0].OpenCrashes != 1 {
		t.Fatalf("unexpected project summary: %+v", projects[0])
	}

	targets, err := st.ListTargets(ctx, "demo-parser")
	if err != nil {
		t.Fatalf("ListTargets failed: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].CoveragePct != 74.2 || len(targets[0].CoverageTrend) != 2 {
		t.Fatalf("unexpected target coverage state: %+v", targets[0])
	}

	runs, err := st.ListRuns(ctx, "FuzzParseExpr", 10)
	if err != nil {
		t.Fatalf("ListRuns failed: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}

	crashes, err := st.ListCrashes(ctx, true, "")
	if err != nil {
		t.Fatalf("ListCrashes failed: %v", err)
	}
	if len(crashes) != 1 {
		t.Fatalf("expected 1 open crash, got %d", len(crashes))
	}

	history, err := st.GetCoverageHistory(ctx, "FuzzParseExpr", 10)
	if err != nil {
		t.Fatalf("GetCoverageHistory failed: %v", err)
	}
	if len(history) != 2 || history[0].CoveragePct != 62.5 || history[1].CoveragePct != 74.2 {
		t.Fatalf("unexpected coverage history: %+v", history)
	}

	stats, err := st.GetFleetStats(ctx)
	if err != nil {
		t.Fatalf("GetFleetStats failed: %v", err)
	}
	if stats.TotalProjects != 1 || stats.TotalTargets != 1 || stats.OpenCrashes != 1 || stats.RunsLast24h != 2 {
		t.Fatalf("unexpected fleet stats: %+v", stats)
	}
}
