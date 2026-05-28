package subagent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

func echo(name, text string) Spec {
	m := model.Once(false, func(context.Context, []message.Message, []model.ToolSpec) (*model.Response, error) {
		return &model.Response{Message: message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: text}}}, StopReason: "stop"}, nil
	})
	return Spec{Name: name, Model: m}
}

func TestPool_RunsTasksAndReturnsResults(t *testing.T) {
	p := New(WithMaxConcurrent(2), WithMaxQueued(8))
	p.Register(echo("a", "ra"))
	p.Register(echo("b", "rb"))

	ta, err := p.Submit(context.Background(), "a", "go")
	if err != nil {
		t.Fatalf("Submit a: %v", err)
	}
	tb, _ := p.Submit(context.Background(), "b", "go")

	p.Close()
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	ra, _ := ta.Wait()
	rb, _ := tb.Wait()
	if len(ra.Messages) == 0 || ra.Messages[len(ra.Messages)-1].Text() != "ra" {
		t.Errorf("task a result = %+v", ra)
	}
	if rb.Messages[len(rb.Messages)-1].Text() != "rb" {
		t.Errorf("task b result = %+v", rb)
	}
}

func TestPool_RespectsMaxConcurrent(t *testing.T) {
	const limit = 3
	var inFlight, maxSeen atomic.Int64
	release := make(chan struct{})
	gate := model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		n := inFlight.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return &model.Response{Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}, nil
	})
	p := New(WithMaxConcurrent(limit), WithMaxQueued(100))
	p.Register(Spec{Name: "g", Model: gate})
	for i := 0; i < 10; i++ {
		if _, err := p.Submit(context.Background(), "g", "x"); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	p.Close()
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if maxSeen.Load() > limit {
		t.Fatalf("max concurrent = %d, want <= %d", maxSeen.Load(), limit)
	}
}
