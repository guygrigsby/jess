package message

import (
	"encoding/json"
	"testing"
)

func TestMessage_Text(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "concatenates text blocks only",
			msg: Message{Role: RoleAssistant, Content: []ContentBlock{
				{Kind: BlockText, Text: "hello "},
				{Kind: BlockThinking, Text: "(ignored)"},
				{Kind: BlockText, Text: "world"},
			}},
			want: "hello world",
		},
		{
			name: "no text blocks yields empty",
			msg: Message{Role: RoleTool, Content: []ContentBlock{
				{Kind: BlockToolResult, ToolID: "t1", Result: json.RawMessage(`{}`)},
			}},
			want: "",
		},
		{name: "nil content", msg: Message{Role: RoleUser}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.msg.Text(); got != tt.want {
				t.Errorf("Text() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUserText(t *testing.T) {
	m := UserText("hi")
	if m.Role != RoleUser {
		t.Errorf("Role = %q, want user", m.Role)
	}
	if m.Text() != "hi" {
		t.Errorf("Text() = %q, want hi", m.Text())
	}
}
