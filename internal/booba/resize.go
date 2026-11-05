package booba

import (
	"sync"

	"github.com/NimbleMarkets/go-booba/serve"
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
