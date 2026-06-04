package subagent

import (
	"sync"

	ac "github.com/voocel/agentcore"
)

// Event is an agentcore lifecycle event tagged with the AgentPath of the
// subagent that emitted it. The Pool merges events from many concurrent
// subagent runs onto one stream; AgentPath is how a consumer tells them apart
// (e.g. {"research/0001"}, or {"root/0001", "web/0002"} for a nested run).
//
// agentcore.Event has no notion of a subagent path, so jess carries it
// alongside rather than inside the agentcore type.
type Event struct {
	ac.Event
	AgentPath []string
}

// Stream multiplexes Events from many producers (the Pool's per-job mergers)
// onto a single consumer that ranges over Events() (fan-in). Backpressure is by
// blocking send against the buffered channel: a slow consumer slows producers,
// bounding memory.
//
// Send after Close is a no-op (never a panic), so a producer that outlives the
// run is harmless. Close is idempotent and unblocks any producer currently
// blocked on a full buffer, so a graceful Close never hangs behind a slow or
// absent consumer.
type Stream struct {
	mu     sync.RWMutex
	ch     chan Event
	closed bool
	// done is closed by Close before it takes the write lock, so a Send blocked
	// on a full buffer (holding the read lock) unblocks immediately instead of
	// stalling Close.
	done      chan struct{}
	closeOnce sync.Once
}

// NewStream returns a Stream whose channel buffers up to buffer events.
func NewStream(buffer int) *Stream {
	return &Stream{ch: make(chan Event, buffer), done: make(chan struct{})}
}

// Events returns the receive channel. It is closed when Close runs, so callers
// can range over it.
func (s *Stream) Events() <-chan Event { return s.ch }

// Send delivers ev to the consumer. It blocks while the buffer is full, unless
// the stream is closed (then it drops ev and returns).
func (s *Stream) Send(ev Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- ev:
	case <-s.done:
	}
}

// Close closes the stream. Idempotent. Subsequent Send calls are no-ops and the
// Events channel is closed so consumers ranging over it terminate.
func (s *Stream) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	})
}
