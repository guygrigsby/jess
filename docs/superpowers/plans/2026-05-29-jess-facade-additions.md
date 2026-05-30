# jess facade additions (talon port sub-project 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the four facade capabilities the talon port needs — per-run token usage, history seeding, per-run memory provenance, and a model token cap — without leaking agentcore past `internal/acl`.

**Architecture:** Each addition is a small, vendor-free public API on the root `jess`/`event` packages, implemented inside `internal/acl` against agentcore v1.6.9. agentcore stays imported only under `internal/acl/` (the `TestAgentcoreImportBoundary` test must stay green with its empty allowlist). The four tasks are independent and can be done in any order; do them 1→4.

**Tech Stack:** Go 1.26+, no CGO. `github.com/voocel/agentcore v1.6.9`. Branch `feat/facade-additions-for-talon` (already checked out; the design spec is committed there).

**Spec:** `docs/superpowers/specs/2026-05-29-jess-facade-additions-for-talon-design.md` (revised per review).

**Confirmed agentcore v1.6.9 facts the tasks rely on:**
- `func (a *Agent) Prompt(input string) error` — takes NO context (so the run ctx cannot be threaded to tools generically).
- `type CallOption func(*CallConfig)`; `func WithMaxTokens(tokens int) CallOption` (`model.go:157,226`); `CallConfig.MaxTokens int`.
- `func (a *Agent) SetMessages(msgs []AgentMessage) error` (`agent.go:251`); `ac.Message` implements `ac.AgentMessage`.
- `ac.Message.Usage *ac.Usage`; `ac.Usage{Input, Output, TotalTokens int}` (`message.go:135`).
- `ac.ChatModel`: `SupportsTools() bool`, `Generate(ctx, []ac.Message, []ac.ToolSpec, ...ac.CallOption) (*ac.LLMResponse, error)`, `GenerateStream(ctx, []ac.Message, []ac.ToolSpec, ...ac.CallOption) (<-chan ac.StreamEvent, error)`.

**Invariant (do not break):** memory failures never block an LLM call; `internal/acl` is the only agentcore importer.

---

## File structure

- `event/event.go` — add `Usage` type + `RunSummary.Usage` field. (Task 1)
- `internal/acl/runtime.go` — usage aggregation in `captureEnd`; `SetHistory`; provenance snapshot + inject. (Tasks 1, 2, 3)
- `internal/acl/translate.go` — `messagesToACAgent` helper. (Task 2)
- `internal/acl/model.go` — `cappedChatModel`; `LiteLLMConfig.MaxTokens`; wrap in `NewLiteLLMModel`. (Task 4)
- `agent.go` — `Agent.NewSessionWithHistory`. (Task 2)
- `litellm.go` — `WithLLMMaxTokens` option + `LiteLLMConfig.MaxTokens`. (Task 4)
- Tests: `internal/acl/usage_test.go`, `internal/acl/provenance_test.go`, `internal/acl/model_test.go` (extend), `agent_test.go` (extend or create).

---

## Task 1: Per-run token usage

**Files:**
- Modify: `event/event.go` (RunSummary + new Usage type)
- Modify: `internal/acl/runtime.go` (`captureEnd`, add `usageFromACMessages`)
- Test: `internal/acl/usage_test.go` (create)

- [ ] **Step 1: Add the `Usage` type and field to `event/event.go`**

After the `RunSummary` struct, change it to include usage, and add the type:

```go
// RunSummary is the factual outcome of a single run.
type RunSummary struct {
	Turns     int
	ToolCalls int
	EndReason string // stop, max_turns, aborted, error
	Usage     Usage  // token usage aggregated over this run (zero if unreported)
}

// Usage reports token consumption aggregated over a single run. Input/Output
// are prompt/completion tokens; Total is the reported total. Cache and cost
// fields are intentionally omitted for now (add later, backward-compatibly).
type Usage struct {
	Input  int
	Output int
	Total  int
}
```

- [ ] **Step 2: Write the failing test `internal/acl/usage_test.go`**

This uses the existing `streamFn` fake model helper in `runtime_test.go` (same package). A run whose model reports usage must surface it in `Result.Summary.Usage`; a second run on the same Runtime must NOT accumulate the first run's tokens.

```go
package acl

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

func TestRun_SummaryUsage_PerRunNotCumulative(t *testing.T) {
	m := streamFn(func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (model.Chunk, error) {
		return model.Chunk{Done: true, StopReason: "stop", Message: message.Message{
			Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "ok"}},
		}, Usage: model.Usage{Input: 10, Output: 5, TotalTokens: 15}}, nil
	})
	rt, err := NewRuntime(Config{Model: m})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	run1, err := rt.Prompt(context.Background(), "a")
	if err != nil {
		t.Fatalf("Prompt 1: %v", err)
	}
	res1, _ := run1.Wait()
	if res1.Summary == nil || res1.Summary.Usage.Input != 10 || res1.Summary.Usage.Output != 5 || res1.Summary.Usage.Total != 15 {
		t.Fatalf("run1 usage = %+v, want Input 10 Output 5 Total 15", res1.Summary)
	}

	run2, err := rt.Continue(context.Background())
	if err != nil {
		t.Fatalf("Continue 2: %v", err)
	}
	res2, _ := run2.Wait()
	if res2.Summary == nil || res2.Summary.Usage.Input != 10 || res2.Summary.Usage.Total != 15 {
		t.Fatalf("run2 usage = %+v, want only this run's tokens (Input 10 Total 15), not cumulative", res2.Summary)
	}
}
```

- [ ] **Step 3: Run the test, verify it fails**

Run: `go test -race -run TestRun_SummaryUsage_PerRunNotCumulative ./internal/acl/`
Expected: FAIL — `Result.Summary.Usage` is the zero value (no aggregation yet), or `Usage` doesn't compile until Step 1 is in. (Step 1 must be applied first; the failure is the assertion, not a compile error.)

- [ ] **Step 4: Implement aggregation in `internal/acl/runtime.go`**

Add the helper (near `captureEnd`):

```go
// usageFromACMessages sums token usage over a run's messages. agentcore reports
// usage per assistant message, so the run total is their sum. This is the
// PER-RUN total — deliberately NOT Agent.TotalUsage(), which is cumulative
// across the whole session and would leak earlier turns into a later result.
func usageFromACMessages(msgs []ac.AgentMessage) event.Usage {
	var u event.Usage
	for _, m := range msgs {
		acm, ok := m.(ac.Message)
		if !ok || acm.Usage == nil {
			continue
		}
		u.Input += acm.Usage.Input
		u.Output += acm.Usage.Output
		u.Total += acm.Usage.TotalTokens
	}
	return u
}
```

Then in `captureEnd`, attach it to the summary (after `r.summary = summaryFromAC(ev.Summary)`):

```go
func (r *Run) captureEnd(ev ac.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = messagesFromACAgent(ev.NewMessages)
	r.summary = summaryFromAC(ev.Summary)
	if r.summary != nil {
		r.summary.Usage = usageFromACMessages(ev.NewMessages)
	}
	if r.err == nil {
		r.err = ev.Err
	}
}
```

`event` is already imported in `runtime.go`.

- [ ] **Step 5: Run the test, verify it passes**

Run: `go test -race -run TestRun_SummaryUsage_PerRunNotCumulative ./internal/acl/`
Expected: PASS. If usage is zero, agentcore did not preserve `Message.Usage` on `EventAgentEnd.NewMessages`; in that case capture usage in the `Subscribe` callback when the model's `StreamEventDone` is observed and store it on the run — but verify the NewMessages path first (it should carry it, since the loop stores the assistant message whose `Usage` `assistantMessageToAC` set).

- [ ] **Step 6: Commit**

```bash
git add event/event.go internal/acl/runtime.go internal/acl/usage_test.go
git commit -m "feat(event,acl): per-run token usage in RunSummary"
```

---

## Task 2: History seeding (`Agent.NewSessionWithHistory`)

**Files:**
- Modify: `internal/acl/translate.go` (add `messagesToACAgent`)
- Modify: `internal/acl/runtime.go` (add `(*Runtime).SetHistory`)
- Modify: `agent.go` (add `Agent.NewSessionWithHistory`)
- Test: `agent_test.go` (add)

- [ ] **Step 1: Write the failing test in `agent_test.go`**

A Session seeded with prior history must present that history to the model on the next Prompt. The fake model records the messages it received and returns a fixed reply.

```go
func TestAgent_NewSessionWithHistory_SeedsPriorMessages(t *testing.T) {
	var seen []message.Message
	m := model.Once(false, func(_ context.Context, msgs []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		seen = append([]message.Message(nil), msgs...)
		return &model.Response{Message: message.Message{
			Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "ack"}},
		}, StopReason: "stop"}, nil
	})
	agent, err := New(WithModel(m))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	history := []message.Message{
		{Role: message.RoleUser, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "earlier question"}}},
		{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "earlier answer"}}},
	}
	sess, err := agent.NewSessionWithHistory(history)
	if err != nil {
		t.Fatalf("NewSessionWithHistory: %v", err)
	}
	run, err := sess.Prompt(context.Background(), "follow-up")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	_, _ = run.Wait()

	// The model should have seen the two seeded turns before the new prompt.
	var texts []string
	for _, msg := range seen {
		texts = append(texts, msg.Text())
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "earlier question") || !strings.Contains(joined, "earlier answer") {
		t.Fatalf("seeded history not presented to model; saw: %q", joined)
	}
}
```

Ensure `agent_test.go` imports `context`, `strings`, `testing`, `github.com/guygrigsby/jess/message`, `github.com/guygrigsby/jess/model` (add any missing).

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test -race -run TestAgent_NewSessionWithHistory_SeedsPriorMessages ./`
Expected: FAIL — `agent.NewSessionWithHistory` undefined (does not compile / not declared).

- [ ] **Step 3: Add the `messagesToACAgent` helper in `internal/acl/translate.go`**

```go
// messagesToACAgent translates jess messages to the []ac.AgentMessage shape
// Agent.SetMessages expects. messagesToAC returns []ac.Message; since Go slices
// are not covariant we copy each (ac.Message implements ac.AgentMessage) into
// an interface slice.
func messagesToACAgent(msgs []message.Message) []ac.AgentMessage {
	acMsgs := messagesToAC(msgs)
	out := make([]ac.AgentMessage, len(acMsgs))
	for i := range acMsgs {
		out[i] = acMsgs[i]
	}
	return out
}
```

- [ ] **Step 4: Add `(*Runtime).SetHistory` in `internal/acl/runtime.go`**

```go
// SetHistory seeds the underlying agent with prior conversation messages before
// any run, so a host can resume a conversation whose history lives in its own
// store. Call before the first Prompt/Continue. Seeded history must NOT include
// the configured system prompt (WithSystemPrompt is prepended by agentcore on
// every call); it is conversation turns only.
func (rt *Runtime) SetHistory(history []message.Message) error {
	if len(history) == 0 {
		return nil
	}
	return rt.agent.SetMessages(messagesToACAgent(history))
}
```

`message` is already imported in `runtime.go`.

- [ ] **Step 5: Add `Agent.NewSessionWithHistory` in `agent.go`**

```go
// NewSessionWithHistory opens a Session pre-loaded with prior conversation
// messages, for resuming a conversation whose history is stored by the host
// (e.g. replay after a restart). history is conversation turns only; the system
// prompt is configured via WithSystemPrompt and must not be duplicated here.
func (a *Agent) NewSessionWithHistory(history []message.Message) (*Session, error) {
	s, err := a.newSession()
	if err != nil {
		return nil, err
	}
	if err := s.rt.SetHistory(history); err != nil {
		return nil, err
	}
	return s, nil
}
```

Add `"github.com/guygrigsby/jess/message"` to `agent.go`'s imports.

- [ ] **Step 6: Run the test, verify it passes**

Run: `go test -race -run TestAgent_NewSessionWithHistory_SeedsPriorMessages ./`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/acl/translate.go internal/acl/runtime.go agent.go agent_test.go
git commit -m "feat(jess,acl): Agent.NewSessionWithHistory seeds prior messages"
```

---

## Task 3: Per-run memory provenance

**Files:**
- Modify: `internal/acl/runtime.go` (snapshot Source in `start`, apply in `injectStream`)
- Test: `internal/acl/provenance_test.go` (create)

Context: `injectStream` (runtime.go ~105) already composes the per-Execute ctx (adding the run stream via `event.ContextWithStream`). It is called for every tool Execute and keeps agentcore's incoming ctx as the base. We extend it to also re-apply the run's `memory.Source`, snapshotted from the ctx passed to `Prompt`. agentcore's `progressCtx` (cancellation) is preserved — we never substitute it.

- [ ] **Step 1: Write the failing tests in `internal/acl/provenance_test.go`**

```go
package acl

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	"github.com/guygrigsby/jess/tool"
)

// sourceProbeTool records the memory.Source it observed in its Execute ctx.
type sourceProbeTool struct{ saw chan memory.Source }

func (sourceProbeTool) Name() string           { return "probe" }
func (sourceProbeTool) Description() string     { return "probe" }
func (sourceProbeTool) Schema() map[string]any  { return map[string]any{"type": "object"} }
func (p sourceProbeTool) Execute(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	p.saw <- memory.SourceFromContext(ctx)
	return json.RawMessage(`{"ok":true}`), nil
}

// toolCallThenStop returns a model that calls "probe" on the first turn then stops.
func toolCallThenStop() model.Model {
	var calls atomic.Int32
	return streamFn(func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (model.Chunk, error) {
		if calls.Add(1) == 1 {
			return model.Chunk{Done: true, StopReason: "tool_use", Message: message.Message{
				Role:    message.RoleAssistant,
				Content: []message.ContentBlock{{Kind: message.BlockToolCall, ToolID: "c1", ToolName: "probe", Args: []byte(`{}`)}},
			}}, nil
		}
		return model.Chunk{Done: true, StopReason: "stop", Message: message.Message{
			Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "done"}},
		}}, nil
	})
}

func TestRuntime_InjectsMemorySourceIntoToolCtx(t *testing.T) {
	probe := sourceProbeTool{saw: make(chan memory.Source, 1)}
	rt, err := NewRuntime(Config{Model: toolCallThenStop(), Tools: []tool.Tool{probe}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	src := memory.Source{SessionID: "agent:main:web", MessageID: "run_123", Tool: "remember", Reason: "model decided"}
	run, err := rt.Prompt(memory.WithSource(context.Background(), src), "go")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	_, _ = run.Wait()
	select {
	case got := <-probe.saw:
		if got != src {
			t.Errorf("tool saw Source %+v, want %+v", got, src)
		}
	default:
		t.Fatal("tool was never executed")
	}
}

// blockingTool blocks in Execute until its ctx is cancelled, recording that.
type blockingTool struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (blockingTool) Name() string          { return "probe" }
func (blockingTool) Description() string    { return "probe" }
func (blockingTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (b blockingTool) Execute(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	close(b.cancelled)
	return nil, ctx.Err()
}

// Abort must still cancel a blocking tool: injecting the Source must NOT replace
// agentcore's cancellation ctx.
func TestRuntime_AbortCancelsToolDespiteSourceInject(t *testing.T) {
	tl := blockingTool{started: make(chan struct{}, 1), cancelled: make(chan struct{})}
	rt, err := NewRuntime(Config{Model: toolCallThenStop(), Tools: []tool.Tool{tl}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	src := memory.Source{SessionID: "s", MessageID: "m"}
	run, err := rt.Prompt(memory.WithSource(context.Background(), src), "go")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	select {
	case <-tl.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool never started")
	}
	rt.Abort()
	select {
	case <-tl.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Abort did not cancel the tool ctx; Source inject must not replace agentcore's ctx")
	}
	_, _ = run.Wait()
}
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test -race -run 'TestRuntime_InjectsMemorySourceIntoToolCtx|TestRuntime_AbortCancelsToolDespiteSourceInject' ./internal/acl/`
Expected: the Source-inject test FAILS (probe sees the zero `memory.Source`). The Abort test should already pass (cancellation works today) — that's fine; it is the regression guard for the next step.

- [ ] **Step 3: Snapshot the Source and apply it in `internal/acl/runtime.go`**

Add a field to the `Runtime` struct (next to `curStream`):

```go
	curStream atomic.Pointer[event.Stream]
	curSource atomic.Pointer[memory.Source]
```

In `start`, after `rt.curStream.Store(run.stream)`, snapshot the Source from the caller ctx:

```go
	rt.curStream.Store(run.stream)
	if src := memory.SourceFromContext(ctx); src != (memory.Source{}) {
		rt.curSource.Store(&src)
	}
```

In the run-end cleanup in the `Subscribe` callback, clear it next to `rt.curStream.Store(nil)`:

```go
		rt.curStream.Store(nil)
		rt.curSource.Store(nil)
```

And in the `startFn` error path (where `rt.curStream.Store(nil)` is already called), add `rt.curSource.Store(nil)` alongside.

Extend `injectStream` to also apply the Source (keeping agentcore's incoming ctx as the base):

```go
func (rt *Runtime) injectStream(ctx context.Context) context.Context {
	if s := rt.curStream.Load(); s != nil {
		ctx = event.ContextWithStream(ctx, s)
	}
	if src := rt.curSource.Load(); src != nil {
		ctx = memory.WithSource(ctx, *src)
	}
	return ctx
}
```

`memory` is already imported in `runtime.go` (Config.Store is `memory.Store`).

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test -race -run 'TestRuntime_InjectsMemorySourceIntoToolCtx|TestRuntime_AbortCancelsToolDespiteSourceInject' ./internal/acl/`
Expected: BOTH PASS (probe sees the Source; Abort still cancels the blocking tool).

- [ ] **Step 5: Commit**

```bash
git add internal/acl/runtime.go internal/acl/provenance_test.go
git commit -m "feat(acl): inject the run's memory.Source into tool ctx (provenance)"
```

---

## Task 4: LiteLLM token cap (`WithLLMMaxTokens`)

**Files:**
- Modify: `internal/acl/model.go` (add `cappedChatModel`, `LiteLLMConfig.MaxTokens`, wrap in `NewLiteLLMModel`)
- Modify: `litellm.go` (add `WithLLMMaxTokens`, `LiteLLMConfig.MaxTokens`)
- Test: `internal/acl/model_test.go` (add)

- [ ] **Step 1: Write the failing test in `internal/acl/model_test.go`**

A capping model must append `WithMaxTokens(n)` to the options on both `Generate` and `GenerateStream`. A fake `ac.ChatModel` resolves the received `CallOption`s into a `CallConfig` and records `MaxTokens`.

```go
// captureModel records the resolved MaxTokens from the CallOptions it receives.
type captureModel struct{ genMax, streamMax int }

func (m *captureModel) SupportsTools() bool { return true }
func (m *captureModel) Generate(_ context.Context, _ []ac.Message, _ []ac.ToolSpec, opts ...ac.CallOption) (*ac.LLMResponse, error) {
	var cc ac.CallConfig
	for _, o := range opts {
		o(&cc)
	}
	m.genMax = cc.MaxTokens
	return &ac.LLMResponse{Message: ac.Message{Role: ac.RoleAssistant}}, nil
}
func (m *captureModel) GenerateStream(_ context.Context, _ []ac.Message, _ []ac.ToolSpec, opts ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	var cc ac.CallConfig
	for _, o := range opts {
		o(&cc)
	}
	m.streamMax = cc.MaxTokens
	ch := make(chan ac.StreamEvent)
	close(ch)
	return ch, nil
}

func TestCappedChatModel_AppendsMaxTokens(t *testing.T) {
	cap := &captureModel{}
	capped := cappedChatModel{cm: cap, max: 99}
	if _, err := capped.Generate(context.Background(), nil, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := capped.GenerateStream(context.Background(), nil, nil); err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	if cap.genMax != 99 {
		t.Errorf("Generate MaxTokens = %d, want 99", cap.genMax)
	}
	if cap.streamMax != 99 {
		t.Errorf("GenerateStream MaxTokens = %d, want 99", cap.streamMax)
	}
}
```

Ensure `model_test.go` imports `context`, `testing`, and `ac "github.com/voocel/agentcore"` (it already uses `ac` and `model`).

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test -race -run TestCappedChatModel_AppendsMaxTokens ./internal/acl/`
Expected: FAIL — `cappedChatModel` undefined.

- [ ] **Step 3: Implement `cappedChatModel` and wiring in `internal/acl/model.go`**

Add the wrapper:

```go
// cappedChatModel wraps an ac.ChatModel to cap max output tokens per call.
// agentcore's WithMaxTokens is a per-call option (not a construction option) and
// the agent loop builds its own options, so capping must be applied on each
// Generate/GenerateStream call here. max <= 0 should never reach this type
// (NewLiteLLMModel only wraps when max > 0).
type cappedChatModel struct {
	cm  ac.ChatModel
	max int
}

func (c cappedChatModel) SupportsTools() bool { return c.cm.SupportsTools() }

func (c cappedChatModel) Generate(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, opts ...ac.CallOption) (*ac.LLMResponse, error) {
	return c.cm.Generate(ctx, msgs, tools, append(opts, ac.WithMaxTokens(c.max))...)
}

func (c cappedChatModel) GenerateStream(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, opts ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	return c.cm.GenerateStream(ctx, msgs, tools, append(opts, ac.WithMaxTokens(c.max))...)
}
```

Add `MaxTokens int` to `LiteLLMConfig`:

```go
type LiteLLMConfig struct {
	APIKey    string
	BaseURL   string
	MaxTokens int
}
```

Wrap in `NewLiteLLMModel` (after `cm, err := llm.NewModel(...)`):

```go
	cm, err := llm.NewModel(provider, modelID, opts...)
	if err != nil {
		return nil, err
	}
	if cfg.MaxTokens > 0 {
		cm = cappedChatModel{cm: cm, max: cfg.MaxTokens}
	}
	return newNativeModel(cm), nil
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test -race -run TestCappedChatModel_AppendsMaxTokens ./internal/acl/`
Expected: PASS.

- [ ] **Step 5: Add the public option in `litellm.go`**

Add `MaxTokens` to the root `LiteLLMConfig` and the option:

```go
type LiteLLMConfig struct {
	APIKey    string
	BaseURL   string
	MaxTokens int
}
```

```go
// WithLLMMaxTokens caps the model's max output tokens per call (0 = provider
// default). Prevents over-long generations and provider 400s.
func WithLLMMaxTokens(n int) LiteLLMOption {
	return func(c *LiteLLMConfig) { c.MaxTokens = n }
}
```

And pass it through in `LiteLLM` where `acl.LiteLLMConfig{...}` is built:

```go
	return acl.NewLiteLLMModel(provider, modelID, acl.LiteLLMConfig{
		APIKey:    cfg.APIKey,
		BaseURL:   cfg.BaseURL,
		MaxTokens: cfg.MaxTokens,
	})
```

- [ ] **Step 6: Verify build + the capping test still pass**

Run: `go build ./... && go test -race -run TestCappedChatModel_AppendsMaxTokens ./internal/acl/`
Expected: build OK; PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/acl/model.go litellm.go internal/acl/model_test.go
git commit -m "feat(jess,acl): WithLLMMaxTokens caps litellm output tokens per call"
```

---

## Task 5: Final verification gate

**Files:** none (verification only)

- [ ] **Step 1: Format**

Run: `gofmt -l .`
Expected: no output. If any files listed, `gofmt -w` them, re-verify, and `git commit --amend` into the relevant task commit (or a small `chore: gofmt` commit).

- [ ] **Step 2: Vet + race tests + boundary**

Run:
```bash
go vet ./...
go test -race ./...
go test -race -run TestAgentcoreImportBoundary ./internal/acl/
```
Expected: all PASS. The boundary test confirms no new agentcore import escaped `internal/acl` (the new public APIs in `event`, `agent.go`, `litellm.go` stay vendor-free).

- [ ] **Step 3: Lint + license audit (if present)**

Run: `make lint && make license-audit`
Expected: 0 issues / clean.

- [ ] **Step 4: Confirm the public surface is vendor-free**

Run: `grep -rn "voocel/agentcore" event/ litellm.go agent.go session.go run.go || echo "PUBLIC SURFACE CLEAN"`
Expected: `PUBLIC SURFACE CLEAN` (the additions touched only vendor-free files plus `internal/acl`).

- [ ] **Step 5: Hand off to finishing-a-development-branch**

All four additions complete and verified. Proceed to `superpowers:finishing-a-development-branch` to push `feat/facade-additions-for-talon`, open a PR, and run the per-phase review loop, before starting sub-project 2 (the talon port).

---

## Self-Review

**Spec coverage:**
- Addition 1 (usage) → Task 1. ✓ (`event.Usage` + `RunSummary.Usage`, aggregated from `EventAgentEnd.NewMessages`; two-run non-cumulative test.)
- Addition 2 (history seeding) → Task 2. ✓ (`Agent.NewSessionWithHistory` → `Runtime.SetHistory` → `SetMessages`; `messagesToACAgent` slice helper; system-prompt rule in doc.)
- Addition 3 (provenance) → Task 3. ✓ (snapshot `memory.Source` in `start`, apply in `injectStream`; Source-reaches-tool AND Abort-still-cancels tests.)
- Addition 4 (max-tokens) → Task 4. ✓ (`WithLLMMaxTokens` + ACL `cappedChatModel` appending `ac.WithMaxTokens` on both methods; captured-options test.)
- Testing/enforcement (boundary, vet, race, lint, license) → Task 5. ✓

**Placeholder scan:** none — every step has real code/commands. The one fallback note (Task 1 Step 5, "if NewMessages drops usage…") is a contingency with a concrete alternative, not a missing requirement.

**Type consistency:** `event.Usage{Input,Output,Total}` is used identically in `usageFromACMessages` (Task 1). `memory.Source` zero-comparison (`!= (memory.Source{})`) is valid (all-string struct, comparable). `cappedChatModel{cm, max}` fields match its methods and the `NewLiteLLMModel` wrap. `messagesToACAgent`/`SetHistory`/`NewSessionWithHistory` names are consistent across Tasks 2 call sites. `ac.WithMaxTokens`/`ac.CallConfig.MaxTokens`/`ac.CallOption` match the confirmed v1.6.9 signatures. `streamFn` and the `Config{Model:..., Tools:...}` shape match the existing `runtime_test.go` helpers.
