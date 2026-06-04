package jess_test

import (
	"context"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/subagent"
)

// WithSubagents must build a usable agent: the parent's model drives the run and
// the delegating "subagent" tool is wired in (a subagent inheriting the parent
// model). This is a smoke test of the wiring, not of delegation behavior.
func TestWithSubagentsBuildsAgent(t *testing.T) {
	echo := jess.Once(false, func(context.Context, []ac.Message, []ac.ToolSpec) (*ac.LLMResponse, error) {
		return &ac.LLMResponse{Message: ac.Message{
			Role:    ac.RoleAssistant,
			Content: []ac.ContentBlock{ac.TextBlock("ok")},
		}}, nil
	})
	agent := jess.New(
		jess.WithModel(echo),
		jess.WithLedger(ledger.DiscardSink{}),
		jess.WithSubagents(subagent.Spec{Name: "research"}), // inherits parent model
	)
	if agent == nil {
		t.Fatal("expected a non-nil agent")
	}
	ch, wait := jess.Stream(context.Background(), agent, "hi")
	for range ch {
	}
	if wait() == nil {
		t.Fatal("expected a run summary")
	}
}
