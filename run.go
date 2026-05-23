package jess

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// DefaultMaxIterations bounds the multi-turn loop in the absence of a
// caller override. Set high enough that real workflows complete but
// low enough that a runaway model can't spin tool calls forever.
const DefaultMaxIterations = 16

// DefaultEventBuffer sizes the event channel so a momentarily slow
// consumer doesn't immediately block the loop. The harness still
// respects context cancellation on every send — a permanently stuck
// consumer eventually unblocks via ctx-done rather than deadlock.
const DefaultEventBuffer = 32

// RunRequest is the input to Run. Required fields (Provider, Model)
// are validated up front; optional fields fall back to documented
// defaults.
type RunRequest struct {
	// Provider is the streaming backend. Required. The harness does
	// NOT consult ProviderRegistry here — callers that want
	// registry-driven routing should resolve the provider via the
	// registry first, then pass it in.
	Provider Provider
	// Model is the canonical "<provider>/<model>" ID. Required.
	// Forwarded verbatim to Provider.Stream; the harness does not
	// validate the prefix matches Provider.Name().
	Model ModelID
	// System is an optional system prompt. Forwarded to every
	// Stream call this run makes.
	System string
	// History is the conversation so far, oldest first. The harness
	// appends each turn's assistant message + any tool-result
	// messages to a copy; the caller's slice is never mutated.
	History []Message
	// Tools is the registry the harness dispatches tool calls
	// through. nil disables tool-calling entirely (text-only
	// chat); pass NewToolRunner() with no tools for the same
	// effect with a non-nil empty registry.
	Tools *ToolRunner
	// Options is per-iteration tuning forwarded to every Stream
	// call. Zero value is safe.
	Options Options

	// MaxIterations bounds the loop. 0 means use
	// DefaultMaxIterations. Negative means unlimited (don't —
	// dangerous; provided for explicit opt-in).
	MaxIterations int
}

// RunResult is the terminal summary delivered on EventDone and via
// Run's return-value variant (if the API grows one). Captures the
// final history, accumulated usage, why the loop terminated, and
// any error.
type RunResult struct {
	// History is the conversation after the run: caller's input
	// History plus one assistant message per iteration plus tool-
	// result messages for any tools that ran. The caller's
	// original slice is not mutated.
	History []Message
	// Usage accumulates token counts across every DeltaUsage the
	// providers reported. Zero when no provider emitted usage.
	Usage Usage
	// Iterations counts how many times the harness called
	// Provider.Stream. Always at least 1 for a started run.
	Iterations int
	// StoppedReason explains the loop exit; see StopReason godocs
	// for semantics.
	StoppedReason StopReason
	// Err is the underlying error for StopError / StopCanceled,
	// nil otherwise.
	Err error
}

// Run starts the agent loop and returns a channel of Events. The
// caller drains the channel until close; close happens after the
// harness sends EventDone (or immediately on certain ctx-cancel
// races, in which case the consumer should consult ctx.Err()).
//
// Synchronous validation: invalid inputs (nil Provider, empty Model)
// return an error here and produce no events. The caller doesn't
// have to start a drain loop to discover misconfiguration.
//
// Concurrency: the harness owns one goroutine for the loop plus one
// per parallel tool dispatch. All sends to the returned channel are
// serialized through an internal mutex, so the channel is the only
// ordering surface — consumers do not need their own synchronization.
//
// The harness never closes a channel it didn't open and does not
// re-close on panic; consumers should defer-recover their own draining
// if they want crash tolerance.
func Run(ctx context.Context, req RunRequest) (<-chan Event, error) {
	if req.Provider == nil {
		return nil, errors.New("jess: RunRequest.Provider is required")
	}
	if req.Model == "" {
		return nil, errors.New("jess: RunRequest.Model is required")
	}
	maxIter := req.MaxIterations
	if maxIter == 0 {
		maxIter = DefaultMaxIterations
	}

	events := make(chan Event, DefaultEventBuffer)
	r := &runState{
		ctx:     ctx,
		req:     req,
		events:  events,
		maxIter: maxIter,
		// Defensive copy so the caller's slice isn't mutated.
		history: append([]Message(nil), req.History...),
	}
	go r.loop()
	return events, nil
}

// runState carries the per-run state across the loop goroutine and
// the parallel tool-dispatch helpers. The mutex guards `events` (so
// concurrent tool-end emissions serialize correctly) and `usage`
// (accumulated across iterations and possibly written from the
// stream goroutine while tools are still draining).
type runState struct {
	ctx     context.Context
	req     RunRequest
	events  chan Event
	maxIter int

	mu      sync.Mutex
	history []Message
	usage   Usage
}

// emit sends a non-terminal event. Respects ctx cancellation so a
// stuck consumer doesn't deadlock the harness mid-iteration — when
// the run is being torn down, we'd rather drop a Text/Usage/Tool
// event than hang waiting for the consumer to drain.
//
// Holding the mutex over the send keeps parallel tool-end emissions
// in some sequential order on the channel (the relative order among
// parallel ends is not guaranteed — see EventToolEnd godoc).
//
// Returns true if the event was delivered; false if the context
// canceled mid-send. Callers should usually exit early on a false
// return. NOT for terminator events — use emitTerminal so EventDone
// / EventError can never be silently dropped.
func (r *runState) emit(e Event) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case r.events <- e:
		return true
	case <-r.ctx.Done():
		return false
	}
}

// emitTerminal sends an event that MUST be delivered before the
// channel closes (EventDone, EventError). Unlike emit, it does not
// respect ctx — the consumer owes us a drain regardless of ctx
// state, because the terminator is the only signal that distinguishes
// "normal run completion" from "channel just happened to close."
//
// The channel is buffered (DefaultEventBuffer) so a momentarily slow
// consumer doesn't block this; a permanently stuck consumer that
// fills the buffer is a contract violation and will deadlock the
// harness. That's intentional — silent loss of EventDone is worse
// than a visible hang.
func (r *runState) emitTerminal(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events <- e
}

// finish wraps up the run: emit EventDone with the supplied result
// and close the channel. Safe to call exactly once per run.
func (r *runState) finish(result RunResult) {
	// Copy under lock for a stable snapshot.
	r.mu.Lock()
	result.History = append([]Message(nil), r.history...)
	result.Usage = r.usage
	r.mu.Unlock()
	r.emitTerminal(Event{
		Kind:      EventDone,
		Iteration: result.Iterations - 1,
		Result:    &result,
	})
	close(r.events)
}

// loop runs the multi-turn iterate-until-no-more-tool-calls cycle.
// Exits via finish() under all conditions: normal completion, max-
// iterations, context cancellation, provider/tool error.
func (r *runState) loop() {
	for iter := 0; iter < r.maxIter || r.maxIter < 0; iter++ {
		if err := r.ctx.Err(); err != nil {
			r.finish(RunResult{
				Iterations:    iter,
				StoppedReason: StopCanceled,
				Err:           err,
			})
			return
		}
		r.emit(Event{Kind: EventIterationStart, Iteration: iter})

		toolCalls, ok := r.streamIteration(iter)
		if !ok {
			// streamIteration already emitted EventError + finish
			// when it returned false. Nothing more to do.
			return
		}

		r.emit(Event{Kind: EventIterationEnd, Iteration: iter})

		if len(toolCalls) == 0 {
			// Model finished naturally — no tool calls means the
			// iteration produced a final reply.
			r.finish(RunResult{
				Iterations:    iter + 1,
				StoppedReason: StopFinish,
			})
			return
		}

		// Dispatch tool calls. dispatchTools returns false if a
		// tool errored at the infra level (already emitted
		// EventError + finish).
		if !r.dispatchTools(iter, toolCalls) {
			return
		}
	}
	// Loop fell out — hit the iteration cap.
	r.finish(RunResult{
		Iterations:    r.maxIter,
		StoppedReason: StopMaxIterations,
	})
}

// streamIteration runs one Provider.Stream call, draining the delta
// channel and emitting events. Returns the assembled tool calls (if
// any) and ok=false when an error or cancellation terminated the run
// (in which case it already called finish).
func (r *runState) streamIteration(iter int) (toolCalls []ToolCall, ok bool) {
	req := r.buildRequest()
	deltaCh, err := r.req.Provider.Stream(r.ctx, req)
	if err != nil {
		r.errorAndFinish(iter, fmt.Errorf("provider %s: %w", r.req.Provider.Name(), err))
		return nil, false
	}

	var assistantText string
	for {
		select {
		case <-r.ctx.Done():
			r.finish(RunResult{
				Iterations:    iter + 1,
				StoppedReason: StopCanceled,
				Err:           r.ctx.Err(),
			})
			return nil, false
		case d, more := <-deltaCh:
			if !more {
				// Stream closed normally. Append the assembled
				// assistant turn so the next iteration sees it.
				r.appendAssistant(assistantText, toolCalls)
				return toolCalls, true
			}
			switch d.Kind {
			case DeltaText:
				assistantText += d.Text
				r.emit(Event{Kind: EventText, Iteration: iter, Text: d.Text})
			case DeltaReasoning:
				r.emit(Event{Kind: EventReasoning, Iteration: iter, Text: d.Text})
			case DeltaUsage:
				if d.Usage != nil {
					r.mu.Lock()
					r.usage.InputTokens += d.Usage.InputTokens
					r.usage.OutputTokens += d.Usage.OutputTokens
					r.usage.ReasoningTokens += d.Usage.ReasoningTokens
					r.usage.CachedInputTokens += d.Usage.CachedInputTokens
					u := r.usage
					r.mu.Unlock()
					// Emit the snapshot AFTER releasing the lock
					// so a slow consumer doesn't hold up other
					// stream writers.
					_ = u
					r.emit(Event{Kind: EventUsage, Iteration: iter, Usage: d.Usage})
				}
			case DeltaToolCall:
				if d.ToolCall != nil {
					toolCalls = append(toolCalls, *d.ToolCall)
				}
			case DeltaError:
				err := d.Err
				if err == nil {
					err = errors.New("jess: provider DeltaError without Err set")
				}
				r.errorAndFinish(iter, fmt.Errorf("provider %s: %w", r.req.Provider.Name(), err))
				return nil, false
			}
		}
	}
}

// dispatchTools runs every tool call in toolCalls concurrently against
// r.req.Tools, emits EventToolStart/End around each, and appends the
// results to history as RoleTool messages. Returns false if any tool
// returned an infrastructure error (already emitted EventError +
// finish in that case).
func (r *runState) dispatchTools(iter int, toolCalls []ToolCall) bool {
	if r.req.Tools == nil {
		// Model requested tools but the caller didn't wire any —
		// terminate the run with an explanatory error rather than
		// silently dropping the call.
		err := fmt.Errorf("jess: model requested tool %q but RunRequest.Tools is nil", toolCalls[0].Name)
		r.errorAndFinish(iter, err)
		return false
	}

	type result struct {
		call ToolCall
		out  ToolResult
		err  error
	}
	results := make([]result, len(toolCalls))

	var wg sync.WaitGroup
	for i, call := range toolCalls {
		r.emit(Event{Kind: EventToolStart, Iteration: iter, ToolCall: &toolCalls[i]})
		wg.Add(1)
		go func(idx int, c ToolCall) {
			defer wg.Done()
			out, err := r.req.Tools.Run(r.ctx, c)
			results[idx] = result{call: c, out: out, err: err}
		}(i, call)
	}
	wg.Wait()

	for _, res := range results {
		if res.err != nil {
			r.errorAndFinish(iter, fmt.Errorf("tool %s: %w", res.call.Name, res.err))
			return false
		}
		r.emit(Event{
			Kind:      EventToolEnd,
			Iteration: iter,
			ToolResult: &ToolEvent{
				CallID: res.call.ID,
				Name:   res.call.Name,
				Result: res.out,
			},
		})
		r.appendToolResult(res.call.ID, res.out.Output)
	}
	return true
}

// buildRequest constructs the provider Request from current state.
// Always copies the history so the provider can't mutate it.
func (r *runState) buildRequest() Request {
	r.mu.Lock()
	hist := append([]Message(nil), r.history...)
	r.mu.Unlock()
	var tools []ToolSpec
	if r.req.Tools != nil {
		tools = r.req.Tools.Specs()
	}
	return Request{
		Model:    r.req.Model,
		System:   r.req.System,
		Messages: hist,
		Options:  r.req.Options,
		Tools:    tools,
	}
}

// appendAssistant records the model's turn — visible text plus any
// finalized tool calls.
func (r *runState) appendAssistant(text string, calls []ToolCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.history = append(r.history, Message{
		Role:      RoleAssistant,
		Content:   text,
		ToolCalls: append([]ToolCall(nil), calls...),
	})
}

// appendToolResult records one tool's output as a RoleTool message
// the next iteration will send back to the model.
func (r *runState) appendToolResult(callID, output string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.history = append(r.history, Message{
		Role:       RoleTool,
		Content:    output,
		ToolCallID: callID,
	})
}

// errorAndFinish emits EventError followed by EventDone(StopError) and
// closes the channel. EventError uses emitTerminal so it's always
// delivered even when ctx is canceled — consumers need to know WHY
// the run ended, not just that it ended. Safe to call exactly once
// per run (a second close in finish would panic — that's a caller
// bug).
func (r *runState) errorAndFinish(iter int, err error) {
	r.emitTerminal(Event{Kind: EventError, Iteration: iter, Err: err})
	r.finish(RunResult{
		Iterations:    iter + 1,
		StoppedReason: StopError,
		Err:           err,
	})
}
