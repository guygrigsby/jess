package acl

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

// echoOnce is a model.Model (via model.Once) that returns a fixed assistant
// message, so a real agentcore.Agent can be driven without a network.
func echoOnce(text string) model.Model {
	return model.Once(false, func(_ context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		return &model.Response{
			Message:    message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: text}}},
			StopReason: "stop",
		}, nil
	})
}

func TestNewRuntime_RequiresModel(t *testing.T) {
	if _, err := NewRuntime(Config{}); err == nil {
		t.Fatal("expected error when Model is nil")
	}
	if _, err := NewRuntime(Config{Model: echoOnce("hi")}); err != nil {
		t.Fatalf("NewRuntime with a model: %v", err)
	}
}
