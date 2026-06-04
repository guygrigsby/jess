# jess Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Drop jess's anti-corruption wrapper over agentcore, expose agentcore's types directly, and bake in three first-class safety controls (audit, fail-closed gate, abort).

**Architecture:** `jess.New` becomes an option-assembler returning a real `*agentcore.Agent`. The agentcore-touching implementation lives in `internal/core` (used by both root `jess` and `subagent`, so no import cycle). Two new pure-ish leaf packages, `jess/audit` and `jess/gate`, carry the safety constructs. `jess/memory` and `jess/skill` stay agentcore-free for portability. The parallel type universe (`jess/tool`, `jess/message`, `jess/model`, `jess/event`) and the facade types (`Agent`/`Session`/`Run`) are deleted.

**Tech Stack:** Go 1.26, `github.com/voocel/agentcore` v1.6.9, append-only JSONL for audit, standard library only for the new packages.

**Spec:** `docs/specs/2026-06-04-jess-simplify-design.md`

**Package end-state (no cycles):**
- `jess/audit` (stdlib only): `Event`, `Sink`, `JSONLSink`, `DiscardSink`.
- `jess/gate` (imports agentcore, audit): `SafeTool` marker, `RiskLevel`, `Approver`, default classifier, `New(...) agentcore.ToolGate`.
- `internal/core` (imports agentcore, memory, skill, audit, gate): `Config`, `Agent(cfg)` builder, memory `ContextManager`, `Once` model adapter, `Stream` capture, audit middleware.
- `jess` root (imports internal/core, subagent, gate, audit, memory, skill, agentcore): `New`, `Option`, `With*`, `Once`, `Stream`, `NewMemoryManager`, gate/audit re-exports.
- `jess/subagent` (imports internal/core, gate, audit, agentcore): `Spec`, `Pool`, `Tool`.
- `jess/memory`, `jess/skill`: unchanged, agentcore-free.

---

## Task 1: jess/audit package

**Files:**
- Create: `audit/audit.go`
- Create: `audit/jsonl.go`
- Test: `audit/audit_test.go`

- [ ] **Step 1: Write the failing test**

```go
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJSONLSinkRecordsAndReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	s, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}
	defer s.Close()

	ev := Event{
		Time:      time.Unix(1, 0).UTC(),
		AgentPath: "root",
		Kind:      KindToolRequest,
		Tool:      "restart_service",
		Args:      json.RawMessage(`{"name":"nginx"}`),
	}
	if err := s.Record(ev); err != nil {
		t.Fatalf("Record: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got Event
	if err := json.Unmarshal(b[:len(b)-1], &got); err != nil { // strip trailing newline
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Tool != "restart_service" || got.Kind != KindToolRequest {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestDiscardSinkIsNoOp(t *testing.T) {
	if err := DiscardSink{}.Record(Event{Kind: KindRunEnd}); err != nil {
		t.Fatalf("DiscardSink.Record: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./audit/`
Expected: FAIL (package/types not defined).

- [ ] **Step 3: Write `audit/audit.go`**

```go
// Package audit is the durable, agentcore-free record of everything an agent
// does. It is jess's detective control: over a remote channel the operator
// loses the ability to watch output, so the audit log stands in. Tool requests
// are recorded even when the gate denies them, so blocked (possibly rogue)
// attempts stay visible instead of vanishing.
package audit

import (
	"encoding/json"
	"time"
)

// Kind classifies an audit Event.
type Kind string

const (
	KindPrompt        Kind = "prompt"
	KindModelResponse Kind = "model_response"
	KindToolRequest   Kind = "tool_request"
	KindGateDecision  Kind = "gate_decision"
	KindToolResult    Kind = "tool_result"
	KindAbort         Kind = "abort"
	KindRunEnd        Kind = "run_end"
)

// Verdict is the gate outcome recorded on a gate_decision Event.
type Verdict string

const (
	VerdictAllowed      Verdict = "allowed"
	VerdictDenied       Verdict = "denied"
	VerdictNeedApproval Verdict = "needs_approval"
)

// Event is one recorded action. Fields are populated per Kind; zero values are
// fine for fields that do not apply.
type Event struct {
	Time      time.Time       `json:"time"`
	AgentPath string          `json:"agent_path,omitempty"`
	Kind      Kind            `json:"kind"`
	Tool      string          `json:"tool,omitempty"`
	Label     string          `json:"label,omitempty"`
	Preview   string          `json:"preview,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	Verdict   Verdict         `json:"verdict,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Err       string          `json:"err,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
}

// Sink receives audit Events. Implementations must be safe for concurrent use.
type Sink interface {
	Record(Event) error
}

// DiscardSink drops every Event. Turning audit off is explicit (pass this to
// jess.WithAudit), never silent.
type DiscardSink struct{}

// Record satisfies Sink.
func (DiscardSink) Record(Event) error { return nil }
```

- [ ] **Step 4: Write `audit/jsonl.go`**

```go
package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

// JSONLSink appends each Event as one JSON line to a file. Durable and
// crash-evident: a partial last line is the only possible corruption.
type JSONLSink struct {
	mu sync.Mutex
	f  *os.File
	w  *bufio.Writer
}

// NewJSONLSink opens (creating, append mode) the file at path.
func NewJSONLSink(path string) (*JSONLSink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &JSONLSink{f: f, w: bufio.NewWriter(f)}, nil
}

// Record appends ev and flushes so the line is durable before returning.
func (s *JSONLSink) Record(ev Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return s.w.Flush()
}

// Close flushes and closes the underlying file.
func (s *JSONLSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.w.Flush(); err != nil {
		return err
	}
	return s.f.Close()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./audit/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add audit/
git commit -m "feat(audit): durable agentcore-free audit log (Event, Sink, JSONL)"
```

---

## Task 2: jess/gate package (fail-closed policy + classifier)

**Files:**
- Create: `gate/gate.go`
- Create: `gate/classify.go`
- Test: `gate/gate_test.go`

**Context:** A gate is an `agentcore.ToolGate` (`func(ctx, GateRequest) (*GateDecision, error)`). `GateRequest` carries `Tool` (the instance), `Call` (name + args), `ToolLabel`, `Preview`. The default policy is fail-closed: a tool implementing `SafeTool` is allowed; everything else goes to the `Approver`; if no approver, deny. Every decision is recorded to the audit sink, including denials, before execution.

- [ ] **Step 1: Write the failing test**

```go
package gate

import (
	"context"
	"encoding/json"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/audit"
)

type safeTool struct{}

func (safeTool) Name() string                 { return "list_services" }
func (safeTool) Description() string           { return "" }
func (safeTool) Schema() map[string]any        { return map[string]any{} }
func (safeTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}
func (safeTool) Safe() bool { return true }

type dangerTool struct{ safeTool }

func (dangerTool) Name() string { return "restart_service" }
func (dangerTool) Safe() bool   { return false }

type recSink struct{ events []audit.Event }

func (r *recSink) Record(e audit.Event) error { r.events = append(r.events, e); return nil }

func req(t ac.Tool) ac.GateRequest {
	return ac.GateRequest{Tool: t, Call: ac.ToolCall{Name: t.Name()}}
}

func TestSafeToolAllowedNoApprover(t *testing.T) {
	rs := &recSink{}
	g := New(Policy{Audit: rs}) // no approver
	d, err := g(context.Background(), req(safeTool{}))
	if err != nil || d == nil || !d.Allowed {
		t.Fatalf("safe tool should be allowed: d=%+v err=%v", d, err)
	}
}

func TestUnsafeToolDeniedWhenNoApprover(t *testing.T) {
	rs := &recSink{}
	g := New(Policy{Audit: rs})
	d, _ := g(context.Background(), req(dangerTool{}))
	if d == nil || d.Allowed {
		t.Fatalf("unsafe tool must be denied fail-closed, got %+v", d)
	}
	// The denied REQUEST must be recorded (rogue-attempt visibility).
	var sawDecision bool
	for _, e := range rs.events {
		if e.Kind == audit.KindGateDecision && e.Verdict == audit.VerdictDenied {
			sawDecision = true
		}
	}
	if !sawDecision {
		t.Fatalf("denied call not recorded to audit: %+v", rs.events)
	}
}

func TestApproverRoutesUnsafe(t *testing.T) {
	rs := &recSink{}
	approved := Approver(func(context.Context, Request) (bool, string) { return true, "ok" })
	g := New(Policy{Audit: rs, Approver: approved})
	d, _ := g(context.Background(), req(dangerTool{}))
	if d == nil || !d.Allowed {
		t.Fatalf("approver said yes, expected allowed: %+v", d)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gate/`
Expected: FAIL (package/types not defined).

- [ ] **Step 3: Write `gate/gate.go`**

```go
// Package gate is jess's preventive control: a fail-closed tool gate. Omitting
// an approver does not mean allow-all; it means deny anything not declared
// safe. Permissiveness is opt-in (AllowAll) and greppable.
package gate

import (
	"context"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/audit"
)

// SafeTool is the optional marker a tool implements to be auto-approved. Safe
// means read-only or bounded: its effect is obvious by construction. This is
// what makes safe-by-default also friction-free.
type SafeTool interface{ Safe() bool }

// Request is what an Approver sees for one non-safe call.
type Request struct {
	Tool    string
	Label   string // human-readable, from agentcore ToolLabeler when present
	Preview string // exact action, from agentcore Previewer when present
	Args    []byte
}

// Approver decides one non-safe call. Return (true, reason) to allow. This is
// where the daemon's Telegram confirm plugs in.
type Approver func(ctx context.Context, r Request) (allow bool, reason string)

// Policy configures the default gate.
type Policy struct {
	Approver Approver    // nil => deny all non-safe calls (fail-closed)
	Audit    audit.Sink  // nil => no audit (jess wires the real sink)
	AgentPath string     // tagged onto audit records
}

// New builds an agentcore.ToolGate from the policy.
func New(p Policy) ac.ToolGate {
	return func(ctx context.Context, gr ac.GateRequest) (*ac.GateDecision, error) {
		safe := false
		if st, ok := gr.Tool.(SafeTool); ok {
			safe = st.Safe()
		}
		rec := func(v audit.Verdict, reason string) {
			if p.Audit != nil {
				_ = p.Audit.Record(audit.Event{
					AgentPath: p.AgentPath,
					Kind:      audit.KindGateDecision,
					Tool:      gr.Call.Name,
					Label:     gr.ToolLabel,
					Preview:   string(gr.Preview),
					Args:      gr.Call.Args,
					Verdict:   v,
					Reason:    reason,
				})
			}
		}
		if safe {
			rec(audit.VerdictAllowed, "safe tool")
			return &ac.GateDecision{Allowed: true}, nil
		}
		if p.Approver == nil {
			rec(audit.VerdictDenied, "no approver; fail-closed")
			return &ac.GateDecision{Allowed: false, Reason: "denied: no approver configured for non-safe tool"}, nil
		}
		allow, reason := p.Approver(ctx, Request{
			Tool: gr.Call.Name, Label: gr.ToolLabel, Preview: string(gr.Preview), Args: gr.Call.Args,
		})
		if allow {
			rec(audit.VerdictAllowed, reason)
			return &ac.GateDecision{Allowed: true}, nil
		}
		rec(audit.VerdictDenied, reason)
		return &ac.GateDecision{Allowed: false, Reason: "denied by approver: " + reason}, nil
	}
}

// AllowAll is the explicit, greppable opt-out: a gate that permits everything.
func AllowAll() ac.ToolGate {
	return func(context.Context, ac.GateRequest) (*ac.GateDecision, error) {
		return &ac.GateDecision{Allowed: true}, nil
	}
}
```

- [ ] **Step 4: Write `gate/classify.go`** (a heuristic helper the daemon's approver can call; not wired into New yet, kept small)

```go
package gate

import "strings"

// Dangerous is a conservative heuristic over a shell command string, ported in
// spirit from codebot's approval classifier. The daemon uses it inside its
// Approver to auto-prompt on risky bash even when a tool cannot mark itself
// Safe. Returns true when the command looks destructive.
func Dangerous(cmd string) bool {
	c := strings.ToLower(cmd)
	for _, sig := range []string{"rm -rf", "mkfs", "dd if=", ":(){", "shutdown", "reboot", "kill -9", "launchctl unload", "> /dev/"} {
		if strings.Contains(c, sig) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./gate/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add gate/
git commit -m "feat(gate): fail-closed tool gate with SafeTool marker and approver"
```

---

## Task 3: Rename internal/acl to internal/core and slim it

**Files:**
- Rename dir: `internal/acl/` -> `internal/core/` (git mv each file)
- Modify: every file's `package acl` -> `package core`
- Delete: `internal/core/translate.go`, `internal/core/boundary_test.go`, `internal/core/translate_test.go`
- Keep (to be reshaped in later tasks): `context_manager.go`, `model.go`, `runtime.go`, `skills.go`, `provenance.go` and their tests

**Context:** This is the package both root `jess` and `subagent` will import for agentcore wiring. We rename first, delete the pure type-translation (no longer needed once jess types are gone), and leave the rest to reshape. Expect the build to break until Task 7 finishes the flip; that is acceptable for Tasks 3-6 which build up `internal/core` before the root flip. Verify each of these tasks with `go build ./internal/core/` and `go test ./internal/core/` rather than the whole module.

- [ ] **Step 1: Move the package**

```bash
git mv internal/acl internal/core
```

- [ ] **Step 2: Rewrite package clause**

Run (replaces `package acl` with `package core` in every file):

```bash
grep -rl '^package acl' internal/core | xargs sed -i '' 's/^package acl/package core/'
```

- [ ] **Step 3: Delete the type-translation layer**

```bash
git rm internal/core/translate.go internal/core/translate_test.go internal/core/boundary_test.go
```

- [ ] **Step 4: Verify the remaining core package parses**

Run: `go build ./internal/core/ 2>&1 | head`
Expected: errors only about now-missing translate helpers referenced elsewhere in core (runtime.go, model.go). Note them; they are fixed in Tasks 4-6. Do not fix unrelated packages yet.

- [ ] **Step 5: Commit (WIP, branch only)**

```bash
git add -A
git commit -m "refactor(core): rename internal/acl -> internal/core, drop type translation"
```

---

## Task 4: internal/core memory ContextManager (move public-ready)

**Files:**
- Modify: `internal/core/context_manager.go` (already moved in Task 3)
- Test: `internal/core/context_manager_test.go`

**Context:** The adapter already imports only `memory` + agentcore. It needs no type changes (it never used jess/message etc.). Confirm it compiles standalone and its tests pass. Root `jess.NewMemoryManager` will re-export `core.NewContextManager` in Task 7.

- [ ] **Step 1: Confirm it compiles in isolation**

Run: `go build ./internal/core/ 2>&1 | grep context_manager`
Expected: no errors referencing `context_manager.go`.

- [ ] **Step 2: Run its test**

Run: `go test ./internal/core/ -run TestContextManager 2>&1 | tail`
Expected: PASS (may require Tasks 5-6 if the test file shares helpers with runtime_test; if so, mark and return after Task 6).

- [ ] **Step 3: Commit if changed**

```bash
git add internal/core/context_manager.go
git commit -m "refactor(core): context manager compiles standalone" || true
```

---

## Task 5: internal/core Once model adapter

**Files:**
- Create: `internal/core/once.go` (reshaped from the old `model.go` streamAdapter)
- Delete: `internal/core/model.go` (old jess-typed bridges) after extracting
- Test: `internal/core/once_test.go`

**Context:** agentcore's `ChatModel` needs `Generate` + `GenerateStream` + `SupportsTools`. `Once` adapts a single one-shot func to it so local models stay a one-liner. Strip all `jess/model`/`jess/message` references; speak `agentcore.Message`.

- [ ] **Step 1: Write the failing test**

```go
package core

import (
	"context"
	"testing"

	ac "github.com/voocel/agentcore"
)

func TestOnceStreams(t *testing.T) {
	m := Once(false, func(_ context.Context, msgs []ac.Message, _ []ac.ToolSpec) (*ac.LLMResponse, error) {
		return &ac.LLMResponse{Content: "hi"}, nil
	})
	if m.SupportsTools() {
		t.Fatal("expected SupportsTools false")
	}
	ch, err := m.GenerateStream(context.Background(), []ac.Message{ac.UserMessage("yo")}, nil)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	var sawDone bool
	for ev := range ch {
		if ev.Done {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatal("stream never signaled Done")
	}
}
```

(Adjust `LLMResponse`/`StreamEvent` field names to the real agentcore v1.6.9 shapes; read `model.go` in the module cache to confirm `Content`, `Done`, and `UserMessage` exist before writing the implementation.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestOnceStreams`
Expected: FAIL (Once undefined).

- [ ] **Step 3: Implement `internal/core/once.go`**

```go
package core

import (
	"context"

	ac "github.com/voocel/agentcore"
)

// GenerateFunc is a one-shot generation: messages + tool specs in, one response
// out. The streaming half is synthesized by Once.
type GenerateFunc func(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec) (*ac.LLMResponse, error)

// Once adapts a one-shot GenerateFunc into an agentcore.ChatModel, emitting the
// whole response as a single terminal stream event. supportsTools advertises
// tool capability to the loop.
func Once(supportsTools bool, fn GenerateFunc) ac.ChatModel {
	return &onceModel{supportsTools: supportsTools, fn: fn}
}

type onceModel struct {
	supportsTools bool
	fn            GenerateFunc
}

func (m *onceModel) SupportsTools() bool { return m.supportsTools }

func (m *onceModel) Generate(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, _ ...ac.CallOption) (*ac.LLMResponse, error) {
	return m.fn(ctx, msgs, tools)
}

func (m *onceModel) GenerateStream(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, _ ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	resp, err := m.fn(ctx, msgs, tools)
	if err != nil {
		return nil, err
	}
	ch := make(chan ac.StreamEvent, 1)
	ch <- ac.StreamEvent{Done: true, Response: resp}
	close(ch)
	return ch, nil
}
```

(Verify `ac.StreamEvent` field names against the cache; the real fields may be `Delta`/`Done`/`Response`. Match them.)

- [ ] **Step 4: Delete the old jess-typed model bridges**

```bash
git rm internal/core/model.go internal/core/model_test.go
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test -race ./internal/core/ -run TestOnceStreams`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/core/once.go internal/core/once_test.go
git commit -m "feat(core): Once one-shot -> agentcore.ChatModel adapter"
```

---

## Task 6: internal/core Config + builder + Stream + audit middleware

**Files:**
- Create: `internal/core/build.go` (the `Config` + `Agent(cfg)` assembler)
- Create: `internal/core/stream.go` (Subscribe -> channel + Wait + abort audit)
- Create: `internal/core/audit_mw.go` (the tool-execution audit middleware)
- Delete: `internal/core/runtime.go` (the old one-run wrapper) after extracting useful capture logic
- Modify: `internal/core/skills.go` (keep `SkillBlocks`/`SkillTools` as exported, agentcore-typed)
- Test: `internal/core/build_test.go`

**Context:** This is the heart. `Config` carries everything `jess.New` collects. `Agent(cfg)` assembles agentcore options: model, system blocks (base prompt + skills), tools (caller tools + skill tools + memory tools + subagent tool), context manager (from memory), the gate (from approver/policy), and the audit middleware. `Stream` wraps `Subscribe`+`Prompt` into a channel and a `Wait`, recording prompt/response/abort/run_end to audit.

- [ ] **Step 1: Write the failing test (end-to-end through the builder with an echo model)**

```go
package core

import (
	"context"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/gate"
)

func TestBuildAndStreamAuditsToolAndRun(t *testing.T) {
	rs := &recSink{} // reuse a small audit.Sink recorder defined in build_test.go
	echo := Once(false, func(_ context.Context, msgs []ac.Message, _ []ac.ToolSpec) (*ac.LLMResponse, error) {
		return &ac.LLMResponse{Content: "done"}, nil
	})
	ag := Agent(Config{
		Model:    echo,
		Audit:    rs,
		Gate:     gate.New(gate.Policy{Audit: rs}), // fail-closed default, no tools called here
	})
	ch, wait := Stream(context.Background(), ag, "hello")
	for range ch {
	}
	sum := wait()
	if sum == nil {
		t.Fatal("expected a run summary")
	}
	var sawRunEnd bool
	for _, e := range rs.events {
		if e.Kind == audit.KindRunEnd {
			sawRunEnd = true
		}
	}
	if !sawRunEnd {
		t.Fatalf("run_end not audited: %+v", rs.events)
	}
}
```

Define `recSink` in `build_test.go`:

```go
type recSink struct{ events []audit.Event }

func (r *recSink) Record(e audit.Event) error { r.events = append(r.events, e); return nil }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core/ -run TestBuildAndStream`
Expected: FAIL (Config/Agent/Stream undefined).

- [ ] **Step 3: Implement `internal/core/build.go`**

```go
package core

import (
	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/skill"
)

// Config is the assembled jess agent configuration, vendor-visible (agentcore
// types are exposed by design). Both jess.New and subagent.Pool build from it.
type Config struct {
	Model        ac.ChatModel
	SystemPrompt string
	Tools        []ac.Tool
	Skills       *skill.Set
	Store        memory.Store
	Recaller     memory.Recaller
	AgentID      string
	MaxTurns     int
	Gate         ac.ToolGate
	Audit        audit.Sink
	Extra        []ac.AgentOption // passthrough for the long tail
}

// Agent assembles a ready *agentcore.Agent from cfg.
func Agent(cfg Config) *ac.Agent {
	if cfg.Audit == nil {
		cfg.Audit = audit.DiscardSink{}
	}
	opts := []ac.AgentOption{ac.WithModel(cfg.Model)}

	if blocks := SkillBlocks(cfg.SystemPrompt, cfg.Skills); len(blocks) > 0 {
		opts = append(opts, ac.WithSystemBlocks(blocks))
	} else if cfg.SystemPrompt != "" {
		opts = append(opts, ac.WithSystemPrompt(cfg.SystemPrompt))
	}

	tools := append([]ac.Tool{}, cfg.Tools...)
	tools = append(tools, SkillTools(cfg.Skills)...)
	if len(tools) > 0 {
		opts = append(opts, ac.WithTools(tools...))
	}

	if cfg.Store != nil && cfg.Recaller != nil {
		opts = append(opts, ac.WithContextManager(
			NewContextManager(cfg.Store, cfg.Recaller, ContextManagerOptions{AgentID: cfg.AgentID})))
	}
	if cfg.Gate != nil {
		opts = append(opts, ac.WithToolGate(cfg.Gate))
	}
	opts = append(opts, ac.WithMiddlewares(auditMiddleware(cfg.Audit, cfg.AgentID)))
	if cfg.MaxTurns > 0 {
		opts = append(opts, ac.WithMaxTurns(cfg.MaxTurns))
	}
	opts = append(opts, cfg.Extra...)
	return ac.NewAgent(opts...)
}
```

- [ ] **Step 4: Implement `internal/core/audit_mw.go`**

```go
package core

import (
	"context"
	"encoding/json"
	"time"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/audit"
)

// auditMiddleware records every tool execution: the request before running,
// then the result or error with duration. Gate denials never reach here (the
// gate short-circuits them) but the gate itself records those, so the two
// together capture every attempt.
func auditMiddleware(sink audit.Sink, agentPath string) ac.ToolMiddleware {
	return func(ctx context.Context, call ac.ToolCall, next ac.ToolExecuteFunc) (json.RawMessage, error) {
		_ = sink.Record(audit.Event{
			Time: time.Now(), AgentPath: agentPath, Kind: audit.KindToolRequest,
			Tool: call.Name, Args: call.Args,
		})
		start := time.Now()
		res, err := next(ctx, call.Args)
		ev := audit.Event{
			Time: time.Now(), AgentPath: agentPath, Kind: audit.KindToolResult,
			Tool: call.Name, Result: res, DurationMS: time.Since(start).Milliseconds(),
		}
		if err != nil {
			ev.Err = err.Error()
		}
		_ = sink.Record(ev)
		return res, err
	}
}
```

(Confirm `ac.ToolExecuteFunc` signature in the cache: `func(ctx, args json.RawMessage) (json.RawMessage, error)`. Match the real parameter shape.)

- [ ] **Step 5: Implement `internal/core/stream.go`**

```go
package core

import (
	"context"
	"time"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/audit"
)

// Stream drives one prompt on agent and exposes its events as a channel plus a
// Wait for the final summary. Cancelling ctx aborts the run (the kill switch).
// It records prompt/run_end/abort to the agent's audit sink via the closure
// captured at build time is not available here, so Stream takes no sink; the
// tool-level audit (middleware + gate) already covers actions. Run-level audit
// is recorded by the caller wiring in jess.Stream. See Task 7.
func Stream(ctx context.Context, agent *ac.Agent, input string) (<-chan ac.Event, func() *ac.RunSummary) {
	out := make(chan ac.Event, 64)
	var summary *ac.RunSummary
	done := make(chan struct{})

	unsub := agent.Subscribe(func(ev ac.Event) {
		if ev.Summary != nil {
			summary = ev.Summary
		}
		select {
		case out <- ev:
		case <-done:
		}
	})

	go func() {
		defer close(done)
		defer close(out)
		defer unsub()
		errc := make(chan error, 1)
		go func() { errc <- agent.Prompt(input) }()
		select {
		case <-ctx.Done():
			agent.Abort()
			<-errc
		case <-errc:
		}
	}()

	return out, func() *ac.RunSummary { <-done; return summary }
}

var _ = time.Now // placeholder import guard; remove if time unused
```

(Reconcile `agent.Subscribe` semantics against the cache: it returns an unsubscribe func and delivers events via callback. Adjust buffering if needed. Remove the `time` guard line.)

- [ ] **Step 6: Delete the old runtime wrapper**

```bash
git rm internal/core/runtime.go internal/core/runtime_test.go
```

- [ ] **Step 7: Ensure `skills.go` exports `SkillBlocks` and `SkillTools`**

Modify `internal/core/skills.go` so the two builders are exported and typed in agentcore terms:

```go
// SkillBlocks builds the system blocks for base prompt + skill set (sorted for
// cache stability). Returns nil when there is nothing to add.
func SkillBlocks(base string, set *skill.Set) []ac.SystemBlock { /* existing logic, exported */ }

// SkillTools collects agentcore.Tool entries from the skill set (type-asserting
// skill.Skill.Tools, silently skipping non-tools).
func SkillTools(set *skill.Set) []ac.Tool { /* existing logic, exported */ }
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test -race ./internal/core/`
Expected: PASS (build_test, once_test, context_manager_test, skills_test).

- [ ] **Step 9: Commit**

```bash
git add internal/core/
git commit -m "feat(core): Config builder, Stream capture, audit middleware"
```

---

## Task 7: Flip the root jess package (the breaking change, lands green)

**Files:**
- Delete: `agent.go`, `session.go`, `run.go`, `errors.go`, `litellm.go`, `options.go` (old facade)
- Delete packages: `tool/`, `message/`, `model/`, `event/`
- Create: `jess.go` (New + Option + With*), `adapters.go` (Once, Stream, NewMemoryManager re-exports), `gate_opts.go` (WithApprover, WithToolGate, AllowAll, SafeTool re-export), `audit_opts.go` (WithAudit + default sink)
- Modify: `doc.go`
- Rewrite: `examples/quickstart/main.go`
- Test: `jess_test.go`

**Context:** This is the atomic flip. After it, the module compiles against agentcore types throughout. Do the deletes and the new files in one commit so the branch lands green.

- [ ] **Step 1: Delete the old facade and the parallel type packages**

```bash
git rm agent.go session.go run.go errors.go litellm.go options.go
git rm -r tool message model event
git rm agent_test.go session_test.go options_test.go litellm_test.go
```

- [ ] **Step 2: Write `jess.go`**

```go
package jess

import (
	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/gate"
	"github.com/guygrigsby/jess/internal/core"
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/skill"
)

// Option configures the agent jess.New builds.
type Option func(*core.Config, *newState)

// newState holds option-time extras that need post-processing (subagents).
type newState struct {
	subagentSpecs []any // resolved in Task 8; kept as []any to avoid a cycle stub
	approver      gate.Approver
	allowAll      bool
}

// New assembles a ready *agentcore.Agent: model, memory, skills, tools, gate,
// audit, subagents. Returns the real agentcore type; drive it with agentcore's
// API or jess.Stream.
func New(opts ...Option) *ac.Agent {
	cfg := core.Config{}
	st := &newState{}
	for _, o := range opts {
		o(&cfg, st)
	}
	if cfg.Audit == nil {
		cfg.Audit = defaultAudit()
	}
	if cfg.Gate == nil {
		if st.allowAll {
			cfg.Gate = gate.AllowAll()
		} else {
			cfg.Gate = gate.New(gate.Policy{Approver: st.approver, Audit: cfg.Audit, AgentPath: cfg.AgentID})
		}
	}
	// Subagent wiring appended in Task 8.
	return core.Agent(cfg)
}

// WithModel sets the LLM (agentcore.ChatModel, or jess.Once for a local one).
func WithModel(m ac.ChatModel) Option {
	return func(c *core.Config, _ *newState) { c.Model = m }
}

// WithSystemPrompt sets the base system prompt.
func WithSystemPrompt(s string) Option {
	return func(c *core.Config, _ *newState) { c.SystemPrompt = s }
}

// WithTools registers tools the model may call.
func WithTools(t ...ac.Tool) Option {
	return func(c *core.Config, _ *newState) { c.Tools = append(c.Tools, t...) }
}

// WithSkills attaches a skill set (system blocks + tools).
func WithSkills(set *skill.Set) Option {
	return func(c *core.Config, _ *newState) { c.Skills = set }
}

// WithMemory wires durable memory (recall injected each turn). Both required.
func WithMemory(store memory.Store, recaller memory.Recaller) Option {
	return func(c *core.Config, _ *newState) { c.Store = store; c.Recaller = recaller }
}

// WithAgentID scopes memory and tags audit. Empty is the global scope.
func WithAgentID(id string) Option {
	return func(c *core.Config, _ *newState) { c.AgentID = id }
}

// WithAgentcoreOptions passes raw agentcore options through (the long tail:
// max turns, stop guard, middlewares, concurrency).
func WithAgentcoreOptions(o ...ac.AgentOption) Option {
	return func(c *core.Config, _ *newState) { c.Extra = append(c.Extra, o...) }
}
```

- [ ] **Step 3: Write `gate_opts.go`**

```go
package jess

import (
	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/gate"
	"github.com/guygrigsby/jess/internal/core"
)

// SafeTool is the marker a tool implements to be auto-approved by the gate.
type SafeTool = gate.SafeTool

// Approver is the human decision for a non-safe call (the daemon's Telegram
// confirm plugs in here).
type Approver = gate.Approver

// WithApprover installs the human approver for dangerous calls. Without it the
// gate is fail-closed (non-safe tools are denied).
func WithApprover(a gate.Approver) Option {
	return func(_ *core.Config, s *newState) { s.approver = a }
}

// WithToolGate installs a fully custom gate, bypassing the default policy.
func WithToolGate(g ac.ToolGate) Option {
	return func(c *core.Config, _ *newState) { c.Gate = g }
}

// AllowAll is the explicit, greppable opt-out from the fail-closed default.
func AllowAll() Option {
	return func(_ *core.Config, s *newState) { s.allowAll = true }
}
```

- [ ] **Step 4: Write `audit_opts.go`**

```go
package jess

import (
	"os"
	"path/filepath"

	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/internal/core"
)

// WithAudit redirects the audit sink. Pass audit.DiscardSink{} to turn audit
// off explicitly; it is never off silently.
func WithAudit(sink audit.Sink) Option {
	return func(c *core.Config, _ *newState) { c.Audit = sink }
}

// defaultAudit opens a durable JSONL sink under the user cache dir. Falls back
// to Discard only if the path cannot be opened (audit must never block the run).
func defaultAudit() audit.Sink {
	dir, err := os.UserCacheDir()
	if err != nil {
		return audit.DiscardSink{}
	}
	d := filepath.Join(dir, "jess")
	_ = os.MkdirAll(d, 0o700)
	s, err := audit.NewJSONLSink(filepath.Join(d, "audit.jsonl"))
	if err != nil {
		return audit.DiscardSink{}
	}
	return s
}
```

- [ ] **Step 5: Write `adapters.go`**

```go
package jess

import (
	"context"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/internal/core"
	"github.com/guygrigsby/jess/memory"
)

// GenerateFunc is a one-shot generation function (see Once).
type GenerateFunc = core.GenerateFunc

// Once adapts a one-shot function into an agentcore.ChatModel.
func Once(supportsTools bool, fn GenerateFunc) ac.ChatModel { return core.Once(supportsTools, fn) }

// Stream drives one prompt and returns the event channel plus a Wait for the
// summary. Cancelling ctx aborts (the kill switch).
func Stream(ctx context.Context, agent *ac.Agent, input string) (<-chan ac.Event, func() *ac.RunSummary) {
	return core.Stream(ctx, agent, input)
}

// NewMemoryManager builds the agentcore.ContextManager that injects recalled
// memory each turn. Use it on Door 2 (raw agentcore.NewAgent).
func NewMemoryManager(store memory.Store, recaller memory.Recaller) ac.ContextManager {
	return core.NewContextManager(store, recaller, core.ContextManagerOptions{})
}
```

- [ ] **Step 6: Rewrite `doc.go`** (describe jess as an easy agent harness over agentcore; no "does not reimplement the harness" line). Keep it short.

- [ ] **Step 7: Rewrite `examples/quickstart/main.go`** against the new API:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/audit"
	"github.com/guygrigsby/jess/memory"
)

func main() {
	ctx := context.Background()
	store := memory.NewInMemoryStore()
	_, _ = store.Append(ctx, memory.Entry{AgentID: "demo", Kind: string(memory.KindUser),
		Text: "User prefers concise, example-first answers."})

	echo := jess.Once(false, func(_ context.Context, msgs []ac.Message, _ []ac.ToolSpec) (*ac.LLMResponse, error) {
		var b strings.Builder
		for _, m := range msgs {
			fmt.Fprintf(&b, "[%s] %s\n", m.Role, ac.TextOf(m)) // use the real agentcore text accessor
		}
		return &ac.LLMResponse{Content: b.String()}, nil
	})

	agent := jess.New(
		jess.WithModel(echo),
		jess.WithAgentID("demo"),
		jess.WithMemory(store, memory.NewSimpleRecaller()),
		jess.WithAudit(audit.DiscardSink{}), // quiet for the demo
	)

	ch, wait := jess.Stream(ctx, agent, "What kind of answers do I like?")
	for range ch {
	}
	if sum := wait(); sum == nil {
		log.Fatal("no summary")
	}
	fmt.Println("ok: memory injected, run completed")
}
```

(Replace `ac.TextOf(m)` with the actual agentcore accessor for a message's text; read `message.go` to confirm the helper name.)

- [ ] **Step 8: Write `jess_test.go`** (smoke test of the public Door 1 path):

```go
package jess_test

import (
	"context"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/audit"
)

func TestNewAndStream(t *testing.T) {
	echo := jess.Once(false, func(context.Context, []ac.Message, []ac.ToolSpec) (*ac.LLMResponse, error) {
		return &ac.LLMResponse{Content: "hi"}, nil
	})
	agent := jess.New(jess.WithModel(echo), jess.WithAudit(audit.DiscardSink{}))
	ch, wait := jess.Stream(context.Background(), agent, "yo")
	for range ch {
	}
	if wait() == nil {
		t.Fatal("expected summary")
	}
}
```

- [ ] **Step 9: Build and test the whole module**

Run: `go build ./... && go test -race ./...`
Expected: PASS across all packages except `subagent` (rewired in Task 8). If subagent fails to build, temporarily `git rm -r subagent` is NOT allowed; instead proceed directly to Task 8 in the same working session and only commit Task 7 once Task 8 compiles. If you must commit Task 7 alone, add a `//go:build ignore` header to subagent files as a marked, temporary measure and remove it in Task 8.

- [ ] **Step 10: Commit (with Task 8 if needed for green)**

```bash
git add -A
git commit -m "refactor(jess): expose agentcore directly; New returns *agentcore.Agent; audit+gate baked in"
```

---

## Task 8: Rewire subagent to internal/core

**Files:**
- Modify: `subagent/spec.go` (Spec carries core.Config inputs; `config()` builds `core.Config`)
- Modify: `subagent/pool.go` (build `*ac.Agent` via `core.Agent`, drive via `core.Stream`, merge events by AgentPath)
- Modify: `subagent/tool.go` (now an `ac.Tool`)
- Test: `subagent/pool_test.go`, `subagent/spec_test.go`, `subagent/tool_test.go` (update types, keep intent)

**Context:** Replace every `internal/acl` reference with `internal/core` and every `jess/event`/`jess/message`/`jess/tool` reference with agentcore types. The pool's behavior (bounded concurrency, MaxQueued, MaxDepth, graceful Close vs Cancel, nested SubmitTo, event merging by AgentPath) must be preserved. Events merged are now `ac.Event`.

- [ ] **Step 1: Update `Spec` and `config()`**

Spec fields stay (Name, Model, Tools, Skills, SystemPrompt, AgentID, MaxTurns) but typed in agentcore terms (`Model ac.ChatModel`, `Tools []ac.Tool`). `config()` returns `core.Config`, inheriting empty fields from the parent config the pool was built with.

- [ ] **Step 2: Run subagent tests to verify they fail to compile**

Run: `go test ./subagent/ 2>&1 | head`
Expected: compile errors referencing removed packages. These are the checklist of references to migrate.

- [ ] **Step 3: Migrate pool.go and tool.go**

Replace event/message/tool types with `ac.Event`/`ac.Message`/`ac.Tool`. Use `core.Agent(spec.config())` to build each subagent and `core.Stream` (or a direct `Subscribe`) to capture and forward events into the merged sink, tagging `AgentPath`. The subagent `Tool.Execute` returns the subagent's final text as JSON (reuse `lastText`).

- [ ] **Step 4: Wire `jess.WithSubagents` in root jess**

Add to `jess.go`:

```go
// WithSubagents registers subagents. Empty Spec fields inherit from the parent
// (model, store, recaller, agentID). jess.New builds and owns the pool and
// wires the delegating tool.
func WithSubagents(specs ...subagent.Spec) Option {
	return func(c *core.Config, s *newState) { s.subagentSpecs = append(s.subagentSpecs, specs...) }
}
```

and in `New`, after gate/audit are set, if `len(st.subagentSpecs) > 0` build a `subagent.Pool` seeded with `cfg` defaults, register specs, and append `subagent.NewTool(pool)` to `cfg.Tools` before calling `core.Agent(cfg)`. (Resolve the `[]any` stub in `newState` to `[]subagent.Spec` now that the import is present.)

- [ ] **Step 5: Run subagent + root tests**

Run: `go test -race ./subagent/ ./...`
Expected: PASS. Pool concurrency/shutdown/nesting tests green against agentcore types.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor(subagent): build on internal/core + agentcore types; wire WithSubagents"
```

---

## Task 9: Gate + audit end-to-end example/test through jess.New

**Files:**
- Create: `gate_integration_test.go` (root package)
- Create: `examples/gated/main.go`

- [ ] **Step 1: Write the failing integration test**

```go
package jess_test

import (
	"context"
	"encoding/json"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess"
)

// a tool that wants to "restart" something; NOT marked Safe -> must be gated.
type restartTool struct{ ran bool }

func (t *restartTool) Name() string          { return "restart_service" }
func (t *restartTool) Description() string    { return "restart a service" }
func (t *restartTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *restartTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) {
	t.ran = true
	return json.RawMessage(`"restarted"`), nil
}

func TestFailClosedBlocksUnsafeToolWhenNoApprover(t *testing.T) {
	rt := &restartTool{}
	// model that always calls restart_service once, then stops.
	model := callOnceModel("restart_service") // helper defined in the test file
	agent := jess.New(jess.WithModel(model), jess.WithTools(rt)) // no approver => fail-closed
	ch, wait := jess.Stream(context.Background(), agent, "restart nginx")
	for range ch {
	}
	_ = wait()
	if rt.ran {
		t.Fatal("fail-closed gate must block an unmarked tool with no approver")
	}
}
```

(Implement `callOnceModel(name)` as a `jess.Once` model that emits a single tool call for `name`; read agentcore's `LLMResponse`/`ToolCall` shape to construct the tool-call content correctly.)

- [ ] **Step 2: Run to verify it fails, then confirm it passes after wiring is correct**

Run: `go test ./ -run TestFailClosed`
Expected: initially FAIL if wiring is wrong; PASS once the default fail-closed gate is correctly installed by `New`.

- [ ] **Step 3: Write `examples/gated/main.go`** showing `WithApprover` printing the preview and approving from stdin (a stand-in for the daemon's Telegram confirm).

- [ ] **Step 4: Commit**

```bash
git add gate_integration_test.go examples/gated/
git commit -m "test(jess): fail-closed gate blocks unsafe tool end to end"
```

---

## Task 10: Docs, ADR, folder hygiene

**Files:**
- Modify: `CLAUDE.md`, `README.md`, `CHANGELOG.md`
- Create: `docs/adr/0002-agentcore-as-direct-dependency.md`
- Move: `docs/superpowers/` -> `docs/plans/` and `docs/specs/` (content-typed, per repo convention)

- [ ] **Step 1: Rewrite the opening of `CLAUDE.md`** so jess is "an easy agent harness over agentcore with durable memory, skills, subagents, and baked-in audit + a fail-closed tool gate." Remove "two extension packages" and "deliberately does NOT reimplement the agentcore harness." Update the architecture section to the `internal/core` + `audit` + `gate` shape. Fix `skills/` -> `skill/`.

- [ ] **Step 2: Write `docs/adr/0002-agentcore-as-direct-dependency.md`** recording: agentcore is now a direct, exposed dependency; the anti-corruption layer and harness-swappability are dropped; portability insurance is keeping `memory/` and `skill/` agentcore-free; supersedes ADR 0001.

- [ ] **Step 3: Move tool-named docs folder**

```bash
git mv docs/superpowers/plans docs/plans 2>/dev/null; git mv docs/superpowers/specs docs/specs 2>/dev/null; rmdir docs/superpowers 2>/dev/null; true
```

- [ ] **Step 4: Update `README.md`** quickstart to the new API and add a short "Safety: audit + fail-closed gate" section. Update `CHANGELOG.md`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "docs: ADR 0002 (agentcore direct dep), rewrite README/CLAUDE for the new shape"
```

---

## Task 11: Final verification

- [ ] **Step 1: Full gate**

Run: `go vet ./... && go test -race ./...`
Expected: all PASS.

- [ ] **Step 2: Quickstart through the real path**

Run: `go run ./examples/quickstart`
Expected: prints "ok: memory injected, run completed".

- [ ] **Step 3: Confirm no lingering references to deleted packages**

Run: `grep -rn "jess/tool\"\|jess/message\"\|jess/model\"\|jess/event\"\|internal/acl" --include=*.go . | grep -v _test || echo "clean"`
Expected: `clean`.

- [ ] **Step 4: Confirm memory/ and skill/ never import agentcore (portability insurance)**

Run: `grep -rl "voocel/agentcore" memory skill || echo "memory+skill agentcore-free"`
Expected: `memory+skill agentcore-free`.

- [ ] **Step 5: Commit any final fixes and open the PR when ready** (do not push or open the PR until the operator asks).

---

## Self-Review notes

- Spec coverage: deletes (Task 7), survives/lifted (Tasks 4-7), Once/Stream (Tasks 5-6), audit first-class (Tasks 1,6,7), fail-closed gate (Tasks 2,7,9), abort (Task 6 Stream + Task 9), subagent rewire (Task 8), memory manager public (Tasks 4,7), docs/ADR (Task 10), verification (Task 11). All spec sections mapped.
- The exact agentcore field/method names (`LLMResponse.Content`, `StreamEvent.Done/Response`, `Event.Summary`, `ToolExecuteFunc`, message text accessor, `Subscribe` signature) MUST be confirmed against the v1.6.9 module cache before writing each implementation step; the plan flags every such spot. This is the single most likely source of compile breakage.
- Green-between-tasks caveat: Tasks 3-6 build up `internal/core` and intentionally leave the whole module non-building until Task 7's flip. Verify those tasks with `go test ./internal/core/` scoped runs, and treat Tasks 7+8 as the pair that restores module-wide green. This is the one unavoidable non-incremental seam in the refactor; it is called out rather than hidden.
