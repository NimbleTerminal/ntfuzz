package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
)

type internalRequestKey struct{}

// IsInternalRequest reports whether r originated from an in-process localTransport call.
func IsInternalRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	v, _ := r.Context().Value(internalRequestKey{}).(bool)
	return v
}

// NewLocalClient routes the web terminal's fixed API calls directly to its
// handler in-process without loopback HTTP traffic or external request accounting.
func NewLocalClient(handler http.Handler) *Client {
	return &Client{baseURL: "http://snapshot.local", httpClient: &http.Client{Timeout: 5 * time.Second, Transport: localTransport{handler}}}
}

type localTransport struct{ handler http.Handler }

func (t localTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	w := &localResponse{headers: make(http.Header)}
	req := request.WithContext(context.WithValue(request.Context(), internalRequestKey{}, true))
	t.handler.ServeHTTP(w, req)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return &http.Response{StatusCode: w.status, Header: w.headers, Body: io.NopCloser(bytes.NewReader(w.body.Bytes())), Request: request}, nil
}

type localResponse struct {
	headers http.Header
	body    bytes.Buffer
	status  int
}

func (w *localResponse) Header() http.Header { return w.headers }
func (w *localResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *localResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}
