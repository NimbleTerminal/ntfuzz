package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/NimbleTerminal/ntfuzzy/internal/orchestrator"
	"github.com/NimbleTerminal/ntfuzzy/internal/store"
)

type stubWorkerStatus struct {
	jobs []orchestrator.ActiveJob
}

func (s stubWorkerStatus) ActiveJobs() []orchestrator.ActiveJob { return s.jobs }
func (s stubWorkerStatus) WorkerCount() int                     { return 2 }

func TestAPIServerAndClient(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "api_test.duckdb"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	pID, err := st.SyncProject(ctx, store.ProjectSyncInput{
		Name:     "demo-codec",
		Repo:     "./examples/demo-codec",
		Branch:   "main",
		GitSHA:   "a1b2c3d4e5f6",
		Dirty:    false,
		SyncedAt: &now,
	})
	if err != nil {
		t.Fatalf("SyncProject: %v", err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "example.com/demo/codec", "FuzzDecodePacket")
	if err != nil {
		t.Fatalf("UpsertTarget: %v", err)
	}
	_, err = st.InsertRun(ctx, store.Run{
		TargetID:       tID,
		Started:        now,
		DurationSec:    4.2,
		Execs:          84000,
		ExecsPerSec:    20000,
		NewInteresting: 5,
		CorpusCount:    19,
		Crashes:        1,
		CoveragePct:    81.4,
		Status:         "crash",
		Worker:         "local-1",
		GitSHA:         "a1b2c3d4e5f6",
		GoVersion:      "go1.27.3",
	})
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	crashID, err := st.RecordCrash(ctx, store.Crash{
		TargetID:         tID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            "panic: malformed packet header",
		StackFingerprint: "fp-malformed-header",
		InputHash:        "cafebabe00112233",
		MinimizedInput:   ".ntfuzz/corpus/demo-codec/crashes/FuzzDecodePacket/cafebabe00112233",
		GitSHA:           "a1b2c3d4e5f6",
		GoVersion:        "go1.27.3",
	})
	if err != nil {
		t.Fatalf("RecordCrash: %v", err)
	}

	srv := NewServer(st, stubWorkerStatus{
		jobs: []orchestrator.ActiveJob{
			{
				WorkerID:  "local-1",
				Project:   "demo-codec",
				Package:   "example.com/demo/codec",
				Target:    "FuzzDecodePacket",
				FuzzTime:  "10s",
				GitSHA:    "a1b2c3d4e5f6",
				StartedAt: now,
				Status:    "fuzzing",
			},
		},
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL)

	status, err := client.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status.OpenCrashes != 1 || status.RunsLast24h != 1 || status.ActiveWorkers != 1 || status.SchemaVersion != store.CurrentSchemaVersion {
		t.Fatalf("unexpected status: %+v", status)
	}

	projects, err := client.GetProjects(ctx)
	if err != nil {
		t.Fatalf("GetProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].Name != "demo-codec" || projects[0].GitSHA != "a1b2c3d4e5f6" {
		t.Fatalf("unexpected projects: %+v", projects)
	}

	targets, err := client.GetTargets(ctx, "demo-codec")
	if err != nil {
		t.Fatalf("GetTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Name != "FuzzDecodePacket" || targets[0].CorpusCount != 19 {
		t.Fatalf("unexpected targets: %+v", targets)
	}

	runs, err := client.GetRuns(ctx, strconv.FormatInt(tID, 10), 20)
	if err != nil {
		t.Fatalf("GetRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Execs != 84000 || runs[0].GitSHA != "a1b2c3d4e5f6" {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	crashes, err := client.GetCrashes(ctx, true, "")
	if err != nil {
		t.Fatalf("GetCrashes: %v", err)
	}
	if len(crashes) != 1 || crashes[0].InputHash != "cafebabe00112233" {
		t.Fatalf("unexpected crashes: %+v", crashes)
	}

	// Mark crash fixed via POST /api/crashes/{id}/fix
	if err := client.MarkCrashFixed(ctx, crashID, "https://github.com/example/codec/issues/7"); err != nil {
		t.Fatalf("MarkCrashFixed: %v", err)
	}
	openAfterFix, err := client.GetCrashes(ctx, true, "")
	if err != nil || len(openAfterFix) != 0 {
		t.Fatalf("expected 0 open crashes after MarkCrashFixed, got %+v (err=%v)", openAfterFix, err)
	}

	history, err := client.GetCoverageHistory(ctx, strconv.FormatInt(tID, 10))
	if err != nil {
		t.Fatalf("GetCoverageHistory: %v", err)
	}
	if len(history) != 1 || history[0].CoveragePct != 81.4 || history[0].CorpusCount != 19 {
		t.Fatalf("unexpected history: %+v", history)
	}
}
