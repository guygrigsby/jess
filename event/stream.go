package event

import "sync"

// Stream is a single-consumer fan-out of run Events. Producers (the
// anti-corruption layer, and later the subagent Pool's merger) call Send; the
// host ranges over Events(). Backpressure is by blocking send against the
// buffered channel: a slow consumer slows producers, bounding memory.
//
// Send after Close is a no-op (never a panic), so a producer that outlives the
// run is harmless. Close is idempotent.
type Stream struct {
	mu     sync.RWMutex
	ch     chan Event
	closed bool
}

// NewStream returns a Stream whose channel buffers up to buffer events.
func NewStream(buffer int) *Stream {
	return &Stream{ch: make(chan Event, buffer)}
}

// Events returns the receive channel. It is closed when Close runs, so callers
// can range over it.
func (s *Stream) Events() <-chan Event { return s.ch }

// Send delivers ev to the consumer. It blocks while the buffer is full (unless
// the stream is closed). After Close it drops ev and returns.
//
// The read lock is held across the channel send so Close, which needs the
// write lock, cannot close the channel mid-send.
func (s *Stream) Send(ev Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	s.ch <- ev
}

// Close closes the stream. Idempotent. Subsequent Send calls are no-ops and the
// Events channel is closed so consumers ranging over it terminate.
func (s *Stream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}
