package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// RememberTool is the tool the model uses to save a
// fact to memory. Hosts register it like any other tool:
//
//	store, _ := memory.NewChromemStore(emb, ...)
//	tool := memory.NewRememberTool(store, memory.RememberOptions{
//	    AgentID: "main",
//	})
//	agent := agentcore.NewAgent(
//	    ...
//	    agentcore.WithTools(append(otherTools, tool)...),
//	)
//
// Provenance: source defaults to {Tool: "remember"}. Hosts that
// know which session/message triggered the save thread that
// through ctx via WithSource — the tool reads it on each Execute
// and stamps the resulting Entry. Without WithSource the entry
// still saves; just with less audit info.
type RememberTool struct {
	store   Store
	agentID string
}

// RememberTool's method set (Name, Description, Schema, Execute) satisfies any
// agent tool interface (e.g. agentcore.Tool) structurally; memory stays
// agentcore-free, so no compile-time assertion against that type lives here.

// RememberOptions configures NewRememberTool. AgentID is required
// because every saved Entry needs to be scoped to one agent (per
// jess's multi-agent model). Hosts that don't multi-agent set this
// to "" for global scope.
type RememberOptions struct {
	// AgentID scopes every saved Entry to this agent. Empty means
	// global (visible to all agents on Recall). Set per-agent for
	// the canonical multi-agent setup.
	AgentID string
}

// NewRememberTool builds the tool. Returns nil on impossible config
// (nil Store) — callers should construct explicitly.
func NewRememberTool(store Store, opts RememberOptions) *RememberTool {
	if store == nil {
		return nil
	}
	return &RememberTool{
		store:   store,
		agentID: opts.AgentID,
	}
}

// Name satisfies tool.Tool.
func (t *RememberTool) Name() string { return "remember" }

// Description is what the model reads when deciding whether to
// call the tool. Keep it concrete — "save a fact" is too vague;
// the model needs to know what's worth saving and how to shape
// the call.
func (t *RememberTool) Description() string {
	return "Save a short fact to long-term memory so it's available in future turns and sessions. " +
		"Use kind=user for stable facts about who the user is, " +
		"kind=feedback for explicit guidance the user gave about how to work, " +
		"kind=project for current goals/decisions/incidents (time-bounded), " +
		"kind=reference for pointers to external info (dashboards, projects, docs). " +
		"Pass an optional `key` to make the entry supersede a prior one " +
		"(e.g. key='user.indent-preference' — re-saving with the same key replaces the old value)."
}

// Schema satisfies tool.Tool. JSON-schema describing the args
// the model is expected to produce.
func (t *RememberTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": map[string]any{
				"type":        "string",
				"description": "Memory category. Canonical values: user, feedback, project, reference. Custom kinds accepted.",
				"enum":        []string{"user", "feedback", "project", "reference"},
			},
			"text": map[string]any{
				"type":        "string",
				"description": "The fact to remember — a single short prose snippet. Aim for one sentence; max ~8KB.",
			},
			"key": map[string]any{
				"type":        "string",
				"description": "Optional semantic identifier. Saving with the same (agent, key) pair supersedes the prior entry — use for facts that update over time.",
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional searchable labels.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Optional one-line explanation of why this fact is worth remembering. Captured in provenance for later audit.",
			},
		},
		"required": []string{"kind", "text"},
	}
}

// rememberArgs is the decoded shape of the tool's JSON args.
type rememberArgs struct {
	Kind   string   `json:"kind"`
	Text   string   `json:"text"`
	Key    string   `json:"key,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	Reason string   `json:"reason,omitempty"`
}

// Execute satisfies tool.Tool. Decodes args, stamps source
// from ctx (if any), calls Store.Append. Returns JSON with the
// saved entry's ID + creation time so the model can reference it
// in its reply.
func (t *RememberTool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args rememberArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("remember: invalid args: %w", err)
	}
	if args.Text == "" {
		return nil, fmt.Errorf("remember: text is required")
	}
	if args.Kind == "" {
		args.Kind = string(KindReference) // safest default — recall-only, modest cap
	}

	src := SourceFromContext(ctx)
	if src.Tool == "" {
		src.Tool = "remember"
	}
	if args.Reason != "" {
		src.Reason = args.Reason
	}

	entry := Entry{
		Kind:    args.Kind,
		AgentID: t.agentID,
		Text:    args.Text,
		Key:     args.Key,
		Tags:    args.Tags,
		Source:  src,
	}
	saved, err := t.store.Append(ctx, entry)
	if err != nil {
		return nil, fmt.Errorf("remember: append: %w", err)
	}

	resp := map[string]any{
		"id":         saved.ID,
		"kind":       saved.Kind,
		"created_at": saved.CreatedAt.Format(time.RFC3339Nano),
	}
	if saved.Key != "" {
		resp["key"] = saved.Key
	}
	out, _ := json.Marshal(resp)
	return out, nil
}

// sourceCtxKey is the context key for Source values. Unexported
// type to prevent collisions with other packages.
type sourceCtxKey struct{}

// WithSource returns a context carrying src — the RememberTool
// reads it during Execute to stamp the saved Entry's provenance.
// Hosts call this once per agent run with the session-level info
// (SessionID, MessageID). Reason and Tool are typically set per-
// call by the tool itself, not threaded through ctx.
//
// Calling WithSource is OPTIONAL. The tool falls back to a
// Source{Tool: "remember"} when ctx carries no Source — the
// entry still saves; just with less audit info.
func WithSource(ctx context.Context, src Source) context.Context {
	return context.WithValue(ctx, sourceCtxKey{}, src)
}

// SourceFromContext extracts the Source stamped via WithSource, or
// returns the zero Source if none. Exported so other tools that
// want the same provenance can pick it up consistently.
func SourceFromContext(ctx context.Context) Source {
	if v, ok := ctx.Value(sourceCtxKey{}).(Source); ok {
		return v
	}
	return Source{}
}
