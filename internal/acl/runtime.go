package acl

import (
	"context"
	"errors"
	"sync"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	"github.com/guygrigsby/jess/skills"
	"github.com/guygrigsby/jess/tool"
)

// ErrRunInProgress is returned by Prompt/Continue when a run is already active
// on this Runtime. Use Steer or FollowUp to inject into the running loop.
var ErrRunInProgress = errors.New("acl: a run is already in progress")

// streamBuffer is the per-run event channel capacity. Matches agentcore's own
// EventStream buffer; backpressure beyond it blocks producers.
const streamBuffer = 128

// Config is the vendor-free configuration for a Runtime, built by the root jess
// package from its options. The Runtime translates it into an agentcore.Agent;
// all agentcore construction stays here in the ACL.
type Config struct {
	Model        model.Model     // required
	Tools        []tool.Tool     // standalone jess tools
	Skills       *skills.Set     // optional; contributes SystemBlocks + Tools
	SystemPrompt string          // optional base system prompt
	Store        memory.Store    // optional; with Recaller, wires the memory ContextManager
	Recaller     memory.Recaller // optional
	AgentID      string          // scopes memory recall
	MaxTurns     int             // 0 = agentcore default
}

// newACAgent builds an agentcore.Agent from a Config.
func newACAgent(cfg Config) (*ac.Agent, error) {
	if cfg.Model == nil {
		return nil, errors.New("acl: Config.Model is required")
	}
	opts := []ac.AgentOption{ac.WithModel(ToAC(cfg.Model))}

	tools := WrapTools(cfg.Tools)
	var sysBlocks []ac.SystemBlock
	if cfg.Skills != nil {
		sysBlocks = cfg.Skills.SystemBlocks()
		tools = append(tools, cfg.Skills.Tools()...)
	}
	if cfg.SystemPrompt != "" {
		opts = append(opts, ac.WithSystemPrompt(cfg.SystemPrompt))
	}
	if len(sysBlocks) > 0 {
		opts = append(opts, ac.WithSystemBlocks(sysBlocks))
	}
	if len(tools) > 0 {
		opts = append(opts, ac.WithTools(tools...))
	}
	if cfg.MaxTurns > 0 {
		opts = append(opts, ac.WithMaxTurns(cfg.MaxTurns))
	}
	if cfg.Store != nil && cfg.Recaller != nil {
		cm := memory.NewContextManager(cfg.Store, cfg.Recaller, memory.ContextManagerOptions{AgentID: cfg.AgentID})
		if cm != nil {
			opts = append(opts, ac.WithContextManager(cm))
		}
	}
	return ac.NewAgent(opts...), nil
}

// Runtime drives a single agentcore.Agent. Prompt/Continue/etc. are added in
// later steps.
type Runtime struct {
	agent   *ac.Agent
	mu      sync.Mutex
	running bool
}

// NewRuntime builds a Runtime from cfg.
func NewRuntime(cfg Config) (*Runtime, error) {
	agent, err := newACAgent(cfg)
	if err != nil {
		return nil, err
	}
	return &Runtime{agent: agent}, nil
}

// Result is the outcome of a finished Run.
type Result struct {
	Messages []message.Message
	Summary  *event.RunSummary
}

// Run is the handle for one Prompt/Continue cycle. Events streams live events;
// Wait blocks for the final result.
type Run struct {
	stream *event.Stream
	done   chan struct{}

	mu       sync.Mutex
	messages []message.Message
	summary  *event.RunSummary
	err      error
}

func newRun() *Run {
	return &Run{stream: event.NewStream(streamBuffer), done: make(chan struct{})}
}

// Events returns the live event channel, closed when the run ends.
func (r *Run) Events() <-chan event.Event { return r.stream.Events() }

// Wait blocks until the run finishes and returns its final messages, summary,
// and any run error.
func (r *Run) Wait() (Result, error) {
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return Result{Messages: r.messages, Summary: r.summary}, r.err
}

// captureEnd records the final messages/summary/error from an EventAgentEnd.
func (r *Run) captureEnd(ev ac.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = messagesFromACAgent(ev.NewMessages)
	r.summary = summaryFromAC(ev.Summary)
	r.err = ev.Err
}

// Prompt starts a new run with the given input. Returns ErrRunInProgress if a
// run is already active.
func (rt *Runtime) Prompt(ctx context.Context, input string) (*Run, error) {
	return rt.start(ctx, func() error { return rt.agent.Prompt(input) })
}

// Continue resumes the conversation without new input.
func (rt *Runtime) Continue(ctx context.Context) (*Run, error) {
	return rt.start(ctx, func() error { return rt.agent.Continue() })
}

func (rt *Runtime) start(ctx context.Context, startFn func() error) (*Run, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.running {
		return nil, ErrRunInProgress
	}
	run := newRun()

	var unsub func()
	unsub = rt.agent.Subscribe(func(ev ac.Event) {
		if ev.Type == ac.EventAgentEnd {
			run.captureEnd(ev)
		}
		if jev, ok := EventFromAC(ev); ok {
			run.stream.Send(jev)
		}
		if ev.Type == ac.EventAgentEnd {
			run.stream.Close()
			unsub()
			rt.mu.Lock()
			rt.running = false
			rt.mu.Unlock()
			close(run.done)
		}
	})

	if err := startFn(); err != nil {
		unsub()
		run.stream.Close()
		close(run.done)
		if errors.Is(err, ac.ErrAlreadyRunning) {
			return nil, ErrRunInProgress
		}
		return nil, err
	}
	rt.running = true

	// Bridge jess's ctx to agentcore's abort: cancelling ctx aborts the run.
	go func() {
		select {
		case <-ctx.Done():
			rt.agent.Abort()
		case <-run.done:
		}
	}()
	return run, nil
}

// messagesFromACAgent converts agentcore AgentMessages to jess messages,
// dropping any that are not concrete ac.Message values.
func messagesFromACAgent(msgs []ac.AgentMessage) []message.Message {
	out := make([]message.Message, 0, len(msgs))
	for _, m := range msgs {
		if acm, ok := m.(ac.Message); ok {
			out = append(out, messageFromAC(acm))
		}
	}
	return out
}

// Steer injects a message into the running loop at the next safe point (soft
// preemption). agentcore queues it if no run is active.
func (rt *Runtime) Steer(msg message.Message) {
	rt.agent.Steer(messagesToAC([]message.Message{msg})[0])
}

// FollowUp queues a message to be processed after the current run finishes.
func (rt *Runtime) FollowUp(msg message.Message) {
	rt.agent.FollowUp(messagesToAC([]message.Message{msg})[0])
}

// Abort hard-cancels the current run (context cancellation). Queued steer and
// follow-up messages are processed on continuation.
func (rt *Runtime) Abort() { rt.agent.Abort() }
