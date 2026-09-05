package core

import (
	"context"
	"sync"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/gate"
)

type recSink struct {
	mu     sync.Mutex
	events []ledger.Event
}

func (r *recSink) Record(e ledger.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recSink) snapshot() []ledger.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ledger.Event(nil), r.events...)
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
		if e.Kind == ledger.KindRunEnd {
			sawRunEnd = true
		}
	}
	if !sawRunEnd {
		t.Fatalf("run_end not audited: %+v", rs.snapshot())
	}
}

// TestStreamRecordsRequestHeadAndRunEnd verifies that Stream emits a
// KindRequest event whose RunID matches the KindRunEnd RunID, and that both
// are non-empty, giving every ledger entry a correlated run identifier.
func TestStreamRecordsRequestHeadAndRunEnd(t *testing.T) {
	sink := &recSink{}
	echo := Once(false, func(_ context.Context, _ []ac.Message, _ []ac.ToolSpec) (*ac.LLMResponse, error) {
		return &ac.LLMResponse{Message: ac.Message{
			Role:    ac.RoleAssistant,
			Content: []ac.ContentBlock{ac.TextBlock("ack")},
		}}, nil
	})
	ag := Agent(Config{
		Model: echo,
		Audit: sink,
	})
	ch, wait := Stream(context.Background(), ag, "restart nginx")
	for range ch {
	}
	wait()

	events := sink.snapshot()
	var reqRunID, endRunID string
	for _, e := range events {
		switch e.Kind {
		case ledger.KindRequest:
			reqRunID = e.RunID
		case ledger.KindRunEnd:
			endRunID = e.RunID
		}
	}
	if reqRunID == "" {
		t.Fatalf("no KindRequest event recorded; events: %+v", events)
	}
	if endRunID == "" {
		t.Fatalf("no KindRunEnd with RunID; events: %+v", events)
	}
	if reqRunID != endRunID {
		t.Fatalf("RunID mismatch: KindRequest=%q KindRunEnd=%q", reqRunID, endRunID)
	}
}

// TestReleaseAgentRemovesRegistryEntry pins the registry lifecycle the root
// package's ReleaseAgent depends on: Agent registers the audit wiring keyed
// by the *ac.Agent pointer, and ReleaseAgent must remove it, or every agent a
// long-lived caller builds (one per job, one per attempt) leaks that entry
// forever.
func TestReleaseAgentRemovesRegistryEntry(t *testing.T) {
	echo := Once(false, func(_ context.Context, _ []ac.Message, _ []ac.ToolSpec) (*ac.LLMResponse, error) {
		return &ac.LLMResponse{Message: ac.Message{Role: ac.RoleAssistant, Content: []ac.ContentBlock{ac.TextBlock("done")}}}, nil
	})
	ag := Agent(Config{Model: echo})
	if sinkFor(ag) == nil {
		t.Fatal("Agent did not register the agent")
	}
	ReleaseAgent(ag)
	if sinkFor(ag) != nil {
		t.Fatal("ReleaseAgent left the registry entry in place")
	}
	// Calling it again must not panic.
	ReleaseAgent(ag)
}
