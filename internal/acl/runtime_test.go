package acl

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	"github.com/guygrigsby/jess/tool"
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
	// Mirror the real run invariant: the stream is closed when done is. Wait
	// drains the stream before returning, so an open stream would block it.
	r.stream.Close()
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

// chattyModel emits n text-delta chunks then a Done chunk, producing more
// events than the stream buffer to exercise backpressure handling.
type chattyModel struct{ n int }

func (chattyModel) SupportsTools() bool { return false }
func (m chattyModel) Stream(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (<-chan model.Chunk, error) {
	ch := make(chan model.Chunk)
	go func() {
		defer close(ch)
		for i := 0; i < m.n; i++ {
			select {
			case <-ctx.Done():
				return
			case ch <- model.Chunk{Delta: "x", DeltaKind: event.DeltaText}:
			}
		}
		ch <- model.Chunk{Done: true, Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}
	}()
	return ch, nil
}

// A Wait-only caller must not deadlock even when the run emits more events than
// the stream buffer (Wait drains internally).
func TestRun_WaitWithoutDrainingDoesNotHang(t *testing.T) {
	rt, _ := NewRuntime(Config{Model: chattyModel{n: 300}}) // > streamBuffer (128)
	run, err := rt.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	done := make(chan struct{})
	go func() { _, _ = run.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait hung without draining Events (backpressure deadlock)")
	}
}

// Wait must report a run error even when the caller never inspects Events.
func TestRun_WaitCapturesError(t *testing.T) {
	errModel := model.Once(false, func(context.Context, []message.Message, []model.ToolSpec) (*model.Response, error) {
		return nil, errors.New("model boom")
	})
	rt, _ := NewRuntime(Config{Model: errModel})
	run, err := rt.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if _, werr := run.Wait(); werr == nil {
		t.Fatal("Wait should report the run error, got nil")
	}
}

// streamFnModel adapts a per-Stream-call function to a model.Model. Unlike
// model.Once it can return a different chunk on each turn's Stream call.
type streamFnModel struct {
	fn func(context.Context, []message.Message, []model.ToolSpec) (model.Chunk, error)
}

func streamFn(fn func(context.Context, []message.Message, []model.ToolSpec) (model.Chunk, error)) model.Model {
	return streamFnModel{fn: fn}
}
func (streamFnModel) SupportsTools() bool { return true }
func (m streamFnModel) Stream(ctx context.Context, msgs []message.Message, tools []model.ToolSpec) (<-chan model.Chunk, error) {
	ch := make(chan model.Chunk, 1)
	go func() {
		defer close(ch)
		c, err := m.fn(ctx, msgs, tools)
		if err != nil {
			ch <- model.Chunk{Err: err}
			return
		}
		ch <- c
	}()
	return ch, nil
}

// streamProbeTool records whether a run stream was injected into its ctx.
type streamProbeTool struct{ saw chan bool }

func (streamProbeTool) Name() string           { return "probe" }
func (streamProbeTool) Description() string    { return "probe" }
func (streamProbeTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (p streamProbeTool) Execute(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	_, ok := event.StreamFromContext(ctx)
	p.saw <- ok
	return json.RawMessage(`{"ok":true}`), nil
}

func TestRuntime_InjectsStreamIntoToolCtx(t *testing.T) {
	var calls atomic.Int32
	m := streamFn(func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (model.Chunk, error) {
		if calls.Add(1) == 1 {
			return model.Chunk{Done: true, StopReason: "tool_use", Message: message.Message{
				Role:    message.RoleAssistant,
				Content: []message.ContentBlock{{Kind: message.BlockToolCall, ToolID: "c1", ToolName: "probe", Args: []byte(`{}`)}},
			}}, nil
		}
		return model.Chunk{Done: true, StopReason: "stop", Message: message.Message{
			Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "done"}},
		}}, nil
	})
	probe := streamProbeTool{saw: make(chan bool, 1)}
	rt, err := NewRuntime(Config{Model: m, Tools: []tool.Tool{probe}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	run, err := rt.Prompt(context.Background(), "go")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	_, _ = run.Wait()
	select {
	case sawStream := <-probe.saw:
		if !sawStream {
			t.Error("tool did not see an injected run stream in its context")
		}
	default:
		t.Fatal("tool was never executed")
	}
}
