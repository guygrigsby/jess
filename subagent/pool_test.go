package subagent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guygrigsby/jess/event"
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

func TestPool_MergesTaggedEvents(t *testing.T) {
	p := New(WithMaxConcurrent(2))
	p.Register(echo("a", "ra"))

	task, _ := p.Submit(context.Background(), "a", "go")
	p.Close()

	var sawRunEndForTask bool
	for ev := range p.Events() {
		if len(ev.AgentPath) == 0 {
			t.Errorf("event missing AgentPath: %+v", ev)
		}
		if ev.Kind == event.KindRunEnd && ev.AgentPath[len(ev.AgentPath)-1] == task.AgentPath()[len(task.AgentPath())-1] {
			sawRunEndForTask = true
		}
	}
	if !sawRunEndForTask {
		t.Error("did not see a tagged run_end for the task")
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestPool_UnknownAgent(t *testing.T) {
	p := New()
	defer func() { p.Close(); _ = p.Wait() }()
	if _, err := p.Submit(context.Background(), "nope", "x"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("err = %v, want ErrUnknownAgent", err)
	}
}

func TestPool_MaxDepth(t *testing.T) {
	p := New(WithMaxDepth(2))
	p.Register(echo("a", "r"))
	defer func() { p.Close(); _ = p.Wait() }()
	if _, err := p.Submit(context.Background(), "a", "x", "p1/0001", "p2/0001"); !errors.Is(err, ErrMaxDepth) {
		t.Fatalf("err = %v, want ErrMaxDepth", err)
	}
}

func TestPool_SubmitBlocksWhenQueueFullThenCtxCancels(t *testing.T) {
	release := make(chan struct{})
	block := model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return &model.Response{Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}, nil
	})
	p := New(WithMaxConcurrent(1), WithMaxQueued(1))
	p.Register(Spec{Name: "b", Model: block})
	_, _ = p.Submit(context.Background(), "b", "1") // occupies the worker
	_, _ = p.Submit(context.Background(), "b", "2") // fills the queue (cap 1)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := p.Submit(ctx, "b", "3") // must block (queue full) then ctx-cancel
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	close(release)
	p.Close()
	_ = p.Wait()
}

func TestPool_CancelAbortsInFlight(t *testing.T) {
	started := make(chan struct{})
	block := model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	p := New(WithMaxConcurrent(1))
	p.Register(Spec{Name: "b", Model: block})
	task, _ := p.Submit(context.Background(), "b", "x")
	<-started
	p.Cancel()
	for range p.Events() {
	}
	_ = p.Wait()
	if _, err := task.Wait(); err == nil {
		t.Log("task completed despite cancel (acceptable if it finished first)")
	}
}

// Concurrent Submit and Close must not panic (send on closed channel) — the
// airtight guarantee for the pool's lifecycle.
func TestPool_ConcurrentSubmitAndCloseNoPanic(t *testing.T) {
	p := New(WithMaxConcurrent(4), WithMaxQueued(50))
	p.Register(echo("a", "r"))

	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Submit(context.Background(), "a", "x") // may succeed or get ErrPoolClosed
		}()
	}
	go p.Close() // race Close against the submits
	wg.Wait()
	p.Close() // idempotent
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestPool_SubmitToForwardsToSink(t *testing.T) {
	p := New(WithMaxConcurrent(2))
	p.Register(echo("a", "ra"))
	sink := event.NewStream(64)

	task, err := p.SubmitTo(context.Background(), sink, "a", "go")
	if err != nil {
		t.Fatalf("SubmitTo: %v", err)
	}

	done := make(chan struct{})
	var sawTagged bool
	go func() {
		for ev := range sink.Events() {
			if len(ev.AgentPath) > 0 {
				sawTagged = true
			}
		}
		close(done)
	}()
	p.Close()
	if _, err := task.Wait(); err != nil {
		t.Fatalf("task: %v", err)
	}
	sink.Close() // caller owns the sink; close it to end the consumer
	<-done
	if !sawTagged {
		t.Error("expected tagged events on the provided sink")
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
