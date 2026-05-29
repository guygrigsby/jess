package acl

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	"github.com/guygrigsby/jess/tool"
)

// sourceProbeTool records the memory.Source it observed in its Execute ctx.
type sourceProbeTool struct{ saw chan memory.Source }

func (sourceProbeTool) Name() string           { return "probe" }
func (sourceProbeTool) Description() string    { return "probe" }
func (sourceProbeTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (p sourceProbeTool) Execute(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	p.saw <- memory.SourceFromContext(ctx)
	return json.RawMessage(`{"ok":true}`), nil
}

// toolCallThenStop returns a model that calls "probe" on the first turn then stops.
func toolCallThenStop() model.Model {
	var calls atomic.Int32
	return streamFn(func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (model.Chunk, error) {
		if calls.Add(1) == 1 {
			return model.Chunk{Done: true, StopReason: "tool_use", Message: message.Message{
				Role:    message.RoleAssistant,
				Content: []message.ContentBlock{{Kind: message.BlockToolCall, ToolID: "c1", ToolName: "probe", Args: []byte(`{}`)}},
			}}, nil
		}
		return model.Chunk{Done: true, StopReason: "stop", Message: message.Message{
			Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "done"}},
		}}, nil
	})
}

func TestRuntime_InjectsMemorySourceIntoToolCtx(t *testing.T) {
	probe := sourceProbeTool{saw: make(chan memory.Source, 1)}
	rt, err := NewRuntime(Config{Model: toolCallThenStop(), Tools: []tool.Tool{probe}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	src := memory.Source{SessionID: "agent:main:web", MessageID: "run_123", Tool: "remember", Reason: "model decided"}
	run, err := rt.Prompt(memory.WithSource(context.Background(), src), "go")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	_, _ = run.Wait()
	select {
	case got := <-probe.saw:
		if got != src {
			t.Errorf("tool saw Source %+v, want %+v", got, src)
		}
	default:
		t.Fatal("tool was never executed")
	}
}

// blockingTool blocks in Execute until its ctx is cancelled, recording that.
type blockingTool struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (blockingTool) Name() string           { return "probe" }
func (blockingTool) Description() string    { return "probe" }
func (blockingTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (b blockingTool) Execute(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	close(b.cancelled)
	return nil, ctx.Err()
}

// Abort must still cancel a blocking tool: injecting the Source must NOT replace
// agentcore's cancellation ctx.
func TestRuntime_AbortCancelsToolDespiteSourceInject(t *testing.T) {
	tl := blockingTool{started: make(chan struct{}, 1), cancelled: make(chan struct{})}
	rt, err := NewRuntime(Config{Model: toolCallThenStop(), Tools: []tool.Tool{tl}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	src := memory.Source{SessionID: "s", MessageID: "m"}
	run, err := rt.Prompt(memory.WithSource(context.Background(), src), "go")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	select {
	case <-tl.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool never started")
	}
	rt.Abort()
	select {
	case <-tl.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Abort did not cancel the tool ctx; Source inject must not replace agentcore's ctx")
	}
	_, _ = run.Wait()
}

// toolCallPerRun calls "probe" once at the start of each run (when the latest
// message is the user's prompt) and stops after the tool result — so it fires on
// every run, not just the first (unlike toolCallThenStop).
func toolCallPerRun() model.Model {
	return streamFn(func(_ context.Context, msgs []message.Message, _ []model.ToolSpec) (model.Chunk, error) {
		if len(msgs) > 0 && msgs[len(msgs)-1].Role == message.RoleUser {
			return model.Chunk{Done: true, StopReason: "tool_use", Message: message.Message{
				Role:    message.RoleAssistant,
				Content: []message.ContentBlock{{Kind: message.BlockToolCall, ToolID: "c1", ToolName: "probe", Args: []byte(`{}`)}},
			}}, nil
		}
		return model.Chunk{Done: true, StopReason: "stop", Message: message.Message{
			Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "done"}},
		}}, nil
	})
}

// Source must not leak across runs: a run started without a Source must see the
// zero Source in its tools, not the prior run's (curSource is cleared at run end).
func TestRuntime_SourceClearedBetweenRuns(t *testing.T) {
	probe := sourceProbeTool{saw: make(chan memory.Source, 2)}
	rt, err := NewRuntime(Config{Model: toolCallPerRun(), Tools: []tool.Tool{probe}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	src := memory.Source{SessionID: "s1", MessageID: "m1"}
	run1, err := rt.Prompt(memory.WithSource(context.Background(), src), "go")
	if err != nil {
		t.Fatalf("Prompt 1: %v", err)
	}
	_, _ = run1.Wait()
	if got := <-probe.saw; got != src {
		t.Fatalf("run1 source = %+v, want %+v", got, src)
	}

	run2, err := rt.Prompt(context.Background(), "go2") // no Source
	if err != nil {
		t.Fatalf("Prompt 2: %v", err)
	}
	_, _ = run2.Wait()
	if got := <-probe.saw; got != (memory.Source{}) {
		t.Fatalf("run2 saw stale Source %+v; curSource not cleared between runs", got)
	}
}
