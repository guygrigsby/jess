// Package ledger is the durable, agentcore-free record of everything an agent
// does. It is jess's detective control: over a remote channel the operator
// loses the ability to watch output, so the ledger stands in. Tool requests
// are recorded even when the gate denies them, so blocked (possibly rogue)
// attempts stay visible instead of vanishing.
package ledger

import (
	"encoding/json"
	"time"
)

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
)

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
