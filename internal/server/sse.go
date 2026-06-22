package server

import (
	"sync"

	"github.com/shandley/trellis/internal/core"
)

// hub is a tiny in-memory pub/sub broadcaster for SSE. Each subscriber gets its
// own buffered channel; publishers never block on a slow subscriber (events are
// dropped for a subscriber whose buffer is full).
type hub struct {
	mu     sync.Mutex
	subs   map[chan core.Event]struct{}
	closed bool
}

func newHub() *hub {
	return &hub{subs: make(map[chan core.Event]struct{})}
}

// subscribe returns a new event channel and an unsubscribe function. The caller
// must invoke unsubscribe when done (e.g. on client disconnect).
func (h *hub) subscribe() (chan core.Event, func()) {
	ch := make(chan core.Event, 64)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.subs[ch]; ok {
				delete(h.subs, ch)
				close(ch)
			}
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

// publish delivers an event to every current subscriber, dropping it for any
// subscriber whose buffer is full rather than blocking.
func (h *hub) publish(ev core.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// close shuts down the hub and closes all subscriber channels.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}
