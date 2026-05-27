package acl

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/tool"
	ac "github.com/voocel/agentcore"
)

func TestMessagesToAC_TextAndToolCall(t *testing.T) {
	in := []message.Message{{
		Role: message.RoleAssistant,
		Content: []message.ContentBlock{
			{Kind: message.BlockText, Text: "calling"},
			{Kind: message.BlockToolCall, ToolID: "c1", ToolName: "search", Args: []byte(`{"q":"x"}`)},
		},
	}}
	got := messagesToAC(in)
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %d", len(got))
	}
	m := got[0]
	if m.Role != ac.RoleAssistant || len(m.Content) != 2 {
		t.Fatalf("role/content wrong: %+v", m)
	}
	if m.Content[0].Type != ac.ContentText || m.Content[0].Text != "calling" {
		t.Errorf("block0 = %+v", m.Content[0])
	}
	if m.Content[1].Type != ac.ContentToolCall || m.Content[1].ToolCall == nil ||
		m.Content[1].ToolCall.ID != "c1" || m.Content[1].ToolCall.Name != "search" {
		t.Errorf("block1 = %+v", m.Content[1])
	}
}

func TestMessagesToAC_ToolResultBecomesToolMessage(t *testing.T) {
	in := []message.Message{{
		Role: message.RoleTool,
		Content: []message.ContentBlock{
			{Kind: message.BlockToolResult, ToolID: "c1", Result: []byte(`{"ok":true}`), IsError: false},
			{Kind: message.BlockToolResult, ToolID: "c2", Result: []byte(`"boom"`), IsError: true},
		},
	}}
	got := messagesToAC(in)
	if len(got) != 2 {
		t.Fatalf("two tool-result blocks -> two ac messages, got %d", len(got))
	}
	if got[0].Role != ac.RoleTool || got[0].Metadata["tool_call_id"] != "c1" || got[0].Metadata["is_error"] != false {
		t.Errorf("msg0 = %+v", got[0])
	}
	if got[1].Metadata["tool_call_id"] != "c2" || got[1].Metadata["is_error"] != true {
		t.Errorf("msg1 = %+v", got[1])
	}
}

func TestMessagesToAC_SkipsStrayToolResultInNonToolMessage(t *testing.T) {
	in := []message.Message{{
		Role: message.RoleAssistant,
		Content: []message.ContentBlock{
			{Kind: message.BlockText, Text: "hi"},
			{Kind: message.BlockToolResult, ToolID: "c1", Result: []byte(`{}`)},
		},
	}}
	got := messagesToAC(in)
	if len(got) != 1 || len(got[0].Content) != 1 {
		t.Fatalf("want 1 message with 1 block (stray tool result skipped), got %+v", got)
	}
	if got[0].Content[0].Type != ac.ContentText || got[0].Content[0].Text != "hi" {
		t.Errorf("block = %+v", got[0].Content[0])
	}
}

func TestMessageFromAC_AssistantBlocks(t *testing.T) {
	in := ac.Message{Role: ac.RoleAssistant, Content: []ac.ContentBlock{
		ac.TextBlock("hi"),
		ac.ThinkingBlock("hmm"),
		ac.ToolCallBlock(ac.ToolCall{ID: "c1", Name: "search", Args: []byte(`{"q":"x"}`)}),
	}}
	got := messageFromAC(in)
	if got.Role != message.RoleAssistant || len(got.Content) != 3 {
		t.Fatalf("got %+v", got)
	}
	if got.Content[0].Kind != message.BlockText || got.Content[0].Text != "hi" {
		t.Errorf("b0 = %+v", got.Content[0])
	}
	if got.Content[1].Kind != message.BlockThinking || got.Content[1].Text != "hmm" {
		t.Errorf("b1 = %+v", got.Content[1])
	}
	if got.Content[2].Kind != message.BlockToolCall || got.Content[2].ToolID != "c1" ||
		got.Content[2].ToolName != "search" {
		t.Errorf("b2 = %+v", got.Content[2])
	}
}

func TestMessageFromAC_ToolResult(t *testing.T) {
	in := ac.ToolResultMsg("c1", []byte(`{"ok":true}`), true)
	got := messageFromAC(in)
	if got.Role != message.RoleTool || len(got.Content) != 1 {
		t.Fatalf("got %+v", got)
	}
	b := got.Content[0]
	if b.Kind != message.BlockToolResult || b.ToolID != "c1" || !b.IsError || string(b.Result) != `{"ok":true}` {
		t.Errorf("result block = %+v", b)
	}
}

func TestRoleRoundTrip(t *testing.T) {
	tests := []struct {
		jess message.Role
		acr  ac.Role
	}{
		{message.RoleUser, ac.RoleUser},
		{message.RoleAssistant, ac.RoleAssistant},
		{message.RoleSystem, ac.RoleSystem},
		{message.RoleTool, ac.RoleTool},
	}
	for _, tt := range tests {
		t.Run(string(tt.jess), func(t *testing.T) {
			if got := roleToAC(tt.jess); got != tt.acr {
				t.Errorf("roleToAC(%q) = %q, want %q", tt.jess, got, tt.acr)
			}
			if got := roleFromAC(tt.acr); got != tt.jess {
				t.Errorf("roleFromAC(%q) = %q, want %q", tt.acr, got, tt.jess)
			}
		})
	}
}

type stubJessTool struct{}

func (stubJessTool) Name() string           { return "echo" }
func (stubJessTool) Description() string    { return "d" }
func (stubJessTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (stubJessTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

func TestWrapTool(t *testing.T) {
	var acTool ac.Tool = WrapTool(stubJessTool{})
	if acTool.Name() != "echo" || acTool.Description() != "d" {
		t.Fatalf("name/desc wrong: %q/%q", acTool.Name(), acTool.Description())
	}
	if acTool.Schema()["type"] != "object" {
		t.Errorf("schema = %v", acTool.Schema())
	}
	got, err := acTool.Execute(context.Background(), json.RawMessage(`{"a":1}`))
	if err != nil || string(got) != `{"a":1}` {
		t.Errorf("execute = %s, %v", got, err)
	}
}

func TestWrapTools(t *testing.T) {
	got := WrapTools([]tool.Tool{stubJessTool{}, stubJessTool{}})
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
}

func TestEventFromAC(t *testing.T) {
	tests := []struct {
		name   string
		in     ac.Event
		wantOK bool
		want   event.EventKind
	}{
		{"agent start", ac.Event{Type: ac.EventAgentStart}, true, event.KindRunStart},
		{"turn start", ac.Event{Type: ac.EventTurnStart}, true, event.KindTurnStart},
		{"delta", ac.Event{Type: ac.EventMessageUpdate, Delta: "hi"}, true, event.KindMessageDelta},
		{"tool start", ac.Event{Type: ac.EventToolExecStart, Tool: "search"}, true, event.KindToolStart},
		{"tool end", ac.Event{Type: ac.EventToolExecEnd, Tool: "search", IsError: true}, true, event.KindToolEnd},
		{"turn end", ac.Event{Type: ac.EventTurnEnd}, true, event.KindTurnEnd},
		{"agent end", ac.Event{Type: ac.EventAgentEnd, Summary: &ac.RunSummary{TurnCount: 2, ToolCalls: 1, EndReason: ac.EndReasonStop}}, true, event.KindRunEnd},
		{"error", ac.Event{Type: ac.EventError}, true, event.KindError},
		{"message start dropped", ac.Event{Type: ac.EventMessageStart}, false, ""},
		{"retry dropped", ac.Event{Type: ac.EventRetry}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := EventFromAC(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.Kind != tt.want {
				t.Errorf("Kind = %q, want %q", got.Kind, tt.want)
			}
		})
	}
}

func TestEventFromAC_RunEndSummary(t *testing.T) {
	got, ok := EventFromAC(ac.Event{Type: ac.EventAgentEnd, Summary: &ac.RunSummary{TurnCount: 3, ToolCalls: 2, EndReason: ac.EndReasonMaxTurns}})
	if !ok || got.Summary == nil {
		t.Fatalf("ok=%v summary=%v", ok, got.Summary)
	}
	if got.Summary.Turns != 3 || got.Summary.ToolCalls != 2 || got.Summary.EndReason != "max_turns" {
		t.Errorf("summary = %+v", got.Summary)
	}
}
