package booba

import (
	"embed"
	"net/http"
)

//go:embed api-docs.html llms.txt openapi.json
var discoveryFiles embed.FS

const discoveryLinks = `</api/docs>; rel="service-doc"; type="text/html", </openapi.json>; rel="service-desc"; type="application/json", </llms.txt>; rel="describedby"; type="text/markdown"`

func registerDiscovery(mux *http.ServeMux) {
	for _, resource := range []struct{ path, file, contentType string }{
		{"/api/docs", "api-docs.html", "text/html; charset=utf-8"},
		{"/llms.txt", "llms.txt", "text/markdown; charset=utf-8"},
		{"/openapi.json", "openapi.json", "application/json; charset=utf-8"},
	} {
		body, err := discoveryFiles.ReadFile(resource.file)
		if err != nil {
			panic(err)
		} // Embedded, known files: a missing file is a build-time bug.
		mux.HandleFunc("GET "+resource.path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", resource.contentType)
			if r.Method != http.MethodHead {
				_, _ = w.Write(body)
			}
		})
	}
}

// Preserve the original writer (including WebSocket upgrade interfaces).
func withDiscoveryLinks(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Link", discoveryLinks)
		next.ServeHTTP(w, r)
	})
}
