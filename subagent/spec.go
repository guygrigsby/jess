// Package subagent runs jess subagents with bounded concurrency. A Pool
// executes named Spec definitions as tasks, merging their events (tagged by
// AgentPath) onto one stream. It builds each subagent through internal/core, so
// subagents are real *agentcore.Agent runs that inherit the parent's audit and
// gate.
package subagent

import (
	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/internal/core"
	"github.com/guygrigsby/jess/skill"
)

// Spec defines a named subagent: its model and capabilities. The Pool runs a
// Spec as a task, each task on its own isolated run.
type Spec struct {
	Name         string
	Model        ac.ChatModel
	Tools        []ac.Tool
	Skills       *skill.Set
	SystemPrompt string
	AgentID      string
	MaxTurns     int

	// Gate and Audit are inherited from the Pool's base config when left nil, so
	// subagents share the parent's safety controls by default.
	Gate  ac.ToolGate
	Audit audit.Sink
}

// config maps a Spec to a core.Config, inheriting unset fields (Model, Gate,
// Audit) from base — the Pool's parent config — so an empty Spec field defers
// to the parent.
func (s Spec) config(base core.Config) core.Config {
	cfg := core.Config{
		Model:        s.Model,
		Tools:        s.Tools,
		Skills:       s.Skills,
		SystemPrompt: s.SystemPrompt,
		AgentID:      s.AgentID,
		MaxTurns:     s.MaxTurns,
		Gate:         s.Gate,
		Audit:        s.Audit,
	}
	if cfg.Model == nil {
		cfg.Model = base.Model
	}
	if cfg.Gate == nil {
		cfg.Gate = base.Gate
	}
	if cfg.Audit == nil {
		cfg.Audit = base.Audit
	}
	if cfg.AgentID == "" {
		cfg.AgentID = base.AgentID
	}
	return cfg
}

// Result is the outcome of a finished subagent Task.
type Result struct {
	AgentPath []string
	Output    string // the subagent's final assistant text
	Summary   *ac.RunSummary
}

// Task is the handle for one submitted subagent run.
type Task struct {
	agentPath []string
	done      chan struct{}
	res       Result
	err       error
}

// AgentPath returns a copy of the task's path segment(s) (e.g.
// {"research/0001"}). The copy keeps the task's internal path immutable from
// callers, who could otherwise corrupt the path observed on events/results.
func (t *Task) AgentPath() []string {
	return append([]string(nil), t.agentPath...)
}

// Wait blocks until the task finishes and returns its result and error. The
// returned Result carries a fresh copy of AgentPath, so a caller mutating it
// cannot corrupt the task's stored path or a later Wait call's result.
func (t *Task) Wait() (Result, error) {
	<-t.done
	res := t.res
	res.AgentPath = append([]string(nil), t.res.AgentPath...)
	return res, t.err
}
