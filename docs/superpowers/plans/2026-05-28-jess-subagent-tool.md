# jess Subagent Tool + Event Bubbling Implementation Plan (Phase 3b)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the model delegate to subagents (an LLM-facing `subagent` tool backed by the Pool) and have those subagents' events bubble into the parent run's stream, tagged by `AgentPath` (stream injection).

**Architecture:** Phase 3b of ADR 0001. Three moving parts: (1) `event` gains context plumbing to carry the active run's stream; (2) the ACL runtime injects the current run's stream into every tool's ctx; (3) the Pool gains a sink-directed `SubmitTo`, and a new `subagent.Tool` (a jess `tool.Tool`) reads the parent stream from ctx and forwards its subagents' (already AgentPath-tagged) events there, returning results as the tool result. Concurrent tool calls each forward to their own parent stream (no single-consumer conflict).

**Tech Stack:** Go 1.26. `internal/acl`, `event`, `subagent`, `tool`, `message`, `model`. `golang.org/x/sync/errgroup` already present.

**Design invariants:**
- Stream injection is via context: `event.ContextWithStream`/`StreamFromContext`. The ACL is the sole injector (it owns the run stream); the subagent tool is a reader.
- The subagent tool forwards to the parent stream via `Pool.SubmitTo` (per-task sink), so multiple concurrent tool calls never share a single consumer.
- A subagent run completes within the tool's `Execute` (its result feeds the turn), so forwarding happens while the parent stream is open; `Stream.Send` after close is a no-op anyway.

---

## File structure

| File | Responsibility |
|---|---|
| `event/context.go` | `ContextWithStream` / `StreamFromContext` |
| `event/context_test.go` | round-trip + absent |
| `internal/acl/runtime.go` | inject current run stream into tool ctx (runtime-owned tool wrapping) |
| `internal/acl/model.go` | `wrappedTool` carries an optional ctx injector |
| `subagent/pool.go` | `SubmitTo` (sink-directed jobs) |
| `subagent/tool.go` | `Tool` — LLM-facing `tool.Tool` backed by the Pool |
| `subagent/tool_test.go` | tool Execute forwards tagged events + returns results |

---

### Task 1: event context plumbing

**Files:**
- Create: `event/context.go`
- Test: `event/context_test.go`

- [ ] **Step 1: Write the failing test** at `event/context_test.go`:

```go
package event

import (
	"context"
	"testing"
)

func TestStreamContext_RoundTrip(t *testing.T) {
	s := NewStream(1)
	ctx := ContextWithStream(context.Background(), s)
	got, ok := StreamFromContext(ctx)
	if !ok || got != s {
		t.Fatalf("StreamFromContext = %v, %v; want the injected stream", got, ok)
	}
}

func TestStreamFromContext_Absent(t *testing.T) {
	if _, ok := StreamFromContext(context.Background()); ok {
		t.Error("expected no stream in a bare context")
	}
	if _, ok := StreamFromContext(ContextWithStream(context.Background(), nil)); ok {
		t.Error("a nil stream should report not-ok")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./event/ -run TestStream`
Expected: build failure (`undefined: ContextWithStream`).

- [ ] **Step 3: Write the implementation** at `event/context.go`:

```go
package event

import "context"

type streamKey struct{}

// ContextWithStream returns ctx carrying s as the active run's event stream.
// The anti-corruption layer injects the current run's stream so components
// running mid-run (such as a subagent tool) can forward events into it.
func ContextWithStream(ctx context.Context, s *Stream) context.Context {
	return context.WithValue(ctx, streamKey{}, s)
}

// StreamFromContext returns the active run's stream if one was injected and is
// non-nil.
func StreamFromContext(ctx context.Context) (*Stream, bool) {
	s, ok := ctx.Value(streamKey{}).(*Stream)
	return s, ok && s != nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./event/`
Expected: PASS. Then `gofmt -l event/`, `go vet ./event/`.

- [ ] **Step 5: Commit**

```bash
git add event/context.go event/context_test.go
git commit -m "feat(event): context plumbing for the active run stream"
```

---

### Task 2: Pool.SubmitTo (sink-directed jobs)

**Files:**
- Modify: `subagent/pool.go` (`job.sink`, `runJob`, `SubmitTo`, refactor `Submit`)
- Test: `subagent/pool_test.go` (append)

- [ ] **Step 1: Append the failing test**:

```go
func TestPool_SubmitToForwardsToSink(t *testing.T) {
	p := New(WithMaxConcurrent(2))
	p.Register(echo("a", "ra"))
	sink := event.NewStream(64)

	task, err := p.SubmitTo(context.Background(), sink, "a", "go")
	if err != nil {
		t.Fatalf("SubmitTo: %v", err)
	}
	p.Close()

	var sawTagged bool
	for ev := range sink.Events() {
		if len(ev.AgentPath) > 0 {
			sawTagged = true
		}
	}
	if !sawTagged {
		t.Error("expected tagged events on the provided sink")
	}
	if _, err := task.Wait(); err != nil {
		t.Fatalf("task: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
```

Note: the test closes `sink` itself? No — the Pool must close the sink when... actually the sink is caller-owned. Here the test must be able to range `sink.Events()` to completion. The Pool does NOT own `sink`, so it must not close it. But then `for range sink.Events()` never ends. Fix the test to drain with a bounded approach: collect events until the task is done, then check. Replace the range with:

```go
	done := make(chan struct{})
	var sawTagged bool
	go func() {
		for ev := range sink.Events() {
			if len(ev.AgentPath) > 0 {
				sawTagged = true
			}
		}
		close(done)
	}()
	p.Close()
	if _, err := task.Wait(); err != nil {
		t.Fatalf("task: %v", err)
	}
	sink.Close() // caller owns the sink; close it to end the consumer
	<-done
	if !sawTagged {
		t.Error("expected tagged events on the provided sink")
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
```

Use this corrected form in the test file.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./subagent/ -run TestPool_SubmitToForwardsToSink`
Expected: build failure (`undefined: p.SubmitTo`).

- [ ] **Step 3: Implement.** In `subagent/pool.go`:

Add a `sink` field to `job`:

```go
type job struct {
	spec  Spec
	input string
	path  []string
	sink  *event.Stream // nil => the pool's merged stream
	task  *Task
}
```

Refactor the existing `Submit` to delegate to a shared internal `submit` with a nil sink, and add `SubmitTo`:

```go
// Submit queues a run whose events go to the pool's merged stream (Events()).
func (p *Pool) Submit(ctx context.Context, name, input string, parentPath ...string) (*Task, error) {
	return p.submit(ctx, nil, name, input, parentPath...)
}

// SubmitTo queues a run whose events are forwarded to sink (AgentPath-tagged)
// instead of the pool's merged stream. Used to bubble a subagent's events into
// a parent run's stream. The sink is caller-owned; the pool never closes it.
func (p *Pool) SubmitTo(ctx context.Context, sink *event.Stream, name, input string, parentPath ...string) (*Task, error) {
	return p.submit(ctx, sink, name, input, parentPath...)
}

func (p *Pool) submit(ctx context.Context, sink *event.Stream, name, input string, parentPath ...string) (*Task, error) {
	// (body identical to the previous Submit, but build the job with sink set)
	...
	j := &job{spec: spec, input: input, path: path, sink: sink, task: task}
	...
}
```

(Move the existing Submit body into `submit`, adding `sink` to the job.)

Update `runJob` to forward to the job's sink, defaulting to the merged stream:

```go
func (p *Pool) runJob(j *job) {
	defer close(j.task.done)
	rt, err := acl.NewRuntime(j.spec.config())
	if err != nil {
		j.task.err = err
		return
	}
	run, err := rt.Prompt(p.ctx, j.input)
	if err != nil {
		j.task.err = err
		return
	}
	dst := p.stream
	if j.sink != nil {
		dst = j.sink
	}
	for ev := range run.Events() {
		ev.AgentPath = prependPath(j.path, ev.AgentPath)
		dst.Send(ev)
	}
	res, werr := run.Wait()
	j.task.res = Result{AgentPath: j.path, Messages: res.Messages, Summary: res.Summary}
	j.task.err = werr
}
```

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./subagent/`
Expected: PASS (existing + new). Then `gofmt -l subagent/`, `go vet ./subagent/`.

- [ ] **Step 5: Commit**

```bash
git add subagent/pool.go subagent/pool_test.go
git commit -m "feat(subagent): Pool.SubmitTo forwards a task's events to a sink"
```

---

### Task 3: ACL injects the current run stream into tool ctx

**Files:**
- Modify: `internal/acl/model.go` (`wrappedTool` carries an injector; `WrapTools` unchanged for the nil case)
- Modify: `internal/acl/runtime.go` (`curStream`, runtime-owned tool wrapping, set/clear in `start`)
- Test: `internal/acl/runtime_test.go` (append)

- [ ] **Step 1: Append the failing test** at `internal/acl/runtime_test.go`:

This uses a scripted model that emits a tool call on the first turn (so agentcore executes the tool), then a final message; the tool records whether it saw an injected stream.

```go
// streamProbeTool records whether a run stream was injected into its ctx.
type streamProbeTool struct{ saw chan bool }

func (streamProbeTool) Name() string             { return "probe" }
func (streamProbeTool) Description() string       { return "probe" }
func (streamProbeTool) Schema() map[string]any    { return map[string]any{"type": "object"} }
func (p streamProbeTool) Execute(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	_, ok := event.StreamFromContext(ctx)
	p.saw <- ok
	return json.RawMessage(`{"ok":true}`), nil
}

// toolThenDone returns an assistant message with one tool call on the first
// Stream, then a plain text message on subsequent Streams.
func toolThenDone(toolName string) model.Model {
	var calls atomic.Int32
	return streamFn(func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (model.Chunk, error) {
		if calls.Add(1) == 1 {
			return model.Chunk{Done: true, StopReason: "tool_use", Message: message.Message{
				Role: message.RoleAssistant,
				Content: []message.ContentBlock{{Kind: message.BlockToolCall, ToolID: "c1", ToolName: toolName, Args: []byte(`{}`)}},
			}}, nil
		}
		return model.Chunk{Done: true, StopReason: "stop", Message: message.Message{
			Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "done"}},
		}}, nil
	})
}

func TestRuntime_InjectsStreamIntoToolCtx(t *testing.T) {
	probe := streamProbeTool{saw: make(chan bool, 1)}
	rt, err := NewRuntime(Config{Model: toolThenDone("probe"), Tools: []tool.Tool{probe}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	run, err := rt.Prompt(context.Background(), "go")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	_, _ = run.Wait()
	select {
	case sawStream := <-probe.saw:
		if !sawStream {
			t.Error("tool did not see an injected run stream in its context")
		}
	default:
		t.Fatal("tool was never executed")
	}
}
```

You will also need a tiny `streamFn` test helper that adapts a per-Stream-call function to a `model.Model` (since `model.Once` only emits one Done chunk; here two Streams happen — one per turn). Add to the test file:

```go
type streamFnModel struct {
	fn func(context.Context, []message.Message, []model.ToolSpec) (model.Chunk, error)
}

func streamFn(fn func(context.Context, []message.Message, []model.ToolSpec) (model.Chunk, error)) model.Model {
	return streamFnModel{fn: fn}
}
func (streamFnModel) SupportsTools() bool { return true }
func (m streamFnModel) Stream(ctx context.Context, msgs []message.Message, tools []model.ToolSpec) (<-chan model.Chunk, error) {
	ch := make(chan model.Chunk, 1)
	go func() {
		defer close(ch)
		c, err := m.fn(ctx, msgs, tools)
		if err != nil {
			ch <- model.Chunk{Err: err}
			return
		}
		ch <- c
	}()
	return ch, nil
}
```

Add imports as needed: `"encoding/json"`, `"sync/atomic"`, `"github.com/guygrigsby/jess/tool"` (event/message/model already imported).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/acl/ -run TestRuntime_InjectsStreamIntoToolCtx`
Expected: FAIL — the tool sees `ok == false` (no injection yet).

- [ ] **Step 3: Implement the injection.**

In `internal/acl/model.go`, give `wrappedTool` an optional injector:

```go
type wrappedTool struct {
	t      tool.Tool
	inject func(context.Context) context.Context
}

func (w wrappedTool) Name() string          { return w.t.Name() }
func (w wrappedTool) Description() string    { return w.t.Description() }
func (w wrappedTool) Schema() map[string]any { return w.t.Schema() }
func (w wrappedTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	if w.inject != nil {
		ctx = w.inject(ctx)
	}
	return w.t.Execute(ctx, args)
}

// WrapTool adapts a single jess tool to an agentcore.Tool.
func WrapTool(t tool.Tool) ac.Tool { return wrappedTool{t: t} }

// wrapToolsInject adapts jess tools, applying inject to each Execute's context.
func wrapToolsInject(ts []tool.Tool, inject func(context.Context) context.Context) []ac.Tool {
	out := make([]ac.Tool, 0, len(ts))
	for _, t := range ts {
		out = append(out, wrappedTool{t: t, inject: inject})
	}
	return out
}
```

Keep the existing `WrapTools` (it returns `wrappedTool{t: t}` with nil inject) for callers that don't inject.

In `internal/acl/runtime.go`:

1. Add `"sync/atomic"` and `"github.com/guygrigsby/jess/event"` imports (event likely already imported).
2. Add a `curStream atomic.Pointer[event.Stream]` field to `Runtime`.
3. Change `newACAgent` to accept an injector and use `wrapToolsInject` for `cfg.Tools`:

```go
func newACAgent(cfg Config, inject func(context.Context) context.Context) (*ac.Agent, error) {
	if cfg.Model == nil {
		return nil, errors.New("acl: Config.Model is required")
	}
	opts := []ac.AgentOption{ac.WithModel(ToAC(cfg.Model))}
	tools := wrapToolsInject(cfg.Tools, inject)
	if cfg.Skills != nil {
		sysBlocks := cfg.Skills.SystemBlocks()
		if len(sysBlocks) > 0 {
			opts = append(opts, ac.WithSystemBlocks(sysBlocks))
		}
		tools = append(tools, cfg.Skills.Tools()...) // skill tools are not stream-injected (out of scope)
	}
	// ... rest unchanged (SystemPrompt, WithTools(tools...), MaxTurns, ContextManager) ...
}
```

(Adjust the existing body: it previously computed `sysBlocks` outside the `if`; keep behavior, just swap `WrapTools` -> `wrapToolsInject`.)

4. `NewRuntime` builds the runtime first, then the agent with the runtime's injector:

```go
func NewRuntime(cfg Config) (*Runtime, error) {
	rt := &Runtime{}
	agent, err := newACAgent(cfg, rt.injectStream)
	if err != nil {
		return nil, err
	}
	rt.agent = agent
	return rt, nil
}

// injectStream adds the current run's stream to ctx, if a run is active.
func (rt *Runtime) injectStream(ctx context.Context) context.Context {
	if s := rt.curStream.Load(); s != nil {
		return event.ContextWithStream(ctx, s)
	}
	return ctx
}
```

5. In `start`, set `curStream` to the new run's stream before starting, and clear it when the run ends. In the subscribe callback's `EventAgentEnd` branch, add `rt.curStream.Store(nil)` (alongside clearing `running`). Set it right after `run := newRun()`:

```go
	run := newRun()
	rt.curStream.Store(run.stream)
```

And in the `EventAgentEnd` handling and the `startFn` error path, `rt.curStream.Store(nil)`.

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./internal/acl/ -run TestRuntime_InjectsStreamIntoToolCtx` then `go test -race ./internal/acl/`
Expected: PASS. Then `gofmt -l internal/acl/`, `go vet ./internal/acl/`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/model.go internal/acl/runtime.go internal/acl/runtime_test.go
git commit -m "feat(acl): inject the current run stream into tool contexts"
```

---

### Task 4: the LLM-facing subagent Tool

**Files:**
- Create: `subagent/tool.go`
- Test: `subagent/tool_test.go`

- [ ] **Step 1: Write the failing test** at `subagent/tool_test.go`:

```go
package subagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guygrigsby/jess/event"
)

func TestTool_RunsSubagentAndForwardsEvents(t *testing.T) {
	p := New(WithMaxConcurrent(2))
	p.Register(echo("research", "found it"))
	tl := NewTool(p)

	if tl.Name() != "subagent" {
		t.Fatalf("Name = %q", tl.Name())
	}

	sink := event.NewStream(64)
	done := make(chan struct{})
	var sawTagged bool
	go func() {
		for ev := range sink.Events() {
			if len(ev.AgentPath) > 0 {
				sawTagged = true
			}
		}
		close(done)
	}()

	ctx := event.ContextWithStream(context.Background(), sink)
	out, err := tl.Execute(ctx, json.RawMessage(`{"agent":"research","task":"dig"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sink.Close()
	<-done

	if !sawTagged {
		t.Error("subagent events were not forwarded to the parent stream")
	}
	var resp struct {
		Agent  string `json:"agent"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("result not JSON: %v", err)
	}
	if resp.Agent != "research" || resp.Output != "found it" {
		t.Errorf("result = %+v", resp)
	}
}

func TestTool_UnknownAgentError(t *testing.T) {
	tl := NewTool(New())
	if _, err := tl.Execute(context.Background(), json.RawMessage(`{"agent":"nope","task":"x"}`)); err == nil {
		t.Fatal("expected error for unknown agent")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./subagent/ -run TestTool`
Expected: build failure (`undefined: NewTool`).

- [ ] **Step 3: Write the implementation** at `subagent/tool.go`:

```go
package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/guygrigsby/jess/event"
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
	out := lastText(res.Messages)
	body, _ := json.Marshal(map[string]any{
		"agent":  args.Agent,
		"output": out,
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
```

Add the `message` import (`"github.com/guygrigsby/jess/message"`).

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./subagent/ -run TestTool` then `go test -race ./subagent/`
Expected: PASS. Then `gofmt -l subagent/`, `go vet ./subagent/`.

- [ ] **Step 5: Commit**

```bash
git add subagent/tool.go subagent/tool_test.go
git commit -m "feat(subagent): LLM-facing subagent tool with event bubbling"
```

---

### Task 5: phase gate

**Files:** none (verification).

- [ ] **Step 1: Boundary holds**

Run: `go test ./internal/acl/ -run TestAgentcoreImportBoundary`
Expected: PASS. Confirm `go list -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}' ./subagent/ ./event/ | grep voocel/agentcore` is empty.

- [ ] **Step 2: Full race suite (guarded)**

Run: `go test -race -count=2 -timeout 150s ./...`
Expected: all PASS.

- [ ] **Step 3: Lint + license**

Run: `make lint` (expect `0 issues.`) and `make license-audit` (expect pass).

- [ ] **Step 4: Commit (if any fixes)**

```bash
git add -A && git commit -m "chore: phase 3b gate" --allow-empty
```

---

## Self-review

**Spec coverage (Phase 3b of ADR 0001):**
- `event` context plumbing for the active run stream — Task 1. ✓
- ACL injects the current run stream into tool ctx (runtime-owned wrapping) — Task 3. ✓
- `Pool.SubmitTo` (sink-directed; tagged) — Task 2. ✓
- LLM-facing `subagent.Tool` (jess `tool.Tool`) backed by the Pool, forwarding events into the parent stream, returning results — Task 4. ✓
- Boundary + race gate — Task 5. ✓
- Out of scope (Phase 4): relocating memory/skills adapters into the ACL, README/doc.go/quickstart rewrite, shrinking the boundary allowlist. Skill-contributed tools are not stream-injected here (only host `cfg.Tools`); revisit if a skill ships a subagent tool.

**Placeholder scan:** none. The Task 2 test note corrects the sink-draining pattern inline (caller owns/closes the sink) rather than leaving a hang.

**Type consistency:** `event.ContextWithStream`/`StreamFromContext`; `Pool.Submit`/`SubmitTo`/`submit`/`job.sink`; `wrappedTool{t,inject}`/`wrapToolsInject`/`WrapTool`; `Runtime.curStream`/`injectStream`/`newACAgent(cfg, inject)`; `subagent.Tool`/`NewTool`/`toolArgs`/`lastText`. Reuses `acl.Runtime`/`acl.NewRuntime`, `event.Stream`, `model.Model`, `tool.Tool`, `message.Message`.
