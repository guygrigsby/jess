// Package event is jess's observable run stream, independent of any harness.
// The anti-corruption layer flattens the harness's wider event taxonomy into
// these kinds; nothing here imports agentcore.
package event

import "encoding/json"

// EventKind identifies a lifecycle event in a run.
type EventKind string

const (
	KindRunStart     EventKind = "run_start"
	KindTurnStart    EventKind = "turn_start"
	KindMessageDelta EventKind = "message_delta"
	KindToolStart    EventKind = "tool_start"
	KindToolEnd      EventKind = "tool_end"
	KindTurnEnd      EventKind = "turn_end"
	KindRunEnd       EventKind = "run_end"
	KindError        EventKind = "error"
)

// DeltaKind classifies the text carried by a KindMessageDelta event so a
// streaming consumer can distinguish assistant prose from reasoning or
// streamed tool-call arguments.
type DeltaKind string

const (
	DeltaText     DeltaKind = ""         // regular assistant text
	DeltaThinking DeltaKind = "thinking" // model reasoning/thinking
	DeltaToolCall DeltaKind = "toolcall" // streamed tool-call argument JSON
)

// RunSummary is the factual outcome of a single run.
type RunSummary struct {
	Turns     int
	ToolCalls int
	EndReason string // stop, max_turns, aborted, error
}

// Event is one observation from a run. Fields are populated according to Kind.
//
// AgentPath is nil for the root agent and carries name/instance segments for
// subagents (for example []string{"research/0007"}, nested for deeper
// trees), so a single stream represents an entire agent tree.
type Event struct {
	Kind      EventKind
	AgentPath []string

	Delta      string          // KindMessageDelta
	DeltaKind  DeltaKind       // KindMessageDelta: classifies Delta
	Tool       string          // KindToolStart, KindToolEnd: tool name
	ToolCallID string          // KindToolStart, KindToolEnd: unique call ID (correlates start/end)
	Args       json.RawMessage // KindToolStart
	Result     json.RawMessage // KindToolEnd
	IsError    bool            // KindToolEnd
	Err        error           // KindError
	Summary    *RunSummary     // KindRunEnd
}

// IsSubagent reports whether the event came from a subagent (non-empty
// AgentPath) rather than the root agent.
func (e Event) IsSubagent() bool { return len(e.AgentPath) > 0 }
