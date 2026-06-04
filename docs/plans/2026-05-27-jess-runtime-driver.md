# jess Runtime Driver Implementation Plan (Phase 2b-runtime, ACL driver)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The ACL runtime driver that constructs and drives an `agentcore.Agent` from vendor-free jess config, exposing a `Run` whose events flow through `EventFromAC` onto an `event.Stream`, with one-run-at-a-time enforcement, ctx-based abort, and steer/follow-up/abort delegation.

**Architecture:** Phase 2b-runtime (driver half) of ADR 0001. New file `internal/acl/runtime.go` adds `Config`, `Runtime`, and `Run`. The root `jess` package (Agent/Session/Run wrappers + options) is a separate follow-on plan; this plan delivers and tests the driver in isolation using `model.Once` to drive a real `agentcore.Agent`.

**Tech Stack:** Go 1.26, `github.com/voocel/agentcore` (`ac`) inside `internal/acl` only. jess `event`/`message`/`model`/`tool`/`memory`/`skills`. Stdlib `context`, `sync`, `errors`.

**Verified agentcore mechanics (v1.6.9):**
- `NewAgent(...AgentOption)`; options: `WithModel(ChatModel)`, `WithSystemPrompt(string)`, `WithSystemBlocks([]SystemBlock)`, `WithTools(...Tool)`, `WithMaxTurns(int)`, `WithContextManager(ContextManager)`.
- `Agent.Prompt(input string) error` / `Continue() error` are async; they return `ErrAlreadyRunning` if a run is active.
- `Agent.Subscribe(func(Event)) func()` registers a listener invoked for every event (serialized on the run's `consumeLoop` goroutine); returns an unsubscribe func.
- `EventAgentEnd` carries `NewMessages []AgentMessage` and `Summary *RunSummary`. `AgentMessage` is an interface; concrete `Message` values type-assert to `ac.Message`.
- `Agent.Abort()` cancels the run's context. `Agent.Steer(AgentMessage)` / `FollowUp(AgentMessage)`.
- `memory.NewContextManager(store, recaller, memory.ContextManagerOptions{AgentID})` returns an `agentcore.ContextManager` (nil if store/recaller nil).

---

## File structure

| File | Responsibility |
|---|---|
| `internal/acl/runtime.go` | `Config`, `newACAgent`, `Runtime` (Prompt/Continue/Steer/FollowUp/Abort), `Run` (Events/Wait), `Result`, `ErrRunInProgress` |
| `internal/acl/runtime_test.go` | drive a real agentcore.Agent via model.Once: event sequence, Wait result, ErrRunInProgress, abort |

---

### Task 1: Config + agent construction

**Files:**
- Create: `internal/acl/runtime.go`
- Test: `internal/acl/runtime_test.go`

- [ ] **Step 1: Write the failing test** at `internal/acl/runtime_test.go`:

```go
package acl

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

// echoOnce is a model.Model (via model.Once) that returns a fixed assistant
// message, so a real agentcore.Agent can be driven without a network.
func echoOnce(text string) model.Model {
	return model.Once(false, func(_ context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		return &model.Response{
			Message:    message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: text}}},
			StopReason: "stop",
		}, nil
	})
}

func TestNewRuntime_RequiresModel(t *testing.T) {
	if _, err := NewRuntime(Config{}); err == nil {
		t.Fatal("expected error when Model is nil")
	}
	if _, err := NewRuntime(Config{Model: echoOnce("hi")}); err != nil {
		t.Fatalf("NewRuntime with a model: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/acl/ -run TestNewRuntime`
Expected: build failure (`undefined: NewRuntime`, `Config`).

- [ ] **Step 3: Write the implementation** at `internal/acl/runtime.go`:

```go
package acl

import (
	"errors"

	ac "github.com/voocel/agentcore"
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/skills"
)

// ErrRunInProgress is returned by Prompt/Continue when a run is already active
// on this Runtime. Use Steer or FollowUp to inject into the running loop.
var ErrRunInProgress = errors.New("acl: a run is already in progress")

// streamBuffer is the per-run event channel capacity. Matches agentcore's own
// EventStream buffer; backpressure beyond it blocks producers.
const streamBuffer = 128

// Config is the vendor-free configuration for a Runtime, built by the root jess
// package from its options. The Runtime translates it into an agentcore.Agent;
// all agentcore construction stays here in the ACL.
type Config struct {
	Model        model.Model     // required
	Tools        []tool.Tool     // standalone jess tools
	Skills       *skills.Set     // optional; contributes SystemBlocks + Tools
	SystemPrompt string          // optional base system prompt
	Store        memory.Store    // optional; with Recaller, wires the memory ContextManager
	Recaller     memory.Recaller // optional
	AgentID      string          // scopes memory recall
	MaxTurns     int             // 0 = agentcore default
}

// newACAgent builds an agentcore.Agent from a Config.
func newACAgent(cfg Config) (*ac.Agent, error) {
	if cfg.Model == nil {
		return nil, errors.New("acl: Config.Model is required")
	}
	opts := []ac.AgentOption{ac.WithModel(ToAC(cfg.Model))}

	tools := WrapTools(cfg.Tools)
	var sysBlocks []ac.SystemBlock
	if cfg.Skills != nil {
		sysBlocks = cfg.Skills.SystemBlocks()
		tools = append(tools, cfg.Skills.Tools()...)
	}
	if cfg.SystemPrompt != "" {
		opts = append(opts, ac.WithSystemPrompt(cfg.SystemPrompt))
	}
	if len(sysBlocks) > 0 {
		opts = append(opts, ac.WithSystemBlocks(sysBlocks))
	}
	if len(tools) > 0 {
		opts = append(opts, ac.WithTools(tools...))
	}
	if cfg.MaxTurns > 0 {
		opts = append(opts, ac.WithMaxTurns(cfg.MaxTurns))
	}
	if cfg.Store != nil && cfg.Recaller != nil {
		cm := memory.NewContextManager(cfg.Store, cfg.Recaller, memory.ContextManagerOptions{AgentID: cfg.AgentID})
		if cm != nil {
			opts = append(opts, ac.WithContextManager(cm))
		}
	}
	return ac.NewAgent(opts...), nil
}
```

Note: this file references `model`, `tool` packages — add their imports (`"github.com/guygrigsby/jess/model"`, `"github.com/guygrigsby/jess/tool"`) to the import block. `Runtime`/`NewRuntime` are added in Task 3; this task only needs `Config` + `newACAgent` to compile, but the test calls `NewRuntime`. To keep Task 1 self-contained, ALSO add the minimal `Runtime` shell here so the test compiles:

```go
// Runtime drives a single agentcore.Agent. Fleshed out in later steps.
type Runtime struct {
	agent *ac.Agent
	mu    sync.Mutex
	running bool
}

// NewRuntime builds a Runtime from cfg.
func NewRuntime(cfg Config) (*Runtime, error) {
	agent, err := newACAgent(cfg)
	if err != nil {
		return nil, err
	}
	return &Runtime{agent: agent}, nil
}
```

Add `"sync"` to imports.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/acl/ -run TestNewRuntime`
Expected: PASS. Then `go test ./internal/acl/`, `gofmt -l internal/acl/`, `go vet ./internal/acl/`.

If `skills.Set.SystemBlocks()`/`Tools()` or `memory` symbols don't resolve, confirm the import paths (`github.com/guygrigsby/jess/skills`, `.../memory`) and method names against the current source; adapt minimally.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/runtime.go internal/acl/runtime_test.go
git commit -m "feat(acl): runtime Config + agentcore.Agent construction"
```

---

### Task 2: Run handle (Events, Wait, result capture)

**Files:**
- Modify: `internal/acl/runtime.go`
- Test: `internal/acl/runtime_test.go` (append)

- [ ] **Step 1: Append the failing test**:

```go
import "github.com/guygrigsby/jess/event" // add to the test import block

func TestRun_WaitReturnsResultAfterDone(t *testing.T) {
	// A Run whose done is already closed returns its captured result.
	r := newRun()
	r.messages = []message.Message{{Role: message.RoleAssistant}}
	r.summary = &event.RunSummary{Turns: 1, EndReason: "stop"}
	close(r.done)

	res, err := r.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if len(res.Messages) != 1 || res.Summary == nil || res.Summary.EndReason != "stop" {
		t.Fatalf("result = %+v", res)
	}
}

func TestMessagesFromACAgent(t *testing.T) {
	in := []ac.AgentMessage{
		ac.Message{Role: ac.RoleAssistant, Content: []ac.ContentBlock{ac.TextBlock("hi")}},
	}
	got := messagesFromACAgent(in)
	if len(got) != 1 || got[0].Text() != "hi" {
		t.Fatalf("got %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/acl/ -run 'TestRun_Wait|TestMessagesFromACAgent'`
Expected: build failure (`undefined: newRun`, `messagesFromACAgent`).

- [ ] **Step 3: Append the implementation** to `internal/acl/runtime.go` (add `"github.com/guygrigsby/jess/event"`, `"github.com/guygrigsby/jess/message"` to imports):

```go
// Result is the outcome of a finished Run.
type Result struct {
	Messages []message.Message
	Summary  *event.RunSummary
}

// Run is the handle for one Prompt/Continue cycle. Events streams live events;
// Wait blocks for the final result.
type Run struct {
	stream *event.Stream
	done   chan struct{}

	mu       sync.Mutex
	messages []message.Message
	summary  *event.RunSummary
	err      error
}

func newRun() *Run {
	return &Run{stream: event.NewStream(streamBuffer), done: make(chan struct{})}
}

// Events returns the live event channel, closed when the run ends.
func (r *Run) Events() <-chan event.Event { return r.stream.Events() }

// Wait blocks until the run finishes and returns its final messages, summary,
// and any run error.
func (r *Run) Wait() (Result, error) {
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return Result{Messages: r.messages, Summary: r.summary}, r.err
}

// captureEnd records the final messages/summary/error from an EventAgentEnd.
func (r *Run) captureEnd(ev ac.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = messagesFromACAgent(ev.NewMessages)
	r.summary = summaryFromAC(ev.Summary)
	r.err = ev.Err
}

// messagesFromACAgent converts agentcore AgentMessages to jess messages,
// dropping any that are not concrete ac.Message values.
func messagesFromACAgent(msgs []ac.AgentMessage) []message.Message {
	out := make([]message.Message, 0, len(msgs))
	for _, m := range msgs {
		if acm, ok := m.(ac.Message); ok {
			out = append(out, messageFromAC(acm))
		}
	}
	return out
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/acl/ -run 'TestRun_Wait|TestMessagesFromACAgent'`
Expected: PASS. Then `go test ./internal/acl/`, `gofmt -l internal/acl/`, `go vet ./internal/acl/`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/runtime.go internal/acl/runtime_test.go
git commit -m "feat(acl): Run handle with Events/Wait and result capture"
```

---

### Task 3: Prompt/Continue — subscribe, stream, ctx-abort, one-run guard

This is the concurrency core. `start` holds the Runtime lock across the whole
setup so the AgentEnd listener (which clears `running`) cannot run until
`running=true` is established.

**Files:**
- Modify: `internal/acl/runtime.go`
- Test: `internal/acl/runtime_test.go` (append)

- [ ] **Step 1: Append the failing tests**:

```go
func TestRuntime_PromptStreamsAndCompletes(t *testing.T) {
	rt, _ := NewRuntime(Config{Model: echoOnce("hello")})
	run, err := rt.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var kinds []event.EventKind
	for ev := range run.Events() {
		kinds = append(kinds, ev.Kind)
	}
	// must start with run_start and end with run_end
	if len(kinds) < 2 || kinds[0] != event.KindRunStart || kinds[len(kinds)-1] != event.KindRunEnd {
		t.Fatalf("event kinds = %v", kinds)
	}
	res, err := run.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Summary == nil {
		t.Fatal("expected a run summary")
	}
}

func TestRuntime_SecondPromptWhileRunningErrors(t *testing.T) {
	// A model that blocks until the test lets it finish keeps the run active.
	release := make(chan struct{})
	blocking := model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &model.Response{Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}, nil
	})
	rt, _ := NewRuntime(Config{Model: blocking})
	run, err := rt.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("first Prompt: %v", err)
	}
	if _, err := rt.Prompt(context.Background(), "again"); err != ErrRunInProgress {
		t.Fatalf("second Prompt err = %v, want ErrRunInProgress", err)
	}
	close(release)
	if _, err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/acl/ -run TestRuntime_Prompt`
Expected: build failure (`rt.Prompt` undefined).

- [ ] **Step 3: Append the implementation** to `internal/acl/runtime.go` (add `"context"` to imports):

```go
// Prompt starts a new run with the given input. Returns ErrRunInProgress if a
// run is already active.
func (rt *Runtime) Prompt(ctx context.Context, input string) (*Run, error) {
	return rt.start(ctx, func() error { return rt.agent.Prompt(input) })
}

// Continue resumes the conversation without new input.
func (rt *Runtime) Continue(ctx context.Context) (*Run, error) {
	return rt.start(ctx, func() error { return rt.agent.Continue() })
}

func (rt *Runtime) start(ctx context.Context, startFn func() error) (*Run, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.running {
		return nil, ErrRunInProgress
	}
	run := newRun()

	var unsub func()
	unsub = rt.agent.Subscribe(func(ev ac.Event) {
		if ev.Type == ac.EventAgentEnd {
			run.captureEnd(ev)
		}
		if jev, ok := EventFromAC(ev); ok {
			run.stream.Send(jev)
		}
		if ev.Type == ac.EventAgentEnd {
			run.stream.Close()
			unsub()
			rt.mu.Lock()
			rt.running = false
			rt.mu.Unlock()
			close(run.done)
		}
	})

	if err := startFn(); err != nil {
		unsub()
		run.stream.Close()
		close(run.done)
		if errors.Is(err, ac.ErrAlreadyRunning) {
			return nil, ErrRunInProgress
		}
		return nil, err
	}
	rt.running = true

	// Bridge jess's ctx to agentcore's abort: cancelling ctx aborts the run.
	go func() {
		select {
		case <-ctx.Done():
			rt.agent.Abort()
		case <-run.done:
		}
	}()
	return run, nil
}
```

Concurrency note for the reviewer: `start` holds `rt.mu` for its whole body. The AgentEnd branch of the listener calls `rt.mu.Lock()` to clear `running`, so it blocks until `start` returns — guaranteeing `running=true` is set before it can be cleared. The listener's `stream.Send`/`Close`/`close(done)` do not take `rt.mu`, but `close(run.done)` happens after the `rt.mu` section, so `Wait` cannot return before `start` finishes. No lost-wakeup, no double-close (one AgentEnd per run; unsub prevents further callbacks).

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./internal/acl/ -run TestRuntime_Prompt`
Expected: PASS (run twice for stability). Then `go test -race ./internal/acl/`, `gofmt -l internal/acl/`, `go vet ./internal/acl/`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/runtime.go internal/acl/runtime_test.go
git commit -m "feat(acl): Runtime Prompt/Continue with stream wiring + ctx-abort"
```

---

### Task 4: Steer / FollowUp / Abort delegation

**Files:**
- Modify: `internal/acl/runtime.go`
- Test: `internal/acl/runtime_test.go` (append)

- [ ] **Step 1: Append the failing test**:

```go
func TestRuntime_AbortStopsRun(t *testing.T) {
	blocking := model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		<-ctx.Done() // never returns until aborted
		return nil, ctx.Err()
	})
	rt, _ := NewRuntime(Config{Model: blocking})
	run, err := rt.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	rt.Abort()
	// Events channel must close (run ends) without hanging.
	for range run.Events() {
	}
	res, _ := run.Wait()
	if res.Summary != nil && res.Summary.EndReason != "aborted" && res.Summary.EndReason != "error" {
		t.Logf("end reason after abort: %q", res.Summary.EndReason) // accept aborted/error
	}
}

func TestRuntime_SteerFollowUpDoNotPanic(t *testing.T) {
	rt, _ := NewRuntime(Config{Model: echoOnce("x")})
	// Smoke test: queue operations are safe to call.
	rt.Steer(message.UserText("steer"))
	rt.FollowUp(message.UserText("later"))
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/acl/ -run 'TestRuntime_Abort|TestRuntime_Steer'`
Expected: build failure (`rt.Abort`, `rt.Steer` undefined).

- [ ] **Step 3: Append the implementation** to `internal/acl/runtime.go`:

```go
// Steer injects a message into the running loop at the next safe point (soft
// preemption). It is a no-op shape if no run is active (agentcore queues it).
func (rt *Runtime) Steer(msg message.Message) {
	rt.agent.Steer(messagesToAC([]message.Message{msg})[0])
}

// FollowUp queues a message to be processed after the current run finishes.
func (rt *Runtime) FollowUp(msg message.Message) {
	rt.agent.FollowUp(messagesToAC([]message.Message{msg})[0])
}

// Abort hard-cancels the current run (context cancellation). Queued steer and
// follow-up messages are processed on continuation.
func (rt *Runtime) Abort() { rt.agent.Abort() }
```

Note: `messagesToAC([]message.Message{msg})[0]` is safe for user/assistant messages (1:1). Steer/FollowUp are meant for user messages; a tool-result message would expand and `[0]` would still be valid (the first expanded message). Callers pass user messages.

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./internal/acl/ -run 'TestRuntime_Abort|TestRuntime_Steer'`
Expected: PASS (the abort test must not hang). Then `go test -race ./internal/acl/`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/runtime.go internal/acl/runtime_test.go
git commit -m "feat(acl): Steer/FollowUp/Abort delegation"
```

---

### Task 5: phase gate

**Files:** none (verification).

- [ ] **Step 1: Boundary holds**

Run: `go test ./internal/acl/ -run TestAgentcoreImportBoundary`
Expected: PASS (all new agentcore use is in `internal/acl`).

- [ ] **Step 2: Full race suite**

Run: `go test -race ./...`
Expected: all PASS. Run the acl package a few times (`go test -race -count=3 ./internal/acl/`) to shake the Prompt/abort concurrency.

- [ ] **Step 3: Lint + license**

Run: `make lint` (expect `0 issues.`) and `make license-audit` (expect pass).

- [ ] **Step 4: Commit (if any fixes)**

```bash
git add -A && git commit -m "chore: phase 2b-runtime driver gate" --allow-empty
```

---

## Self-review

**Spec coverage (Phase 2b-runtime driver):**
- `Config` + agentcore.Agent construction (model, tools, skills, system prompt, memory CM, max turns) — Task 1. ✓
- `Run` (Events/Wait) + result capture from EventAgentEnd — Task 2. ✓
- Prompt/Continue: subscribe -> EventFromAC -> Stream, one-run guard (ErrRunInProgress), ctx->Abort bridge — Task 3. ✓
- Steer/FollowUp/Abort delegation — Task 4. ✓
- Boundary + race gate — Task 5. ✓
- Out of scope (root jess wrapper plan): `jess.New`/`Agent`/`Session`, options (`WithModel`/`WithMemory`/`WithSkills`/`WithTools`/`WithAgentID`/`WithSystemPrompt`/`WithMaxTurns`), default-session convenience, `jess.LiteLLM` options (#25). Memory remember/recall tool auto-registration deferred (those tools implement agentcore.Tool today; retyped to jess/tool.Tool in Phase 4).

**Placeholder scan:** none. Task 1 includes a minimal `Runtime`/`NewRuntime` shell so the test compiles; Task 3 fleshes out the methods (not a placeholder — explicitly staged).

**Type consistency:** `Config`, `newACAgent`, `Runtime`, `NewRuntime`, `Run`, `newRun`, `Result`, `captureEnd`, `messagesFromACAgent`, `start`, `Prompt`, `Continue`, `Steer`, `FollowUp`, `Abort`, `ErrRunInProgress`, `streamBuffer` are consistent across tasks. Reuses `EventFromAC`/`summaryFromAC`/`messageFromAC`/`messagesToAC`/`ToAC`/`WrapTools` (Phase 2a/2b-model) and `event`/`message`/`model`/`tool`/`memory`/`skills` packages.
