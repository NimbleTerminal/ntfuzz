package metrics

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

var (
	allowedRoutes = map[string]bool{
		"/":                     true,
		"/ws":                   true,
		"/wt":                   true,
		"/cert-hash":            true,
		"/api/projects":         true,
		"/api/targets":          true,
		"/api/runs":             true,
		"/api/crashes":          true,
		"/api/crashes/{id}/fix": true,
		"/api/coverage/history": true,
		"/api/status":           true,
		"/static/*":             true,
		"other":                 true,
	}
	allowedMethods = map[string]bool{
		"GET":     true,
		"POST":    true,
		"HEAD":    true,
		"OPTIONS": true,
		"PUT":     true,
		"DELETE":  true,
		"OTHER":   true,
	}
	allowedStatusClasses = map[string]bool{
		"1xx": true,
		"2xx": true,
		"3xx": true,
		"4xx": true,
		"5xx": true,
	}
)

func FuzzClassifyLabels(f *testing.F) {
	f.Add("/", "GET", 200)
	f.Add("/ws", "GET", 101)
	f.Add("/api/crashes/42/fix", "POST", 200)
	f.Add("/api/crashes/42/extra/fix", "POST", 404)
	f.Add("/static/ntwebterm.js", "GET", 200)
	f.Add("/wp-login.php", "PROPFIND", 418)
	f.Add("", "", 0)

	f.Fuzz(func(t *testing.T, path, method string, statusCode int) {
		route := ClassifyRoute(path)
		if !allowedRoutes[route] {
			t.Fatalf("ClassifyRoute(%q) returned unbounded route %q", path, route)
		}
		if route == "/api/crashes/{id}/fix" {
			trimmed := strings.TrimSuffix(strings.TrimPrefix(path, "/api/crashes/"), "/fix")
			if trimmed == "" || strings.Contains(trimmed, "/") {
				t.Fatalf("ClassifyRoute(%q) matched crash fix with invalid id segment %q", path, trimmed)
			}
		}

		mt := ClassifyMethod(method)
		if !allowedMethods[mt] {
			t.Fatalf("ClassifyMethod(%q) returned unbounded method %q", method, mt)
		}

		sc := ClassifyStatus(statusCode)
		if !allowedStatusClasses[sc] {
			t.Fatalf("ClassifyStatus(%d) returned unbounded status class %q", statusCode, sc)
		}
	})
}

func FuzzResolveListenAddr(f *testing.F) {
	f.Add(false, "", 9090)
	f.Add(true, "", 9090)
	f.Add(true, ":9091", 9091)
	f.Add(false, "127.0.0.1:9090", 9090)
	f.Add(true, "[::1]:0", 9090)
	f.Add(true, "bad-addr", 9090)
	f.Add(true, "127.0.0.1:99999", -1)

	f.Fuzz(func(t *testing.T, enabled bool, addr string, defaultPort int) {
		resolved, err := ResolveListenAddr(enabled, addr, defaultPort)
		if !enabled && strings.TrimSpace(addr) == "" {
			if err != nil || resolved != "" {
				t.Fatalf("expected disabled ResolveListenAddr to return (\"\", nil), got (%q, %v)", resolved, err)
			}
			return
		}
		if err != nil {
			return
		}
		host, portStr, splitErr := net.SplitHostPort(resolved)
		if splitErr != nil {
			t.Fatalf("ResolveListenAddr returned unparseable host:port %q: %v", resolved, splitErr)
		}
		if host == "" {
			t.Fatalf("ResolveListenAddr returned empty host in %q", resolved)
		}
		port, portErr := strconv.Atoi(portStr)
		if portErr != nil || port < 0 || port > 65535 {
			t.Fatalf("ResolveListenAddr returned invalid port %q in %q", portStr, resolved)
		}
	})
}
