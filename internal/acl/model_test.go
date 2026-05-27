package acl

import (
	"testing"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	ac "github.com/voocel/agentcore"
)

func TestToolSpecFromAC(t *testing.T) {
	in := ac.ToolSpec{Name: "search", Description: "d", Parameters: map[string]any{"type": "object"}}
	got := toolSpecFromAC(in)
	if got.Name != "search" || got.Description != "d" || got.Schema["type"] != "object" {
		t.Fatalf("got %+v", got)
	}
}

func TestToolSpecFromAC_NonMapParameters(t *testing.T) {
	got := toolSpecFromAC(ac.ToolSpec{Name: "x", Parameters: "not-a-map"})
	if got.Schema != nil {
		t.Errorf("non-map Parameters should yield nil Schema, got %v", got.Schema)
	}
}

func TestDeltaEventType(t *testing.T) {
	tests := []struct {
		in   event.DeltaKind
		want ac.StreamEventType
	}{
		{event.DeltaText, ac.StreamEventTextDelta},
		{event.DeltaThinking, ac.StreamEventThinkingDelta},
		{event.DeltaToolCall, ac.StreamEventToolCallDelta},
	}
	for _, tt := range tests {
		if got := deltaEventType(tt.in); got != tt.want {
			t.Errorf("deltaEventType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAssistantMessageToAC(t *testing.T) {
	jm := message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "hi"}}}
	got := assistantMessageToAC(jm, model.Usage{Input: 1, Output: 2, TotalTokens: 3}, "stop")
	if got.Role != ac.RoleAssistant || len(got.Content) != 1 || got.Content[0].Text != "hi" {
		t.Fatalf("message = %+v", got)
	}
	if got.StopReason != ac.StopReason("stop") || got.Usage == nil || got.Usage.TotalTokens != 3 {
		t.Errorf("stop/usage = %v / %+v", got.StopReason, got.Usage)
	}
}
