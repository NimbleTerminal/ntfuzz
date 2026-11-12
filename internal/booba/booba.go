package booba

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"nimbleterminal.dev/booba/serve"

	"nimbleterminal.dev/ntfuzz/internal/api"
	"nimbleterminal.dev/ntfuzz/internal/metrics"
	"nimbleterminal.dev/ntfuzz/internal/tui"
)

//go:embed index.html
var embeddedIndexHTML []byte

//go:embed touch_keyboard.mjs
var embeddedTouchKeyboardJS []byte

//go:embed viewport.mjs
var embeddedViewportJS []byte

// BridgeOptions configures the combined HTTP API + Booba TUI server.
type BridgeOptions struct {
	ListenAddr        string
	RefreshInterval   time.Duration
	Debug             bool
	ReadOnly          bool
	Listener          net.Listener
	MaxConnections    int
	IdleTimeout       time.Duration
	OriginPatterns    []string
	ServerContext     context.Context
	Branding          Branding
	Metrics           *metrics.Metrics
	MetricsListenAddr string
	MetricsListener   net.Listener
}

// NewHandler constructs an http.Handler that serves both:
//   - /api/* JSON endpoints (for both local/remote TUIs and browser/WASM consumers)
//   - /, /ws, /static/* Booba Ghostty/ntwebterm browser TUI sessions
func NewHandler(apiServer http.Handler, apiBaseURL string, opts BridgeOptions) (http.Handler, error) {
	host, port, err := parseHostPort(opts.ListenAddr)
	if err != nil {
		return nil, err
	}

	renderedIndexHTML, err := RenderIndexHTML(opts.Branding)
	if err != nil {
		return nil, err
	}

	if opts.Metrics != nil {
		if setter, ok := apiServer.(interface{ SetMetrics(*metrics.Metrics) }); ok {
			setter.SetMetrics(opts.Metrics)
		}
	}

	cfg := serve.DefaultConfig()
	cfg.Host = host
	cfg.Port = port
	cfg.HTTP3Port = -1
	cfg.Debug = opts.Debug
	if len(opts.OriginPatterns) > 0 {
		cfg.OriginPatterns = append([]string(nil), opts.OriginPatterns...)
	}
	if opts.ReadOnly {
		cfg.MaxConnections = 32
		cfg.IdleTimeout = 10 * time.Minute
	}
	if opts.MaxConnections > 0 {
		cfg.MaxConnections = opts.MaxConnections
	}
	if opts.IdleTimeout > 0 {
		cfg.IdleTimeout = opts.IdleTimeout
	}

	refresh := opts.RefreshInterval
	if refresh <= 0 {
		refresh = 2 * time.Second
	}

	var serverOpts []serve.Option
	if opts.Metrics != nil {
		serverOpts = append(serverOpts,
			serve.WithConnectMiddleware(connectMetricsMiddleware(opts.Metrics)),
			serve.WithSessionMiddleware(preserveResizeEvents, sessionMetricsMiddleware(opts.ServerContext, opts.Metrics)),
		)
	} else {
		serverOpts = append(serverOpts, serve.WithSessionMiddleware(preserveResizeEvents))
	}

	boobaServer := serve.NewServer(cfg, serverOpts...)
	boobaHandler, err := boobaServer.HTTPHandlerFor(func(sess serve.Session) (tea.Model, []tea.ProgramOption) {
		var client *api.Client
		if apiServer != nil {
			client = api.NewLocalClient(apiServer)
		} else {
			client = api.NewClient(apiBaseURL)
		}
		m, err := tui.NewActionModelOptions(client, refresh, opts.ReadOnly)
		if err != nil {
			return tui.NewModelOptions(client, refresh, opts.ReadOnly), nil
		}
		return m, nil
	})
	if err != nil {
		return nil, fmt.Errorf("create booba handler: %w", err)
	}

	mux := http.NewServeMux()
	registerDiscovery(mux)
	mux.HandleFunc("GET /touch-keyboard.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(embeddedTouchKeyboardJS)
	})
	mux.HandleFunc("GET /viewport.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(embeddedViewportJS)
	})
	if apiServer != nil {
		mux.Handle("/api/", apiServer)
		mux.Handle("/crashes", apiServer)
		mux.Handle("/crashes/", apiServer)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(renderedIndexHTML)
			return
		}
		if opts.Metrics != nil && (r.URL.Path == "/ws" || r.URL.Path == "/wt") {
			serveTrackedTerminalRequest(w, r, boobaHandler, opts.Metrics)
			return
		}
		boobaHandler.ServeHTTP(w, r)
	})

	if opts.Metrics != nil {
		return opts.Metrics.InstrumentHTTP(withDiscoveryLinks(mux), api.IsInternalRequest), nil
	}
	return withDiscoveryLinks(mux), nil
}

// ListenAndServe starts the combined HTTP API and Booba web TUI server until ctx is canceled.
func ListenAndServe(ctx context.Context, apiServer http.Handler, opts BridgeOptions) error {
	listenAddr := strings.TrimSpace(opts.ListenAddr)
	if listenAddr == "" {
		listenAddr = "127.0.0.1:8080"
	}
	host, port, err := parseHostPort(listenAddr)
	if err != nil {
		return err
	}
	clientHost := host
	if clientHost == "" || clientHost == "0.0.0.0" || clientHost == "::" {
		clientHost = "127.0.0.1"
	}
	apiBaseURL := fmt.Sprintf("http://%s:%d", clientHost, port)

	if opts.ServerContext == nil {
		opts.ServerContext = ctx
	}
	if opts.Metrics == nil && (strings.TrimSpace(opts.MetricsListenAddr) != "" || opts.MetricsListener != nil) {
		mode := metrics.ModeServe
		if opts.ReadOnly {
			mode = metrics.ModeView
		}
		opts.Metrics = metrics.New(mode)
	}

	handler, err := NewHandler(apiServer, apiBaseURL, opts)
	if err != nil {
		return err
	}

	if opts.Metrics != nil && (strings.TrimSpace(opts.MetricsListenAddr) != "" || opts.MetricsListener != nil) {
		_, stopMetrics, err := opts.Metrics.StartServer(ctx, opts.MetricsListenAddr, opts.MetricsListener)
		if err != nil {
			return err
		}
		defer func() { _ = stopMetrics() }()
	}

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	if opts.Listener != nil {
		err = httpServer.Serve(opts.Listener)
	} else {
		err = httpServer.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func parseHostPort(addr string) (string, int, error) {
	if addr == "" {
		return "127.0.0.1", 8080, nil
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port in %q", addr)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return host, port, nil
}
