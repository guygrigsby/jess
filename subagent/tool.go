package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Tool is the agentcore.Tool the model calls to delegate to a subagent. It runs
// the named subagent on the Pool and returns the subagent's final text output.
// The subagent's events are merged onto the Pool's stream (AgentPath-tagged) for
// any observer of the Pool.
type Tool struct {
	pool *Pool
}

// NewTool builds the subagent tool backed by pool. Register the available
// subagent Specs on the pool before use.
func NewTool(pool *Pool) *Tool { return &Tool{pool: pool} }

// Name satisfies agentcore.Tool.
func (t *Tool) Name() string { return "subagent" }

// Description is what the model sees.
func (t *Tool) Description() string {
	return "Delegate a task to a specialized subagent. Provide the subagent's " +
		"name and the task to run. The subagent runs with its own context and " +
		"returns its final output."
}

// Schema satisfies agentcore.Tool.
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

// Execute runs the subagent and returns its output as JSON. The subagent's
// events flow onto the Pool's merged stream, tagged by AgentPath.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args toolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("subagent: invalid args: %w", err)
	}
	if strings.TrimSpace(args.Agent) == "" || strings.TrimSpace(args.Task) == "" {
		return nil, fmt.Errorf("subagent: agent and task are required")
	}

	task, err := t.pool.Submit(ctx, args.Agent, args.Task)
	if err != nil {
		return nil, fmt.Errorf("subagent: %w", err)
	}

	res, werr := task.Wait()
	if werr != nil {
		return nil, fmt.Errorf("subagent %q: %w", args.Agent, werr)
	}
	body, _ := json.Marshal(map[string]any{
		"agent":  args.Agent,
		"output": res.Output,
	})
	return body, nil
}
