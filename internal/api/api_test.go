package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/NimbleTerminal/ntfuzzy/internal/store"
)

type stubWorkerStatus struct {
	jobs []ActiveJob
}

func (s stubWorkerStatus) ActiveJobs() []ActiveJob { return s.jobs }
func (s stubWorkerStatus) WorkerCount() int        { return 2 }

func TestAPIServerAndClient(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "api_test.duckdb"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	pID, err := st.UpsertProject(ctx, "demo-codec", "./examples/demo-codec", "main", &now)
	if err != nil {
		t.Fatalf("UpsertProject: %v", err)
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
		Crashes:        1,
		CoveragePct:    81.4,
		Status:         "crash",
		Worker:         "local-1",
	})
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	_, err = st.RecordCrash(ctx, store.Crash{
		TargetID:       tID,
		FirstSeen:      now,
		LastSeen:       now,
		Stack:          "panic: malformed packet header",
		InputHash:      "cafebabe00112233",
		MinimizedInput: "testdata/fuzz/FuzzDecodePacket/cafebabe00112233",
	})
	if err != nil {
		t.Fatalf("RecordCrash: %v", err)
	}

	srv := NewServer(st, stubWorkerStatus{
		jobs: []ActiveJob{
			{
				WorkerID:  "local-1",
				Project:   "demo-codec",
				Package:   "example.com/demo/codec",
				Target:    "FuzzDecodePacket",
				FuzzTime:  "10s",
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
	if status.OpenCrashes != 1 || status.RunsLast24h != 1 || status.ActiveWorkers != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}

	projects, err := client.GetProjects(ctx)
	if err != nil {
		t.Fatalf("GetProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].Name != "demo-codec" {
		t.Fatalf("unexpected projects: %+v", projects)
	}

	targets, err := client.GetTargets(ctx, "demo-codec")
	if err != nil {
		t.Fatalf("GetTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Name != "FuzzDecodePacket" {
		t.Fatalf("unexpected targets: %+v", targets)
	}

	runs, err := client.GetRuns(ctx, strconv.FormatInt(tID, 10), 20)
	if err != nil {
		t.Fatalf("GetRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Execs != 84000 {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	crashes, err := client.GetCrashes(ctx, true, "")
	if err != nil {
		t.Fatalf("GetCrashes: %v", err)
	}
	if len(crashes) != 1 || crashes[0].InputHash != "cafebabe00112233" {
		t.Fatalf("unexpected crashes: %+v", crashes)
	}

	history, err := client.GetCoverageHistory(ctx, strconv.FormatInt(tID, 10))
	if err != nil {
		t.Fatalf("GetCoverageHistory: %v", err)
	}
	if len(history) != 1 || history[0].CoveragePct != 81.4 {
		t.Fatalf("unexpected history: %+v", history)
	}
}
