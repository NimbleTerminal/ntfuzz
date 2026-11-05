package booba

import (
	"context"
	"encoding/json"
	"github.com/NimbleMarkets/go-booba/sip"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NimbleTerminal/ntfuzz/internal/api"
	"github.com/NimbleTerminal/ntfuzz/internal/store"
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
	handler, err := NewHandler(api.NewSnapshotServer(st, store.Snapshot{PublishedAt: time.Now()}), "", BridgeOptions{ListenAddr: "127.0.0.1:8081", ReadOnly: true, RefreshInterval: time.Hour})
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
