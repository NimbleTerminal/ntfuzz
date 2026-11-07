package metrics

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResolveListenAddr(t *testing.T) {
	// Disabled by default when neither --metrics nor --metrics-listen is set.
	addr, err := ResolveListenAddr(false, "", DefaultServeMetricsPort)
	if err != nil || addr != "" {
		t.Fatalf("expected disabled empty addr, got %q, err=%v", addr, err)
	}

	// Enabled via --metrics binds to loopback on default port.
	addr, err = ResolveListenAddr(true, "", DefaultServeMetricsPort)
	if err != nil || addr != "127.0.0.1:9090" {
		t.Fatalf("expected 127.0.0.1:9090, got %q, err=%v", addr, err)
	}

	// Omitted host in --metrics-listen :9464 binds to loopback by default.
	addr, err = ResolveListenAddr(false, ":9464", DefaultServeMetricsPort)
	if err != nil || addr != "127.0.0.1:9464" {
		t.Fatalf("expected 127.0.0.1:9464, got %q, err=%v", addr, err)
	}

	// Explicit host:port is preserved.
	addr, err = ResolveListenAddr(false, "127.0.0.1:0", DefaultViewMetricsPort)
	if err != nil || addr != "127.0.0.1:0" {
		t.Fatalf("expected 127.0.0.1:0, got %q, err=%v", addr, err)
	}

	// Invalid addresses fail validation.
	if _, err := ResolveListenAddr(false, "not-an-addr", DefaultServeMetricsPort); err == nil {
		t.Fatal("expected error for malformed listen address")
	}
	if _, err := ResolveListenAddr(false, "127.0.0.1:99999", DefaultServeMetricsPort); err == nil {
		t.Fatal("expected error for out-of-range port")
	}
}

func TestBoundedRouteMethodStatusClassification(t *testing.T) {
	cases := map[string]string{
		"/":                                "/",
		"/ws":                              "/ws",
		"/wt":                              "/wt",
		"/cert-hash":                       "/cert-hash",
		"/static/booba/booba.js":           "/static/*",
		"/static/ntwebterm/secret-id.wasm": "/static/*",
		"/api/projects":                    "/api/projects",
		"/api/targets":                     "/api/targets",
		"/api/runs":                        "/api/runs",
		"/api/crashes":                     "/api/crashes",
		"/api/crashes/42/fix":              "/api/crashes/{id}/fix",
		"/api/crashes/abc-123/fix":         "/api/crashes/{id}/fix",
		"/api/crashes/a/b/fix":             "other",
		"/api/coverage/history":            "/api/coverage/history",
		"/api/stats":                       "/api/stats",
		"/api/stats/throughput":            "/api/stats/{query}",
		"/api/stats/coverage":              "/api/stats/{query}",
		"/api/stats/a/b":                   "other",
		"/api/status":                      "/api/status",
		"/metrics":                         "other",
		"/arbitrary/repo/target":           "other",
	}
	for in, want := range cases {
		if got := ClassifyRoute(in); got != want {
			t.Errorf("ClassifyRoute(%q) = %q, want %q", in, got, want)
		}
	}

	if got := ClassifyMethod("get"); got != "GET" {
		t.Errorf("ClassifyMethod(get) = %q, want GET", got)
	}
	if got := ClassifyMethod("PROPFIND"); got != "OTHER" {
		t.Errorf("ClassifyMethod(PROPFIND) = %q, want OTHER", got)
	}

	if got := ClassifyStatus(101); got != "1xx" {
		t.Errorf("ClassifyStatus(101) = %q, want 1xx", got)
	}
	if got := ClassifyStatus(200); got != "2xx" {
		t.Errorf("ClassifyStatus(200) = %q, want 2xx", got)
	}
	if got := ClassifyStatus(404); got != "4xx" {
		t.Errorf("ClassifyStatus(404) = %q, want 4xx", got)
	}
	if got := ClassifyStatus(503); got != "5xx" {
		t.Errorf("ClassifyStatus(503) = %q, want 5xx", got)
	}
}

type fakeHijackWriter struct {
	*httptest.ResponseRecorder
	flushed  bool
	hijacked bool
	deadline time.Time
}

func (w *fakeHijackWriter) Flush() {
	w.flushed = true
	w.ResponseRecorder.Flush()
}

func (w *fakeHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

func (w *fakeHijackWriter) SetWriteDeadline(d time.Time) error {
	w.deadline = d
	return nil
}

func TestResponseRecorderPreservesOptionalInterfaces(t *testing.T) {
	m := New(ModeServe)
	base := &fakeHijackWriter{ResponseRecorder: httptest.NewRecorder()}
	wantDeadline := time.Now().Add(time.Minute)

	h := m.InstrumentHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(wantDeadline); err != nil {
			t.Errorf("ResponseController.SetWriteDeadline: %v", err)
		}
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		} else {
			t.Error("wrapped ResponseWriter does not implement http.Flusher")
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("wrapped ResponseWriter does not implement http.Hijacker")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("Hijack: %v", err)
		}
		_ = conn.Close()
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "/ws?session=secret&ip=1.2.3.4", nil)
	h.ServeHTTP(base, req)

	if !base.flushed {
		t.Error("expected Flush to reach underlying writer")
	}
	if !base.hijacked {
		t.Error("expected Hijack to reach underlying writer")
	}
	if !base.deadline.Equal(wantDeadline) {
		t.Errorf("expected Unwrap to preserve SetWriteDeadline, got %v", base.deadline)
	}
}

func TestViewerModeDoesNotFabricateOrchestratorMetrics(t *testing.T) {
	viewMetrics := New(ModeView)
	viewMetrics.RegisterWorkerStatus(func() int { return 4 }, func() int { return 2 })

	mfs, err := viewMetrics.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	names := make(map[string]bool, len(mfs))
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	if names["ntfuzz_orchestrator_workers_active"] || names["ntfuzz_orchestrator_workers_configured"] {
		t.Fatalf("viewer mode must not expose orchestrator worker metrics: %v", names)
	}
	if names["ntfuzz_snapshot_publications_total"] {
		t.Fatalf("viewer mode must not expose orchestrator publication metrics: %v", names)
	}
	if !names["ntfuzz_snapshot_age_seconds"] || !names["ntfuzz_snapshot_refreshes_total"] {
		t.Fatalf("viewer mode missing snapshot age/refresh metrics: %v", names)
	}
}

func TestMetricsServerIsolation(t *testing.T) {
	m := New(ModeServe)
	m.RecordSnapshotPublication(25*time.Millisecond, nil)
	m.RecordSnapshotPublication(10*time.Millisecond, errors.New("disk full"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr, stop, err := m.StartServer(ctx, "127.0.0.1:0", nil)
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	defer func() { _ = stop() }()

	baseURL := "http://" + addr.String()

	// GET /metrics succeeds on the dedicated metrics listener.
	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on /metrics, got %d", resp.StatusCode)
	}
	text := string(body)
	if !strings.Contains(text, `ntfuzz_snapshot_publications_total{mode="serve",result="success"} 1`) {
		t.Fatalf("missing publication success metric in:\n%s", text)
	}
	if !strings.Contains(text, `ntfuzz_snapshot_publications_total{mode="serve",result="error"} 1`) {
		t.Fatalf("missing publication error metric in:\n%s", text)
	}

	// Non-metrics routes on the metrics listener return 404.
	respAPI, err := http.Get(baseURL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status on metrics listener: %v", err)
	}
	_ = respAPI.Body.Close()
	if respAPI.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for /api/status on metrics listener, got %d", respAPI.StatusCode)
	}

	// Non-GET/HEAD on /metrics returns 405.
	respPost, err := http.Post(baseURL+"/metrics", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("POST /metrics: %v", err)
	}
	_ = respPost.Body.Close()
	if respPost.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST /metrics, got %d", respPost.StatusCode)
	}
}
