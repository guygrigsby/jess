package core

import (
	"sync"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/gate"
	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/skill"
)

// agentMeta carries the run-level wiring (audit sink, agent path, and the
// per-agent runState) that Stream needs but that agentcore.Agent does not
// expose. Agent registers it keyed by the *ac.Agent pointer so jess.Stream
// can recover it without changing the public signature (New returns the bare
// *ac.Agent by design).
type agentMeta struct {
	sink ledger.Sink
	path string
	rs   *runState
}

var agentRegistry sync.Map // map[*ac.Agent]agentMeta

// ReleaseAgent removes a from the internal audit registry. Call it when an
// agent is permanently done (e.g. a pool discarding a per-job agent) to avoid
// unbounded growth. Safe to call more than once.
func ReleaseAgent(a *ac.Agent) { agentRegistry.Delete(a) }

func sinkFor(a *ac.Agent) ledger.Sink {
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

func runStateFor(a *ac.Agent) *runState {
	if v, ok := agentRegistry.Load(a); ok {
		return v.(agentMeta).rs
	}
	return nil
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
	Audit        ledger.Sink
	Extra        []ac.AgentOption // passthrough for the long tail

	// Approver and AllowAll configure the default gate that Agent builds when
	// Gate is nil. A non-nil Gate (jess.WithToolGate) wins and both are ignored.
	// AllowAll is the explicit, greppable opt-out from the fail-closed default.
	// Neither bypasses the audit middleware's "no durable record, no action"
	// enforcement: that is gate-independent.
	Approver gate.Approver
	AllowAll bool
}

// Agent assembles a ready *agentcore.Agent from cfg. The audit middleware is
// always installed (a nil sink becomes DiscardSink), so every tool execution is
// recorded unless audit is explicitly discarded.
func Agent(cfg Config) *ac.Agent {
	if cfg.Model == nil {
		panic("core.Agent: Config.Model is required (set jess.WithModel)")
	}
	if cfg.Audit == nil {
		cfg.Audit = ledger.DiscardSink{}
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

	// Compute the safe tool set from every registered tool. Only tools that
	// explicitly implement gate.SafeTool AND return Safe()=true are allowed to
	// bypass the durable-record requirement. Everything else (including tools
	// that don't implement the marker, and any tool injected after this point via
	// WithAgentcoreOptions) is treated as requiring a durable KindAction before
	// it can run (fail-safe; unknown tools cannot run unaudited).
	safe := map[string]bool{}
	for _, tl := range tools {
		if st, ok := tl.(gate.SafeTool); ok && st.Safe() {
			safe[tl.Name()] = true
		}
	}

	rs := &runState{}
	if cfg.Store != nil && cfg.Recaller != nil {
		opts = append(opts, ac.WithContextManager(
			NewContextManager(cfg.Store, cfg.Recaller, ContextManagerOptions{
				AgentID:  cfg.AgentID,
				Audit:    cfg.Audit,
				RunState: rs,
			})))
	}
	// Resolve the gate. A custom gate (jess.WithToolGate) wins outright. Otherwise
	// build the default fail-closed gate here so it can be wired to rs for
	// run-linkage on denied actions: AllowAll opts out explicitly, anything else
	// requires an approver (nil approver => deny non-safe). Neither path bypasses
	// the audit middleware's "no durable record, no action" enforcement below.
	g := cfg.Gate
	if g == nil {
		if cfg.AllowAll {
			g = gate.AllowAll()
		} else {
			g = gate.New(gate.Policy{
				Approver:  cfg.Approver,
				Audit:     cfg.Audit,
				AgentPath: cfg.AgentID,
				RunID:     rs.runID,
				RequestRef: func() ledger.Ref {
					id, _ := rs.request()
					return ledger.Ref{Source: ledger.RefTool, ID: id.String()}
				},
			})
		}
	}
	if g != nil {
		opts = append(opts, ac.WithToolGate(g))
	}
	opts = append(opts, ac.WithMiddlewares(auditMiddleware(cfg.Audit, safe, rs, cfg.AgentID)))
	if cfg.MaxTurns > 0 {
		opts = append(opts, ac.WithMaxTurns(cfg.MaxTurns))
	}
	opts = append(opts, cfg.Extra...)
	a := ac.NewAgent(opts...)
	agentRegistry.Store(a, agentMeta{sink: cfg.Audit, path: cfg.AgentID, rs: rs})
	return a
}
