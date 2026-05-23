package jess

// Event is one notification emitted by the harness as a Run progresses.
// Events flow out through a single channel; consumers drain it until
// close (which signals the run has terminated and EventDone has been
// delivered or the channel is closed for context cancellation).
//
// Which fields are populated depends on Kind — see EventKind godocs.
// Iteration is set on every Event so consumers can correlate streams
// with the multi-turn loop tick that produced them.
type Event struct {
	// Kind discriminates which fields below are meaningful.
	Kind EventKind
	// Iteration is the 0-based loop-tick this event belongs to.
	// Tools dispatched within an iteration carry the iteration's
	// number; EventDone carries the final iteration's number.
	Iteration int

	// Text is set for EventText (assistant token chunk) and
	// EventReasoning (hidden thinking chunk, where supported by
	// the provider). Empty for other kinds.
	Text string

	// ToolCall is non-nil for EventToolStart — the resolved call
	// the harness is about to dispatch. The harness emits one
	// EventToolStart per call within an iteration.
	ToolCall *ToolCall

	// ToolResult is non-nil for EventToolEnd — the runner's
	// response, including the originating call ID for correlation.
	// IsError lets consumers style soft failures distinctly from
	// successes without inspecting Output.
	ToolResult *ToolEvent

	// Usage is non-nil for EventUsage — per-iteration token
	// accounting reported by the provider's DeltaUsage.
	Usage *Usage

	// Err is non-nil for EventError. The harness writes EventError
	// then EventDone (with StoppedReason=StopError) and closes the
	// channel. Two events for the same failure so consumers don't
	// have to special-case the channel-close.
	Err error

	// Result is non-nil for EventDone — the terminal summary of
	// the run (history, usage totals, stop reason, iteration count).
	Result *RunResult
}

// EventKind discriminates the Event variant.
type EventKind int

const (
	// EventText is a visible assistant-response token chunk.
	// Concatenate Text across all EventText events (within a single
	// iteration, separated by EventIterationStart/End boundaries)
	// to reconstruct the model's reply for that turn.
	EventText EventKind = iota
	// EventReasoning is a hidden thinking chunk (o1, Claude
	// thinking, etc.). Most providers will not emit this.
	EventReasoning
	// EventToolStart fires once per assembled tool call, right
	// before the harness dispatches it. ToolCall is non-nil.
	EventToolStart
	// EventToolEnd fires once per tool call when it completes —
	// success or IsError. ToolResult is non-nil and carries the
	// call's ID so consumers can pair EventToolStart with EventToolEnd.
	// Order is not guaranteed across parallel calls within the same
	// iteration; pair by ID.
	EventToolEnd
	// EventIterationStart marks the beginning of one tick of the
	// multi-turn loop. Useful for UIs that group events by iteration.
	EventIterationStart
	// EventIterationEnd marks the end of an iteration's provider
	// stream. The harness has not yet decided whether to loop —
	// EventDone (no tool calls finalized) or another EventIterationStart
	// (tool calls finalized, results dispatched) follows.
	EventIterationEnd
	// EventUsage carries per-iteration token accounting from the
	// provider's DeltaUsage. Emitted at most once per iteration,
	// near its end.
	EventUsage
	// EventError indicates a fatal run failure (provider setup or
	// mid-stream error, tool infra error, context error). Always
	// followed by EventDone with StoppedReason=StopError. Err is
	// non-nil.
	EventError
	// EventDone is the terminal event — the harness will close the
	// event channel immediately after sending it. Result is non-nil
	// and carries the run's final summary.
	EventDone
)

// String returns a human-readable label for k. Stable for logging.
func (k EventKind) String() string {
	switch k {
	case EventText:
		return "text"
	case EventReasoning:
		return "reasoning"
	case EventToolStart:
		return "tool_start"
	case EventToolEnd:
		return "tool_end"
	case EventIterationStart:
		return "iteration_start"
	case EventIterationEnd:
		return "iteration_end"
	case EventUsage:
		return "usage"
	case EventError:
		return "error"
	case EventDone:
		return "done"
	default:
		return "unknown"
	}
}

// ToolEvent is the payload of an EventToolEnd. Distinct from ToolResult
// because the harness needs to pair tool-call ID + tool name with the
// result so consumers can correlate across the parallel dispatch.
type ToolEvent struct {
	// CallID matches the originating ToolCall.ID from EventToolStart.
	CallID string
	// Name is the tool name (mirrors the originating ToolCall.Name
	// for convenience — saves consumers a map lookup).
	Name string
	// Result is what the Tool's Run method returned.
	Result ToolResult
}

// StopReason explains why a run terminated. Set on RunResult.StoppedReason.
type StopReason int

const (
	// StopFinish: the model finished an iteration without finalizing
	// any tool calls. The loop's natural exit.
	StopFinish StopReason = iota
	// StopMaxIterations: the multi-turn loop hit MaxIterations
	// without the model deciding to stop. Caller should treat this
	// as "model wanted to keep going" — possibly raise the cap, or
	// surface as an error depending on context.
	StopMaxIterations
	// StopCanceled: the context handed to Run was canceled (deadline
	// exceeded or Cancel called). RunResult.Err carries ctx.Err().
	StopCanceled
	// StopError: a provider setup error, a mid-stream DeltaError, or
	// a Tool infrastructure error aborted the run. RunResult.Err
	// carries the underlying failure.
	StopError
)

// String returns a human-readable label for r. Stable for logging.
func (r StopReason) String() string {
	switch r {
	case StopFinish:
		return "finish"
	case StopMaxIterations:
		return "max_iterations"
	case StopCanceled:
		return "canceled"
	case StopError:
		return "error"
	default:
		return "unknown"
	}
}
