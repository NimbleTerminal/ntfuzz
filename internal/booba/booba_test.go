package booba

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
