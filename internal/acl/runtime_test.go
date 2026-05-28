package acl

import (
	"context"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/event"
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

func TestRun_WaitReturnsResultAfterDone(t *testing.T) {
	r := newRun()
	r.messages = []message.Message{{Role: message.RoleAssistant}}
	r.summary = &event.RunSummary{Turns: 1, EndReason: "stop"}
	close(r.done)

	res, err := r.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Messages) != 1 || res.Summary == nil || res.Summary.EndReason != "stop" {
		t.Fatalf("result = %+v", res)
	}
}

func TestMessagesFromACAgent(t *testing.T) {
	in := []ac.AgentMessage{
		ac.Message{Role: ac.RoleAssistant, Content: []ac.ContentBlock{ac.TextBlock("hi")}},
	}
	got := messagesFromACAgent(in)
	if len(got) != 1 || got[0].Text() != "hi" {
		t.Fatalf("got %+v", got)
	}
}
