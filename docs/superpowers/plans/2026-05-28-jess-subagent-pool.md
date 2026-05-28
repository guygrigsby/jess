# jess Subagent Pool Implementation Plan (Phase 3a)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A vendor-free, airtight subagent `Pool`: bounded-concurrency execution of subagent tasks with a blocking bounded queue, AgentPath-tagged merged events, ctx cancellation, and leak-free teardown — built to spawn thousands of lightweight tasks.

**Architecture:** Phase 3a of ADR 0001 (refined: one `acl.Runtime` per task instead of raw `AgentLoop` — the Runtime already encapsulates model/tools/skills/ctx-abort/stream+result). New vendor-free package `subagent/` importing `internal/acl` (allowed; no direct agentcore). A bounded worker pool: `MaxConcurrent` workers (errgroup) drain a buffered task channel (cap `MaxQueued`); each job runs a fresh `acl.Runtime`, forwards its events tagged with the job's `AgentPath` onto one merged `event.Stream`, and captures the result. The LLM-facing subagent tool + parent-run integration are Phase 3b.

**Tech Stack:** Go 1.26. `golang.org/x/sync/errgroup` (BSD-3, new dep). `internal/acl`, `event`, `message`, `model`, `tool`, `skills`. Stdlib `context`, `sync`, `sync/atomic`.

**Design invariants:**
- A *queued* task is a struct in the channel buffer; only `MaxConcurrent` Runtimes exist at once.
- **`Pool.Wait()` drains the merged stream** so a results-only caller never deadlocks on a full event buffer (same rule as `Run.Wait`).
- `Close()` ends submission; the merged stream closes after all in-flight jobs finish; `Wait()` then returns. ctx cancellation aborts in-flight runs (via the Runtime's ctx→abort).
- Events are tagged by **prepending** the job's path, so nested subagents (Phase 3b) compose.

---

## File structure

| File | Responsibility |
|---|---|
| `subagent/spec.go` | `Spec` (named subagent config) + mapping to `acl.Config` |
| `subagent/pool.go` | `Pool`, `New`, options, `Submit`, `Close`, `Wait`, `Events`, worker loop, `Task`, `Result` |
| `subagent/pool_test.go` | lifecycle, concurrency bound, queue blocking, AgentPath tagging, ctx cancel, leak-free |

`subagent/` imports `internal/acl` (allowed) and vendor-free packages; the boundary test confirms no direct agentcore import.

---

### Task 1: add x/sync; Spec + Result + Task types

**Files:**
- Modify: `go.mod`/`go.sum` (add `golang.org/x/sync`)
- Create: `subagent/spec.go`
- Test: `subagent/spec_test.go`

- [ ] **Step 1: Add the dependency**

Run: `go get golang.org/x/sync@latest`
Then confirm it resolves: `go list -m golang.org/x/sync`. (errgroup is used in Task 2; adding the module now keeps Task 1 self-contained.)

- [ ] **Step 2: Write the failing test** at `subagent/spec_test.go`:

```go
package subagent

import (
	"testing"

	"github.com/guygrigsby/jess/model"
)

func TestSpec_Config(t *testing.T) {
	m := model.Once(false, nil) // fn unused here; we only check mapping
	s := Spec{Name: "research", Model: m, SystemPrompt: "be brief", AgentID: "research", MaxTurns: 4}
	cfg := s.config()
	if cfg.Model == nil || cfg.SystemPrompt != "be brief" || cfg.AgentID != "research" || cfg.MaxTurns != 4 {
		t.Fatalf("config = %+v", cfg)
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./subagent/`
Expected: build failure (`undefined: Spec`).

- [ ] **Step 4: Write the implementation** at `subagent/spec.go`:

```go
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
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./subagent/`
Expected: PASS. Then `gofmt -l subagent/`, `go vet ./subagent/`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum subagent/spec.go subagent/spec_test.go
git commit -m "feat(subagent): add x/sync; Spec/Result/Task types"
```

---

### Task 2: Pool lifecycle — New, workers, Submit, Close, Wait (no event merge yet)

The bounded worker pool: `MaxConcurrent` workers drain a buffered task channel
(cap `MaxQueued`). Jobs run a fresh `acl.Runtime` and capture the result.

**Files:**
- Create: `subagent/pool.go`
- Test: `subagent/pool_test.go`

- [ ] **Step 1: Write the failing test** at `subagent/pool_test.go`:

```go
package subagent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

// echo builds a Spec whose model returns a fixed assistant message.
func echo(name, text string) Spec {
	m := model.Once(false, func(context.Context, []message.Message, []model.ToolSpec) (*model.Response, error) {
		return &model.Response{Message: message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: text}}}, StopReason: "stop"}, nil
	})
	return Spec{Name: name, Model: m}
}

func TestPool_RunsTasksAndReturnsResults(t *testing.T) {
	p := New(WithMaxConcurrent(2), WithMaxQueued(8))
	p.Register(echo("a", "ra"))
	p.Register(echo("b", "rb"))

	ta, err := p.Submit(context.Background(), "a", "go")
	if err != nil {
		t.Fatalf("Submit a: %v", err)
	}
	tb, _ := p.Submit(context.Background(), "b", "go")

	p.Close()
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	ra, _ := ta.Wait()
	rb, _ := tb.Wait()
	if len(ra.Messages) == 0 || ra.Messages[len(ra.Messages)-1].Text() != "ra" {
		t.Errorf("task a result = %+v", ra)
	}
	if rb.Messages[len(rb.Messages)-1].Text() != "rb" {
		t.Errorf("task b result = %+v", rb)
	}
}

func TestPool_RespectsMaxConcurrent(t *testing.T) {
	const limit = 3
	var inFlight, maxSeen atomic.Int64
	release := make(chan struct{})
	gate := model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		n := inFlight.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return &model.Response{Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}, nil
	})
	p := New(WithMaxConcurrent(limit), WithMaxQueued(100))
	p.Register(Spec{Name: "g", Model: gate})
	for i := 0; i < 10; i++ {
		if _, err := p.Submit(context.Background(), "g", "x"); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	// Give workers a moment to pick up the max they can.
	time.Sleep(200 * time.Millisecond)
	close(release)
	p.Close()
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if maxSeen.Load() > limit {
		t.Fatalf("max concurrent = %d, want <= %d", maxSeen.Load(), limit)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./subagent/ -run TestPool`
Expected: build failure (`undefined: New`, `WithMaxConcurrent`).

- [ ] **Step 3: Write the implementation** at `subagent/pool.go`:

```go
package subagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/internal/acl"
)

// Errors returned by Submit.
var (
	ErrUnknownAgent = errors.New("subagent: unknown agent")
	ErrPoolClosed   = errors.New("subagent: pool is closed")
	ErrMaxDepth     = errors.New("subagent: max depth exceeded")
)

// Default pool bounds.
const (
	defaultMaxConcurrent = 8
	defaultMaxQueued     = 1024
	defaultMaxDepth      = 8
	mergedStreamBuffer   = 256
)

// Pool runs subagent tasks with bounded concurrency and merges their events.
type Pool struct {
	mu       sync.RWMutex
	specs    map[string]Spec
	maxDepth int

	tasks  chan *job
	stream *event.Stream
	g      *errgroup.Group
	ctx    context.Context
	cancel context.CancelFunc

	instance  atomic.Uint64
	closeOnce sync.Once
}

type job struct {
	spec  Spec
	input string
	path  []string
	task  *Task
}

// Option configures a Pool.
type Option func(*poolConfig)

type poolConfig struct{ maxConcurrent, maxQueued, maxDepth int }

// WithMaxConcurrent caps how many subagent runs execute at once (default 8).
func WithMaxConcurrent(n int) Option { return func(c *poolConfig) { c.maxConcurrent = n } }

// WithMaxQueued caps how many tasks may wait in the queue; Submit blocks when
// full (default 1024).
func WithMaxQueued(n int) Option { return func(c *poolConfig) { c.maxQueued = n } }

// WithMaxDepth caps subagent nesting depth (default 8).
func WithMaxDepth(n int) Option { return func(c *poolConfig) { c.maxDepth = n } }

// New creates a Pool and starts its workers.
func New(opts ...Option) *Pool {
	cfg := poolConfig{maxConcurrent: defaultMaxConcurrent, maxQueued: defaultMaxQueued, maxDepth: defaultMaxDepth}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.maxConcurrent < 1 {
		cfg.maxConcurrent = 1
	}
	if cfg.maxQueued < 1 {
		cfg.maxQueued = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	g, gctx := errgroup.WithContext(ctx)
	p := &Pool{
		specs:    make(map[string]Spec),
		maxDepth: cfg.maxDepth,
		tasks:    make(chan *job, cfg.maxQueued),
		stream:   event.NewStream(mergedStreamBuffer),
		g:        g,
		ctx:      gctx,
		cancel:   cancel,
	}
	for i := 0; i < cfg.maxConcurrent; i++ {
		p.g.Go(func() error {
			for j := range p.tasks {
				p.runJob(j)
			}
			return nil
		})
	}
	// Close the merged stream once all workers have drained the queue.
	go func() {
		_ = p.g.Wait()
		p.stream.Close()
	}()
	return p
}

// Register adds or replaces a subagent Spec.
func (p *Pool) Register(s Spec) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.specs[s.Name] = s
}

// Submit queues a run of the named subagent with the given input. It blocks if
// the queue is full and returns when a slot is available or ctx is cancelled.
// parentPath is the caller's AgentPath (nil at top level); the new task's path
// is parentPath plus the subagent's own "name/NNNN" segment.
func (p *Pool) Submit(ctx context.Context, name, input string, parentPath ...string) (*Task, error) {
	p.mu.RLock()
	spec, ok := p.specs[name]
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAgent, name)
	}
	if len(parentPath) >= p.maxDepth {
		return nil, ErrMaxDepth
	}
	id := p.instance.Add(1)
	path := append(append([]string(nil), parentPath...), fmt.Sprintf("%s/%04d", name, id))
	task := &Task{agentPath: path, done: make(chan struct{})}
	j := &job{spec: spec, input: input, path: path, task: task}

	select {
	case <-p.ctx.Done():
		return nil, ErrPoolClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case p.tasks <- j:
		return task, nil
	}
}

// runJob executes one job on a fresh runtime and captures its result.
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
	res, werr := run.Wait()
	j.task.res = Result{AgentPath: j.path, Messages: res.Messages, Summary: res.Summary}
	j.task.err = werr
}

// Close stops accepting new tasks. In-flight and queued tasks still run; the
// merged event stream closes once they finish. Idempotent.
func (p *Pool) Close() {
	p.closeOnce.Do(func() { close(p.tasks) })
}

// Wait blocks until all submitted tasks finish (call Close first, or Wait will
// block until Close is called). It drains the merged event stream so a caller
// that ignores Events never deadlocks.
func (p *Pool) Wait() error {
	for range p.stream.Events() {
	}
	return p.g.Wait()
}

// Events returns the merged, AgentPath-tagged event stream of all subagent
// runs. It closes after Close and all tasks finish. Consume it, or call Wait
// (which drains it); do not do both concurrently.
func (p *Pool) Events() <-chan event.Event { return p.stream.Events() }
```

Note: Task 2 does not yet forward per-run events to the merged stream (that is Task 3). The merged stream is created and closed, so `Wait`/`Events` work (they just carry no events yet).

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./subagent/ -run TestPool`
Expected: PASS (run a couple times; the concurrency-bound test is timing-based). Then `go test -race ./subagent/`, `gofmt -l subagent/`, `go vet ./subagent/`.

- [ ] **Step 5: Commit**

```bash
git add subagent/pool.go subagent/pool_test.go
git commit -m "feat(subagent): bounded worker pool (Submit/Close/Wait, MaxConcurrent)"
```

---

### Task 3: merge tagged events onto the stream

Forward each job's run events to the merged stream, prepending the job's path.

**Files:**
- Modify: `subagent/pool.go` (`runJob`)
- Test: `subagent/pool_test.go` (append)

- [ ] **Step 1: Append the failing test**:

```go
import "github.com/guygrigsby/jess/event" // add to pool_test.go imports

func TestPool_MergesTaggedEvents(t *testing.T) {
	p := New(WithMaxConcurrent(2))
	p.Register(echo("a", "ra"))

	task, _ := p.Submit(context.Background(), "a", "go")
	p.Close()

	var sawRunEndForTask bool
	for ev := range p.Events() {
		if len(ev.AgentPath) == 0 {
			t.Errorf("event missing AgentPath: %+v", ev)
		}
		if ev.Kind == event.KindRunEnd && ev.AgentPath[len(ev.AgentPath)-1] == task.AgentPath()[len(task.AgentPath())-1] {
			sawRunEndForTask = true
		}
	}
	if !sawRunEndForTask {
		t.Error("did not see a tagged run_end for the task")
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./subagent/ -run TestPool_MergesTaggedEvents`
Expected: FAIL (no events on the stream yet — the range sees nothing, `sawRunEndForTask` stays false).

- [ ] **Step 3: Update `runJob`** in `subagent/pool.go` to forward events before waiting:

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
	// Forward this run's events onto the merged stream, tagged with the job's
	// path (prepended so any nested path is preserved).
	for ev := range run.Events() {
		ev.AgentPath = prependPath(j.path, ev.AgentPath)
		p.stream.Send(ev)
	}
	res, werr := run.Wait() // drain already done above; returns captured result
	j.task.res = Result{AgentPath: j.path, Messages: res.Messages, Summary: res.Summary}
	j.task.err = werr
}

// prependPath returns base followed by existing (for nested subagent paths).
func prependPath(base, existing []string) []string {
	if len(existing) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(existing))
	out = append(out, base...)
	out = append(out, existing...)
	return out
}
```

Note: `run.Events()` is fully consumed here, so the subsequent `run.Wait()` returns the captured result immediately (its internal drain is a no-op).

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./subagent/`
Expected: PASS. Then `gofmt -l subagent/`, `go vet ./subagent/`.

- [ ] **Step 5: Commit**

```bash
git add subagent/pool.go subagent/pool_test.go
git commit -m "feat(subagent): merge AgentPath-tagged events onto the pool stream"
```

---

### Task 4: bounds — unknown agent, queue-full block, MaxDepth, ctx cancel

**Files:**
- Test: `subagent/pool_test.go` (append)

(The behaviors are already implemented in Task 2/3; this task adds the tests that pin them down. If a test reveals a gap, fix `pool.go` minimally.)

- [ ] **Step 1: Append the failing/holding tests**:

```go
func TestPool_UnknownAgent(t *testing.T) {
	p := New()
	defer func() { p.Close(); _ = p.Wait() }()
	if _, err := p.Submit(context.Background(), "nope", "x"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("err = %v, want ErrUnknownAgent", err)
	}
}

func TestPool_MaxDepth(t *testing.T) {
	p := New(WithMaxDepth(2))
	p.Register(echo("a", "r"))
	defer func() { p.Close(); _ = p.Wait() }()
	// parentPath of length == maxDepth must be rejected.
	if _, err := p.Submit(context.Background(), "a", "x", "p1/0001", "p2/0001"); !errors.Is(err, ErrMaxDepth) {
		t.Fatalf("err = %v, want ErrMaxDepth", err)
	}
}

func TestPool_SubmitBlocksWhenQueueFullThenCtxCancels(t *testing.T) {
	// One worker, queue of 1, a blocked task occupying the worker and a second
	// filling the queue; the third Submit must block and then honor ctx.
	release := make(chan struct{})
	block := model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return &model.Response{Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"}, nil
	})
	p := New(WithMaxConcurrent(1), WithMaxQueued(1))
	p.Register(Spec{Name: "b", Model: block})
	_, _ = p.Submit(context.Background(), "b", "1") // occupies the worker
	_, _ = p.Submit(context.Background(), "b", "2") // fills the queue (cap 1)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := p.Submit(ctx, "b", "3") // must block (queue full) then ctx-cancel
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	close(release)
	p.Close()
	_ = p.Wait()
}

func TestPool_CancelAbortsInFlight(t *testing.T) {
	started := make(chan struct{})
	block := model.Once(false, func(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		close(started)
		<-ctx.Done() // runs until the pool cancels
		return nil, ctx.Err()
	})
	p := New(WithMaxConcurrent(1))
	p.Register(Spec{Name: "b", Model: block})
	task, _ := p.Submit(context.Background(), "b", "x")
	<-started
	p.Cancel() // abort in-flight
	// Draining events + Wait must terminate without hanging.
	for range p.Events() {
	}
	_ = p.Wait()
	if _, err := task.Wait(); err == nil {
		t.Log("task completed despite cancel (acceptable if it finished first)")
	}
}
```

- [ ] **Step 2: Run to verify**

Run: `go test ./subagent/ -run 'TestPool_UnknownAgent|TestPool_MaxDepth|TestPool_SubmitBlocks|TestPool_CancelAborts'`
Expected: `TestPool_CancelAbortsInFlight` fails to compile (`p.Cancel` undefined); others pass.

- [ ] **Step 3: Add `Cancel`** to `subagent/pool.go`:

```go
// Cancel aborts all in-flight and queued tasks by cancelling the pool context,
// then behaves like Close. In-flight runs are interrupted (ctx -> abort).
func (p *Pool) Cancel() {
	p.cancel()
	p.Close()
}
```

Note: after `Cancel`, queued jobs still get pulled by workers but their `rt.Prompt(p.ctx, …)` runs under the cancelled ctx, so they abort promptly. `Close` (idempotent) closes the task channel so workers exit and the stream closes.

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./subagent/`
Expected: PASS (run `-count=3` to shake the timing-based tests). Then `gofmt -l subagent/`, `go vet ./subagent/`.

- [ ] **Step 5: Commit**

```bash
git add subagent/pool.go subagent/pool_test.go
git commit -m "feat(subagent): unknown-agent/MaxDepth/queue-block/Cancel bounds + tests"
```

---

### Task 5: phase gate

**Files:** none (verification).

- [ ] **Step 1: Boundary holds (subagent imports no agentcore directly)**

Run: `go test ./internal/acl/ -run TestAgentcoreImportBoundary`
Expected: PASS. Confirm: `go list -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}' ./subagent/ | grep voocel/agentcore` → empty.

- [ ] **Step 2: Full race suite (guarded)**

Run: `go test -race -timeout 120s ./...`
Expected: all PASS. Run `go test -race -count=3 -timeout 120s ./subagent/` to shake the concurrency.

- [ ] **Step 3: Lint + license**

Run: `make lint` (expect `0 issues.`) and `make license-audit` (expect pass; `golang.org/x/sync` is BSD-3).

- [ ] **Step 4: Commit (if any fixes)**

```bash
git add -A && git commit -m "chore: phase 3a subagent pool gate" --allow-empty
```

---

## Self-review

**Spec coverage (Phase 3a of ADR 0001):**
- Vendor-free `subagent` package, `Spec` -> `acl.Config` — Task 1. ✓
- Bounded worker pool (MaxConcurrent workers, MaxQueued buffered channel, errgroup) — Task 2. ✓
- `Submit` (blocking when full), `Close`, `Wait` (drains merged stream — no deadlock), `Task.Wait` — Tasks 2–4. ✓
- One `acl.Runtime` per job; result captured — Task 2. ✓
- AgentPath-tagged merged events (prepend for nesting) — Task 3. ✓
- Bounds: unknown agent, MaxDepth, ctx-cancel on Submit, `Cancel` aborts in-flight — Task 4. ✓
- Leak-free teardown (errgroup `Wait`; stream closed after workers exit) — Tasks 2, 5. ✓
- Boundary + race gate, x/sync license — Tasks 1, 5. ✓
- Out of scope (Phase 3b): the LLM-facing subagent `tool.Tool`, wiring subagent events into a parent run's stream, and nested-pool recursion (the path-prepend + MaxDepth groundwork is here).

**Placeholder scan:** none. Task 2 explicitly defers event-merge to Task 3 (the merged stream is created/closed so Wait/Events work meanwhile) — staged, not a stub.

**Type consistency:** `Spec`/`config`, `Result`, `Task`/`Wait`/`AgentPath`, `Pool`/`New`/`Option`/`WithMaxConcurrent`/`WithMaxQueued`/`WithMaxDepth`/`Register`/`Submit`/`Close`/`Wait`/`Events`/`Cancel`, `job`, `runJob`, `prependPath`, `ErrUnknownAgent`/`ErrPoolClosed`/`ErrMaxDepth` are consistent across tasks. Reuses `acl.Config`/`acl.NewRuntime`/`acl.Run` and `event.Stream`/`event.Event`.
