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
	"github.com/NimbleMarkets/go-booba/serve"

	"github.com/NimbleTerminal/ntfuzzy/internal/api"
	"github.com/NimbleTerminal/ntfuzzy/internal/tui"
)

//go:embed index.html
var embeddedIndexHTML []byte

// BridgeOptions configures the combined HTTP API + Booba TUI server.
type BridgeOptions struct {
	ListenAddr      string
	RefreshInterval time.Duration
	Debug           bool
}

// NewHandler constructs an http.Handler that serves both:
//   - /api/* JSON endpoints (for both local/remote TUIs and browser/WASM consumers)
//   - /, /ws, /static/* Booba Ghostty/ntwebterm browser TUI sessions
func NewHandler(apiServer *api.Server, apiBaseURL string, opts BridgeOptions) (http.Handler, error) {
	host, port, err := parseHostPort(opts.ListenAddr)
	if err != nil {
		return nil, err
	}

	cfg := serve.DefaultConfig()
	cfg.Host = host
	cfg.Port = port
	cfg.HTTP3Port = -1
	cfg.Debug = opts.Debug

	refresh := opts.RefreshInterval
	if refresh <= 0 {
		refresh = 2 * time.Second
	}

	boobaServer := serve.NewServer(cfg)
	boobaHandler, err := boobaServer.HTTPHandlerFor(func(sess serve.Session) (tea.Model, []tea.ProgramOption) {
		client := api.NewClient(apiBaseURL)
		m, err := tui.NewActionModel(client, refresh)
		if err != nil {
			return tui.NewModel(client, refresh), nil
		}
		return m, nil
	})
	if err != nil {
		return nil, fmt.Errorf("create booba handler: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", apiServer)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(embeddedIndexHTML)
			return
		}
		boobaHandler.ServeHTTP(w, r)
	})

	return mux, nil
}

// ListenAndServe starts the combined HTTP API and Booba web TUI server until ctx is canceled.
func ListenAndServe(ctx context.Context, apiServer *api.Server, opts BridgeOptions) error {
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

	handler, err := NewHandler(apiServer, apiBaseURL, opts)
	if err != nil {
		return err
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

	err = httpServer.ListenAndServe()
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
