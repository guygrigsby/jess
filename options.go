package jess

import (
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/model"
	"github.com/guygrigsby/jess/skill"
	"github.com/guygrigsby/jess/tool"
)

// Option configures an Agent (and through it every Session it spawns). Obtain
// options from the With* constructors; the zero value is not useful.
type Option func(*options)

// options is the private accumulator the With* functions populate. New turns it
// into the agent configuration.
type options struct {
	model        model.Model
	tools        []tool.Tool
	skills       *skill.Set
	systemPrompt string
	store        memory.Store
	recaller     memory.Recaller
	agentID      string
	maxTurns     int
}

// WithModel sets the LLM the agent uses. Required. Use jess.LiteLLM for a cloud
// provider, or implement model.Model for a local/custom model.
func WithModel(m model.Model) Option { return func(o *options) { o.model = m } }

// WithAgentID scopes the agent's memory. Empty uses the global memory scope.
func WithAgentID(id string) Option { return func(o *options) { o.agentID = id } }

// WithSystemPrompt sets a base system prompt.
func WithSystemPrompt(s string) Option { return func(o *options) { o.systemPrompt = s } }

// WithTools registers standalone tools the model may call.
func WithTools(tools ...tool.Tool) Option {
	return func(o *options) { o.tools = append(o.tools, tools...) }
}

// WithSkills registers a skill set, contributing system-prompt blocks and tools.
func WithSkills(set *skill.Set) Option { return func(o *options) { o.skills = set } }

// WithMemory wires durable memory: recalled entries are injected each turn and
// degrade to no-memory on error (never blocking the LLM call). Both store and
// recaller are required for memory to engage.
func WithMemory(store memory.Store, recaller memory.Recaller) Option {
	return func(o *options) { o.store = store; o.recaller = recaller }
}

// WithMaxTurns caps the agent loop's turns per run. 0 uses the harness default.
func WithMaxTurns(n int) Option { return func(o *options) { o.maxTurns = n } }
