package jess_test

import (
	"context"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/audit"
)

func TestNewAndStream(t *testing.T) {
	echo := jess.Once(false, func(context.Context, []ac.Message, []ac.ToolSpec) (*ac.LLMResponse, error) {
		return &ac.LLMResponse{Message: ac.Message{
			Role:    ac.RoleAssistant,
			Content: []ac.ContentBlock{ac.TextBlock("hi")},
		}}, nil
	})
	agent := jess.New(jess.WithModel(echo), jess.WithAudit(audit.DiscardSink{}))
	ch, wait := jess.Stream(context.Background(), agent, "yo")
	for range ch {
	}
	if wait() == nil {
		t.Fatal("expected summary")
	}
}
