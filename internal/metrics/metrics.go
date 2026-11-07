// Package metrics provides optional Prometheus-compatible observability for
// ntfuzz's HTTP API, Booba browser terminal, snapshot publisher, and read-only
// snapshot viewer.
package metrics

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server modes used for the bounded "mode" label.
const (
	ModeServe = "serve"
	ModeView  = "view"
)

// Default loopback ports when --metrics is enabled without an explicit address.
const (
	DefaultServeMetricsPort = 9090
	DefaultViewMetricsPort  = 9091
)

// Bounded disconnect-reason categories for closed terminal sessions.
const (
	ReasonClientDisconnect = "client_disconnect"
	ReasonIdleTimeout      = "idle_timeout"
	ReasonServerShutdown   = "server_shutdown"
	ReasonError            = "error"
)

// Bounded rejection-reason categories for rejected terminal connections.
const (
	RejectReasonConnectionLimit = "connection_limit"
	RejectReasonUnauthorized    = "unauthorized"
	RejectReasonOrigin          = "origin"
	RejectReasonHandshake       = "handshake"
	RejectReasonError           = "error"
)

// Bounded result categories for snapshot publication and refresh operations.
const (
	ResultSuccess = "success"
	ResultError   = "error"
)

var (
	allDisconnectReasons = []string{
		ReasonClientDisconnect,
		ReasonIdleTimeout,
		ReasonServerShutdown,
		ReasonError,
	}
	allRejectReasons = []string{
		RejectReasonConnectionLimit,
		RejectReasonUnauthorized,
		RejectReasonOrigin,
		RejectReasonHandshake,
		RejectReasonError,
	}
	sessionDurationBuckets = []float64{0.1, 0.5, 1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600}
)

// Metrics holds the Prometheus registry and collectors for a single ntfuzz
// server instance ("serve" or "view"). All counters reset on process restart;
// historical retention requires an external Prometheus-compatible collector.
type Metrics struct {
	mode     string
	registry *prometheus.Registry

	httpRequests        *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec

	sessionsActive   *prometheus.GaugeVec
	sessionsOpened   *prometheus.CounterVec
	sessionsClosed   *prometheus.CounterVec
	sessionsRejected *prometheus.CounterVec
	sessionDuration  *prometheus.HistogramVec
	bytesReceived    *prometheus.CounterVec
	bytesSent        *prometheus.CounterVec

	// Viewer-only snapshot metrics (mode == "view").
	snapshotPublishedUnixNano atomic.Int64
	snapshotAge               prometheus.GaugeFunc
	snapshotRefreshes         *prometheus.CounterVec

	// Orchestrator-only snapshot publication metrics (mode == "serve").
	snapshotPublications    *prometheus.CounterVec
	snapshotPublishDuration *prometheus.HistogramVec

	workerOnce sync.Once
	nowMu      sync.RWMutex
	nowFn      func() time.Time
}

// New constructs a new isolated Prometheus metrics registry and collectors for
// the given server mode ("serve" or "view").
func New(mode string) *Metrics {
	m := &Metrics{
		mode:     normalizeMode(mode),
		registry: prometheus.NewRegistry(),
		nowFn:    time.Now,
	}

	m.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ntfuzz",
		Name:      "http_requests_total",
		Help:      "Total external HTTP requests handled, labeled by server mode, route template, HTTP method, and status class.",
	}, []string{"mode", "route", "method", "status_class"})

	m.httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "ntfuzz",
		Name:      "http_request_duration_seconds",
		Help:      "External HTTP request latency in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"mode", "route", "method", "status_class"})

	m.sessionsActive = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "ntfuzz",
		Name:      "terminal_sessions_active",
		Help:      "Number of currently active browser-terminal sessions.",
	}, []string{"mode"})

	m.sessionsOpened = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ntfuzz",
		Name:      "terminal_sessions_opened_total",
		Help:      "Total browser-terminal sessions opened.",
	}, []string{"mode"})

	m.sessionsClosed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ntfuzz",
		Name:      "terminal_sessions_closed_total",
		Help:      "Total browser-terminal sessions closed, labeled by bounded disconnect reason.",
	}, []string{"mode", "reason"})

	m.sessionsRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ntfuzz",
		Name:      "terminal_sessions_rejected_total",
		Help:      "Total browser-terminal connection attempts rejected before opening a session, labeled by bounded reason.",
	}, []string{"mode", "reason"})

	m.sessionDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "ntfuzz",
		Name:      "terminal_session_duration_seconds",
		Help:      "Duration of closed browser-terminal sessions in seconds.",
		Buckets:   sessionDurationBuckets,
	}, []string{"mode", "reason"})

	m.bytesReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ntfuzz",
		Name:      "terminal_bytes_received_total",
		Help:      "Total terminal input payload bytes received from browser-terminal clients at the PTY session boundary.",
	}, []string{"mode"})

	m.bytesSent = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ntfuzz",
		Name:      "terminal_bytes_sent_total",
		Help:      "Total terminal output payload bytes read from the PTY session and sent to browser-terminal clients.",
	}, []string{"mode"})

	m.registry.MustRegister(
		m.httpRequests,
		m.httpRequestDuration,
		m.sessionsActive,
		m.sessionsOpened,
		m.sessionsClosed,
		m.sessionsRejected,
		m.sessionDuration,
		m.bytesReceived,
		m.bytesSent,
	)

	// Pre-initialize bounded label combinations so metric families are present
	// immediately on scrape with zero values before traffic arrives.
	m.sessionsActive.WithLabelValues(m.mode).Set(0)
	m.sessionsOpened.WithLabelValues(m.mode).Add(0)
	m.bytesReceived.WithLabelValues(m.mode).Add(0)
	m.bytesSent.WithLabelValues(m.mode).Add(0)
	for _, r := range allDisconnectReasons {
		m.sessionsClosed.WithLabelValues(m.mode, r).Add(0)
	}
	for _, r := range allRejectReasons {
		m.sessionsRejected.WithLabelValues(m.mode, r).Add(0)
	}

	if m.mode == ModeView {
		m.snapshotAge = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace:   "ntfuzz",
			Name:        "snapshot_age_seconds",
			Help:        "Age in seconds of the currently loaded immutable snapshot generation in viewer mode.",
			ConstLabels: prometheus.Labels{"mode": m.mode},
		}, func() float64 {
			ns := m.snapshotPublishedUnixNano.Load()
			if ns <= 0 {
				return 0
			}
			age := m.now().Sub(time.Unix(0, ns)).Seconds()
			if age < 0 {
				return 0
			}
			return age
		})
		m.snapshotRefreshes = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "ntfuzz",
			Name:      "snapshot_refreshes_total",
			Help:      "Total snapshot refresh checks in viewer mode, labeled by result (success or error).",
		}, []string{"mode", "result"})
		m.registry.MustRegister(m.snapshotAge, m.snapshotRefreshes)
		m.snapshotRefreshes.WithLabelValues(m.mode, ResultSuccess).Add(0)
		m.snapshotRefreshes.WithLabelValues(m.mode, ResultError).Add(0)
	} else {
		m.snapshotPublications = prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "ntfuzz",
			Name:      "snapshot_publications_total",
			Help:      "Total immutable snapshot publication attempts in orchestrator mode, labeled by result (success or error).",
		}, []string{"mode", "result"})
		m.snapshotPublishDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "ntfuzz",
			Name:      "snapshot_publish_duration_seconds",
			Help:      "Duration of immutable snapshot publication attempts in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"mode", "result"})
		m.registry.MustRegister(m.snapshotPublications, m.snapshotPublishDuration)
		m.snapshotPublications.WithLabelValues(m.mode, ResultSuccess).Add(0)
		m.snapshotPublications.WithLabelValues(m.mode, ResultError).Add(0)
	}

	return m
}

// Mode returns the bounded mode label ("serve" or "view") for this collector.
func (m *Metrics) Mode() string {
	if m == nil {
		return ModeServe
	}
	return m.mode
}

// Registry returns the underlying Prometheus registry.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.registry
}

// SetClock overrides the clock used for snapshot age calculations in tests.
func (m *Metrics) SetClock(now func() time.Time) {
	if m == nil || now == nil {
		return
	}
	m.nowMu.Lock()
	m.nowFn = now
	m.nowMu.Unlock()
}

func (m *Metrics) now() time.Time {
	m.nowMu.RLock()
	fn := m.nowFn
	m.nowMu.RUnlock()
	return fn()
}

// RegisterWorkerStatus registers live orchestrator worker gauges in "serve"
// mode. It is a no-op in "view" mode so viewer processes never fabricate
// live worker activity from static snapshots.
func (m *Metrics) RegisterWorkerStatus(configuredFn, activeFn func() int) {
	if m == nil || m.mode != ModeServe || configuredFn == nil || activeFn == nil {
		return
	}
	m.workerOnce.Do(func() {
		active := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace:   "ntfuzz",
			Name:        "orchestrator_workers_active",
			Help:        "Number of currently active fuzzing worker jobs in orchestrator mode.",
			ConstLabels: prometheus.Labels{"mode": m.mode},
		}, func() float64 {
			return float64(activeFn())
		})
		configured := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace:   "ntfuzz",
			Name:        "orchestrator_workers_configured",
			Help:        "Number of configured fuzzing workers in orchestrator mode.",
			ConstLabels: prometheus.Labels{"mode": m.mode},
		}, func() float64 {
			return float64(configuredFn())
		})
		m.registry.MustRegister(active, configured)
	})
}

// RecordHTTPRequest records a single external HTTP request.
func (m *Metrics) RecordHTTPRequest(route, method string, statusCode int, duration time.Duration) {
	if m == nil {
		return
	}
	rt := ClassifyRoute(route)
	mt := ClassifyMethod(method)
	sc := ClassifyStatus(statusCode)
	m.httpRequests.WithLabelValues(m.mode, rt, mt, sc).Inc()
	m.httpRequestDuration.WithLabelValues(m.mode, rt, mt, sc).Observe(max(0, duration.Seconds()))
}

// SessionOpened records a newly opened browser-terminal session.
func (m *Metrics) SessionOpened() {
	if m == nil {
		return
	}
	m.sessionsActive.WithLabelValues(m.mode).Inc()
	m.sessionsOpened.WithLabelValues(m.mode).Inc()
}

// SessionClosed records a closed browser-terminal session, its duration, and
// its bounded disconnect reason category.
func (m *Metrics) SessionClosed(duration time.Duration, reason string) {
	if m == nil {
		return
	}
	r := normalizeDisconnectReason(reason)
	m.sessionsActive.WithLabelValues(m.mode).Dec()
	m.sessionsClosed.WithLabelValues(m.mode, r).Inc()
	m.sessionDuration.WithLabelValues(m.mode, r).Observe(max(0, duration.Seconds()))
}

// SessionRejected records a rejected browser-terminal connection attempt before
// a session could be opened.
func (m *Metrics) SessionRejected(reason string) {
	if m == nil {
		return
	}
	r := normalizeRejectReason(reason)
	m.sessionsRejected.WithLabelValues(m.mode, r).Inc()
}

// AddBytesReceived increments the terminal input byte counter.
func (m *Metrics) AddBytesReceived(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.bytesReceived.WithLabelValues(m.mode).Add(float64(n))
}

// AddBytesSent increments the terminal output byte counter.
func (m *Metrics) AddBytesSent(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.bytesSent.WithLabelValues(m.mode).Add(float64(n))
}

// SetSnapshotPublishedAt updates the timestamp of the currently loaded snapshot
// in viewer mode.
func (m *Metrics) SetSnapshotPublishedAt(publishedAt time.Time) {
	if m == nil || publishedAt.IsZero() {
		return
	}
	m.snapshotPublishedUnixNano.Store(publishedAt.UTC().UnixNano())
}

// RecordSnapshotRefresh records a viewer snapshot refresh attempt and updates
// the active snapshot timestamp when successful.
func (m *Metrics) RecordSnapshotRefresh(publishedAt time.Time, err error) {
	if m == nil || m.snapshotRefreshes == nil {
		return
	}
	if err != nil {
		m.snapshotRefreshes.WithLabelValues(m.mode, ResultError).Inc()
		return
	}
	if !publishedAt.IsZero() {
		m.snapshotPublishedUnixNano.Store(publishedAt.UTC().UnixNano())
	}
	m.snapshotRefreshes.WithLabelValues(m.mode, ResultSuccess).Inc()
}

// AddSnapshotRefreshes adds pre-existing refresh counts when a Metrics
// instance is attached to an already-opened Viewer.
func (m *Metrics) AddSnapshotRefreshes(successes, failures int64) {
	if m == nil || m.snapshotRefreshes == nil {
		return
	}
	if successes > 0 {
		m.snapshotRefreshes.WithLabelValues(m.mode, ResultSuccess).Add(float64(successes))
	}
	if failures > 0 {
		m.snapshotRefreshes.WithLabelValues(m.mode, ResultError).Add(float64(failures))
	}
}

// RecordSnapshotPublication records an orchestrator snapshot publication
// attempt and its duration.
func (m *Metrics) RecordSnapshotPublication(duration time.Duration, err error) {
	if m == nil || m.snapshotPublications == nil || m.snapshotPublishDuration == nil {
		return
	}
	res := ResultSuccess
	if err != nil {
		res = ResultError
	}
	m.snapshotPublications.WithLabelValues(m.mode, res).Inc()
	m.snapshotPublishDuration.WithLabelValues(m.mode, res).Observe(max(0, duration.Seconds()))
}

// Handler returns an isolated http.Handler that serves Prometheus metrics only
// at GET/HEAD /metrics.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	promHandler := promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		promHandler.ServeHTTP(w, r)
	})
}

// InstrumentHTTP wraps next to record external HTTP request counts, status
// classes, and latencies. If skipFn returns true for a request (for example,
// internal in-process API calls), the request is passed straight through
// without incrementing external visitor metrics.
func (m *Metrics) InstrumentHTTP(next http.Handler, skipFn func(*http.Request) bool) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skipFn != nil && skipFn(r) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &responseRecorder{
			ResponseWriter: w,
			metrics:        m,
			route:          r.URL.Path,
			method:         r.Method,
			start:          time.Now(),
		}
		defer rec.finish()
		next.ServeHTTP(rec, r)
	})
}

// responseRecorder wraps http.ResponseWriter while preserving optional
// interfaces (Unwrap, http.Flusher, http.Hijacker) required by WebSocket
// upgrades and http.ResponseController.
type responseRecorder struct {
	http.ResponseWriter
	metrics     *Metrics
	route       string
	method      string
	start       time.Time
	status      int
	wroteHeader bool
	finishOnce  sync.Once
}

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
	if code == http.StatusSwitchingProtocols {
		r.finish()
	}
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(p)
}

func (r *responseRecorder) Flush() {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	if fl, ok := r.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
		return
	}
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	var (
		conn net.Conn
		brw  *bufio.ReadWriter
		err  error
	)
	if hj, ok := r.ResponseWriter.(http.Hijacker); ok {
		conn, brw, err = hj.Hijack()
	} else {
		conn, brw, err = http.NewResponseController(r.ResponseWriter).Hijack()
	}
	if err == nil {
		if !r.wroteHeader {
			r.status = http.StatusSwitchingProtocols
			r.wroteHeader = true
		}
		r.finish()
	}
	return conn, brw, err
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *responseRecorder) StatusCode() int {
	if !r.wroteHeader || r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func (r *responseRecorder) finish() {
	r.finishOnce.Do(func() {
		status := r.status
		if !r.wroteHeader || status == 0 {
			status = http.StatusOK
		}
		r.metrics.RecordHTTPRequest(r.route, r.method, status, time.Since(r.start))
	})
}

// ClassifyRoute maps a request path onto a small, bounded set of route
// templates so arbitrary URLs, crash IDs, static asset paths, or scanner
// paths never create unbounded label cardinality.
func ClassifyRoute(path string) string {
	switch path {
	case "/":
		return "/"
	case "/ws":
		return "/ws"
	case "/wt":
		return "/wt"
	case "/cert-hash":
		return "/cert-hash"
	case "/api/projects":
		return "/api/projects"
	case "/api/targets":
		return "/api/targets"
	case "/api/runs":
		return "/api/runs"
	case "/api/crashes":
		return "/api/crashes"
	case "/api/coverage/history":
		return "/api/coverage/history"
	case "/api/crash-replays":
		return "/api/crash-replays"
	case "/api/stats":
		return "/api/stats"
	case "/api/status":
		return "/api/status"
	}
	if strings.HasPrefix(path, "/static/") {
		return "/static/*"
	}
	if isStatsQueryPath(path) {
		return "/api/stats/{query}"
	}
	if isCrashFixPath(path) {
		return "/api/crashes/{id}/fix"
	}
	return "other"
}

func isStatsQueryPath(path string) bool {
	const prefix = "/api/stats/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	queryPart := strings.TrimPrefix(path, prefix)
	return queryPart != "" && !strings.Contains(queryPart, "/")
}

func isCrashFixPath(path string) bool {
	const prefix = "/api/crashes/"
	const suffix = "/fix"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	idPart := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return idPart != "" && !strings.Contains(idPart, "/")
}

// ClassifyMethod maps an HTTP method to a bounded label value.
func ClassifyMethod(method string) string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet:
		return http.MethodGet
	case http.MethodPost:
		return http.MethodPost
	case http.MethodHead:
		return http.MethodHead
	case http.MethodOptions:
		return http.MethodOptions
	case http.MethodPut:
		return http.MethodPut
	case http.MethodDelete:
		return http.MethodDelete
	default:
		return "OTHER"
	}
}

// ClassifyStatus maps an HTTP response status code to its status class ("1xx".."5xx").
func ClassifyStatus(code int) string {
	switch {
	case code >= 100 && code < 200:
		return "1xx"
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	default:
		return "5xx"
	}
}

// ResolveListenAddr resolves the metrics listener bind address.
// When disabled (enabled == false and addr == ""), it returns ("", nil).
// When enabled without a host (for example "--metrics" or "--metrics-listen :9090"),
// it binds to loopback (127.0.0.1) by default.
func ResolveListenAddr(enabled bool, addr string, defaultPort int) (string, error) {
	trimmed := strings.TrimSpace(addr)
	if !enabled && trimmed == "" {
		return "", nil
	}
	if trimmed == "" {
		if defaultPort <= 0 || defaultPort > 65535 {
			defaultPort = DefaultServeMetricsPort
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(defaultPort)), nil
	}
	host, portStr, err := net.SplitHostPort(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid metrics listen address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return "", fmt.Errorf("invalid port in metrics listen address %q", addr)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// StartServer binds and serves the isolated Prometheus /metrics endpoint until
// ctx is canceled or the returned shutdown function is called.
func (m *Metrics) StartServer(ctx context.Context, addr string, ln net.Listener) (net.Addr, func() error, error) {
	if m == nil {
		return nil, func() error { return nil }, nil
	}
	var err error
	if ln == nil {
		resolved, err := ResolveListenAddr(true, addr, DefaultServeMetricsPort)
		if err != nil {
			return nil, nil, err
		}
		ln, err = net.Listen("tcp", resolved)
		if err != nil {
			return nil, nil, fmt.Errorf("listen metrics %q: %w", resolved, err)
		}
	}

	srv := &http.Server{
		Handler:           m.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	var stopOnce sync.Once
	var stopErr error
	stop := func() error {
		stopOnce.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				stopErr = err
			}
		})
		return stopErr
	}

	go func() {
		<-ctx.Done()
		_ = stop()
	}()

	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			_ = ln.Close()
		}
	}()

	return ln.Addr(), stop, err
}

func normalizeMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), ModeView) {
		return ModeView
	}
	return ModeServe
}

func normalizeDisconnectReason(reason string) string {
	switch reason {
	case ReasonClientDisconnect, ReasonIdleTimeout, ReasonServerShutdown, ReasonError:
		return reason
	default:
		return ReasonError
	}
}

func normalizeRejectReason(reason string) string {
	switch reason {
	case RejectReasonConnectionLimit, RejectReasonUnauthorized, RejectReasonOrigin, RejectReasonHandshake, RejectReasonError:
		return reason
	default:
		return RejectReasonHandshake
	}
}
