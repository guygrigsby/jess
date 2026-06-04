package jess

import (
	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/internal/core"
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/skill"
)

// Option configures the agent jess.New builds.
type Option func(*core.Config, *newState)

// newState holds option-time extras that need post-processing after all options
// have run (the subagent pool). The gate's approver/allowAll live on core.Config
// so core.Agent can build the default gate wired to its per-agent runState.
type newState struct {
	subagentSpecs []any // resolved to []subagent.Spec by WithSubagents/attachSubagents
}

// New assembles a ready *agentcore.Agent: model, memory, skills, tools, gate,
// audit, subagents. It returns the real agentcore type; drive it with
// agentcore's API or jess.Stream.
//
// The gate is fail-closed by default: without an approver (or AllowAll), any
// tool not marked Safe is denied. Audit is on by default (a durable SQLite sink
// under the user cache dir); pass WithLedger(ledger.DiscardSink{}) to turn it off
// explicitly. With audit off, non-safe actions cannot run: "no durable record,
// no action".
func New(opts ...Option) *ac.Agent {
	cfg := core.Config{}
	st := &newState{}
	for _, o := range opts {
		o(&cfg, st)
	}
	if cfg.Audit == nil {
		cfg.Audit = defaultLedger()
	}
	// The default gate (built from cfg.Approver/cfg.AllowAll, wired to runState
	// for run-linkage) is constructed in core.Agent. A custom WithToolGate sets
	// cfg.Gate and wins there.
	attachSubagents(&cfg, st)
	return core.Agent(cfg)
}

// WithModel sets the LLM (agentcore.ChatModel, or jess.Once for a local one).
func WithModel(m ac.ChatModel) Option {
	return func(c *core.Config, _ *newState) { c.Model = m }
}

// WithSystemPrompt sets the base system prompt.
func WithSystemPrompt(s string) Option {
	return func(c *core.Config, _ *newState) { c.SystemPrompt = s }
}

// WithTools registers tools the model may call.
func WithTools(t ...ac.Tool) Option {
	return func(c *core.Config, _ *newState) { c.Tools = append(c.Tools, t...) }
}

// WithSkills attaches a skill set (system blocks + tools).
func WithSkills(set *skill.Set) Option {
	return func(c *core.Config, _ *newState) { c.Skills = set }
}

// WithMemory wires durable memory (recall injected each turn). Both required.
func WithMemory(store memory.Store, recaller memory.Recaller) Option {
	return func(c *core.Config, _ *newState) { c.Store = store; c.Recaller = recaller }
}

// WithAgentID scopes memory and tags audit. Empty is the global scope.
func WithAgentID(id string) Option {
	return func(c *core.Config, _ *newState) { c.AgentID = id }
}

// WithMaxTurns caps the agent loop turns. 0 leaves the agentcore default.
func WithMaxTurns(n int) Option {
	return func(c *core.Config, _ *newState) { c.MaxTurns = n }
}

// WithAgentcoreOptions passes raw agentcore options through (the long tail:
// stop guard, extra middlewares, concurrency).
//
// WARNING: passing ac.WithMiddlewares here does NOT install custom tool
// middleware alongside jess's audit enforcement — it will be overridden.
// jess appends the audit-enforcement middleware last (after all Extra options)
// so that "no durable record, no action" cannot be bypassed. Custom tool
// middleware is not supported through this escape hatch.
func WithAgentcoreOptions(o ...ac.AgentOption) Option {
	return func(c *core.Config, _ *newState) { c.Extra = append(c.Extra, o...) }
}
