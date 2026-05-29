// Package subagent runs jess subagents with bounded concurrency. A Pool
// executes named Spec definitions as tasks, merging their events (tagged by
// AgentPath) onto one stream. It is vendor-free: agentcore stays behind
// internal/acl.
package subagent

import (
	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/internal/acl"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	"github.com/guygrigsby/jess/skill"
	"github.com/guygrigsby/jess/tool"
)

// Spec defines a named subagent: its model and capabilities. The Pool runs a
// Spec as a task, each task on its own isolated run.
type Spec struct {
	Name         string
	Model        model.Model
	Tools        []tool.Tool
	Skills       *skill.Set
	SystemPrompt string
	AgentID      string
	MaxTurns     int
}

// config maps a Spec to the internal runtime configuration.
func (s Spec) config() acl.Config {
	return acl.Config{
		Model:        s.Model,
		Tools:        s.Tools,
		Skills:       s.Skills,
		SystemPrompt: s.SystemPrompt,
		AgentID:      s.AgentID,
		MaxTurns:     s.MaxTurns,
	}
}

// Result is the outcome of a finished subagent Task.
type Result struct {
	AgentPath []string
	Messages  []message.Message
	Summary   *event.RunSummary
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
