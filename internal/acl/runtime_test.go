package acl

import (
	"context"
	"errors"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

// echoOnce is a model.Model (via model.Once) that returns a fixed assistant
// message, so a real agentcore.Agent can be driven without a network.
func echoOnce(text string) model.Model {
	return model.Once(false, func(_ context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		return &model.Response{
			Message:    message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: text}}},
			StopReason: "stop",
		}, nil
	})
}

func TestNewRuntime_RequiresModel(t *testing.T) {
	if _, err := NewRuntime(Config{}); err == nil {
		t.Fatal("expected error when Model is nil")
	}
	if _, err := NewRuntime(Config{Model: echoOnce("hi")}); err != nil {
		t.Fatalf("NewRuntime with a model: %v", err)
	}
}

func TestRun_WaitReturnsResultAfterDone(t *testing.T) {
	r := newRun()
	r.messages = []message.Message{{Role: message.RoleAssistant}}
	r.summary = &event.RunSummary{Turns: 1, EndReason: "stop"}
	close(r.done)

	res, err := r.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Messages) != 1 || res.Summary == nil || res.Summary.EndReason != "stop" {
		t.Fatalf("result = %+v", res)
	}
}

func TestMessagesFromACAgent(t *testing.T) {
	in := []ac.AgentMessage{
		ac.Message{Role: ac.RoleAssistant, Content: []ac.ContentBlock{ac.TextBlock("hi")}},
	}
	got := messagesFromACAgent(in)
	if len(got) != 1 || got[0].Text() != "hi" {
		t.Fatalf("got %+v", got)
	}
}

func TestRuntime_PromptStreamsAndCompletes(t *testing.T) {
	rt, _ := NewRuntime(Config{Model: echoOnce("hello")})
	run, err := rt.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var kinds []event.EventKind
	for ev := range run.Events() {
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) < 2 || kinds[0] != event.KindRunStart || kinds[len(kinds)-1] != event.KindRunEnd {
		t.Fatalf("event kinds = %v", kinds)
	}
	res, err := run.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Summary == nil {
		t.Fatal("expected a run summary")
	}
}

func TestRuntime_SecondPromptWhileRunningErrors(t *testing.T) {
	release := make(chan struct{})
	blocking := model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &model.Response{Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}, nil
	})
	rt, _ := NewRuntime(Config{Model: blocking})
	run, err := rt.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("first Prompt: %v", err)
	}
	if _, err := rt.Prompt(context.Background(), "again"); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("second Prompt err = %v, want ErrRunInProgress", err)
	}
	close(release)
	if _, err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestRuntime_AbortStopsRun(t *testing.T) {
	blocking := model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		<-ctx.Done() // never returns until aborted
		return nil, ctx.Err()
	})
	rt, _ := NewRuntime(Config{Model: blocking})
	run, err := rt.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	rt.Abort()
	for range run.Events() { // must close (run ends) without hanging
	}
	res, _ := run.Wait()
	if res.Summary != nil {
		t.Logf("end reason after abort: %q", res.Summary.EndReason) // accept aborted/error
	}
}

func TestRuntime_SteerFollowUpDoNotPanic(t *testing.T) {
	rt, _ := NewRuntime(Config{Model: echoOnce("x")})
	rt.Steer(message.UserText("steer"))
	rt.FollowUp(message.UserText("later"))
}
