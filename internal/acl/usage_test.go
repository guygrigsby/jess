package acl

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

func TestRun_SummaryUsage_PerRunNotCumulative(t *testing.T) {
	m := streamFn(func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (model.Chunk, error) {
		return model.Chunk{Done: true, StopReason: "stop", Message: message.Message{
			Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "ok"}},
		}, Usage: model.Usage{Input: 10, Output: 5, TotalTokens: 15}}, nil
	})
	rt, err := NewRuntime(Config{Model: m})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	run1, err := rt.Prompt(context.Background(), "a")
	if err != nil {
		t.Fatalf("Prompt 1: %v", err)
	}
	res1, _ := run1.Wait()
	if res1.Summary == nil || res1.Summary.Usage.Input != 10 || res1.Summary.Usage.Output != 5 || res1.Summary.Usage.Total != 15 {
		t.Fatalf("run1 usage = %+v, want Input 10 Output 5 Total 15", res1.Summary)
	}

	run2, err := rt.Prompt(context.Background(), "b")
	if err != nil {
		t.Fatalf("Prompt 2: %v", err)
	}
	res2, _ := run2.Wait()
	if res2.Summary == nil || res2.Summary.Usage.Input != 10 || res2.Summary.Usage.Total != 15 {
		t.Fatalf("run2 usage = %+v, want only this run's tokens (Input 10 Total 15), not cumulative", res2.Summary)
	}
}
