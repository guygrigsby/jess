package jess

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

func testModel() model.Model {
	return model.Once(false, func(context.Context, []message.Message, []model.ToolSpec) (*model.Response, error) {
		return &model.Response{Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}, nil
	})
}

func TestOptions_Apply(t *testing.T) {
	var o options
	for _, opt := range []Option{
		WithModel(testModel()),
		WithAgentID("agent-1"),
		WithSystemPrompt("be terse"),
		WithMaxTurns(5),
		WithMemory(memory.NewInMemoryStore(), memory.NewSimpleRecaller()),
	} {
		opt(&o)
	}
	if o.model == nil || o.agentID != "agent-1" || o.systemPrompt != "be terse" || o.maxTurns != 5 {
		t.Fatalf("options not applied: %+v", o)
	}
	if o.store == nil || o.recaller == nil {
		t.Error("WithMemory did not set store/recaller")
	}
}
