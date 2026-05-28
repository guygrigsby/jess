package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
)

// Tool is the agentcore-free tool the model calls to delegate to a subagent.
// It runs the named subagent on the Pool, forwards the subagent's events into
// the caller's run stream (when one is in context), and returns the subagent's
// final text output.
type Tool struct {
	pool *Pool
}

// NewTool builds the subagent tool backed by pool. Register the available
// subagent Specs on the pool before use.
func NewTool(pool *Pool) *Tool { return &Tool{pool: pool} }

// Name satisfies jess/tool.Tool.
func (t *Tool) Name() string { return "subagent" }

// Description is what the model sees.
func (t *Tool) Description() string {
	return "Delegate a task to a specialized subagent. Provide the subagent's " +
		"name and the task to run. The subagent runs with its own context and " +
		"returns its final output."
}

// Schema satisfies jess/tool.Tool.
func (t *Tool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent": map[string]any{"type": "string", "description": "Name of the subagent to run."},
			"task":  map[string]any{"type": "string", "description": "The task/prompt for the subagent."},
		},
		"required": []string{"agent", "task"},
	}
}

type toolArgs struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

// Execute runs the subagent and returns its output as JSON. If a run stream is
// present in ctx (injected by the runtime), the subagent's events are forwarded
// there, tagged by AgentPath.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args toolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("subagent: invalid args: %w", err)
	}
	if strings.TrimSpace(args.Agent) == "" || strings.TrimSpace(args.Task) == "" {
		return nil, fmt.Errorf("subagent: agent and task are required")
	}

	var task *Task
	var err error
	if sink, ok := event.StreamFromContext(ctx); ok {
		task, err = t.pool.SubmitTo(ctx, sink, args.Agent, args.Task)
	} else {
		task, err = t.pool.Submit(ctx, args.Agent, args.Task)
	}
	if err != nil {
		return nil, fmt.Errorf("subagent: %w", err)
	}

	res, werr := task.Wait()
	if werr != nil {
		return nil, fmt.Errorf("subagent %q: %w", args.Agent, werr)
	}
	body, _ := json.Marshal(map[string]any{
		"agent":  args.Agent,
		"output": lastText(res.Messages),
	})
	return body, nil
}

// lastText returns the text of the final assistant message, if any.
func lastText(msgs []message.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == message.RoleAssistant {
			if s := msgs[i].Text(); s != "" {
				return s
			}
		}
	}
	return ""
}
