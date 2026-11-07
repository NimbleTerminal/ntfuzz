package booba

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"nimbleterminal.dev/ntfuzz/internal/api"
)

func TestDiscoveryRoutes(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "snapshot"}[readOnly], func(t *testing.T) {
			// The bridge must own /api/docs even when the delegated API rejects it.
			h, err := NewHandler(http.NotFoundHandler(), "", BridgeOptions{ReadOnly: readOnly})
			if err != nil {
				t.Fatal(err)
			}
			for path, mime := range map[string]string{"/": "text/html", "/api/docs": "text/html", "/llms.txt": "text/markdown", "/openapi.json": "application/json"} {
				r := httptest.NewRecorder()
				h.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
				if r.Code != 200 || !strings.HasPrefix(r.Header().Get("Content-Type"), mime) {
					t.Fatalf("%s: %d %v", path, r.Code, r.Header())
				}
				for _, rel := range []string{`rel="service-doc"`, `rel="service-desc"`, `rel="describedby"`} {
					if !strings.Contains(r.Header().Get("Link"), rel) {
						t.Errorf("%s missing %s", path, rel)
					}
				}
				if path == "/" {
					body := strings.Split(r.Body.String(), "<script")[0]
					for _, href := range []string{`href="/api/crashes?open=true"`, `href="/api/projects"`, `href="/api/status"`, `href="/api/docs"`, `href="/llms.txt"`, `href="/openapi.json"`} {
						if !strings.Contains(body, href) {
							t.Errorf("HTML missing %s", href)
						}
					}
				}
				head := httptest.NewRecorder()
				h.ServeHTTP(head, httptest.NewRequest("HEAD", path, nil))
				if head.Code != 200 {
					t.Errorf("HEAD %s: %d", path, head.Code)
				}
			}
			r := httptest.NewRecorder()
			h.ServeHTTP(r, httptest.NewRequest("POST", "/openapi.json", nil))
			if r.Code == 200 {
				t.Error("POST unexpectedly served documentation")
			}
			r = httptest.NewRecorder()
			h.ServeHTTP(r, httptest.NewRequest("GET", "/api/status", nil))
			if r.Code != 404 {
				t.Error("API delegation changed")
			}
		})
	}
}

func TestOpenAPIWireSchemas(t *testing.T) {
	raw, err := discoveryFiles.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		OpenAPI    string                                `json:"openapi"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.0.3" || len(spec.Paths) != 7 {
		t.Fatal("unexpected API document")
	}
	for _, path := range []string{"/api/projects", "/api/targets", "/api/runs", "/api/crashes", "/api/coverage/history", "/api/stats/{query}", "/api/status"} {
		if _, ok := spec.Paths[path]["get"]; !ok {
			t.Errorf("missing GET %s", path)
		}
	}
	for _, dto := range []any{api.Project{}, api.Target{}, api.Run{}, api.Crash{}, api.CoveragePoint{}, api.StatsBucket{}, api.StatsResponse{}, api.ActiveJob{}, api.StatusResponse{}} {
		typ := reflect.TypeOf(dto)
		schema := spec.Components.Schemas[typ.Name()]
		if len(schema.Properties) != typ.NumField() {
			t.Errorf("%s schema fields differ from DTO", typ.Name())
		}
		required := map[string]bool{}
		for _, key := range schema.Required {
			required[key] = true
		}
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")
			name := tag[0]
			if _, ok := schema.Properties[name]; !ok {
				t.Errorf("%s missing %s", typ.Name(), name)
			}
			if required[name] != (len(tag) == 1) {
				t.Errorf("%s.%s required mismatch", typ.Name(), name)
			}
		}
	}
}
