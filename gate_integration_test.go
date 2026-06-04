package jess_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/gate"
	"github.com/guygrigsby/jess/ledger"
)

// restartTool wants to restart a service; NOT marked Safe -> must be gated.
type restartTool struct{ ran bool }

func (t *restartTool) Name() string          { return "restart_service" }
func (t *restartTool) Description() string   { return "restart a named service" }
func (t *restartTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *restartTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	t.ran = true
	return json.RawMessage(`"restarted"`), nil
}

// callOnceModel returns a jess.Once model that emits exactly one tool call to
// toolName on its first Generate, then returns plain text on the next call so
// the loop terminates.
func callOnceModel(toolName string) ac.ChatModel {
	var calls int
	return jess.Once(true, func(_ context.Context, _ []ac.Message, _ []ac.ToolSpec) (*ac.LLMResponse, error) {
		calls++
		if calls == 1 {
			// First turn: emit a tool-call assistant message.
			return &ac.LLMResponse{
				Message: ac.Message{
					Role: ac.RoleAssistant,
					Content: []ac.ContentBlock{
						ac.ToolCallBlock(ac.ToolCall{
							ID:   fmt.Sprintf("call_%d", time.Now().UnixNano()),
							Name: toolName,
							Args: json.RawMessage(`{}`),
						}),
					},
					StopReason: ac.StopReasonToolUse,
				},
			}, nil
		}
		// Second turn: plain text terminates the loop.
		return &ac.LLMResponse{
			Message: ac.Message{
				Role:       ac.RoleAssistant,
				Content:    []ac.ContentBlock{ac.TextBlock("done")},
				StopReason: ac.StopReasonStop,
			},
		}, nil
	})
}

// TestFailClosedBlocksUnsafeToolWhenNoApprover proves that, with no approver
// wired, the default fail-closed gate denies a tool that does not implement
// SafeTool. The tool's Execute must never run.
func TestFailClosedBlocksUnsafeToolWhenNoApprover(t *testing.T) {
	rt := &restartTool{}
	agent := jess.New(
		jess.WithModel(callOnceModel("restart_service")),
		jess.WithTools(rt),
		jess.WithLedger(ledger.DiscardSink{}),
		// no WithApprover -> fail-closed
	)

	ch, wait := jess.Stream(context.Background(), agent, "restart nginx")
	for range ch {
	}
	_ = wait()

	if rt.ran {
		t.Fatal("fail-closed gate must block an unmarked tool with no approver")
	}
}

// TestWithApproverAllowsUnsafeTool proves that an approver returning allow=true
// lets the tool through and Execute runs. The ledger must be durable: the audit
// middleware enforces "no record, no action" for non-safe tools regardless of
// the gate, so a SQLite (DurableSink) ledger is required for the tool to run.
func TestWithApproverAllowsUnsafeTool(t *testing.T) {
	led, err := ledger.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer func() { _ = led.Close() }()

	rt := &restartTool{}
	approver := gate.Approver(func(_ context.Context, _ gate.Request) (bool, string) {
		return true, "approved in test"
	})
	agent := jess.New(
		jess.WithModel(callOnceModel("restart_service")),
		jess.WithTools(rt),
		jess.WithApprover(approver),
		jess.WithLedger(led),
	)

	ch, wait := jess.Stream(context.Background(), agent, "restart nginx")
	for range ch {
	}
	_ = wait()

	if !rt.ran {
		t.Fatal("approver returned allow=true but tool did not run")
	}
}

// TestApproverAllowsButNonDurableLedgerDenies proves the gate-independent
// enforcement: even with an approver returning allow=true, a non-safe tool
// cannot run when the ledger is not durable. No durable record, no action.
func TestApproverAllowsButNonDurableLedgerDenies(t *testing.T) {
	rt := &restartTool{}
	approver := gate.Approver(func(_ context.Context, _ gate.Request) (bool, string) {
		return true, "approved in test"
	})
	agent := jess.New(
		jess.WithModel(callOnceModel("restart_service")),
		jess.WithTools(rt),
		jess.WithApprover(approver),
		jess.WithLedger(ledger.DiscardSink{}), // not a DurableSink
	)

	ch, wait := jess.Stream(context.Background(), agent, "restart nginx")
	for range ch {
	}
	_ = wait()

	if rt.ran {
		t.Fatal("non-safe tool ran with a non-durable ledger; enforcement bypassed")
	}
}
