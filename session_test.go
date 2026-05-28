package jess

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/event"
)

func TestSession_PromptEndToEnd(t *testing.T) {
	a, _ := New(WithModel(testModel()))
	sess, err := a.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	run, err := sess.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var sawStart, sawEnd bool
	for ev := range run.Events() {
		switch ev.Kind {
		case event.KindRunStart:
			sawStart = true
		case event.KindRunEnd:
			sawEnd = true
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("missing run_start/run_end (start=%v end=%v)", sawStart, sawEnd)
	}
	if _, err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestAgent_PromptUsesDefaultSession(t *testing.T) {
	a, _ := New(WithModel(testModel()))
	run, err := a.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Agent.Prompt: %v", err)
	}
	for range run.Events() {
	}
	if _, err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
