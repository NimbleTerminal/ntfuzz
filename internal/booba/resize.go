package booba

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NimbleMarkets/go-booba/serve"

	"nimbleterminal.dev/ntfuzz/internal/metrics"
)

// go-booba v0.11's built-in idle wrapper embeds only serve.Session, hiding
// ResizeEvents from its Bubble Tea adapter. Re-emit protocol resize updates at
// the outer session boundary so idle expiry and live TUI resizing both work.
type resizeSession struct {
	serve.Session
	mu     sync.Mutex
	events chan serve.WindowSize
}

func preserveResizeEvents(session serve.Session) serve.Session {
	return &resizeSession{Session: session, events: make(chan serve.WindowSize, 1)}
}
func (s *resizeSession) ResizeEvents() <-chan serve.WindowSize { return s.events }
func (s *resizeSession) Resize(cols, rows int) {
	s.ResizeWindow(serve.WindowSize{Width: cols, Height: rows})
}
func (s *resizeSession) ResizeWindow(size serve.WindowSize) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if resizer, ok := s.Session.(serve.WindowResizer); ok {
		resizer.ResizeWindow(size)
	} else {
		s.Session.Resize(size.Width, size.Height)
	}
	select {
	case s.events <- size:
	default:
		select {
		case <-s.events:
		default:
		}
		s.events <- size
	}
}

type wsAttemptKey struct{}

type wsAttempt struct {
	opened   atomic.Bool
	rejected atomic.Bool
}

func serveTrackedTerminalRequest(w http.ResponseWriter, r *http.Request, handler http.Handler, m *metrics.Metrics) {
	attempt := &wsAttempt{}
	r = r.WithContext(context.WithValue(r.Context(), wsAttemptKey{}, attempt))
	handler.ServeHTTP(w, r)
	if !attempt.opened.Load() && attempt.rejected.CompareAndSwap(false, true) {
		status := http.StatusOK
		if sc, ok := w.(interface{ StatusCode() int }); ok {
			status = sc.StatusCode()
		}
		switch status {
		case http.StatusServiceUnavailable:
			m.SessionRejected(metrics.RejectReasonConnectionLimit)
		case http.StatusUnauthorized:
			m.SessionRejected(metrics.RejectReasonUnauthorized)
		case http.StatusForbidden:
			m.SessionRejected(metrics.RejectReasonOrigin)
		default:
			m.SessionRejected(metrics.RejectReasonHandshake)
		}
	}
}

func connectMetricsMiddleware(m *metrics.Metrics) serve.ConnectMiddleware {
	return func(next serve.ConnectHandler) serve.ConnectHandler {
		return func(r *http.Request) error {
			err := next(r)
			if err != nil && m != nil {
				if attempt, ok := r.Context().Value(wsAttemptKey{}).(*wsAttempt); ok {
					if !attempt.rejected.CompareAndSwap(false, true) {
						return err
					}
				}
				reason := metrics.RejectReasonHandshake
				var ce *serve.ConnectError
				if errors.As(err, &ce) {
					switch ce.Status {
					case http.StatusServiceUnavailable:
						reason = metrics.RejectReasonConnectionLimit
					case http.StatusUnauthorized:
						reason = metrics.RejectReasonUnauthorized
					case http.StatusForbidden:
						reason = metrics.RejectReasonOrigin
					}
				}
				m.SessionRejected(reason)
			}
			return err
		}
	}
}

func sessionMetricsMiddleware(serverCtx context.Context, m *metrics.Metrics) serve.SessionMiddleware {
	return func(base serve.Session) serve.Session {
		if m == nil {
			return base
		}
		if attempt, ok := base.Context().Value(wsAttemptKey{}).(*wsAttempt); ok {
			attempt.opened.Store(true)
		}
		m.SessionOpened()
		ms := &metricSession{
			Session:   base,
			metrics:   m,
			serverCtx: serverCtx,
			start:     time.Now(),
		}
		ms.in = &meteringWriter{inner: base.InputWriter(), m: m}
		ms.out = &meteringReader{inner: base.OutputReader(), m: m}
		return ms
	}
}

type metricSession struct {
	serve.Session
	metrics   *metrics.Metrics
	serverCtx context.Context
	start     time.Time
	in        io.Writer
	out       io.Reader
	closeOnce sync.Once
	closeErr  error
}

func (s *metricSession) InputWriter() io.Writer  { return s.in }
func (s *metricSession) OutputReader() io.Reader { return s.out }

func (s *metricSession) ResizeWindow(size serve.WindowSize) {
	if resizer, ok := s.Session.(serve.WindowResizer); ok {
		resizer.ResizeWindow(size)
	} else {
		s.Session.Resize(size.Width, size.Height)
	}
}

func (s *metricSession) ResizeEvents() <-chan serve.WindowSize {
	if src, ok := s.Session.(interface {
		ResizeEvents() <-chan serve.WindowSize
	}); ok {
		return src.ResizeEvents()
	}
	return nil
}

func (s *metricSession) Close() error {
	s.closeOnce.Do(func() {
		innerAlreadyDone := false
		select {
		case <-s.Session.Done():
			innerAlreadyDone = true
		default:
		}
		s.closeErr = s.Session.Close()
		reason := metrics.ReasonClientDisconnect
		switch {
		case s.serverCtx != nil && s.serverCtx.Err() != nil:
			reason = metrics.ReasonServerShutdown
		case innerAlreadyDone:
			reason = metrics.ReasonIdleTimeout
		case s.closeErr != nil && !errors.Is(s.closeErr, io.EOF):
			reason = metrics.ReasonError
		}
		s.metrics.SessionClosed(time.Since(s.start), reason)
	})
	return s.closeErr
}

type meteringWriter struct {
	inner io.Writer
	m     *metrics.Metrics
}

func (w *meteringWriter) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	if n > 0 {
		w.m.AddBytesReceived(n)
	}
	return n, err
}

type meteringReader struct {
	inner io.Reader
	m     *metrics.Metrics
}

func (r *meteringReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 {
		r.m.AddBytesSent(n)
	}
	return n, err
}
