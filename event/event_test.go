package event

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestEvent_Shape(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
		want EventKind
	}{
		{"delta", Event{Kind: KindMessageDelta, Delta: "hi"}, KindMessageDelta},
		{"tool end error", Event{Kind: KindToolEnd, Tool: "x", IsError: true, Result: json.RawMessage(`{}`)}, KindToolEnd},
		{"error carries err", Event{Kind: KindError, Err: errors.New("boom")}, KindError},
		{
			name: "run end carries summary and agent path",
			ev:   Event{Kind: KindRunEnd, AgentPath: []string{"research/0007"}, Summary: &RunSummary{Turns: 3, ToolCalls: 2, EndReason: "stop"}},
			want: KindRunEnd,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ev.Kind != tt.want {
				t.Errorf("Kind = %q, want %q", tt.ev.Kind, tt.want)
			}
		})
	}
}

func TestEvent_IsSubagent(t *testing.T) {
	if (Event{}).IsSubagent() {
		t.Error("empty AgentPath should be root, not subagent")
	}
	if !(Event{AgentPath: []string{"research/0007"}}).IsSubagent() {
		t.Error("non-empty AgentPath should be a subagent event")
	}
}
