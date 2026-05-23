package jess

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedProvider returns a fixed list of Deltas per iteration. Each
// element of `turns` is one Stream call's payload. Used to script
// multi-turn loops deterministically.
type scriptedProvider struct {
	name      string
	turns     [][]Delta
	turnIdx   int
	setupErr  error
	captureRq *Request // optional: written with each call's Request
	hangFor   time.Duration
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) Stream(ctx context.Context, req Request) (<-chan Delta, error) {
	if p.setupErr != nil {
		return nil, p.setupErr
	}
	if p.captureRq != nil {
		*p.captureRq = req
	}
	if p.turnIdx >= len(p.turns) {
		// Default to a clean stop if scripted turns run out.
		ch := make(chan Delta)
		close(ch)
		return ch, nil
	}
	deltas := p.turns[p.turnIdx]
	p.turnIdx++
	ch := make(chan Delta, len(deltas))
	go func() {
		defer close(ch)
		if p.hangFor > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.hangFor):
			}
		}
		for _, d := range deltas {
			select {
			case <-ctx.Done():
				return
			case ch <- d:
			}
		}
	}()
	return ch, nil
}

// staticToolFn is a Tool whose Run is a single function. Mirrors
// staticTool from tool_test.go but exposed here so both files share
// the helper without duplication. Reusing avoids drift.
type staticToolFn struct {
	name string
	desc string
	fn   func(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

func (t *staticToolFn) Spec() ToolSpec {
	return ToolSpec{Name: t.name, Description: t.desc, ParametersSchema: json.RawMessage(`{}`)}
}

func (t *staticToolFn) Run(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	return t.fn(ctx, args)
}

// drainEvents reads events until the channel closes, returning all
// observed events in order. Times out the test if the channel never
// closes — every Run() should always close exactly once.
func drainEvents(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var got []Event
	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-timeout:
			t.Fatalf("drainEvents timed out; got so far: %d events", len(got))
		case e, more := <-ch:
			if !more {
				return got
			}
			got = append(got, e)
		}
	}
}

func lastEvent(events []Event) Event {
	return events[len(events)-1]
}

func TestRun_RejectsNilProvider(t *testing.T) {
	_, err := Run(context.Background(), RunRequest{Model: "openai/gpt-x"})
	if err == nil {
		t.Fatal("expected error for nil provider")
	}
	if !strings.Contains(err.Error(), "Provider") {
		t.Errorf("error should mention Provider: %v", err)
	}
}

func TestRun_RejectsEmptyModel(t *testing.T) {
	p := &scriptedProvider{name: "fake"}
	_, err := Run(context.Background(), RunRequest{Provider: p})
	if err == nil {
		t.Fatal("expected error for empty Model")
	}
	if !strings.Contains(err.Error(), "Model") {
		t.Errorf("error should mention Model: %v", err)
	}
}

// Happy path: model emits text + usage, no tools. Run should exit
// with StopFinish after a single iteration.
func TestRun_TextOnly_SingleIteration_FinishesNaturally(t *testing.T) {
	p := &scriptedProvider{
		name: "fake",
		turns: [][]Delta{
			{
				{Kind: DeltaText, Text: "hello "},
				{Kind: DeltaText, Text: "world"},
				{Kind: DeltaUsage, Usage: &Usage{InputTokens: 5, OutputTokens: 2}},
			},
		},
	}
	ch, err := Run(context.Background(), RunRequest{
		Provider: p,
		Model:    "fake/m",
	})
	if err != nil {
		t.Fatalf("Run errored: %v", err)
	}
	events := drainEvents(t, ch)

	// Expected: iter_start, text, text, usage, iter_end, done.
	kinds := []EventKind{}
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	wantKinds := []EventKind{
		EventIterationStart, EventText, EventText, EventUsage,
		EventIterationEnd, EventDone,
	}
	if !equalKindSlices(kinds, wantKinds) {
		t.Errorf("event kinds = %v, want %v", kinds, wantKinds)
	}

	done := lastEvent(events)
	if done.Result == nil {
		t.Fatal("EventDone should carry Result")
	}
	if done.Result.StoppedReason != StopFinish {
		t.Errorf("StoppedReason = %v, want StopFinish", done.Result.StoppedReason)
	}
	if done.Result.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", done.Result.Iterations)
	}
	if done.Result.Usage.InputTokens != 5 || done.Result.Usage.OutputTokens != 2 {
		t.Errorf("Usage not accumulated: %+v", done.Result.Usage)
	}
	// History should contain the assistant turn the harness appended.
	if len(done.Result.History) != 1 {
		t.Fatalf("History len = %d, want 1", len(done.Result.History))
	}
	if done.Result.History[0].Role != RoleAssistant {
		t.Errorf("History role = %v, want assistant", done.Result.History[0].Role)
	}
	if done.Result.History[0].Content != "hello world" {
		t.Errorf("History content = %q, want %q", done.Result.History[0].Content, "hello world")
	}
}

// Tool roundtrip: model calls a tool, the harness runs it, second
// iteration produces the final reply.
func TestRun_ToolCall_ThenFinalReply(t *testing.T) {
	p := &scriptedProvider{
		name: "fake",
		turns: [][]Delta{
			// Iteration 0: model asks for a tool.
			{
				{Kind: DeltaToolCall, ToolCall: &ToolCall{
					ID: "c1", Name: "echo", ArgumentsJSON: `{"msg":"hi"}`,
				}},
			},
			// Iteration 1: model sees tool result, replies with text.
			{
				{Kind: DeltaText, Text: "done"},
			},
		},
	}
	toolCalled := false
	tools, err := NewToolRunner(&staticToolFn{
		name: "echo",
		fn: func(_ context.Context, args json.RawMessage) (ToolResult, error) {
			toolCalled = true
			return ToolResult{Output: "echoed: " + string(args)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ch, err := Run(context.Background(), RunRequest{
		Provider: p,
		Model:    "fake/m",
		Tools:    tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, ch)

	if !toolCalled {
		t.Error("tool was not called")
	}

	// Confirm we see EventToolStart followed by EventToolEnd with the
	// matching CallID, then a second iteration of EventText.
	var sawStart, sawEnd bool
	for _, e := range events {
		switch e.Kind {
		case EventToolStart:
			if e.ToolCall == nil || e.ToolCall.ID != "c1" {
				t.Errorf("EventToolStart payload: %+v", e.ToolCall)
			}
			sawStart = true
		case EventToolEnd:
			if e.ToolResult == nil || e.ToolResult.CallID != "c1" {
				t.Errorf("EventToolEnd payload: %+v", e.ToolResult)
			}
			if !sawStart {
				t.Error("EventToolEnd before EventToolStart")
			}
			sawEnd = true
		}
	}
	if !sawStart || !sawEnd {
		t.Errorf("missing tool events: start=%v end=%v", sawStart, sawEnd)
	}

	done := lastEvent(events)
	if done.Result.StoppedReason != StopFinish {
		t.Errorf("StoppedReason = %v, want StopFinish", done.Result.StoppedReason)
	}
	if done.Result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", done.Result.Iterations)
	}
	// History should be: assistant(tool_call), tool(result), assistant(text)
	if len(done.Result.History) != 3 {
		t.Fatalf("History len = %d, want 3: %+v", len(done.Result.History), done.Result.History)
	}
	if done.Result.History[0].Role != RoleAssistant || len(done.Result.History[0].ToolCalls) != 1 {
		t.Errorf("History[0] should be assistant with one tool call: %+v", done.Result.History[0])
	}
	if done.Result.History[1].Role != RoleTool || done.Result.History[1].ToolCallID != "c1" {
		t.Errorf("History[1] should be tool result for c1: %+v", done.Result.History[1])
	}
	if done.Result.History[2].Content != "done" {
		t.Errorf("History[2] content = %q, want done", done.Result.History[2].Content)
	}
}

// MaxIterations cap: a model that keeps calling tools forever should
// be stopped, not allowed to loop indefinitely.
func TestRun_MaxIterations_StopsRunawayLoop(t *testing.T) {
	turnWithToolCall := []Delta{
		{Kind: DeltaToolCall, ToolCall: &ToolCall{
			ID: "loop", Name: "ping", ArgumentsJSON: `{}`,
		}},
	}
	p := &scriptedProvider{
		name:  "fake",
		turns: [][]Delta{turnWithToolCall, turnWithToolCall, turnWithToolCall, turnWithToolCall},
	}
	tools, _ := NewToolRunner(&staticToolFn{
		name: "ping",
		fn: func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
			return ToolResult{Output: "pong"}, nil
		},
	})
	ch, err := Run(context.Background(), RunRequest{
		Provider:      p,
		Model:         "fake/m",
		Tools:         tools,
		MaxIterations: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, ch)
	done := lastEvent(events)
	if done.Result.StoppedReason != StopMaxIterations {
		t.Errorf("StoppedReason = %v, want StopMaxIterations", done.Result.StoppedReason)
	}
	if done.Result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", done.Result.Iterations)
	}
}

func TestRun_ProviderSetupError(t *testing.T) {
	sentinel := errors.New("auth missing")
	p := &scriptedProvider{name: "fake", setupErr: sentinel}
	ch, err := Run(context.Background(), RunRequest{
		Provider: p,
		Model:    "fake/m",
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, ch)
	done := lastEvent(events)
	if done.Result.StoppedReason != StopError {
		t.Errorf("StoppedReason = %v, want StopError", done.Result.StoppedReason)
	}
	if !errors.Is(done.Result.Err, sentinel) {
		t.Errorf("Err = %v, want wrap of sentinel", done.Result.Err)
	}
	// EventError should precede EventDone.
	var sawError bool
	for _, e := range events {
		if e.Kind == EventError {
			sawError = true
			if !errors.Is(e.Err, sentinel) {
				t.Errorf("EventError.Err = %v, want wrap of sentinel", e.Err)
			}
		}
	}
	if !sawError {
		t.Error("missing EventError")
	}
}

func TestRun_MidStreamProviderError(t *testing.T) {
	sentinel := errors.New("stream broken")
	p := &scriptedProvider{
		name: "fake",
		turns: [][]Delta{
			{
				{Kind: DeltaText, Text: "partial "},
				{Kind: DeltaError, Err: sentinel},
			},
		},
	}
	ch, err := Run(context.Background(), RunRequest{Provider: p, Model: "fake/m"})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, ch)
	done := lastEvent(events)
	if done.Result.StoppedReason != StopError {
		t.Errorf("StoppedReason = %v, want StopError", done.Result.StoppedReason)
	}
	if !errors.Is(done.Result.Err, sentinel) {
		t.Errorf("Err did not wrap sentinel: %v", done.Result.Err)
	}
}

func TestRun_ContextCancel(t *testing.T) {
	p := &scriptedProvider{
		name:    "fake",
		turns:   [][]Delta{{{Kind: DeltaText, Text: "anything"}}},
		hangFor: 100 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := Run(ctx, RunRequest{Provider: p, Model: "fake/m"})
	if err != nil {
		t.Fatal(err)
	}
	// Cancel before the scripted provider can emit anything.
	cancel()
	events := drainEvents(t, ch)
	done := lastEvent(events)
	if done.Result.StoppedReason != StopCanceled {
		t.Errorf("StoppedReason = %v, want StopCanceled", done.Result.StoppedReason)
	}
	if !errors.Is(done.Result.Err, context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", done.Result.Err)
	}
}

// Tool infra error: Tool.Run returns a Go err. Harness aborts the run
// rather than feeding the err text back to the model.
func TestRun_ToolInfraError_AbortsRun(t *testing.T) {
	p := &scriptedProvider{
		name: "fake",
		turns: [][]Delta{
			{{Kind: DeltaToolCall, ToolCall: &ToolCall{ID: "c1", Name: "broken"}}},
		},
	}
	sentinel := errors.New("service down")
	tools, _ := NewToolRunner(&staticToolFn{
		name: "broken",
		fn: func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
			return ToolResult{}, sentinel
		},
	})
	ch, err := Run(context.Background(), RunRequest{
		Provider: p, Model: "fake/m", Tools: tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, ch)
	done := lastEvent(events)
	if done.Result.StoppedReason != StopError {
		t.Errorf("StoppedReason = %v, want StopError", done.Result.StoppedReason)
	}
	if !errors.Is(done.Result.Err, sentinel) {
		t.Errorf("Err = %v, want wrap of sentinel", done.Result.Err)
	}
}

// Tool domain error (IsError=true) should NOT abort — the harness
// feeds the error message back to the model and continues.
func TestRun_ToolDomainError_ContinuesLoop(t *testing.T) {
	p := &scriptedProvider{
		name: "fake",
		turns: [][]Delta{
			{{Kind: DeltaToolCall, ToolCall: &ToolCall{ID: "c1", Name: "soft"}}},
			{{Kind: DeltaText, Text: "recovered"}},
		},
	}
	tools, _ := NewToolRunner(&staticToolFn{
		name: "soft",
		fn: func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
			return ToolResult{Output: "bad input", IsError: true}, nil
		},
	})
	ch, err := Run(context.Background(), RunRequest{
		Provider: p, Model: "fake/m", Tools: tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, ch)
	done := lastEvent(events)
	if done.Result.StoppedReason != StopFinish {
		t.Errorf("tool-domain error should not abort; StoppedReason = %v", done.Result.StoppedReason)
	}
	// History should include the IsError tool result.
	if len(done.Result.History) != 3 {
		t.Fatalf("History len = %d, want 3", len(done.Result.History))
	}
	if done.Result.History[1].Role != RoleTool || done.Result.History[1].Content != "bad input" {
		t.Errorf("tool result not appended to history: %+v", done.Result.History[1])
	}
}

// Two tools requested in one iteration — both should run (concurrently)
// and the harness should emit EventToolStart for both before EventToolEnd
// for both.
func TestRun_ParallelToolCalls(t *testing.T) {
	p := &scriptedProvider{
		name: "fake",
		turns: [][]Delta{
			{
				{Kind: DeltaToolCall, ToolCall: &ToolCall{ID: "c1", Name: "a"}},
				{Kind: DeltaToolCall, ToolCall: &ToolCall{ID: "c2", Name: "b"}},
			},
			{{Kind: DeltaText, Text: "done"}},
		},
	}
	tools, _ := NewToolRunner(
		&staticToolFn{name: "a", fn: func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
			return ToolResult{Output: "A"}, nil
		}},
		&staticToolFn{name: "b", fn: func(_ context.Context, _ json.RawMessage) (ToolResult, error) {
			return ToolResult{Output: "B"}, nil
		}},
	)
	ch, err := Run(context.Background(), RunRequest{
		Provider: p, Model: "fake/m", Tools: tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, ch)
	startIDs := map[string]bool{}
	endIDs := map[string]bool{}
	for _, e := range events {
		switch e.Kind {
		case EventToolStart:
			startIDs[e.ToolCall.ID] = true
		case EventToolEnd:
			endIDs[e.ToolResult.CallID] = true
		}
	}
	if !startIDs["c1"] || !startIDs["c2"] {
		t.Errorf("missing EventToolStart for c1/c2: %v", startIDs)
	}
	if !endIDs["c1"] || !endIDs["c2"] {
		t.Errorf("missing EventToolEnd for c1/c2: %v", endIDs)
	}
}

// Model calls a tool but no ToolRunner is configured: the run should
// abort with a clear error, not silently drop the call.
func TestRun_ToolCallWithoutRunner_Errors(t *testing.T) {
	p := &scriptedProvider{
		name: "fake",
		turns: [][]Delta{
			{{Kind: DeltaToolCall, ToolCall: &ToolCall{ID: "c1", Name: "x"}}},
		},
	}
	ch, err := Run(context.Background(), RunRequest{
		Provider: p, Model: "fake/m", Tools: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, ch)
	done := lastEvent(events)
	if done.Result.StoppedReason != StopError {
		t.Errorf("StoppedReason = %v, want StopError", done.Result.StoppedReason)
	}
	if !strings.Contains(done.Result.Err.Error(), "Tools is nil") {
		t.Errorf("Err should mention nil Tools: %v", done.Result.Err)
	}
}

// Caller's History slice must not be mutated — defensive copy verified.
func TestRun_DoesNotMutateCallerHistory(t *testing.T) {
	original := []Message{
		{Role: RoleUser, Content: "ping"},
	}
	p := &scriptedProvider{
		name:  "fake",
		turns: [][]Delta{{{Kind: DeltaText, Text: "pong"}}},
	}
	ch, _ := Run(context.Background(), RunRequest{
		Provider: p, Model: "fake/m", History: original,
	})
	_ = drainEvents(t, ch)
	if len(original) != 1 {
		t.Errorf("caller History was mutated: len = %d", len(original))
	}
}

// Capture the Request the provider sees so we can confirm Tools surface
// gets propagated as ToolSpecs.
func TestRun_ToolSpecsForwardedToProvider(t *testing.T) {
	var captured Request
	p := &scriptedProvider{
		name:      "fake",
		captureRq: &captured,
		turns:     [][]Delta{{{Kind: DeltaText, Text: "ok"}}},
	}
	tools, _ := NewToolRunner(&staticToolFn{name: "x", desc: "describes x"})
	ch, _ := Run(context.Background(), RunRequest{
		Provider: p, Model: "fake/m", Tools: tools,
	})
	_ = drainEvents(t, ch)
	if len(captured.Tools) != 1 || captured.Tools[0].Name != "x" {
		t.Errorf("provider should see x in Request.Tools: %+v", captured.Tools)
	}
}

func equalKindSlices(a, b []EventKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
