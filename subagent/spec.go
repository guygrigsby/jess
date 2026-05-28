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
	"github.com/guygrigsby/jess/skills"
	"github.com/guygrigsby/jess/tool"
)

// Spec defines a named subagent: its model and capabilities. The Pool runs a
// Spec as a task, each task on its own isolated run.
type Spec struct {
	Name         string
	Model        model.Model
	Tools        []tool.Tool
	Skills       *skills.Set
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

// AgentPath returns the task's path segment(s) (e.g. {"research/0001"}).
func (t *Task) AgentPath() []string { return t.agentPath }

// Wait blocks until the task finishes and returns its result and error.
func (t *Task) Wait() (Result, error) {
	<-t.done
	return t.res, t.err
}
