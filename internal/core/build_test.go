package core

import (
	"context"
	"sync"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/gate"
)

type recSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recSink) Record(e audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recSink) snapshot() []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.Event(nil), r.events...)
}

func TestBuildAndStreamAuditsToolAndRun(t *testing.T) {
	rs := &recSink{}
	echo := Once(false, func(_ context.Context, _ []ac.Message, _ []ac.ToolSpec) (*ac.LLMResponse, error) {
		return &ac.LLMResponse{Message: ac.Message{
			Role:    ac.RoleAssistant,
			Content: []ac.ContentBlock{ac.TextBlock("done")},
		}}, nil
	})
	ag := Agent(Config{
		Model: echo,
		Audit: rs,
		Gate:  gate.New(gate.Policy{Audit: rs}), // fail-closed default, no tools called here
	})
	ch, wait := Stream(context.Background(), ag, "hello")
	for range ch {
	}
	sum := wait()
	if sum == nil {
		t.Fatal("expected a run summary")
	}
	var sawRunEnd bool
	for _, e := range rs.snapshot() {
		if e.Kind == audit.KindRunEnd {
			sawRunEnd = true
		}
	}
	if !sawRunEnd {
		t.Fatalf("run_end not audited: %+v", rs.snapshot())
	}
}
