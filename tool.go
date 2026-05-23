package jess

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// ToolSpec describes a callable tool the harness surfaces to the model.
// ParametersSchema is the JSON schema for the tool's input arguments;
// provider adapters map this onto their wire format (OpenAI
// tools[].function.parameters, Anthropic tools[].input_schema, etc.).
//
// Stable for the lifetime of the owning Tool — the harness reads it once
// per run to build the provider request.
type ToolSpec struct {
	Name             string
	Description      string
	ParametersSchema json.RawMessage
}

// ToolCall is the model's resolved tool invocation. Provider streams emit
// argument fragments; the harness buffers and finalizes them, then hands
// the assembled call to a Tool to execute.
type ToolCall struct {
	// ID is the provider-issued call site identifier. Callers echo it
	// back as tool_use_id when sending the result in a follow-up turn.
	ID string
	// Name matches a registered ToolSpec.Name.
	Name string
	// ArgumentsJSON is the model's chosen input as a JSON-encoded string.
	// Length-bounded by the provider; the harness does not interpret it.
	ArgumentsJSON string
}

// ToolResult is what a Tool returns after running. Output is the text
// the model sees as the tool's response. IsError surfaces a tool-domain
// failure (the model should reconsider) — distinct from an infra error
// returned via the err return value of Run, which the harness treats
// as a run-level abort.
type ToolResult struct {
	Output  string
	IsError bool
}

// Tool is the contract for one executable function exposed to the model.
// Implementations must be safe for concurrent use — the harness dispatches
// multiple tool calls in parallel within a single iteration.
type Tool interface {
	// Spec returns the tool's surface metadata. The returned value should
	// be stable across calls; the harness caches it per run.
	Spec() ToolSpec
	// Run executes the tool. argsJSON conforms to ParametersSchema by
	// contract — implementations should still validate and return
	// IsError=true with a parseable message on malformed input rather
	// than returning a Go error, so the model gets to recover.
	//
	// Return err only for infra-level failures (timeout, panic, missing
	// dependency); the harness will end the run rather than feed err's
	// text back to the model.
	Run(ctx context.Context, argsJSON json.RawMessage) (ToolResult, error)
}

// ToolRunner is a registry of named tools the harness can dispatch.
// Construct once via NewToolRunner and reuse across runs; safe for
// concurrent use.
type ToolRunner struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewToolRunner builds a runner over the given tools. Duplicate Names
// are rejected at construction time so failures surface before the
// first model call rather than as a confusing per-call error.
//
// An empty list is valid — Specs returns nil and Run rejects every
// call as unknown. Useful for text-only chat that still wants the
// harness's loop semantics.
func NewToolRunner(tools ...Tool) (*ToolRunner, error) {
	r := &ToolRunner{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("jess: nil Tool in NewToolRunner")
		}
		name := t.Spec().Name
		if name == "" {
			return nil, fmt.Errorf("jess: Tool has empty Spec().Name")
		}
		if _, exists := r.tools[name]; exists {
			return nil, fmt.Errorf("jess: duplicate Tool name %q", name)
		}
		r.tools[name] = t
	}
	return r, nil
}

// Register adds a tool to the runner after construction. Useful when
// the tool set is built up incrementally (e.g. one tool per plugin
// as plugins finish loading). Returns an error if a tool with the
// same name is already registered — callers can Unregister first
// for explicit replacement semantics.
func (r *ToolRunner) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("jess: nil Tool")
	}
	name := t.Spec().Name
	if name == "" {
		return fmt.Errorf("jess: Tool has empty Spec().Name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("jess: tool %q already registered", name)
	}
	r.tools[name] = t
	return nil
}

// Unregister removes a tool by name. No-op if the name isn't registered.
func (r *ToolRunner) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
}

// Specs returns the surface metadata for every registered tool, sorted
// by name so the order is stable across calls (matters for prompt
// caching — providers hash the tool list).
func (r *ToolRunner) Specs() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Spec())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names returns the registered tool names, sorted. Convenience for
// callers that just want to know what's available without the full
// spec payload.
func (r *ToolRunner) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Run dispatches call to the matching Tool. Unknown names return an
// error listing the registered tools so callers can route a clear
// "not implemented" response back to the model. Argument parsing
// (decoding ArgumentsJSON into the tool's input shape) is the Tool's
// responsibility — Run passes the raw bytes through.
//
// Concurrency: Run holds the registry read lock only long enough to
// look up the Tool, then releases it before invoking Run. Long-running
// tools don't block Register/Unregister.
func (r *ToolRunner) Run(ctx context.Context, call ToolCall) (ToolResult, error) {
	r.mu.RLock()
	t, ok := r.tools[call.Name]
	r.mu.RUnlock()
	if !ok {
		return ToolResult{}, fmt.Errorf("jess: unknown tool %q (registered: %v)", call.Name, r.Names())
	}
	return t.Run(ctx, json.RawMessage(call.ArgumentsJSON))
}
