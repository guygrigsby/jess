package acl

import (
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
	mu      sync.Mutex //nolint:unused // used in later Prompt/Continue steps
	running bool       //nolint:unused // used in later Prompt/Continue steps
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
//
//nolint:unused // called in the Prompt/Continue step
func (r *Run) captureEnd(ev ac.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = messagesFromACAgent(ev.NewMessages)
	r.summary = summaryFromAC(ev.Summary)
	r.err = ev.Err
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
