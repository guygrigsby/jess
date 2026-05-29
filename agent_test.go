package jess

import (
	"context"
	"strings"
	"testing"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

func TestNew_RequiresModel(t *testing.T) {
	if _, err := New(); err == nil {
		t.Fatal("expected error without WithModel")
	}
	a, err := New(WithModel(testModel()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("expected an Agent")
	}
}

func TestAgent_NewSessionWithHistory_SeedsPriorMessages(t *testing.T) {
	var seen []message.Message
	m := model.Once(false, func(_ context.Context, msgs []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		seen = append([]message.Message(nil), msgs...)
		return &model.Response{Message: message.Message{
			Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "ack"}},
		}, StopReason: "stop"}, nil
	})
	agent, err := New(WithModel(m))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	history := []message.Message{
		{Role: message.RoleUser, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "earlier question"}}},
		{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "earlier answer"}}},
	}
	sess, err := agent.NewSessionWithHistory(history)
	if err != nil {
		t.Fatalf("NewSessionWithHistory: %v", err)
	}
	run, err := sess.Prompt(context.Background(), "follow-up")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	_, _ = run.Wait()

	// The model should have seen the two seeded turns before the new prompt.
	var texts []string
	for _, msg := range seen {
		texts = append(texts, msg.Text())
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "earlier question") || !strings.Contains(joined, "earlier answer") {
		t.Fatalf("seeded history not presented to model; saw: %q", joined)
	}
}
