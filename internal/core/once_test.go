package core

import (
	"context"
	"testing"

	ac "github.com/voocel/agentcore"
)

func TestOnceStreams(t *testing.T) {
	m := Once(false, func(_ context.Context, _ []ac.Message, _ []ac.ToolSpec) (*ac.LLMResponse, error) {
		return &ac.LLMResponse{Message: ac.Message{
			Role:    ac.RoleAssistant,
			Content: []ac.ContentBlock{ac.TextBlock("hi")},
		}}, nil
	})
	if m.SupportsTools() {
		t.Fatal("expected SupportsTools false")
	}
	ch, err := m.GenerateStream(context.Background(), []ac.Message{ac.UserMsg("yo")}, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	var sawDone bool
	var text string
	for ev := range ch {
		if ev.Type == ac.StreamEventDone {
			sawDone = true
			text = ev.Message.TextContent()
		}
	}
	if !sawDone {
		t.Fatal("stream never signaled a done event")
	}
	if text != "hi" {
		t.Fatalf("done event message text = %q, want %q", text, "hi")
	}
}
