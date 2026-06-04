// Package ledger is the durable, agentcore-free record of everything an agent
// does. It is jess's detective control: over a remote channel the operator
// loses the ability to watch output, so the ledger stands in. Tool requests
// are recorded even when the gate denies them, so blocked (possibly rogue)
// attempts stay visible instead of vanishing.
package ledger

import (
	"crypto/rand"
	"encoding/json"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// EventID is a ULID: time-ordered, globally unique, the ledger primary key.
type EventID = ulid.ULID

// monotonic entropy so ids minted in the same millisecond still sort in
// creation order. ulid.Monotonic is not goroutine-safe, so guard it.
var (
	entMu   sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// NewEventID mints a fresh, monotonically increasing time-ordered id.
func NewEventID() ulid.ULID {
	entMu.Lock()
	defer entMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy)
}

// Kind classifies an audit Event.
type Kind string

const (
	KindPrompt        Kind = "prompt"
	KindModelResponse Kind = "model_response"
	KindToolRequest   Kind = "tool_request"
	KindGateDecision  Kind = "gate_decision"
	KindToolResult    Kind = "tool_result"
	KindAbort         Kind = "abort"
	KindRunEnd        Kind = "run_end"
	KindRequest       Kind = "request"   // chain head: the run input
	KindRetrieved     Kind = "retrieved" // memory recall, by ref
	KindAction        Kind = "action"    // atomic intent + gate verdict, committed before execution
)

// RefSource names what a Ref points at, so resolution never guesses from id shape.
type RefSource string

const (
	RefTool   RefSource = "tool"   // ID is a ledger EventID
	RefMemory RefSource = "memory" // ID is a memory.Entry.ID
)

// Ref addresses an available item by id plus a content hash captured at decision
// time, so drift or deletion is detectable. Refs, not copies.
type Ref struct {
	Source RefSource `json:"source"`
	ID     string    `json:"id"`
	Hash   string    `json:"hash"`
}

// Verdict is the gate outcome recorded on a gate_decision Event.
type Verdict string

const (
	VerdictAllowed       Verdict = "allowed"
	VerdictDenied        Verdict = "denied"
	VerdictNeedsApproval Verdict = "needs_approval"
)

// Event is one recorded action. Fields are populated per Kind; zero values are
// fine for fields that do not apply.
type Event struct {
	EventID    ulid.ULID       `json:"event_id"`
	RunID      string          `json:"run_id,omitempty"`
	CallID     string          `json:"call_id,omitempty"`
	Refs       []Ref           `json:"refs,omitempty"`
	Time       time.Time       `json:"time"`
	AgentPath  string          `json:"agent_path,omitempty"`
	Kind       Kind            `json:"kind"`
	Tool       string          `json:"tool,omitempty"`
	Label      string          `json:"label,omitempty"`
	Preview    string          `json:"preview,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Verdict    Verdict         `json:"verdict,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Err        string          `json:"err,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
}

// Sink receives audit Events. Implementations must be safe for concurrent use.
type Sink interface {
	Record(Event) error
}

// DiscardSink drops every Event. Turning recording off is explicit (pass this to
// jess.WithLedger), never silent.
type DiscardSink struct{}

// Record satisfies Sink.
func (DiscardSink) Record(Event) error { return nil }
