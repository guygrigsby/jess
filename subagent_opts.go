package jess

import (
	"github.com/guygrigsby/jess/internal/core"
	"github.com/guygrigsby/jess/subagent"
)

// WithSubagents registers subagents. Empty Spec fields inherit from the parent
// (model, gate, audit, agentID). jess.New builds and owns the pool and wires the
// delegating "subagent" tool into the agent's tool set.
func WithSubagents(specs ...subagent.Spec) Option {
	return func(_ *core.Config, s *newState) {
		for _, sp := range specs {
			s.subagentSpecs = append(s.subagentSpecs, sp)
		}
	}
}

// attachSubagents builds the subagent pool (seeded with the assembled config's
// defaults so subagents share the parent's model, gate, and audit) and appends
// the delegating tool to cfg.Tools. No-op when no subagents were registered.
func attachSubagents(cfg *core.Config, st *newState) {
	if len(st.subagentSpecs) == 0 {
		return
	}
	pool := subagent.New(subagent.WithDefaults(cfg.Model, cfg.Gate, cfg.Audit, cfg.AgentID))
	for _, raw := range st.subagentSpecs {
		if sp, ok := raw.(subagent.Spec); ok {
			pool.Register(sp)
		}
	}
	cfg.Tools = append(cfg.Tools, subagent.NewTool(pool))
}
