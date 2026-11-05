package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NimbleTerminal/ntfuzz/internal/api"
	"github.com/NimbleTerminal/ntfuzz/internal/metrics"
	"github.com/NimbleTerminal/ntfuzz/internal/store"
)

func TestSnapshotIsolationAndRefresh(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writer, err := store.Open(filepath.Join(dir, "live.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)
	project, err := writer.SyncProject(ctx, store.ProjectSyncInput{Name: "demo' OR 1=1 --", Repo: "https://example.com/demo", SyncedAt: &now, GitSHA: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := writer.UpsertTarget(ctx, project, "example.com/demo", "FuzzParse")
	if err != nil {
		t.Fatal(err)
	}
	_, err = writer.InsertRun(ctx, store.Run{TargetID: target, Started: now, DurationSec: 1, Execs: 123, ExecsPerSec: 123, NewInteresting: 2, CorpusCount: 3, CoveragePct: 45.6, Status: "ok", Worker: "local-1", GitSHA: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	crash, err := writer.RecordCrash(ctx, store.Crash{TargetID: target, FirstSeen: now, LastSeen: now, Stack: "NULL", InputHash: "input", MinimizedInput: "'quoted'"})
	if err != nil {
		t.Fatal(err)
	}
	snapshots := filepath.Join(dir, "snapshots")
	first, err := writer.PublishSnapshot(ctx, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Open(snapshots)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	live := api.NewServer(writer, nil)
	for _, path := range []string{"/api/projects", "/api/targets", "/api/runs", "/api/crashes", "/api/coverage/history?target=FuzzParse", "/api/targets?project=demo%27%20OR%201%3D1%20--", "/api/targets?project=%27%20OR%201%3D1%20--", "/api/runs?target=%27%20OR%201%3D1%20--"} {
		want := httptest.NewRecorder()
		live.ServeHTTP(want, httptest.NewRequest("GET", path, nil))
		got := httptest.NewRecorder()
		v.ServeHTTP(got, httptest.NewRequest("GET", path, nil))
		if want.Code != 200 || got.Code != 200 {
			t.Fatalf("%s: live=%d %s snapshot=%d %s", path, want.Code, want.Body, got.Code, got.Body)
		}
		var a, b any
		if err := json.Unmarshal(want.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(got.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s mismatch:\n%v\n%v", path, a, b)
		}
	}
	response := httptest.NewRecorder()
	v.ServeHTTP(response, httptest.NewRequest("POST", "/api/crashes/1/fix", nil))
	if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("write accepted: %d", response.Code)
	}
	if err = writer.MarkCrashFixed(ctx, crash, now, ""); err != nil {
		t.Fatal(err)
	}
	old, err := v.db.ListCrashes(ctx, true, "")
	if err != nil || len(old) != 1 {
		t.Fatalf("snapshot changed with writer: %v %v", old, err)
	}
	if err = v.db.MarkCrashFixed(ctx, crash, now, ""); err != store.ErrReadOnly {
		t.Fatalf("read-only store accepted write: %v", err)
	}
	second, err := writer.PublishSnapshot(ctx, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if second.File == first.File {
		t.Fatal("generation reused")
	}
	if err = v.Refresh(); err != nil {
		t.Fatal(err)
	}
	current, err := v.db.ListCrashes(ctx, true, "")
	if err != nil || len(current) != 0 {
		t.Fatalf("refresh failed: %v %v", current, err)
	}
	if err = os.WriteFile(filepath.Join(snapshots, "current.json"), []byte(`{"file":"../live.duckdb"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err = v.Refresh(); err == nil {
		t.Fatal("unsafe manifest accepted")
	}
	response = httptest.NewRecorder()
	v.ServeHTTP(response, httptest.NewRequest("GET", "/api/status", nil))
	var status api.StatusResponse
	if err = json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || !status.ReadOnly || status.SnapshotAt == nil || !status.SnapshotAt.Equal(second.PublishedAt) || status.ActiveWorkers != 0 {
		t.Fatalf("bad snapshot status: %+v", status)
	}
}

func TestSnapshotPublicationAndViewerRefreshMetrics(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	serveMetrics := metrics.New(metrics.ModeServe)
	viewMetrics := metrics.New(metrics.ModeView)

	writer, err := store.Open(filepath.Join(dir, "live.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMetrics(serveMetrics)

	snapshots := filepath.Join(dir, "snapshots")
	first, err := writer.PublishSnapshot(ctx, snapshots)
	if err != nil {
		t.Fatal(err)
	}

	// Open viewer with metrics and verify initial refresh success + snapshot age.
	v, err := OpenWithMetrics(snapshots, viewMetrics)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	// Advance fake viewer clock 12.5 seconds past the published snapshot timestamp.
	viewMetrics.SetClock(func() time.Time {
		return first.PublishedAt.Add(12500 * time.Millisecond)
	})

	// Attempting to publish from the read-only snapshot store fails and records an error publication metric if attached.
	v.db.SetMetrics(serveMetrics)
	if _, err := v.db.PublishSnapshot(ctx, snapshots); err == nil {
		t.Fatal("expected read-only store PublishSnapshot to fail")
	}

	// Corrupt manifest so viewer Refresh fails, then verify error count increments and snapshot age still reflects first.PublishedAt.
	if err := os.WriteFile(filepath.Join(snapshots, "current.json"), []byte(`{"file":"../outside.duckdb"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := v.Refresh(); err == nil {
		t.Fatal("expected Refresh on unsafe manifest to fail")
	}

	serveText := scrapeMetricsText(t, serveMetrics)
	if !strings.Contains(serveText, `ntfuzz_snapshot_publications_total{mode="serve",result="success"} 1`) {
		t.Fatalf("expected 1 successful snapshot publication in:\n%s", serveText)
	}
	if !strings.Contains(serveText, `ntfuzz_snapshot_publications_total{mode="serve",result="error"} 1`) {
		t.Fatalf("expected 1 failed snapshot publication in:\n%s", serveText)
	}
	if !strings.Contains(serveText, `ntfuzz_snapshot_publish_duration_seconds_count{mode="serve",result="success"} 1`) {
		t.Fatalf("expected publication duration histogram count in:\n%s", serveText)
	}

	viewText := scrapeMetricsText(t, viewMetrics)
	if !strings.Contains(viewText, `ntfuzz_snapshot_refreshes_total{mode="view",result="success"} 1`) {
		t.Fatalf("expected 1 successful snapshot refresh in:\n%s", viewText)
	}
	if !strings.Contains(viewText, `ntfuzz_snapshot_refreshes_total{mode="view",result="error"} 1`) {
		t.Fatalf("expected 1 failed snapshot refresh in:\n%s", viewText)
	}
	if !strings.Contains(viewText, `ntfuzz_snapshot_age_seconds{mode="view"} 12.5`) {
		t.Fatalf("expected snapshot age of 12.5s in:\n%s", viewText)
	}

	// Publish a second valid snapshot and verify Refresh updates the snapshot age.
	second, err := writer.PublishSnapshot(ctx, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	viewMetrics.SetClock(func() time.Time {
		return second.PublishedAt.Add(3 * time.Second)
	})
	if err := v.Refresh(); err != nil {
		t.Fatalf("Refresh second generation: %v", err)
	}
	viewText = scrapeMetricsText(t, viewMetrics)
	if !strings.Contains(viewText, `ntfuzz_snapshot_refreshes_total{mode="view",result="success"} 2`) {
		t.Fatalf("expected 2 successful snapshot refreshes in:\n%s", viewText)
	}
	if !strings.Contains(viewText, `ntfuzz_snapshot_age_seconds{mode="view"} 3`) {
		t.Fatalf("expected updated snapshot age of 3s in:\n%s", viewText)
	}
}

func scrapeMetricsText(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics status=%d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}
