package core

import (
	"sync"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/skill"
)

// agentMeta carries the run-level wiring (audit sink + agent path) that Stream
// needs but that agentcore.Agent does not expose. Agent registers it keyed by
// the *ac.Agent pointer so jess.Stream can recover it without changing the
// public signature (New returns the bare *ac.Agent by design).
type agentMeta struct {
	sink audit.Sink
	path string
}

var agentRegistry sync.Map // map[*ac.Agent]agentMeta

func sinkFor(a *ac.Agent) audit.Sink {
	if v, ok := agentRegistry.Load(a); ok {
		return v.(agentMeta).sink
	}
	return nil
}

func pathFor(a *ac.Agent) string {
	if v, ok := agentRegistry.Load(a); ok {
		return v.(agentMeta).path
	}
	return ""
}

// Config is the assembled jess agent configuration, vendor-visible (agentcore
// types are exposed by design). Both jess.New and subagent.Pool build from it.
type Config struct {
	Model        ac.ChatModel
	SystemPrompt string
	Tools        []ac.Tool
	Skills       *skill.Set
	Store        memory.Store
	Recaller     memory.Recaller
	AgentID      string
	MaxTurns     int
	Gate         ac.ToolGate
	Audit        audit.Sink
	Extra        []ac.AgentOption // passthrough for the long tail
}

// Agent assembles a ready *agentcore.Agent from cfg. The audit middleware is
// always installed (a nil sink becomes DiscardSink), so every tool execution is
// recorded unless audit is explicitly discarded.
func Agent(cfg Config) *ac.Agent {
	if cfg.Audit == nil {
		cfg.Audit = audit.DiscardSink{}
	}
	opts := []ac.AgentOption{ac.WithModel(cfg.Model)}

	// System prompt: base prompt leads, then skill blocks. When a skill set
	// contributes blocks we must use WithSystemBlocks for the whole thing (the
	// two options are mutually exclusive), so fold the base prompt into a
	// leading block. With no skills, WithSystemPrompt keeps it simple.
	skillBlocks := SkillBlocks(cfg.Skills)
	switch {
	case len(skillBlocks) > 0:
		blocks := skillBlocks
		if cfg.SystemPrompt != "" {
			blocks = append([]ac.SystemBlock{{Text: cfg.SystemPrompt}}, skillBlocks...)
		}
		opts = append(opts, ac.WithSystemBlocks(blocks))
	case cfg.SystemPrompt != "":
		opts = append(opts, ac.WithSystemPrompt(cfg.SystemPrompt))
	}

	tools := append([]ac.Tool{}, cfg.Tools...)
	tools = append(tools, SkillTools(cfg.Skills)...)
	if len(tools) > 0 {
		opts = append(opts, ac.WithTools(tools...))
	}

	if cfg.Store != nil && cfg.Recaller != nil {
		opts = append(opts, ac.WithContextManager(
			NewContextManager(cfg.Store, cfg.Recaller, ContextManagerOptions{AgentID: cfg.AgentID})))
	}
	if cfg.Gate != nil {
		opts = append(opts, ac.WithToolGate(cfg.Gate))
	}
	opts = append(opts, ac.WithMiddlewares(auditMiddleware(cfg.Audit, cfg.AgentID)))
	if cfg.MaxTurns > 0 {
		opts = append(opts, ac.WithMaxTurns(cfg.MaxTurns))
	}
	opts = append(opts, cfg.Extra...)
	a := ac.NewAgent(opts...)
	agentRegistry.Store(a, agentMeta{sink: cfg.Audit, path: cfg.AgentID})
	return a
}
