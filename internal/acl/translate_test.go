package acl

import (
	"testing"

	"github.com/guygrigsby/jess/message"
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
