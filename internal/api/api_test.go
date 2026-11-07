package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nimbleterminal.dev/ntfuzz/internal/orchestrator"
	"nimbleterminal.dev/ntfuzz/internal/store"
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
	defer func() { _ = st.Close() }()

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
		Diagnostic:     "bounded failure detail",
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
		GoVersion:      "go1.27",
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
		GoVersion:        "go1.27",
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

	// Verify no wildcard CORS header is emitted
	rawResp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = rawResp.Body.Close()
	if got := rawResp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Access-Control-Allow-Origin header, got %q", got)
	}

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
	if len(runs) != 1 || runs[0].Diagnostic != "bounded failure detail" || runs[0].Execs != 84000 || runs[0].GitSHA != "a1b2c3d4e5f6" {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	crashes, err := client.GetCrashes(ctx, true, "")
	if err != nil {
		t.Fatalf("GetCrashes: %v", err)
	}
	if len(crashes) != 1 || crashes[0].InputHash != "cafebabe00112233" {
		t.Fatalf("unexpected crashes: %+v", crashes)
	}

	// Test invalid ?open= parameter returns 400
	badOpenResp, err := http.Get(ts.URL + "/api/crashes?open=invalid_bool")
	if err != nil {
		t.Fatal(err)
	}
	_ = badOpenResp.Body.Close()
	if badOpenResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid ?open= parameter, got %d", badOpenResp.StatusCode)
	}

	// Test malformed JSON on POST /api/crashes/{id}/fix returns 400
	badJSONResp, err := http.Post(ts.URL+"/api/crashes/1/fix", "application/json", strings.NewReader("{bad json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = badJSONResp.Body.Close()
	if badJSONResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed JSON body, got %d", badJSONResp.StatusCode)
	}

	// Test unknown crash ID on POST /api/crashes/{id}/fix returns 404
	notFoundResp, err := http.Post(ts.URL+"/api/crashes/99999/fix", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = notFoundResp.Body.Close()
	if notFoundResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown crash ID, got %d", notFoundResp.StatusCode)
	}

	// Mark crash fixed via POST /api/crashes/{id}/fix
	if err := client.MarkCrashFixed(ctx, crashID, "https://github.com/example/codec/issues/7"); err != nil {
		t.Fatalf("MarkCrashFixed: %v", err)
	}
	openAfterFix, err := client.GetCrashes(ctx, true, "")
	if err != nil || len(openAfterFix) != 0 {
		t.Fatalf("expected 0 open crashes after MarkCrashFixed, got %+v (err=%v)", openAfterFix, err)
	}
	allCrashes, err := client.GetCrashes(ctx, false, "")
	if err != nil || len(allCrashes) != 1 || allCrashes[0].FixedAt == nil {
		t.Fatalf("expected 1 total crash when open=false, got %+v (err=%v)", allCrashes, err)
	}

	history, err := client.GetCoverageHistory(ctx, strconv.FormatInt(tID, 10))
	if err != nil {
		t.Fatalf("GetCoverageHistory: %v", err)
	}
	if len(history) != 1 || history[0].CoveragePct != 81.4 || history[0].CorpusCount != 19 {
		t.Fatalf("unexpected history: %+v", history)
	}

	for _, q := range []string{StatsQueryThroughput, StatsQueryCoverage, StatsQueryOutcomes} {
		stats, err := client.GetStats(ctx, q, StatsWindow24h, "demo-codec", "")
		if err != nil {
			t.Fatalf("GetStats(%s): %v", q, err)
		}
		if stats.Query != q || stats.Window != StatsWindow24h || stats.TotalRuns != 1 || stats.TotalExecs != 84000 || len(stats.Buckets) != 1 {
			t.Fatalf("unexpected stats for %s: %+v", q, stats)
		}
	}

	// Invalid stats query or window returns 400
	badStatsResp, err := http.Get(ts.URL + "/api/stats/not-a-query?window=24h")
	if err != nil {
		t.Fatal(err)
	}
	_ = badStatsResp.Body.Close()
	if badStatsResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid stats query, got %d", badStatsResp.StatusCode)
	}
}

func TestRevisionAndReplayEndpoints(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "revisions.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	if _, err = st.SyncProject(ctx, store.ProjectSyncInput{Name: "repo", Repo: "local", GitSHA: "abc", SyncedAt: &now}); err != nil {
		t.Fatal(err)
	}
	for _, handler := range []http.Handler{NewServer(st, nil), NewSnapshotServer(st, store.Snapshot{PublishedAt: now})} {
		for _, path := range []string{"/api/stats/revisions?window=24h", "/api/crash-replays"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
			}
			if strings.Contains(path, "revisions") && !strings.Contains(rec.Body.String(), `"baseline":true`) {
				t.Fatalf("missing baseline: %s", rec.Body.String())
			}
		}
		for _, path := range []string{"/api/stats/revisions?window=bogus", "/api/stats/revisions?target=1"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if rec.Code != 400 {
				t.Fatalf("invalid stats query accepted: %s", path)
			}
		}
	}
}
