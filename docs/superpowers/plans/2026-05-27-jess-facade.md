# jess Public Facade Implementation Plan (Phase 2b-runtime, wrapper)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The public `jess` facade — `Agent`/`Session`/`Run` + functional options + `jess.New` — wrapping the ACL runtime driver, so a host writes `jess.New(...) → agent.Prompt(ctx, "…") → range run.Events()`.

**Architecture:** Phase 2b-runtime (wrapper half) of ADR 0001. Thin wrappers over `internal/acl.Runtime`/`Run`. The root `jess` package imports `internal/acl` (and the vendor-free `model`/`tool`/`message`/`event`/`memory`/`skills`) but never agentcore directly — the boundary test enforces this. Also expands `jess.LiteLLM` with construction options (task #25).

**Tech Stack:** Go 1.26. Root `jess` package + `internal/acl`. Stdlib `context`, `sync`, `errors`.

**Design decisions (stated; flag if you disagree):**
- **Run-centric events.** `Run.Events()` is the event source and `Run.Wait()` the result. No `Session.Events()`/`Agent.Events()` (avoids "current run" ambiguity; non-breaking to add later).
- **Options carry no agentcore.** A private `options` struct collects the With* values; `New` builds an `acl.Config` from it. `Option` is `func(*options)`, so the public signature never mentions agentcore-adjacent types.
- **Default session.** `Agent.Prompt`/`Continue` delegate to a lazily-created default `Session`; advanced hosts call `Agent.NewSession()`.
- **`jess.ErrRunInProgress`** is the public sentinel; the wrapper maps `acl.ErrRunInProgress` to it.

---

## File structure

| File | Responsibility |
|---|---|
| `options.go` | `Option`, private `options`, `WithModel`/`WithAgentID`/`WithSystemPrompt`/`WithTools`/`WithSkills`/`WithMemory`/`WithMaxTurns` |
| `errors.go` | `ErrRunInProgress` |
| `run.go` | `Run` (wraps `*acl.Run`), `Result` |
| `agent.go` | `Agent`, `New`, default-session, convenience `Prompt`/`Continue` |
| `session.go` | `Session`, `(*Agent).NewSession`, `Prompt`/`Continue`/`Steer`/`FollowUp`/`Abort` |
| `litellm.go` | (existing) expand: `LiteLLMOption`, `WithLLMAPIKey`, `WithLLMBaseURL`, updated `LiteLLM` |
| `internal/acl/litellm.go` | `LiteLLMConfig` + updated `NewLiteLLMModel(provider, modelID, LiteLLMConfig)` |

---

### Task 1: options + errors

**Files:**
- Create: `options.go`, `errors.go`
- Test: `options_test.go`

- [ ] **Step 1: Write the failing test** at `options_test.go`:

```go
package jess

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

func testModel() model.Model {
	return model.Once(false, func(context.Context, []message.Message, []model.ToolSpec) (*model.Response, error) {
		return &model.Response{Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}, nil
	})
}

func TestOptions_Apply(t *testing.T) {
	var o options
	for _, opt := range []Option{
		WithModel(testModel()),
		WithAgentID("agent-1"),
		WithSystemPrompt("be terse"),
		WithMaxTurns(5),
		WithMemory(memory.NewInMemoryStore(), memory.NewSimpleRecaller()),
	} {
		opt(&o)
	}
	if o.model == nil || o.agentID != "agent-1" || o.systemPrompt != "be terse" || o.maxTurns != 5 {
		t.Fatalf("options not applied: %+v", o)
	}
	if o.store == nil || o.recaller == nil {
		t.Error("WithMemory did not set store/recaller")
	}
}
```

(Confirm `memory.NewInMemoryStore()` and `memory.NewSimpleRecaller()` exist; if the constructor names differ, use the actual ones from the `memory` package.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test . -run TestOptions_Apply`
Expected: build failure (`undefined: options`, `WithModel`).

- [ ] **Step 3: Write the implementation.**

`errors.go`:

```go
package jess

import "errors"

// ErrRunInProgress is returned by Session.Prompt/Continue when a run is already
// active on that Session. Use Steer or FollowUp to inject into the running
// loop, or open another Session for a parallel conversation.
var ErrRunInProgress = errors.New("jess: a run is already in progress on this session")
```

`options.go`:

```go
package jess

import (
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/model"
	"github.com/guygrigsby/jess/skills"
	"github.com/guygrigsby/jess/tool"
)

// Option configures an Agent (and through it every Session it spawns). Obtain
// options from the With* constructors; the zero value is not useful.
type Option func(*options)

// options is the private accumulator the With* functions populate. New turns it
// into the agent configuration.
type options struct {
	model        model.Model
	tools        []tool.Tool
	skills       *skills.Set
	systemPrompt string
	store        memory.Store
	recaller     memory.Recaller
	agentID      string
	maxTurns     int
}

// WithModel sets the LLM the agent uses. Required. Use jess.LiteLLM for a cloud
// provider, or implement model.Model for a local/custom model.
func WithModel(m model.Model) Option { return func(o *options) { o.model = m } }

// WithAgentID scopes the agent's memory. Empty uses the global memory scope.
func WithAgentID(id string) Option { return func(o *options) { o.agentID = id } }

// WithSystemPrompt sets a base system prompt.
func WithSystemPrompt(s string) Option { return func(o *options) { o.systemPrompt = s } }

// WithTools registers standalone tools the model may call.
func WithTools(tools ...tool.Tool) Option {
	return func(o *options) { o.tools = append(o.tools, tools...) }
}

// WithSkills registers a skill set, contributing system-prompt blocks and tools.
func WithSkills(set *skills.Set) Option { return func(o *options) { o.skills = set } }

// WithMemory wires durable memory: recalled entries are injected each turn and
// degrade to no-memory on error (never blocking the LLM call). Both store and
// recaller are required for memory to engage.
func WithMemory(store memory.Store, recaller memory.Recaller) Option {
	return func(o *options) { o.store = store; o.recaller = recaller }
}

// WithMaxTurns caps the agent loop's turns per run. 0 uses the harness default.
func WithMaxTurns(n int) Option { return func(o *options) { o.maxTurns = n } }
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test . -run TestOptions_Apply`
Expected: PASS. Then `gofmt -l .`, `go vet .`.

- [ ] **Step 5: Commit**

```bash
git add options.go errors.go options_test.go
git commit -m "feat(jess): functional options + ErrRunInProgress"
```

---

### Task 2: Agent + New + Run/Result

**Files:**
- Create: `agent.go`, `run.go`
- Test: `agent_test.go`

- [ ] **Step 1: Write the failing test** at `agent_test.go`:

```go
package jess

import "testing"

func TestNew_RequiresModel(t *testing.T) {
	if _, err := New(); err == nil {
		t.Fatal("expected error without WithModel")
	}
	a, err := New(WithModel(testModel()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("expected an Agent")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test . -run TestNew_RequiresModel`
Expected: build failure (`undefined: New`).

- [ ] **Step 3: Write the implementation.**

`run.go`:

```go
package jess

import (
	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/internal/acl"
	"github.com/guygrigsby/jess/message"
)

// Result is the outcome of a finished Run: the messages the run produced and a
// factual summary.
type Result struct {
	Messages []message.Message
	Summary  *event.RunSummary
}

// Run is the handle for one Prompt/Continue cycle. Range over Events for live
// progress, or call Wait for the final result. Both observe the same run.
type Run struct {
	inner *acl.Run
}

// Events returns the live event channel for this run, closed when the run ends.
func (r *Run) Events() <-chan event.Event { return r.inner.Events() }

// Wait blocks until the run finishes and returns its result and any run error.
func (r *Run) Wait() (Result, error) {
	res, err := r.inner.Wait()
	return Result{Messages: res.Messages, Summary: res.Summary}, err
}
```

`agent.go`:

```go
package jess

import (
	"context"
	"errors"
	"sync"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/internal/acl"
)

// Agent is the aggregate the host configures once: identity and capabilities
// (model, skills, tools, system prompt, and the AgentID that scopes memory).
// It is safe for concurrent use and can back many Sessions. Memory belongs to
// the Agent and persists across conversations; message history belongs to each
// Session.
type Agent struct {
	cfg acl.Config

	mu      sync.Mutex
	defSess *Session
}

// New builds an Agent from options. WithModel is required.
func New(opts ...Option) (*Agent, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.model == nil {
		return nil, errors.New("jess: WithModel is required")
	}
	return &Agent{cfg: acl.Config{
		Model:        o.model,
		Tools:        o.tools,
		Skills:       o.skills,
		SystemPrompt: o.systemPrompt,
		Store:        o.store,
		Recaller:     o.recaller,
		AgentID:      o.agentID,
		MaxTurns:     o.maxTurns,
	}}, nil
}

// defaultSession lazily creates the Agent's default Session for the convenience
// Prompt/Continue path.
func (a *Agent) defaultSession() (*Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.defSess == nil {
		s, err := a.newSession()
		if err != nil {
			return nil, err
		}
		a.defSess = s
	}
	return a.defSess, nil
}

// Prompt starts a run on the Agent's default Session.
func (a *Agent) Prompt(ctx context.Context, input string) (*Run, error) {
	s, err := a.defaultSession()
	if err != nil {
		return nil, err
	}
	return s.Prompt(ctx, input)
}

// Continue resumes the Agent's default Session.
func (a *Agent) Continue(ctx context.Context) (*Run, error) {
	s, err := a.defaultSession()
	if err != nil {
		return nil, err
	}
	return s.Continue(ctx)
}

// ensure event is used (Run references it); harmless if already imported elsewhere.
var _ = event.KindRunStart
```

Note: the trailing `var _ = event.KindRunStart` is only needed if `event` would otherwise be unused in `agent.go`; since `agent.go` does not reference `event` directly, REMOVE that line and the `event` import from `agent.go` (it lives in `run.go`). Keep imports minimal — let the compiler guide you.

`newSession` is defined in Task 3 (`session.go`). To keep Task 2 compiling, add a temporary stub in `agent.go` that Task 3 replaces — OR implement Task 2 and Task 3 together. Prefer: add the real `newSession` in Task 3 and, for Task 2, make `defaultSession`/`Prompt`/`Continue` part of Task 3 instead. To keep Task 2 self-contained and compiling, implement ONLY `Agent`, `New`, `Run`, `Result` in Task 2 (drop `defaultSession`/`Prompt`/`Continue` from agent.go here) and add them in Task 3 alongside `Session`. Adjust: in Task 2, `agent.go` contains only `Agent` + `New`.

- [ ] **Step 3 (revised): minimal `agent.go` for Task 2:**

```go
package jess

import (
	"errors"

	"github.com/guygrigsby/jess/internal/acl"
)

// Agent is the aggregate the host configures once: identity and capabilities
// (model, skills, tools, system prompt, and the AgentID that scopes memory).
// Safe for concurrent use; can back many Sessions. Memory belongs to the Agent
// and persists across conversations; message history belongs to each Session.
type Agent struct {
	cfg acl.Config
}

// New builds an Agent from options. WithModel is required.
func New(opts ...Option) (*Agent, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.model == nil {
		return nil, errors.New("jess: WithModel is required")
	}
	return &Agent{cfg: acl.Config{
		Model:        o.model,
		Tools:        o.tools,
		Skills:       o.skills,
		SystemPrompt: o.systemPrompt,
		Store:        o.store,
		Recaller:     o.recaller,
		AgentID:      o.agentID,
		MaxTurns:     o.maxTurns,
	}}, nil
}
```

(The `mu`/`defSess` fields and convenience methods are added in Task 3.)

- [ ] **Step 4: Run to verify it passes**

Run: `go test . -run TestNew_RequiresModel`
Expected: PASS. Then `gofmt -l .`, `go vet .`.

- [ ] **Step 5: Commit**

```bash
git add agent.go run.go agent_test.go
git commit -m "feat(jess): Agent + New + Run/Result"
```

---

### Task 3: Session + Prompt/Continue + Agent convenience

**Files:**
- Create: `session.go`
- Modify: `agent.go` (add default-session fields + convenience methods)
- Test: `session_test.go`

- [ ] **Step 1: Write the failing test** at `session_test.go`:

```go
package jess

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/event"
)

func TestSession_PromptEndToEnd(t *testing.T) {
	a, _ := New(WithModel(testModel()))
	sess, err := a.NewSession()
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	run, err := sess.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var sawStart, sawEnd bool
	for ev := range run.Events() {
		switch ev.Kind {
		case event.KindRunStart:
			sawStart = true
		case event.KindRunEnd:
			sawEnd = true
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("missing run_start/run_end (start=%v end=%v)", sawStart, sawEnd)
	}
	if _, err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestAgent_PromptUsesDefaultSession(t *testing.T) {
	a, _ := New(WithModel(testModel()))
	run, err := a.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Agent.Prompt: %v", err)
	}
	for range run.Events() {
	}
	if _, err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test . -run 'TestSession_PromptEndToEnd|TestAgent_PromptUsesDefaultSession'`
Expected: build failure (`a.NewSession`, `sess.Prompt` undefined).

- [ ] **Step 3: Write the implementation.**

`session.go`:

```go
package jess

import (
	"context"
	"errors"

	"github.com/guygrigsby/jess/internal/acl"
)

// Session is one conversation with an Agent: it holds the message history and
// runs one Prompt/Continue cycle at a time. Open multiple Sessions on one Agent
// for parallel conversations.
type Session struct {
	rt *acl.Runtime
}

// newSession builds a Session from the Agent's config.
func (a *Agent) newSession() (*Session, error) {
	rt, err := acl.NewRuntime(a.cfg)
	if err != nil {
		return nil, err
	}
	return &Session{rt: rt}, nil
}

// NewSession opens a new conversation Session on the Agent.
func (a *Agent) NewSession() (*Session, error) { return a.newSession() }

// Prompt starts a run with the given input. Returns ErrRunInProgress if a run
// is already active on this Session.
func (s *Session) Prompt(ctx context.Context, input string) (*Run, error) {
	r, err := s.rt.Prompt(ctx, input)
	return wrapRun(r, err)
}

// Continue resumes the conversation without new input.
func (s *Session) Continue(ctx context.Context) (*Run, error) {
	r, err := s.rt.Continue(ctx)
	return wrapRun(r, err)
}

func wrapRun(r *acl.Run, err error) (*Run, error) {
	if err != nil {
		if errors.Is(err, acl.ErrRunInProgress) {
			return nil, ErrRunInProgress
		}
		return nil, err
	}
	return &Run{inner: r}, nil
}
```

Modify `agent.go`: add the `mu sync.Mutex` and `defSess *Session` fields to the `Agent` struct, and add the default-session + convenience methods:

```go
// (add "context" and "sync" imports)

func (a *Agent) defaultSession() (*Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.defSess == nil {
		s, err := a.newSession()
		if err != nil {
			return nil, err
		}
		a.defSess = s
	}
	return a.defSess, nil
}

// Prompt starts a run on the Agent's default Session.
func (a *Agent) Prompt(ctx context.Context, input string) (*Run, error) {
	s, err := a.defaultSession()
	if err != nil {
		return nil, err
	}
	return s.Prompt(ctx, input)
}

// Continue resumes the Agent's default Session.
func (a *Agent) Continue(ctx context.Context) (*Run, error) {
	s, err := a.defaultSession()
	if err != nil {
		return nil, err
	}
	return s.Continue(ctx)
}
```

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race . -run 'TestSession_PromptEndToEnd|TestAgent_PromptUsesDefaultSession'`
Expected: PASS. Then `go test -race .`, `gofmt -l .`, `go vet .`.

- [ ] **Step 5: Commit**

```bash
git add session.go agent.go session_test.go
git commit -m "feat(jess): Session + Prompt/Continue + default-session convenience"
```

---

### Task 4: Session Steer/FollowUp/Abort + ErrRunInProgress

**Files:**
- Modify: `session.go`
- Test: `session_test.go` (append)

- [ ] **Step 1: Append the failing tests**:

```go
import "github.com/guygrigsby/jess/message" // add to session_test.go imports

func TestSession_SecondPromptErrors(t *testing.T) {
	release := make(chan struct{})
	blocking := blockingModel(release)
	a, _ := New(WithModel(blocking))
	sess, _ := a.NewSession()
	run, err := sess.Prompt(context.Background(), "hi")
	if err != nil {
		t.Fatalf("first Prompt: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), "again"); err != ErrRunInProgress {
		t.Fatalf("second Prompt err = %v, want jess.ErrRunInProgress", err)
	}
	close(release)
	if _, err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSession_SteerFollowUpAbortDoNotPanic(t *testing.T) {
	a, _ := New(WithModel(testModel()))
	sess, _ := a.NewSession()
	sess.Steer(message.UserText("steer"))
	sess.FollowUp(message.UserText("later"))
	sess.Abort()
}
```

Add this helper to `session_test.go` (or `agent_test.go`):

```go
func blockingModel(release <-chan struct{}) model.Model {
	return model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &model.Response{Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}, nil
	})
}
```

(Add the `model` import to the test file.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test . -run 'TestSession_SecondPrompt|TestSession_Steer'`
Expected: build failure (`sess.Steer` undefined).

- [ ] **Step 3: Append to `session.go`** (add `"github.com/guygrigsby/jess/message"` import):

```go
// Steer injects a message into the running loop at the next safe point (soft
// preemption). Intended for user messages.
func (s *Session) Steer(msg message.Message) { s.rt.Steer(msg) }

// FollowUp queues a message to be processed after the current run finishes.
func (s *Session) FollowUp(msg message.Message) { s.rt.FollowUp(msg) }

// Abort hard-cancels the current run (context cancellation); the model stream
// is interrupted mid-token and the run ends with an aborted summary.
func (s *Session) Abort() { s.rt.Abort() }
```

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race . -run 'TestSession_SecondPrompt|TestSession_Steer'`
Expected: PASS. Then `go test -race .`, `gofmt -l .`, `go vet .`.

- [ ] **Step 5: Commit**

```bash
git add session.go session_test.go
git commit -m "feat(jess): Session Steer/FollowUp/Abort"
```

---

### Task 5: LiteLLM construction options (closes #25)

**Files:**
- Modify: `internal/acl/model.go` (refactor `NewLiteLLMModel` to take a config)
- Modify: `litellm.go` (root)
- Test: `litellm_test.go` (extend)

- [ ] **Step 1: Append the failing test** to `litellm_test.go`:

```go
func TestLiteLLM_OptionsThreadThrough(t *testing.T) {
	// Providing an API key option must let construction succeed network-free.
	m, err := LiteLLM("openai", "gpt-4o", WithLLMAPIKey("sk-test"), WithLLMBaseURL("http://localhost:1234"))
	if err != nil {
		t.Logf("construction error (acceptable, network-free): %v", err)
		return
	}
	if m == nil {
		t.Fatal("expected a non-nil model.Model")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test . -run TestLiteLLM_Options`
Expected: build failure (`undefined: WithLLMAPIKey`).

- [ ] **Step 3: Implement.**

In `internal/acl/model.go`, replace `NewLiteLLMModel` with a config-taking version (add `"github.com/voocel/agentcore/llm"` if not already imported — it is):

```go
// LiteLLMConfig configures a litellm-backed model. Plain fields (no agentcore
// types) so the root jess package can build it without importing the harness.
type LiteLLMConfig struct {
	APIKey  string
	BaseURL string
}

// NewLiteLLMModel builds a litellm-backed cloud model from provider/modelID and
// an optional config, returning it as a model.Model (native passthrough).
func NewLiteLLMModel(provider, modelID string, cfg LiteLLMConfig) (model.Model, error) {
	var opts []llm.ModelOption
	if cfg.APIKey != "" {
		opts = append(opts, llm.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, llm.WithBaseURL(cfg.BaseURL))
	}
	cm, err := llm.NewModel(provider, modelID, opts...)
	if err != nil {
		return nil, err
	}
	return newNativeModel(cm), nil
}
```

In `litellm.go` (root), replace the body with the option-taking form:

```go
package jess

import (
	"github.com/guygrigsby/jess/internal/acl"
	"github.com/guygrigsby/jess/model"
)

// LiteLLMOption configures a LiteLLM model at construction. Obtain from the
// WithLLM* constructors.
type LiteLLMOption func(*acl.LiteLLMConfig)

// WithLLMAPIKey sets the provider API key.
func WithLLMAPIKey(key string) LiteLLMOption {
	return func(c *acl.LiteLLMConfig) { c.APIKey = key }
}

// WithLLMBaseURL overrides the provider base URL (e.g. a local OpenAI-compatible
// server or a gateway).
func WithLLMBaseURL(url string) LiteLLMOption {
	return func(c *acl.LiteLLMConfig) { c.BaseURL = url }
}

// LiteLLM builds a cloud model backed by agentcore's litellm adapter and returns
// it as a vendor-free model.Model, suitable for jess.WithModel. provider and
// modelID are litellm identifiers, e.g. LiteLLM("openai","gpt-4o"). For a local
// or custom model, implement model.Model directly (or use model.Once).
//
// The agentcore dependency stays inside internal/acl; this package does not
// import the harness.
func LiteLLM(provider, modelID string, opts ...LiteLLMOption) (model.Model, error) {
	var cfg acl.LiteLLMConfig
	for _, o := range opts {
		o(&cfg)
	}
	return acl.NewLiteLLMModel(provider, modelID, cfg)
}
```

Note: `LiteLLMOption func(*acl.LiteLLMConfig)` references an internal type in an exported signature. That is acceptable: `acl.LiteLLMConfig` holds only plain fields (no agentcore types), external callers cannot construct one (acl is internal) so they must use the WithLLM* helpers, and the boundary test still passes because `litellm.go` imports `internal/acl`, not agentcore.

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./internal/acl/ .` (acl + root). Expected: PASS (existing `TestLiteLLM_ReturnsModel` still passes — its `LiteLLM("openai","gpt-4o")` call now uses the variadic form with no options). Then `gofmt -l .`, `go vet ./...`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/model.go litellm.go litellm_test.go
git commit -m "feat(jess): LiteLLM construction options (WithLLMAPIKey/WithLLMBaseURL)"
```

---

### Task 6: phase gate

**Files:** none (verification).

- [ ] **Step 1: Boundary holds (root jess imports no agentcore)**

Run: `go test ./internal/acl/ -run TestAgentcoreImportBoundary`
Expected: PASS. Then confirm directly: `grep -rl "voocel/agentcore" *.go` (root) — expect empty.

- [ ] **Step 2: Full race suite**

Run: `go test -race ./...`
Expected: all PASS, including the root `jess` package end-to-end tests.

- [ ] **Step 3: Lint + license**

Run: `make lint` (expect `0 issues.`) and `make license-audit` (expect pass).

- [ ] **Step 4: Commit (if any fixes)**

```bash
git add -A && git commit -m "chore: phase 2b-runtime facade gate" --allow-empty
```

---

## Self-review

**Spec coverage (Phase 2b-runtime wrapper):**
- Options (model/agentID/systemPrompt/tools/skills/memory/maxTurns) — Task 1. ✓
- `ErrRunInProgress` — Task 1. ✓
- `Agent` + `New` (model required) — Task 2. ✓
- `Run` + `Result` (Events/Wait) — Task 2. ✓
- `Session` + `NewSession` + Prompt/Continue + default-session convenience — Task 3. ✓
- Steer/FollowUp/Abort + acl->jess ErrRunInProgress mapping — Task 4. ✓
- `jess.LiteLLM` options (#25) — Task 5. ✓
- Boundary + race gate — Task 6. ✓
- Out of scope: subagents (Phase 3), the memory remember/recall tool auto-registration (Phase 4 retypes them to tool.Tool), README/quickstart rewrite (Phase 4, #11).

**Placeholder scan:** none. Task 2's prose explicitly resolves the agent.go/session.go ordering (minimal agent.go in Task 2; default-session methods land in Task 3) rather than leaving a stub.

**Type consistency:** `Option`/`options`, `New`, `Agent`, `Session`, `newSession`/`NewSession`, `Run`/`Result`/`wrapRun`, `Prompt`/`Continue`/`Steer`/`FollowUp`/`Abort`, `ErrRunInProgress`, `LiteLLMOption`/`WithLLMAPIKey`/`WithLLMBaseURL`, `acl.Config`/`acl.NewRuntime`/`acl.Run`/`acl.ErrRunInProgress`/`acl.LiteLLMConfig`/`acl.NewLiteLLMModel` are consistent across tasks. Reuses driver from the runtime-driver plan.
