package booba

import (
	"context"
	"encoding/json"
	"github.com/NimbleMarkets/go-booba/sip"
	"github.com/coder/websocket"
	dto "github.com/prometheus/client_model/go"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"nimbleterminal.dev/ntfuzz/internal/api"
	"nimbleterminal.dev/ntfuzz/internal/metrics"
	"nimbleterminal.dev/ntfuzz/internal/store"
)

func TestBoobaBridgeHandler(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "booba_test.duckdb"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	apiSrv := api.NewServer(st, nil)
	handler, err := NewHandler(apiSrv, "http://127.0.0.1:8080", BridgeOptions{
		ListenAddr:      "127.0.0.1:8080",
		RefreshInterval: time.Second,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	ts := httptest.NewServer(handler)
	defer ts.Close()

	// 1. Check / serves the embedded Booba index page
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "BoobaTerminal") {
		t.Fatalf("unexpected GET / response (%d): %s", resp.StatusCode, string(body))
	}

	// 2. Check /static/booba/booba.js is served from go-booba/serve's embedded assets
	respStatic, err := http.Get(ts.URL + "/static/booba/booba.js")
	if err != nil {
		t.Fatalf("GET /static/booba/booba.js: %v", err)
	}
	_ = respStatic.Body.Close()
	if respStatic.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for /static/booba/booba.js, got %d", respStatic.StatusCode)
	}

	// 3. Check /api/status is served on the same handler
	respAPI, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	apiBody, _ := io.ReadAll(respAPI.Body)
	_ = respAPI.Body.Close()
	if respAPI.StatusCode != http.StatusOK || !strings.Contains(string(apiBody), "fleet_health") {
		t.Fatalf("unexpected GET /api/status (%d): %s", respAPI.StatusCode, string(apiBody))
	}
}

// Exercise a real PTY and the read-only mode's idle middleware: the terminal
// must redraw for subsequent protocol resizes, not only the initial handshake.
func TestReadOnlyTerminalResizesExistingSession(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "resize.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := metrics.New(metrics.ModeView)
	handler, err := NewHandler(api.NewSnapshotServer(st, store.Snapshot{PublishedAt: time.Now()}), "", BridgeOptions{
		ListenAddr:      "127.0.0.1:8081",
		ReadOnly:        true,
		RefreshInterval: time.Hour,
		Metrics:         m,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	output := make(chan string, 64)
	go func() {
		defer close(output)
		for {
			_, frame, err := conn.Read(ctx)
			if err != nil {
				return
			}
			kind, payload, err := sip.DecodeWSMessage(frame)
			if err == nil && kind == sip.MsgOutput {
				select {
				case output <- string(payload):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	border := regexp.MustCompile("─+")
	for _, cols := range []int{140, 80, 120} {
		data, _ := json.Marshal(sip.ResizeMessage{Cols: cols, Rows: 45})
		if err := conn.Write(ctx, websocket.MessageBinary, sip.EncodeWSMessage(sip.MsgResize, data)); err != nil {
			t.Fatal(err)
		}
		deadline := time.NewTimer(2 * time.Second)
		found := false
		for !found {
			select {
			case frame, ok := <-output:
				if !ok {
					t.Fatal("terminal session closed")
				}
				longest := 0
				for _, line := range border.FindAllString(frame, -1) {
					longest = max(longest, utf8.RuneCountInString(line))
				}
				found = longest == cols-4
			case <-deadline.C:
				t.Fatalf("no redraw for %d columns", cols)
			}
		}
		deadline.Stop()
	}
}

func TestSessionLifecycleBytesAndInternalAPIExemption(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "lifecycle.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m := metrics.New(metrics.ModeServe)
	apiSrv := api.NewServer(st, nil)
	handler, err := NewHandler(apiSrv, "", BridgeOptions{
		ListenAddr:      "127.0.0.1:8080",
		RefreshInterval: 40 * time.Millisecond,
		Metrics:         m,
	})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket.Dial: %v", err)
	}
	defer conn.CloseNow()

	resizePayload, _ := json.Marshal(sip.ResizeMessage{Cols: 100, Rows: 30})
	if err := conn.Write(ctx, websocket.MessageBinary, sip.EncodeWSMessage(sip.MsgResize, resizePayload)); err != nil {
		t.Fatalf("write initial resize: %v", err)
	}

	// Wait until session is opened and initial PTY output is sent.
	waitForCondition(t, 3*time.Second, func() bool {
		return metricValue(t, m, "ntfuzz_terminal_sessions_active", map[string]string{"mode": "serve"}) == 1 &&
			metricValue(t, m, "ntfuzz_terminal_sessions_opened_total", map[string]string{"mode": "serve"}) == 1 &&
			metricValue(t, m, "ntfuzz_terminal_bytes_sent_total", map[string]string{"mode": "serve"}) > 0
	})

	// Send terminal input ("r" keystroke to trigger TUI refresh) and verify input bytes are counted.
	if err := conn.Write(ctx, websocket.MessageBinary, sip.EncodeWSMessage(sip.MsgInput, []byte("r"))); err != nil {
		t.Fatalf("write input: %v", err)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return metricValue(t, m, "ntfuzz_terminal_bytes_received_total", map[string]string{"mode": "serve"}) >= 1
	})

	// Allow in-process TUI refreshes to run; verify internal API calls were NOT counted as external HTTP requests.
	time.Sleep(100 * time.Millisecond)
	for _, route := range []string{"/api/status", "/api/projects", "/api/targets", "/api/crashes"} {
		if got := metricValue(t, m, "ntfuzz_http_requests_total", map[string]string{
			"mode": "serve", "route": route, "method": "GET", "status_class": "2xx",
		}); got != 0 {
			t.Fatalf("internal TUI API poll on %s was counted as external HTTP request: %v", route, got)
		}
	}

	// WebSocket upgrade itself was counted once under route="/ws", status_class="1xx".
	if got := metricValue(t, m, "ntfuzz_http_requests_total", map[string]string{
		"mode": "serve", "route": "/ws", "method": "GET", "status_class": "1xx",
	}); got != 1 {
		t.Fatalf("expected 1 /ws 1xx upgrade request, got %v", got)
	}

	// Disconnect client and verify active returns to 0 and disconnect reason + duration are recorded once.
	_ = conn.Close(websocket.StatusNormalClosure, "bye")
	waitForCondition(t, 3*time.Second, func() bool {
		return metricValue(t, m, "ntfuzz_terminal_sessions_active", map[string]string{"mode": "serve"}) == 0 &&
			metricValue(t, m, "ntfuzz_terminal_sessions_closed_total", map[string]string{"mode": "serve", "reason": "client_disconnect"}) == 1 &&
			metricHistogramCount(t, m, "ntfuzz_terminal_session_duration_seconds", map[string]string{"mode": "serve", "reason": "client_disconnect"}) == 1
	})
}

func TestIdleTimeoutAndServerShutdownDisconnectReasons(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "idle.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 1. Verify idle_timeout disconnect reason.
	mIdle := metrics.New(metrics.ModeView)
	snapSrv := api.NewSnapshotServer(st, store.Snapshot{PublishedAt: time.Now()})
	handlerIdle, err := NewHandler(snapSrv, "", BridgeOptions{
		ListenAddr:      "127.0.0.1:8081",
		ReadOnly:        true,
		IdleTimeout:     80 * time.Millisecond,
		RefreshInterval: time.Hour,
		Metrics:         mIdle,
	})
	if err != nil {
		t.Fatal(err)
	}
	srvIdle := httptest.NewServer(handlerIdle)
	defer srvIdle.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsIdleURL := "ws" + strings.TrimPrefix(srvIdle.URL, "http") + "/ws"
	connIdle, _, err := websocket.Dial(ctx, wsIdleURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connIdle.CloseNow()

	resizePayload, _ := json.Marshal(sip.ResizeMessage{Cols: 80, Rows: 24})
	if err := connIdle.Write(ctx, websocket.MessageBinary, sip.EncodeWSMessage(sip.MsgResize, resizePayload)); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, 3*time.Second, func() bool {
		return metricValue(t, mIdle, "ntfuzz_terminal_sessions_active", map[string]string{"mode": "view"}) == 0 &&
			metricValue(t, mIdle, "ntfuzz_terminal_sessions_closed_total", map[string]string{"mode": "view", "reason": "idle_timeout"}) == 1 &&
			metricHistogramCount(t, mIdle, "ntfuzz_terminal_session_duration_seconds", map[string]string{"mode": "view", "reason": "idle_timeout"}) == 1
	})

	// 2. Verify server_shutdown disconnect reason.
	mShutdown := metrics.New(metrics.ModeView)
	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()

	handlerShutdown, err := NewHandler(snapSrv, "", BridgeOptions{
		ListenAddr:      "127.0.0.1:8081",
		ReadOnly:        true,
		RefreshInterval: time.Hour,
		ServerContext:   srvCtx,
		Metrics:         mShutdown,
	})
	if err != nil {
		t.Fatal(err)
	}
	srvShutdown := httptest.NewServer(handlerShutdown)
	defer srvShutdown.Close()

	wsShutdownURL := "ws" + strings.TrimPrefix(srvShutdown.URL, "http") + "/ws"
	connShutdown, _, err := websocket.Dial(ctx, wsShutdownURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connShutdown.CloseNow()

	if err := connShutdown.Write(ctx, websocket.MessageBinary, sip.EncodeWSMessage(sip.MsgResize, resizePayload)); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return metricValue(t, mShutdown, "ntfuzz_terminal_sessions_active", map[string]string{"mode": "view"}) == 1
	})

	srvCancel()
	_ = connShutdown.CloseNow()

	waitForCondition(t, 3*time.Second, func() bool {
		return metricValue(t, mShutdown, "ntfuzz_terminal_sessions_active", map[string]string{"mode": "view"}) == 0 &&
			metricValue(t, mShutdown, "ntfuzz_terminal_sessions_closed_total", map[string]string{"mode": "view", "reason": "server_shutdown"}) == 1
	})
}

func TestRejectedTerminalConnections(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "reject.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m := metrics.New(metrics.ModeView)
	snapSrv := api.NewSnapshotServer(st, store.Snapshot{PublishedAt: time.Now()})
	handler, err := NewHandler(snapSrv, "", BridgeOptions{
		ListenAddr:      "127.0.0.1:8081",
		ReadOnly:        true,
		MaxConnections:  1,
		RefreshInterval: time.Hour,
		Metrics:         m,
	})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.CloseNow()

	resizePayload, _ := json.Marshal(sip.ResizeMessage{Cols: 80, Rows: 24})
	if err := conn1.Write(ctx, websocket.MessageBinary, sip.EncodeWSMessage(sip.MsgResize, resizePayload)); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		return metricValue(t, m, "ntfuzz_terminal_sessions_active", map[string]string{"mode": "view"}) == 1
	})

	// 1. Second concurrent connection exceeds MaxConnections=1 -> rejected with connection_limit (HTTP 503).
	_, resp2, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("expected second connection to be rejected by MaxConnections=1")
	}
	if resp2 != nil && resp2.Body != nil {
		_ = resp2.Body.Close()
	}
	if resp2 == nil || resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable, got %+v", resp2)
	}
	if got := metricValue(t, m, "ntfuzz_terminal_sessions_rejected_total", map[string]string{"mode": "view", "reason": "connection_limit"}); got != 1 {
		t.Fatalf("expected connection_limit rejected=1, got %v", got)
	}
	if got := metricValue(t, m, "ntfuzz_terminal_sessions_opened_total", map[string]string{"mode": "view"}); got != 1 {
		t.Fatalf("expected opened_total to remain 1, got %v", got)
	}

	// Close first session to free the connection slot.
	_ = conn1.Close(websocket.StatusNormalClosure, "done")
	waitForCondition(t, 3*time.Second, func() bool {
		return metricValue(t, m, "ntfuzz_terminal_sessions_active", map[string]string{"mode": "view"}) == 0
	})

	// 2. Cross-origin WebSocket request with untrusted Origin -> rejected with origin (HTTP 403).
	_, respOrigin, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example.invalid"}},
	})
	if err == nil {
		t.Fatal("expected cross-origin websocket to be rejected")
	}
	if respOrigin != nil && respOrigin.Body != nil {
		_ = respOrigin.Body.Close()
	}
	if got := metricValue(t, m, "ntfuzz_terminal_sessions_rejected_total", map[string]string{"mode": "view", "reason": "origin"}); got != 1 {
		t.Fatalf("expected origin rejected=1, got %v", got)
	}

	// 3. Plain HTTP GET /ws without WebSocket upgrade headers -> rejected with handshake.
	respPlain, err := http.Get(server.URL + "/ws")
	if err != nil {
		t.Fatal(err)
	}
	_ = respPlain.Body.Close()
	if got := metricValue(t, m, "ntfuzz_terminal_sessions_rejected_total", map[string]string{"mode": "view", "reason": "handshake"}); got != 1 {
		t.Fatalf("expected handshake rejected=1 after plain GET /ws, got %v", got)
	}

	// 4. WebSocket connection that sends an oversized initial resize -> rejected with handshake before opening a session.
	connBad, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	badResize, _ := json.Marshal(sip.ResizeMessage{Cols: 99999, Rows: 99999})
	_ = connBad.Write(ctx, websocket.MessageBinary, sip.EncodeWSMessage(sip.MsgResize, badResize))
	_ = connBad.CloseNow()

	waitForCondition(t, 2*time.Second, func() bool {
		return metricValue(t, m, "ntfuzz_terminal_sessions_rejected_total", map[string]string{"mode": "view", "reason": "handshake"}) == 2
	})
	if got := metricValue(t, m, "ntfuzz_terminal_sessions_opened_total", map[string]string{"mode": "view"}); got != 1 {
		t.Fatalf("expected opened_total to remain 1 after rejected handshakes, got %v", got)
	}
}

func TestRequestAccountingAndMetricsListenerIsolation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "isolation.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	dashLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	metricsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := metrics.New(metrics.ModeServe)
	apiSrv := api.NewServer(st, nil)
	done := make(chan error, 1)
	go func() {
		done <- ListenAndServe(ctx, apiSrv, BridgeOptions{
			ListenAddr:      dashLn.Addr().String(),
			Listener:        dashLn,
			RefreshInterval: time.Hour,
			Metrics:         m,
			MetricsListener: metricsLn,
		})
	}()
	defer func() {
		cancel()
		<-done
	}()

	dashURL := "http://" + dashLn.Addr().String()
	metricsURL := "http://" + metricsLn.Addr().String()

	// Make external visitor requests with query strings, target names, repo names, and crash IDs.
	for _, reqSpec := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodGet, "/static/booba/booba.js", http.StatusOK},
		{http.MethodGet, "/api/targets?project=confidential-repo-name", http.StatusOK},
		{http.MethodGet, "/api/crashes?open=notabool&target=FuzzSecretTarget", http.StatusBadRequest},
		{http.MethodPost, "/api/crashes/12345/fix", http.StatusNotFound},
		{http.MethodGet, "/metrics", http.StatusNotFound}, // /metrics MUST NOT be exposed on dashboard listener
	} {
		req, err := http.NewRequest(reqSpec.method, dashURL+reqSpec.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", reqSpec.method, reqSpec.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != reqSpec.want {
			t.Fatalf("%s %s: status=%d, want %d", reqSpec.method, reqSpec.path, resp.StatusCode, reqSpec.want)
		}
	}

	// Scrape the isolated metrics listener.
	resp, err := http.Get(metricsURL + "/metrics")
	if err != nil {
		t.Fatalf("GET %s/metrics: %v", metricsURL, err)
	}
	rawMetrics, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics listener returned %d", resp.StatusCode)
	}
	text := string(rawMetrics)

	// Verify bounded route templates and status classes are recorded.
	for _, wantLine := range []string{
		`ntfuzz_http_requests_total{method="GET",mode="serve",route="/",status_class="2xx"} 1`,
		`ntfuzz_http_requests_total{method="GET",mode="serve",route="/static/*",status_class="2xx"} 1`,
		`ntfuzz_http_requests_total{method="GET",mode="serve",route="/api/targets",status_class="2xx"} 1`,
		`ntfuzz_http_requests_total{method="GET",mode="serve",route="/api/crashes",status_class="4xx"} 1`,
		`ntfuzz_http_requests_total{method="POST",mode="serve",route="/api/crashes/{id}/fix",status_class="4xx"} 1`,
		`ntfuzz_http_requests_total{method="GET",mode="serve",route="other",status_class="4xx"} 1`,
	} {
		if !strings.Contains(text, wantLine) {
			t.Errorf("missing expected metric line %q in:\n%s", wantLine, text)
		}
	}

	// Verify sensitive / high-cardinality tokens never appear in metrics exposition.
	for _, forbidden := range []string{
		"confidential-repo-name",
		"FuzzSecretTarget",
		"12345",
		"notabool",
		"booba.js",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("metrics output leaked high-cardinality or sensitive value %q:\n%s", forbidden, text)
		}
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func metricValue(t *testing.T, m *metrics.Metrics, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			if labelsMatch(metric, labels) {
				if c := metric.GetCounter(); c != nil {
					return c.GetValue()
				}
				if g := metric.GetGauge(); g != nil {
					return g.GetValue()
				}
			}
		}
	}
	return 0
}

func metricHistogramCount(t *testing.T, m *metrics.Metrics, name string, labels map[string]string) uint64 {
	t.Helper()
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			if labelsMatch(metric, labels) && metric.GetHistogram() != nil {
				return metric.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	if len(m.GetLabel()) != len(want) {
		return false
	}
	for _, lp := range m.GetLabel() {
		if v, ok := want[lp.GetName()]; !ok || v != lp.GetValue() {
			return false
		}
	}
	return true
}

func TestBrandingDefaultsConfiguredEscapingAndValidation(t *testing.T) {
	// 1. Default unconfigured output retains clean NTFUZZ header linked to canonical repo.
	defaultHTMLBytes, err := RenderIndexHTML(Branding{})
	if err != nil {
		t.Fatalf("RenderIndexHTML default: %v", err)
	}
	defaultHTML := string(defaultHTMLBytes)
	if !strings.Contains(defaultHTML, `<a class="brand-wordmark" href="https://github.com/NimbleTerminal/ntfuzz" target="_blank" rel="noopener noreferrer">NTFUZZ</a>`) {
		t.Fatalf("default HTML missing canonical NTFUZZ repo link:\n%s", defaultHTML)
	}
	if strings.Contains(defaultHTML, `<span class="brand-sep"`) || strings.Contains(defaultHTML, `class="brand-operator"`) {
		t.Fatalf("unconfigured HTML should not render operator separator or badge:\n%s", defaultHTML)
	}
	if strings.Contains(defaultHTML, `style="--ntfuzz-accent:`) {
		t.Fatalf("unconfigured HTML should not set inline accent override:\n%s", defaultHTML)
	}
	if !strings.Contains(defaultHTML, `renderer: 'auto'`) || strings.Contains(defaultHTML, "installRendererHud") {
		t.Fatalf("expected renderer: 'auto' without renderer HUD overlay:\n%s", defaultHTML)
	}
	for _, preserved := range []string{`id="status-dot"`, `id="status-text"`, `id="terminal-container"`, `id="action-controls"`, `.booba-action--keyboard`} {
		if !strings.Contains(defaultHTML, preserved) {
			t.Fatalf("default HTML missing required UI element %q", preserved)
		}
	}

	// 2. Display name without URL renders a non-link span beside NTFUZZ.
	nameOnlyBytes, err := RenderIndexHTML(Branding{Name: "Example Fleet"})
	if err != nil {
		t.Fatalf("RenderIndexHTML name-only: %v", err)
	}
	nameOnlyHTML := string(nameOnlyBytes)
	if !strings.Contains(nameOnlyHTML, `<span class="brand-operator" title="Example Fleet">Example Fleet</span>`) {
		t.Fatalf("expected non-link brand-operator span:\n%s", nameOnlyHTML)
	}
	if !strings.Contains(nameOnlyHTML, `href="https://github.com/NimbleTerminal/ntfuzz"`) {
		t.Fatalf("NTFUZZ project identity link must remain distinct:\n%s", nameOnlyHTML)
	}

	// 3. Configured name, URL, and uppercase #RRGGBB accent color.
	configuredBytes, err := RenderIndexHTML(Branding{
		Name:        "Example Lab",
		URL:         "https://lab.example.org/fuzz?team=core&env=prod",
		AccentColor: "#22C55E",
	})
	if err != nil {
		t.Fatalf("RenderIndexHTML configured: %v", err)
	}
	configuredHTML := string(configuredBytes)
	if !strings.Contains(configuredHTML, `<header class="topbar" style="--ntfuzz-accent: #22c55e;">`) {
		t.Fatalf("expected normalized scoped --ntfuzz-accent on topbar:\n%s", configuredHTML)
	}
	if !strings.Contains(configuredHTML, `<a class="brand-operator" href="https://lab.example.org/fuzz?team=core&amp;env=prod" target="_blank" rel="noopener noreferrer" title="Example Lab">Example Lab</a>`) {
		t.Fatalf("expected linked operator badge with escaped URL:\n%s", configuredHTML)
	}

	// 4. HTML escaping of operator display name (never raw HTML).
	xssBytes, err := RenderIndexHTML(Branding{
		Name: `<script>alert("xss")</script> & 'Corp'`,
		URL:  "https://example.org",
	})
	if err != nil {
		t.Fatalf("RenderIndexHTML escaping: %v", err)
	}
	xssHTML := string(xssBytes)
	if strings.Contains(xssHTML, `<script>alert("xss")</script>`) {
		t.Fatalf("operator name was not escaped:\n%s", xssHTML)
	}
	if !strings.Contains(xssHTML, `&lt;script&gt;alert(&#34;xss&#34;)&lt;/script&gt; &amp; &#39;Corp&#39;`) {
		t.Fatalf("expected HTML-escaped operator name in:\n%s", xssHTML)
	}

	// 5. Precedence: CLI flags > environment variables > defaults.
	env := map[string]string{
		EnvBrandName:   "Env Org",
		EnvBrandURL:    "https://env.example.org",
		EnvBrandAccent: "#112233",
	}
	getenv := func(k string) string { return env[k] }
	fromEnv, err := ResolveBranding(Branding{}, getenv)
	if err != nil {
		t.Fatalf("ResolveBranding from env: %v", err)
	}
	if fromEnv.Name != "Env Org" || fromEnv.URL != "https://env.example.org" || fromEnv.AccentColor != "#112233" {
		t.Fatalf("unexpected env branding: %+v", fromEnv)
	}
	fromFlags, err := ResolveBranding(Branding{Name: "Flag Org", AccentColor: "#AABBCC"}, getenv)
	if err != nil {
		t.Fatalf("ResolveBranding flag override: %v", err)
	}
	if fromFlags.Name != "Flag Org" || fromFlags.URL != "https://env.example.org" || fromFlags.AccentColor != "#aabbcc" {
		t.Fatalf("expected flag override with env fallback, got %+v", fromFlags)
	}

	// 6. Invalid URLs, colors, and control characters are rejected.
	invalidCases := []Branding{
		{Name: "Bad\nName"},
		{Name: "Org", URL: "javascript:alert(1)"},
		{Name: "Org", URL: "data:text/html,<script>alert(1)</script>"},
		{Name: "Org", URL: "ftp://example.org/file"},
		{Name: "Org", URL: "//example.org"},
		{Name: "Org", URL: "/relative/path"},
		{Name: "Org", URL: "https://"},
		{Name: "Org", URL: "https://user:pass@example.org"},
		{Name: "Org", URL: "https://example.org/with space"},
		{Name: "Org", AccentColor: "red"},
		{Name: "Org", AccentColor: "#fff"},
		{Name: "Org", AccentColor: "#12345g"},
		{Name: "Org", AccentColor: "#123456; background: url(evil)"},
		{Name: "Org", AccentColor: "var(--evil)"},
	}
	for _, tc := range invalidCases {
		if _, err := ValidateBranding(tc); err == nil {
			t.Errorf("expected ValidateBranding(%+v) to fail", tc)
		}
	}
}

func TestBrandingEquivalentInServeAndView(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "brand.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	branding := Branding{
		Name:        "Acme Security Fleet",
		URL:         "https://security.example.com",
		AccentColor: "#14b8a6",
	}

	serveHandler, err := NewHandler(api.NewServer(st, nil), "", BridgeOptions{
		ListenAddr: "127.0.0.1:8080",
		ReadOnly:   false,
		Branding:   branding,
	})
	if err != nil {
		t.Fatalf("NewHandler serve: %v", err)
	}

	viewHandler, err := NewHandler(api.NewSnapshotServer(st, store.Snapshot{PublishedAt: time.Now()}), "", BridgeOptions{
		ListenAddr: "127.0.0.1:8081",
		ReadOnly:   true,
		Branding:   branding,
	})
	if err != nil {
		t.Fatalf("NewHandler view: %v", err)
	}

	recServe := httptest.NewRecorder()
	serveHandler.ServeHTTP(recServe, httptest.NewRequest(http.MethodGet, "/", nil))

	recView := httptest.NewRecorder()
	viewHandler.ServeHTTP(recView, httptest.NewRequest(http.MethodGet, "/", nil))

	if recServe.Code != http.StatusOK || recView.Code != http.StatusOK {
		t.Fatalf("unexpected status codes: serve=%d view=%d", recServe.Code, recView.Code)
	}
	if recServe.Body.String() != recView.Body.String() {
		t.Fatal("serve and view produced different HTML for identical branding")
	}
	body := recServe.Body.String()
	if !strings.Contains(body, `style="--ntfuzz-accent: #14b8a6;"`) ||
		!strings.Contains(body, `href="https://security.example.com"`) ||
		!strings.Contains(body, `>Acme Security Fleet</a>`) ||
		!strings.Contains(body, `href="https://github.com/NimbleTerminal/ntfuzz"`) {
		t.Fatalf("rendered HTML missing expected branding elements:\n%s", body)
	}
}
