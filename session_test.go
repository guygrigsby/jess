package jess

import (
	"context"
	"errors"
	"testing"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
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

func blockingModel(release <-chan struct{}) model.Model {
	return model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &model.Response{Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}, nil
	})
}

func TestSession_SecondPromptErrors(t *testing.T) {
	release := make(chan struct{})
	a, _ := New(WithModel(blockingModel(release)))
	sess, _ := a.NewSession()
	run, err := sess.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("first Prompt: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), "again"); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("second Prompt err = %v, want jess.ErrRunInProgress", err)
	}
	close(release)
	if _, err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSession_SteerFollowUpAbortDoNotPanic(t *testing.T) {
	a, _ := New(WithModel(testModel()))
	sess, _ := a.NewSession()
	sess.Steer(message.UserText("steer"))
	sess.FollowUp(message.UserText("later"))
	sess.Abort()
}
