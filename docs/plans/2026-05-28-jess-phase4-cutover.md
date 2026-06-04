# Phase 4: agentcore encapsulation cutover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete ADR 0001's "clean break" migration so `github.com/voocel/agentcore` is imported only under `internal/acl/`, and jess presents the `jess.New` facade everywhere (code, docs, example).

**Architecture:** This is the destructive cutover that the prior additive phases set up. The memory `ContextManager` adapter and the skills→agentcore conversion (the last two agentcore importers in public packages) relocate into `internal/acl/`. `skills/` is renamed to `skill/`. The memory tools are confirmed to satisfy the vendor-free `jess/tool.Tool`. The example and prose are rewritten to the facade. The boundary test's temporary allowlist is emptied, which becomes the machine-checkable proof that encapsulation is complete.

**Tech Stack:** Go 1.26+, no CGO. Module path `github.com/guygrigsby/jess`. agentcore `v1.6.9`. Gates: `go vet ./...`, `go test -race ./...`, the `TestAgentcoreImportBoundary` boundary test, and (if present) `make lint` / `make license-audit`.

**Authoritative scope:** ADR `docs/adr/0001-encapsulate-agentcore-into-jess.md`, Decision 8 ("Migration: clean break"). Read it before starting.

**Invariant to preserve (cardinal):** Memory failures must NEVER block an LLM call. The relocated `ContextManager` keeps swallowing Store/Recaller errors and degrading to no-memory. Do not change that behavior while moving it.

**Ordering rationale (do not reorder):** Each task leaves the tree building and all tests green. The boundary allowlist (Task 7) can only be emptied after every public-package agentcore importer is gone (examples in Task 2, memory adapter in Task 3, skills conversion in Tasks 5–6). Renaming `skills/`→`skill/` (Task 5) precedes deleting `skills/agentcore.go` (Task 6) so the conversion code is authored once against the final package name.

---

## File-by-file change map

| File | Action |
|---|---|
| `.github/copilot-instructions.md` | Conflict-resolve on merge (Task 0); reword post-cutover (Task 7) |
| `examples/quickstart/main.go` | Rewrite to the facade (Task 2) |
| `memory/tool.go`, `memory/recall_tool.go` | Add `tool.Tool` assertion + doc fix (Task 1) |
| `memory/context_manager.go` | **Delete**; moves to `internal/acl/context_manager.go` (Task 3) |
| `memory/memory_test.go`, `memory/redesign_test.go` | Remove the 6 `ContextManager` tests + agentcore import (Task 3) |
| `internal/acl/context_manager.go` | **Create** (moved adapter) (Task 3) |
| `internal/acl/context_manager_test.go` | **Create** (moved tests) (Task 3) |
| `internal/acl/runtime.go` | Retarget `NewContextManager` (Task 3); skills conversion + inject fix (Task 6); `skill` import (Task 5) |
| `skills/` → `skill/` | Rename dir + package; update importers (Task 5) |
| `skills/agentcore.go`, `skills/agentcore_test.go` | **Delete**; logic→ACL free functions (Task 6) |
| `internal/acl/skills.go`, `internal/acl/skills_test.go` | **Create** (Task 6) |
| `options.go`, `subagent/spec.go` | `skills`→`skill` import/identifier (Task 5) |
| `internal/acl/boundary_test.go` | Empty the allowlist (Task 7) |
| `doc.go`, `README.md`, `CHANGELOG.md` | Rewrite to facade; record the break (Task 7) |

---

## Task 0: Sync main and resolve the trivial conflict

PR #3 is `CONFLICTING`: both `origin/main` (PR #4) and this branch added `.github/copilot-instructions.md`. Our branch's version references the real `internal/acl/` boundary; main's references a stale `internal/agentcore/`. Keep ours.

**Files:**
- Modify (conflict resolve): `.github/copilot-instructions.md`

- [ ] **Step 1: Fetch and merge main**

```bash
git fetch origin
git merge origin/main
```

Expected: a single add/add conflict on `.github/copilot-instructions.md` (`CONFLICT (add/add)`).

- [ ] **Step 2: Take our branch's version**

```bash
git checkout --ours .github/copilot-instructions.md
git add .github/copilot-instructions.md
```

- [ ] **Step 3: Verify nothing else conflicts and the tree builds**

```bash
git status   # expect: "All conflicts fixed but you are still merging"
go build ./...
```

Expected: build succeeds, no other conflicted paths.

- [ ] **Step 4: Commit the merge**

```bash
git commit --no-edit
```

---

## Task 1: Confirm memory tools satisfy `jess/tool.Tool`

`RememberTool` and `RecallTool` already have the exact `tool.Tool` method set (`Name`/`Description`/`Schema`/`Execute`) and import no agentcore. This task makes that contract explicit with a compile-time assertion and corrects stale doc comments. No move, no behavior change.

**Files:**
- Modify: `memory/tool.go`
- Modify: `memory/recall_tool.go`

- [ ] **Step 1: Add the import and assertion to `memory/tool.go`**

Add `"github.com/guygrigsby/jess/tool"` to the import block. After the `RememberTool` type declaration (around `memory/tool.go:30`), add:

```go
// RememberTool is a jess tool.Tool; the agent calls it to save a fact.
var _ tool.Tool = (*RememberTool)(nil)
```

Update the type's doc comment that currently says "agentcore.Tool the model uses to save a fact" (around `memory/tool.go:10`) to say "tool.Tool the model uses to save a fact".

- [ ] **Step 2: Add the import and assertion to `memory/recall_tool.go`**

Add `"github.com/guygrigsby/jess/tool"` to the import block. After the `RecallTool` type declaration (around `memory/recall_tool.go:26`), add:

```go
// RecallTool is a jess tool.Tool; the agent calls it to query memory.
var _ tool.Tool = (*RecallTool)(nil)
```

Update the doc comment that says "agentcore.Tool the model uses to query the memory store" (around `memory/recall_tool.go:10`) to "tool.Tool the model uses to query the memory store".

- [ ] **Step 3: Verify it builds and tests pass**

Run: `go build ./... && go test -race ./memory/`
Expected: PASS. (`memory` importing `jess/tool` is cycle-free: `tool` imports only stdlib.)

- [ ] **Step 4: Commit**

```bash
git add memory/tool.go memory/recall_tool.go
git commit -m "feat(memory): assert RememberTool/RecallTool satisfy jess/tool.Tool"
```

---

## Task 2: Rewrite `examples/quickstart` to the facade

The current quickstart imports agentcore and `memory.NewContextManager` directly. Rewrite it to drive a run through `jess.New` → `Agent.Prompt`, wiring memory via `WithMemory`. It must run offline (no network, no model download), so it uses `InMemoryStore` + `SimpleRecaller` + a local `model.Once` echo model that reveals the injected memory in its reply. This removes the example's agentcore import and its dependency on the (about-to-move) `memory.NewContextManager`.

**Files:**
- Modify (full rewrite): `examples/quickstart/main.go`

- [ ] **Step 1: Replace the file contents**

```go
// Command quickstart shows the jess facade end to end with no network access:
// an in-memory store seeds a core memory, a local echo model reveals what the
// agent received (including the injected memory), and the run is driven through
// jess.New -> Agent.Prompt with its live event stream.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/memory"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

func main() {
	ctx := context.Background()

	// 1. Durable memory. InMemoryStore keeps the quickstart offline and fast;
	//    swap in JSONLStore or ChromemStore (+ memory/embed/gomlx) for
	//    persistence or vector recall.
	store := memory.NewInMemoryStore()
	if _, err := store.Append(ctx, memory.Entry{
		AgentID: "demo",
		Kind:    string(memory.KindUser), // user Kind is AlwaysInclude: injected every turn
		Text:    "User prefers concise, example-first answers.",
	}); err != nil {
		log.Fatalf("seed memory: %v", err)
	}

	// 2. A local model. model.Once adapts a one-shot function into the
	//    streaming model.Model; here it echoes what the agent received, so the
	//    injected memory is visible in the reply.
	echo := model.Once(false, func(_ context.Context, msgs []message.Message, _ []model.ToolSpec) (*model.Response, error) {
		var b strings.Builder
		for _, m := range msgs {
			fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.Text())
		}
		return &model.Response{Message: message.Message{
			Role:    message.RoleAssistant,
			Content: []message.ContentBlock{{Kind: message.BlockText, Text: b.String()}},
		}}, nil
	})

	// 3. Wire it all behind the facade.
	agent, err := jess.New(
		jess.WithModel(echo),
		jess.WithAgentID("demo"),
		jess.WithMemory(store, memory.NewSimpleRecaller()),
	)
	if err != nil {
		log.Fatalf("jess.New: %v", err)
	}

	// 4. Drive a run and observe its event stream.
	run, err := agent.Prompt(ctx, "What kind of answers do I like?")
	if err != nil {
		log.Fatalf("Prompt: %v", err)
	}
	for ev := range run.Events() {
		switch ev.Kind {
		case event.KindToolStart:
			fmt.Printf("-> tool %s\n", ev.Tool)
		case event.KindError:
			fmt.Printf("! error: %v\n", ev.Err)
		}
	}

	// 5. Final result. The echoed assistant text contains the injected core
	//    memory, proving memory reached the model through the facade.
	res, err := run.Wait()
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	for _, m := range res.Messages {
		if m.Role == message.RoleAssistant {
			fmt.Println("\nassistant saw:\n" + m.Text())
		}
	}
}
```

- [ ] **Step 2: Verify it builds and runs offline**

Run: `go build ./... && go run ./examples/quickstart`
Expected: build succeeds; output includes `assistant saw:` followed by a block containing `Core memories` and `User prefers concise, example-first answers.` (the injected memory echoed back). No network access.

- [ ] **Step 3: Confirm the example no longer imports agentcore**

Run: `grep -n "voocel/agentcore" examples/quickstart/main.go || echo "CLEAN"`
Expected: `CLEAN`.

- [ ] **Step 4: Commit**

```bash
git add examples/quickstart/main.go
git commit -m "docs(examples): rewrite quickstart to the jess.New facade"
```

---

## Task 3: Move the memory `ContextManager` adapter into `internal/acl/`

`memory/context_manager.go` is the agentcore.ContextManager adapter; per ADR Decision 8 it moves into the ACL. The file becomes `package acl`, imports `jess/memory` for the domain types it consumes (`Store`, `Recaller`, `KindRegistry`, `Entry`, `Query`, `Kind`), and uses the `ac` alias for agentcore to match the package. Its 6 tests move alongside it. The file-local helpers `writeEntries` and `lastTextContent`, and `PassthroughInner`, are used only here, so they move too.

**Files:**
- Create: `internal/acl/context_manager.go`
- Create: `internal/acl/context_manager_test.go`
- Delete: `memory/context_manager.go`
- Modify: `memory/memory_test.go` (remove 4 tests + agentcore import)
- Modify: `memory/redesign_test.go` (remove 2 tests + agentcore import)
- Modify: `internal/acl/runtime.go` (call site)

- [ ] **Step 1: Create `internal/acl/context_manager.go`**

This is `memory/context_manager.go` transformed: `package acl`; agentcore referenced as `ac`; memory domain types qualified with `memory.`. Behavior is byte-for-byte identical.

```go
package acl

import (
	"context"
	"strings"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/memory"
)

// ContextManager is the agentcore.ContextManager adapter for jess/memory. It
// injects recalled memory entries as a leading user message before each LLM
// call, then delegates everything else (Compact, RecoverOverflow, Sync, Usage,
// Snapshot) to an inner manager. Hosts that don't have their own context
// strategy get PassthroughInner (the default applied when Inner is nil).
//
// It lives in the ACL (not in jess/memory) because it speaks agentcore types;
// the root jess package wires it via the facade's WithMemory option. Memory
// failures never block the LLM call: Store/Recaller errors are swallowed and
// the adapter degrades to no-memory, never no-agent (ADR Decision 7).
//
// Lifecycle: Project is called once per LLM turn by agentcore. The adapter
// constructs a Query from AgentID + the conversation's recent user content,
// asks the Recaller for matching entries, formats them into a single inserted
// Message, and returns the inner projection with that Message prepended.
//
// Memory injection never commits to the runtime baseline — entries appear in
// the prompt view for one call and vanish on the next. That keeps the
// conversation history readable and prevents memory text from being re-fed back
// into Recall via the conversation hint.
type ContextManager struct {
	store    memory.Store
	recaller memory.Recaller
	agentID  string
	maxItems int
	header   string
	kinds    *memory.KindRegistry

	inner ac.ContextManager
}

// ContextManagerOptions configures NewContextManager.
type ContextManagerOptions struct {
	// AgentID scopes Recall queries to one agent. Empty matches memories with
	// no AgentID (the global scope); pass an agent identifier for per-agent
	// memory.
	AgentID string
	// MaxItems caps how many memory entries get injected per call. Default 8.
	// Set to 0 for "as many as Recaller returns".
	MaxItems int
	// Header is the prefix line for the injected message. Default "Relevant
	// memories for this conversation:". Set to "" to omit the header entirely.
	Header string
	// Inner is the underlying ContextManager. May be nil — see PassthroughInner
	// for the no-op default applied in that case.
	Inner ac.ContextManager

	// Kinds is the registry of per-Kind policies. nil uses the baked-in
	// defaults from memory.NewKindRegistry. The ContextManager uses it to decide
	// which Kinds bypass recall (AlwaysInclude=true) and how many entries of
	// each Kind to inject per turn.
	Kinds *memory.KindRegistry
}

// NewContextManager wires a Store + Recaller behind an agentcore.ContextManager.
// Returns nil on impossible config (nil Store or nil Recaller) — callers should
// construct both explicitly.
func NewContextManager(store memory.Store, recaller memory.Recaller, opts ContextManagerOptions) *ContextManager {
	if store == nil || recaller == nil {
		return nil
	}
	cm := &ContextManager{
		store:    store,
		recaller: recaller,
		agentID:  opts.AgentID,
		maxItems: opts.MaxItems,
		header:   opts.Header,
		kinds:    opts.Kinds,
		inner:    opts.Inner,
	}
	if cm.maxItems == 0 {
		cm.maxItems = 8
	}
	if cm.header == "" && opts.Header == "" {
		cm.header = "Relevant memories for this conversation:"
	}
	if cm.kinds == nil {
		cm.kinds = memory.NewKindRegistry()
	}
	if cm.inner == nil {
		cm.inner = PassthroughInner{}
	}
	return cm
}

// Project builds the prompt view in three layers:
//
//  1. inner.Project produces the baseline (compaction etc).
//  2. AlwaysInclude Kinds (user / feedback by default) get pulled directly from
//     the Store, capped per-Kind by policy. These bypass recall scoring.
//  3. Recall fills the remaining budget with relevance-scored entries from
//     non-AlwaysInclude Kinds.
//
// The two memory blocks become ONE leading user message prepended to the
// projection (CORE first, then RELEVANT, then conversation). Memory injection
// never commits to the runtime baseline.
func (m *ContextManager) Project(ctx context.Context, msgs []ac.AgentMessage) (ac.ContextProjection, error) {
	proj, err := m.inner.Project(ctx, msgs)
	if err != nil {
		return proj, err
	}

	core := m.alwaysIncludeEntries(ctx)
	relevant := m.recallForBudget(ctx, msgs, m.maxItems-len(core))
	if len(core) == 0 && len(relevant) == 0 {
		return proj, nil
	}

	memMsg := m.formatLayered(core, relevant)
	proj.Messages = append([]ac.AgentMessage{memMsg}, proj.Messages...)
	return proj, nil
}

// alwaysIncludeEntries pulls every entry of every AlwaysInclude Kind for the
// agent, capped per-Kind by KindPolicy.MaxEntries. Failures swallow — memory
// bugs must not block the LLM call.
func (m *ContextManager) alwaysIncludeEntries(ctx context.Context) []memory.Entry {
	var out []memory.Entry
	for _, kind := range m.kinds.AlwaysIncludeKinds() {
		policy := m.kinds.PolicyFor(kind)
		max := policy.MaxEntries
		if max == 0 {
			max = m.maxItems
		}
		entries, err := m.store.Recall(ctx, memory.Query{AgentID: m.agentID, Kind: string(kind)}, max)
		if err != nil {
			continue
		}
		out = append(out, entries...)
	}
	return out
}

// recallForBudget runs the Recaller for non-AlwaysInclude Kinds up to the
// remaining budget. Returns nothing when budget <= 0 (core entries already
// filled the quota).
func (m *ContextManager) recallForBudget(ctx context.Context, msgs []ac.AgentMessage, budget int) []memory.Entry {
	if budget <= 0 {
		return nil
	}
	hint := lastTextContent(msgs)
	entries, err := m.recaller.Recall(ctx, m.store, m.agentID, hint, budget)
	if err != nil {
		return nil
	}
	// Drop entries whose Kind is AlwaysInclude — they're already in `core` from
	// alwaysIncludeEntries. Avoids duplicates if the Recaller doesn't
	// kind-filter (SimpleRecaller doesn't).
	out := entries[:0]
	for _, e := range entries {
		policy := m.kinds.PolicyFor(memory.Kind(e.Kind))
		if policy.AlwaysInclude {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Compact / RecoverOverflow / Sync / Usage / Snapshot delegate to the inner
// manager. Memory has no opinion on compaction strategies.

func (m *ContextManager) Compact(ctx context.Context, msgs []ac.AgentMessage, reason ac.CompactReason) (ac.ContextCommitResult, error) {
	return m.inner.Compact(ctx, msgs, reason)
}

func (m *ContextManager) RecoverOverflow(ctx context.Context, msgs []ac.AgentMessage, cause error) (ac.ContextRecoveryResult, error) {
	return m.inner.RecoverOverflow(ctx, msgs, cause)
}

func (m *ContextManager) Sync(msgs []ac.AgentMessage) { m.inner.Sync(msgs) }

func (m *ContextManager) Usage() *ac.ContextUsage { return m.inner.Usage() }

func (m *ContextManager) Snapshot() *ac.ContextSnapshot { return m.inner.Snapshot() }

// formatLayered builds the injected memory message with two sub-sections: CORE
// (AlwaysInclude entries) and RELEVANT (recall results), each only emitted when
// non-empty. CORE stays at the top so the model sees stable facts before
// situational ones — matters when budget is tight and the model truncates from
// the bottom.
func (m *ContextManager) formatLayered(core, relevant []memory.Entry) ac.Message {
	var b strings.Builder
	if len(core) > 0 {
		b.WriteString("Core memories (always relevant):\n\n")
		writeEntries(&b, core)
		if len(relevant) > 0 {
			b.WriteString("\n")
		}
	}
	if len(relevant) > 0 {
		if m.header != "" {
			b.WriteString(m.header)
		} else {
			b.WriteString("Relevant memories for this conversation:")
		}
		b.WriteString("\n\n")
		writeEntries(&b, relevant)
	}
	return ac.Message{
		Role: ac.Role("user"),
		Content: []ac.ContentBlock{
			ac.TextBlock(b.String()),
		},
	}
}

func writeEntries(b *strings.Builder, entries []memory.Entry) {
	for _, e := range entries {
		b.WriteString("- ")
		if e.Kind != "" {
			b.WriteString("[")
			b.WriteString(e.Kind)
			b.WriteString("] ")
		}
		b.WriteString(e.Text)
		b.WriteByte('\n')
	}
}

// lastTextContent returns the textual content of the trailing message —
// typically the new user turn. Empty when there's no useful text (e.g. a pure
// tool_result turn).
func lastTextContent(msgs []ac.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if t := msgs[i].TextContent(); strings.TrimSpace(t) != "" {
			return t
		}
	}
	return ""
}

// PassthroughInner is the default inner ContextManager when callers don't supply
// one. It returns the input messages unchanged and reports zero usage.
type PassthroughInner struct{}

func (PassthroughInner) Project(ctx context.Context, msgs []ac.AgentMessage) (ac.ContextProjection, error) {
	return ac.ContextProjection{Messages: msgs}, nil
}

func (PassthroughInner) Compact(ctx context.Context, msgs []ac.AgentMessage, _ ac.CompactReason) (ac.ContextCommitResult, error) {
	return ac.ContextCommitResult{Messages: msgs, Changed: false}, nil
}

func (PassthroughInner) RecoverOverflow(ctx context.Context, msgs []ac.AgentMessage, _ error) (ac.ContextRecoveryResult, error) {
	return ac.ContextRecoveryResult{View: msgs, Changed: false}, nil
}

func (PassthroughInner) Sync(_ []ac.AgentMessage)         {}
func (PassthroughInner) Usage() *ac.ContextUsage          { return nil }
func (PassthroughInner) Snapshot() *ac.ContextSnapshot    { return nil }
```

- [ ] **Step 2: Delete the old file**

```bash
git rm memory/context_manager.go
```

- [ ] **Step 3: Create `internal/acl/context_manager_test.go` with the 6 moved tests**

These are the `TestContextManager_*` functions moved verbatim from `memory/memory_test.go` (4) and `memory/redesign_test.go` (2), adapted to `package acl`: agentcore → `ac`, and `memory`-package symbols (`NewInMemoryStore`, `NewSimpleRecaller`, `Entry`, `KindUser`, `KindProject`) qualified with `memory.`. `NewContextManager` / `ContextManagerOptions` are now local to `acl`.

```go
package acl

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/memory"
)

// ContextManager wiring: Project should prepend the recalled memories as a user
// message. Compact/Sync delegate to the inner manager (the PassthroughInner
// default returns input unchanged).
func TestContextManager_Project_PrependsMemoryMessage(t *testing.T) {
	store := memory.NewInMemoryStore()
	_, _ = store.Append(context.Background(), memory.Entry{Text: "user prefers concise replies", Kind: "user"})

	cm := NewContextManager(store, memory.NewSimpleRecaller(), ContextManagerOptions{})
	if cm == nil {
		t.Fatal("NewContextManager returned nil with valid inputs")
	}

	last := ac.Message{
		Role:    ac.Role("user"),
		Content: []ac.ContentBlock{ac.TextBlock("Tell me about Go modules concisely.")},
	}
	proj, err := cm.Project(context.Background(), []ac.AgentMessage{last})
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Messages) != 2 {
		t.Fatalf("expected 2 messages (memory + original), got %d", len(proj.Messages))
	}
	first := proj.Messages[0].TextContent()
	if !strings.Contains(first, "user prefers concise replies") {
		t.Errorf("memory message missing entry text: %q", first)
	}
}

func TestContextManager_Project_EmptyRecallReturnsInputUntouched(t *testing.T) {
	cm := NewContextManager(memory.NewInMemoryStore(), memory.NewSimpleRecaller(), ContextManagerOptions{})
	input := []ac.AgentMessage{
		ac.Message{Role: ac.Role("user"), Content: []ac.ContentBlock{ac.TextBlock("hi")}},
	}
	proj, err := cm.Project(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(proj.Messages, input) {
		t.Errorf("empty recall should pass through; got %v want %v", proj.Messages, input)
	}
}

func TestContextManager_NilStoreOrRecaller_ReturnsNil(t *testing.T) {
	if cm := NewContextManager(nil, memory.NewSimpleRecaller(), ContextManagerOptions{}); cm != nil {
		t.Error("nil Store should yield nil ContextManager")
	}
	if cm := NewContextManager(memory.NewInMemoryStore(), nil, ContextManagerOptions{}); cm != nil {
		t.Error("nil Recaller should yield nil ContextManager")
	}
}

// Race regression: ContextManager.Project must be safe to call concurrently.
func TestContextManager_ConcurrentProject_RaceClean(t *testing.T) {
	store := memory.NewInMemoryStore()
	_, _ = store.Append(context.Background(), memory.Entry{Text: "concurrent fact"})
	cm := NewContextManager(store, memory.NewSimpleRecaller(), ContextManagerOptions{})

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cm.Project(context.Background(), []ac.AgentMessage{
				ac.Message{Role: ac.Role("user"), Content: []ac.ContentBlock{ac.TextBlock("hi")}},
			})
		}()
	}
	wg.Wait()
}

func TestContextManager_AlwaysIncludeBypassesRecall(t *testing.T) {
	store := memory.NewInMemoryStore()
	// One user fact + one project fact. user is AlwaysInclude; project is
	// recall-only with low age weight.
	_, _ = store.Append(context.Background(), memory.Entry{
		AgentID: "main", Kind: string(memory.KindUser),
		Text: "user is a senior engineer",
	})
	_, _ = store.Append(context.Background(), memory.Entry{
		AgentID: "main", Kind: string(memory.KindProject),
		Text: "current goal: ship feature X by Friday",
	})

	cm := NewContextManager(store, memory.NewSimpleRecaller(), ContextManagerOptions{
		AgentID: "main",
	})

	// Prompt with text UNRELATED to either memory. The user fact should still
	// appear (AlwaysInclude); the project fact may or may not (recall-only).
	last := ac.Message{
		Role: ac.Role("user"),
		Content: []ac.ContentBlock{
			ac.TextBlock("How do I write a Go map?"),
		},
	}
	proj, err := cm.Project(context.Background(), []ac.AgentMessage{last})
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Messages) != 2 {
		t.Fatalf("expected memory + original, got %d msgs", len(proj.Messages))
	}
	memContent := proj.Messages[0].TextContent()
	if !strings.Contains(memContent, "senior engineer") {
		t.Errorf("user fact (AlwaysInclude) should appear regardless of relevance; got: %s", memContent)
	}
	if !strings.Contains(memContent, "Core memories") {
		t.Errorf("core block should be labeled; got: %s", memContent)
	}
}

func TestContextManager_RelevantBlockOnlyForRecallKinds(t *testing.T) {
	store := memory.NewInMemoryStore()
	// Only project facts. With no AlwaysInclude entries, ALL surfaced memory
	// should come from the Relevant block.
	_, _ = store.Append(context.Background(), memory.Entry{
		AgentID: "main", Kind: string(memory.KindProject),
		Text: "Go generics use type parameters",
	})
	_, _ = store.Append(context.Background(), memory.Entry{
		AgentID: "main", Kind: string(memory.KindProject),
		Text: "the Rust ownership model is stricter than Go's",
	})

	cm := NewContextManager(store, memory.NewSimpleRecaller(), ContextManagerOptions{
		AgentID: "main",
	})

	last := ac.Message{
		Role: ac.Role("user"),
		Content: []ac.ContentBlock{
			ac.TextBlock("Explain Go generics."),
		},
	}
	proj, _ := cm.Project(context.Background(), []ac.AgentMessage{last})
	if len(proj.Messages) < 2 {
		t.Fatal("expected at least the original + a memory message")
	}
	memContent := proj.Messages[0].TextContent()
	if strings.Contains(memContent, "Core memories") {
		t.Errorf("no AlwaysInclude entries exist; should NOT see Core block; got: %s", memContent)
	}
	if !strings.Contains(memContent, "Relevant memories") {
		t.Errorf("expected Relevant block header; got: %s", memContent)
	}
}
```

- [ ] **Step 4: Remove the moved tests from the two memory test files**

In `memory/memory_test.go`, delete these four functions and their doc comments: `TestContextManager_Project_PrependsMemoryMessage`, `TestContextManager_Project_EmptyRecallReturnsInputUntouched`, `TestContextManager_NilStoreOrRecaller_ReturnsNil`, `TestContextManager_ConcurrentProject_RaceClean` (the block spanning the comment at line ~184 through line ~256).

In `memory/redesign_test.go`, delete `TestContextManager_AlwaysIncludeBypassesRecall` and `TestContextManager_RelevantBlockOnlyForRecallKinds` (lines ~265–345, including the `// ---- ContextManager layered formatting ----` banner).

In BOTH files, remove the now-unused `"github.com/voocel/agentcore"` import. The compiler will also flag other imports that only the deleted tests used — let it guide you (in `memory_test.go` that is likely `reflect` and `sync`; check `strings` is still used by remaining tests before removing). Run `go build ./memory/` and `goimports`/manual fix until clean.

- [ ] **Step 5: Retarget the call site in `internal/acl/runtime.go`**

`runtime.go:73` currently calls `memory.NewContextManager(...)`. `NewContextManager` is now local to `acl`. Change:

```go
	if cfg.Store != nil && cfg.Recaller != nil {
		cm := NewContextManager(cfg.Store, cfg.Recaller, ContextManagerOptions{AgentID: cfg.AgentID})
		if cm != nil {
			opts = append(opts, ac.WithContextManager(cm))
		}
	}
```

(`memory` stays imported in `runtime.go` — `Config.Store`/`Config.Recaller` are still `memory.Store`/`memory.Recaller`.)

- [ ] **Step 6: Verify the whole module builds and tests pass, and `memory/` is agentcore-free**

Run:
```bash
go build ./...
go test -race ./...
grep -rn "voocel/agentcore" memory/ || echo "MEMORY CLEAN"
```
Expected: build + all tests PASS; `MEMORY CLEAN` (no agentcore anywhere under `memory/`, including its tests). The boundary test still passes (the allowlist remains permissive; emptying it is Task 7).

- [ ] **Step 7: Commit**

```bash
git add internal/acl/context_manager.go internal/acl/context_manager_test.go memory/memory_test.go memory/redesign_test.go internal/acl/runtime.go
git commit -m "refactor(acl): relocate memory ContextManager adapter into internal/acl"
```

---

## Task 4: Confirm `examples/` and `memory/` no longer block the boundary

A checkpoint task (no code change beyond verification) to lock in that two of the three allowlist entries are now dead before touching skills.

- [ ] **Step 1: Verify**

```bash
grep -rn "voocel/agentcore" memory/ examples/ || echo "BOTH CLEAN"
go test -race -run TestAgentcoreImportBoundary ./internal/acl/
```
Expected: `BOTH CLEAN`; boundary test PASS.

(No commit — this is a gate. If it fails, return to Task 2/3.)

---

## Task 5: Rename `skills/` → `skill/` (directory + package + importers)

Per ADR Decision 8. The package is renamed and every importer updated. `agentcore.go` / `agentcore_test.go` are NOT touched here — they're deleted in Task 6 against the new name.

**Files:**
- Rename: `skills/skill.go`, `skills/doc.go`, `skills/filesystem.go`, `skills/skill_test.go`, `skills/filesystem_test.go`, `skills/agentcore.go`, `skills/agentcore_test.go` → `skill/...`
- Modify: `options.go`, `internal/acl/runtime.go`, `subagent/spec.go`

- [ ] **Step 1: Move the directory with git**

```bash
git mv skills skill
```

- [ ] **Step 2: Rename the package in every moved file**

Change `package skills` → `package skill` in all 7 files under `skill/`. Also update the `"skills: ..."` error-message prefixes in `skill/skill.go` (in `Add`) to `"skill: ..."` for consistency:

```go
		return errors.New("skill: Skill.Name is required")
...
		return errors.New("skill: skill " + skill.Name + " already registered")
```

Update any doc comment in `skill/doc.go` / `skill/skill.go` that says "surface to agentcore via SystemBlocks() and Tools()" — leave the method references for now (they still exist until Task 6).

- [ ] **Step 3: Update the three importers**

In `options.go`: change import `"github.com/guygrigsby/jess/skills"` → `"github.com/guygrigsby/jess/skill"`; change field `skills *skills.Set` → `skills *skill.Set` (keep the field name `skills`, only the type qualifier changes) and `func WithSkills(set *skills.Set)` → `func WithSkills(set *skill.Set)`.

In `internal/acl/runtime.go`: change import to `"github.com/guygrigsby/jess/skill"`; change `Config.Skills *skills.Set` → `*skill.Set`. (The `cfg.Skills.SystemBlocks()` / `.Tools()` calls remain valid until Task 6.)

In `subagent/spec.go`: change import to `"github.com/guygrigsby/jess/skill"`; change `Skills *skills.Set` → `*skill.Set` and the assignment in `config()` referencing `skills.` accordingly.

- [ ] **Step 4: Verify build and tests**

Run:
```bash
go build ./...
go test -race ./...
grep -rn "guygrigsby/jess/skills" . --include=*.go || echo "NO STALE IMPORTS"
```
Expected: build + tests PASS; `NO STALE IMPORTS`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(skill): rename skills/ to skill/ (package + importers)"
```

---

## Task 6: Move skills→agentcore conversion into ACL free functions; fix the skill-tool inject gap

`skill/agentcore.go` defines `(*Set).SystemBlocks()`/`(*Set).Tools()` returning agentcore types — the last public-package agentcore importer. Methods can't move to another package, so they become free functions in `acl` operating on `*skill.Set` via its exported API (`Names()` + `Get()`). The tool path changes contract: skill tools are now collected as vendor-free `tool.Tool` and wrapped through the same `wrapToolsInject` path as standalone tools — which closes the Phase 3b NOTE (skill tools previously didn't get the run stream injected).

**Files:**
- Create: `internal/acl/skills.go`
- Create: `internal/acl/skills_test.go`
- Delete: `skill/agentcore.go`, `skill/agentcore_test.go`
- Modify: `internal/acl/runtime.go`

- [ ] **Step 1: Create `internal/acl/skills.go`**

```go
package acl

import (
	"sort"
	"strings"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/skill"
	"github.com/guygrigsby/jess/tool"
)

// skillSystemBlocks builds the agentcore system-prompt contributions for a skill
// Set: an index header listing every skill name + one-line description, then one
// block per skill that has a SystemPrompt. Sorted by name for stable output
// (matters for prompt caching). Returns nil for an empty/nil Set.
//
// This is the former skills.Set.SystemBlocks(), relocated into the ACL so the
// skill package stays vendor-free (ADR 0001). It reads the Set through its
// exported API (Names + Get).
func skillSystemBlocks(s *skill.Set) []ac.SystemBlock {
	if s == nil {
		return nil
	}
	names := s.Names()
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	var index strings.Builder
	index.WriteString("Available skills:\n")
	for _, name := range names {
		sk, ok := s.Get(name)
		if !ok {
			continue
		}
		index.WriteString("- ")
		index.WriteString(sk.Name)
		if sk.Description != "" {
			index.WriteString(" — ")
			index.WriteString(sk.Description)
		}
		index.WriteByte('\n')
	}

	blocks := []ac.SystemBlock{{Text: index.String()}}
	for _, name := range names {
		sk, ok := s.Get(name)
		if !ok {
			continue
		}
		body := strings.TrimSpace(sk.SystemPrompt)
		if body == "" {
			continue
		}
		blocks = append(blocks, ac.SystemBlock{
			Text: "## Skill: " + sk.Name + "\n\n" + body,
		})
	}
	return blocks
}

// skillTools collects the jess tools contributed by every skill in the Set, in
// the same order as skillSystemBlocks (sorted by skill name, tools in declared
// order within a skill). Entries in a Skill's Tools slice that do not implement
// tool.Tool are silently skipped — the field is typed `any` to keep the skill
// package decoupled, but only tool.Tool values reach the agent.
//
// Returning tool.Tool (not ac.Tool) lets the runtime wrap skill tools through
// the same wrapToolsInject path as standalone tools, so a skill-shipped
// stream-aware tool now receives the active run's event stream.
func skillTools(s *skill.Set) []tool.Tool {
	if s == nil {
		return nil
	}
	names := s.Names()
	sort.Strings(names)

	var out []tool.Tool
	for _, name := range names {
		sk, ok := s.Get(name)
		if !ok {
			continue
		}
		for _, t := range sk.Tools {
			if jt, ok := t.(tool.Tool); ok {
				out = append(out, jt)
			}
		}
	}
	return out
}
```

- [ ] **Step 2: Delete the old conversion file and its test**

```bash
git rm skill/agentcore.go skill/agentcore_test.go
```

- [ ] **Step 3: Rewire `newACAgent` in `internal/acl/runtime.go`**

Replace the skills handling so skill tools join `cfg.Tools` before wrapping (giving them inject), and the inline NOTE is removed. The relevant region of `newACAgent` becomes:

```go
	opts := []ac.AgentOption{ac.WithModel(ToAC(cfg.Model))}

	allTools := cfg.Tools
	var sysBlocks []ac.SystemBlock
	if cfg.Skills != nil {
		sysBlocks = skillSystemBlocks(cfg.Skills)
		allTools = append(allTools, skillTools(cfg.Skills)...)
	}
	tools := wrapToolsInject(allTools, inject)

	if cfg.SystemPrompt != "" {
		opts = append(opts, ac.WithSystemPrompt(cfg.SystemPrompt))
	}
	if len(sysBlocks) > 0 {
		opts = append(opts, ac.WithSystemBlocks(sysBlocks))
	}
	if len(tools) > 0 {
		opts = append(opts, ac.WithTools(tools...))
	}
```

Note `append(allTools, ...)` may alias `cfg.Tools`' backing array; since `cfg` is a by-value copy and `cfg.Tools` is not used again after this, that is safe. If you prefer to be explicit, start with `allTools := append([]tool.Tool(nil), cfg.Tools...)`.

- [ ] **Step 4: Create `internal/acl/skills_test.go`**

Port the assertions from the deleted `skill/agentcore_test.go`, calling the new free functions. The `fakeTool` now stands in for a `tool.Tool` (same 4 methods), and `skillTools` returns `[]tool.Tool`.

```go
package acl

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guygrigsby/jess/skill"
)

// fakeTool is a minimal tool.Tool for exercising skillTools.
type fakeTool struct{ name string }

func (f fakeTool) Name() string                                                      { return f.name }
func (f fakeTool) Description() string                                               { return "" }
func (f fakeTool) Schema() map[string]any                                            { return nil }
func (f fakeTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }

func mustAddAll(t *testing.T, s *skill.Set, skills ...skill.Skill) {
	t.Helper()
	for _, sk := range skills {
		if err := s.Add(sk); err != nil {
			t.Fatalf("Add %q: %v", sk.Name, err)
		}
	}
}

func TestSkillSystemBlocks(t *testing.T) {
	tests := []struct {
		name       string
		skills     []skill.Skill
		wantBlocks int
		wantIndex  string
	}{
		{
			name:       "empty set returns nil",
			skills:     nil,
			wantBlocks: 0,
		},
		{
			name: "index lists all, body only for skills with a prompt",
			skills: []skill.Skill{
				{Name: "beta", Description: "second", SystemPrompt: "do beta"},
				{Name: "alpha", Description: "first"},
			},
			wantBlocks: 2,
			wantIndex:  "Available skills:\n- alpha — first\n- beta — second\n",
		},
		{
			name: "all skills have prompts",
			skills: []skill.Skill{
				{Name: "a", Description: "da", SystemPrompt: "pa"},
				{Name: "b", Description: "db", SystemPrompt: "pb"},
			},
			wantBlocks: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := skill.NewSet()
			mustAddAll(t, s, tt.skills...)
			blocks := skillSystemBlocks(s)
			if len(blocks) != tt.wantBlocks {
				t.Fatalf("blocks = %d, want %d", len(blocks), tt.wantBlocks)
			}
			if tt.wantIndex != "" && blocks[0].Text != tt.wantIndex {
				t.Errorf("index block = %q, want %q", blocks[0].Text, tt.wantIndex)
			}
		})
	}
}

func TestSkillTools(t *testing.T) {
	tests := []struct {
		name      string
		skills    []skill.Skill
		wantNames []string
	}{
		{
			name:      "no skills",
			skills:    nil,
			wantNames: nil,
		},
		{
			name: "non-Tool entries are skipped",
			skills: []skill.Skill{
				{Name: "x", Tools: []any{fakeTool{"t1"}, "not-a-tool", 42}},
			},
			wantNames: []string{"t1"},
		},
		{
			name: "sorted by skill name, declared order within a skill",
			skills: []skill.Skill{
				{Name: "zeta", Tools: []any{fakeTool{"z1"}}},
				{Name: "alpha", Tools: []any{fakeTool{"a1"}, fakeTool{"a2"}}},
			},
			wantNames: []string{"a1", "a2", "z1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := skill.NewSet()
			mustAddAll(t, s, tt.skills...)
			tools := skillTools(s)
			if len(tools) != len(tt.wantNames) {
				t.Fatalf("got %d tools, want %d", len(tools), len(tt.wantNames))
			}
			for i, want := range tt.wantNames {
				if tools[i].Name() != want {
					t.Errorf("tool[%d] = %q, want %q", i, tools[i].Name(), want)
				}
			}
		})
	}
}
```

- [ ] **Step 5: Verify build, tests, and that `skill/` is agentcore-free**

Run:
```bash
go build ./...
go test -race ./...
grep -rn "voocel/agentcore" skill/ || echo "SKILL CLEAN"
```
Expected: build + tests PASS; `SKILL CLEAN`. The new `TestSkillSystemBlocks` / `TestSkillTools` pass (proving the conversion is byte-identical to the old `Set.SystemBlocks`/`Tools`).

- [ ] **Step 6: Commit**

```bash
git add internal/acl/skills.go internal/acl/skills_test.go internal/acl/runtime.go skill/
git commit -m "refactor(acl): move skill->agentcore conversion into ACL; skill tools get run-stream inject"
```

---

## Task 7: Empty the boundary allowlist (the proof), then rewrite docs

With examples, memory, and skill all agentcore-free, `internal/acl/` is the sole importer. Emptying `preMigrationAgentcoreImporters` turns the boundary test into the machine-checkable proof that ADR 0001 is satisfied. Then rewrite the prose to the facade.

**Files:**
- Modify: `internal/acl/boundary_test.go`
- Modify: `.github/copilot-instructions.md`
- Modify: `doc.go`
- Modify: `README.md`
- Modify/Create: `CHANGELOG.md`

- [ ] **Step 1: Simplify `internal/acl/boundary_test.go`**

Delete the `preMigrationAgentcoreImporters` var (lines ~12–24) and the loop that consults it. The `allowed` helper reduces to the `internal/acl/` prefix check, and the doc comment drops the "temporary pre-migration allowlist" language:

```go
// TestAgentcoreImportBoundary fails if any package outside internal/acl imports
// github.com/voocel/agentcore. This enforces ADR 0001's anti-corruption-layer
// boundary, keeping the vendor-free domain packages (message, event, tool,
// model, subagent, memory, skill, root jess) free of the harness.
//
// It parses each file's import declarations (parser.ImportsOnly) rather than
// scanning raw bytes, so a mere mention of the path in a comment or string
// literal does not trigger a false positive.
func TestAgentcoreImportBoundary(t *testing.T) {
	root, err := filepath.Abs("../..") // module root, two levels above internal/acl
	if err != nil {
		t.Fatal(err)
	}
	const dep = "github.com/voocel/agentcore"

	allowed := func(rel string) bool {
		return strings.HasPrefix(filepath.ToSlash(rel), "internal/acl/")
	}
	// ... (rest of the walk is unchanged)
```

- [ ] **Step 2: Verify the boundary holds with an empty allowlist**

Run: `go test -race -run TestAgentcoreImportBoundary ./internal/acl/`
Expected: PASS. **If this fails, a public package still imports agentcore — fix that package, do not re-add the allowlist.**

- [ ] **Step 3: Reword `.github/copilot-instructions.md`**

Update the anti-corruption-layer bullet to drop the temporary-exception sentence (the allowlist is gone): agentcore is imported only by files under `internal/acl/`; the domain packages (`message`, `tool`, `event`, `model`, `subagent`, `memory`, `skill`, root `jess`) are vendor-free; flag any agentcore import outside `internal/acl/`. Fix any `internal/agentcore/` → `internal/acl/` and `jess/skills` → `jess/skill` references.

- [ ] **Step 4: Rewrite `doc.go`**

Replace the package doc so it describes the facade, not "a thin meta-package on top of agentcore". Mention: `jess.New` returns an `Agent`; `Agent`/`Session` drive runs and expose an `event` stream; `memory` and `skill` are wired via options; agentcore is an internal implementation detail confined to `internal/acl`. Change `jess/skills` → `jess/skill`. Remove the "Hosts wire jess's extensions in via agentcore's AgentOption surface" sentence.

```go
// Package jess is a memory- and skill-augmented agent facade over
// github.com/voocel/agentcore. The host calls jess; jess owns the agent run.
//
// Construct an Agent once with jess.New and functional options, then drive a
// conversation:
//
//	agent, _ := jess.New(
//		jess.WithModel(m),                       // any model.Model (cloud via jess.LiteLLM, or local)
//		jess.WithMemory(store, recaller),        // durable recall, injected each turn
//		jess.WithSkills(set),                    // capability bundles
//	)
//	run, _ := agent.Prompt(ctx, "hello")
//	for ev := range run.Events() { /* observe */ }
//	res, _ := run.Wait()
//
// The surface lives in subpackages:
//   - jess/message — Message, ContentBlock, Role
//   - jess/event   — Event, EventKind, Stream (the observable run stream)
//   - jess/tool    — the Tool interface the model invokes
//   - jess/model   — the vendor-free streaming Model interface
//   - jess/memory  — Store/Recaller/Entry/Kind, the remember & recall tools
//   - jess/skill   — Skill, Set, Loader (capability bundles)
//   - jess/subagent — bounded Pool for fast, abundant subagents
//
// agentcore (the loop, providers, tool dispatch, permission engine, context
// compaction) is an internal implementation detail: it is imported only under
// internal/acl, enforced by a boundary test. No agentcore type appears in
// jess's public API, so the harness is swappable.
//
// Pre-1.0 — API may change before v1. See CHANGELOG.md and the examples/
// directory for runnable wiring.
package jess
```

- [ ] **Step 5: Rewrite `README.md`**

Update README so it describes the facade, not a "library of parts". Specifically:
- The tagline/intro: jess is the thing the host calls; `jess.New` → `Agent`/`Session`; agentcore is an internal detail under `internal/acl`.
- Replace the Quickstart code block with the facade example (mirror `examples/quickstart/main.go`).
- Update the package/layout map to the ADR Decision 2 layout (root `jess`, `message/`, `event/`, `tool/`, `model/`, `memory/`, `skill/`, `subagent/`, `internal/acl/`).
- Change every `jess/skills` / `skills/` reference to `skill`.
- Remove claims that the host wires the `agentcore.Agent` itself.

Keep the existing "Status" section's note that `agentcore` and `chromem-go` are pinned to main.

- [ ] **Step 6: Add a CHANGELOG entry**

Record the break under an Unreleased heading (create `CHANGELOG.md` if absent, Keep a Changelog style):

```markdown
## [Unreleased]

### Changed
- **Breaking:** jess is now a facade over agentcore (`jess.New` -> `Agent`/`Session`/`Run`), not a library of parts. agentcore is an internal implementation detail, imported only under `internal/acl` (enforced by a boundary test). No agentcore type appears in jess's public API. (ADR 0001)
- **Breaking:** package `skills` renamed to `skill`.
- The memory `ContextManager` adapter moved from `memory` into `internal/acl`; hosts wire memory via `jess.WithMemory(store, recaller)` instead of constructing a `ContextManager`.
- `memory.RememberTool` / `memory.RecallTool` now implement `jess/tool.Tool`.

### Added
- Root `jess` package, `jess/message`, `jess/event`, `jess/tool`, `jess/model`, `jess/subagent` (bounded Pool), and the `internal/acl` anti-corruption layer.
```

- [ ] **Step 7: Verify everything**

Run:
```bash
go build ./...
go vet ./...
go test -race ./...
```
Expected: all PASS. Confirm README/doc code compiles by eye against the facade signatures.

- [ ] **Step 8: Commit**

```bash
git add internal/acl/boundary_test.go .github/copilot-instructions.md doc.go README.md CHANGELOG.md
git commit -m "refactor(acl): empty the agentcore-import allowlist; rewrite docs to the facade"
```

---

## Task 8: Final verification gate

The whole-repo green gate that prior phases each ran.

- [ ] **Step 1: Format check**

Run: `gofmt -l .`
Expected: no output (no files need formatting). If any are listed, run `gofmt -w <files>`, re-verify, and amend/commit.

- [ ] **Step 2: Vet, race tests, boundary**

Run:
```bash
go vet ./...
go test -race ./...
go test -race -run TestAgentcoreImportBoundary ./internal/acl/
```
Expected: all PASS.

- [ ] **Step 3: Lint + license audit (if the Makefile defines them)**

Run: `make lint && make license-audit` (or `make check` if it aggregates them). If no Makefile targets exist, skip and note it.
Expected: PASS / clean.

- [ ] **Step 4: Final boundary sanity grep**

Run:
```bash
grep -rln "voocel/agentcore" --include=*.go . | grep -v "^./internal/acl/" || echo "FULLY ENCAPSULATED"
```
Expected: `FULLY ENCAPSULATED` (every remaining importer is under `internal/acl/`).

- [ ] **Step 5: Hand off to finishing-a-development-branch**

All Phase 4 tasks complete and verified. Proceed to `superpowers:finishing-a-development-branch` to update PR #3, run the per-phase Copilot review loop, and decide integration.

---

## Self-Review

**Spec coverage (ADR Decision 8):**
- `skills/` → `skill/`, `skills/agentcore.go` deleted → Tasks 5, 6. ✓
- `memory/context_manager.go` → `internal/acl/`; Store/Recaller/Entry/Kind + tools stay, tools implement `jess/tool.Tool` → Tasks 3 (move), 1 (tools stay + assert). ✓
- New packages already exist (Phases 1–3); no action. ✓
- `examples/quickstart` rewritten to the facade → Task 2. ✓
- `README.md` rewritten to the facade → Task 7. ✓ `doc.go` rewritten → Task 7. ✓
- CHANGELOG records the break; no deprecation shims → Task 7. ✓
- Boundary test allowlist emptied (the proof) → Task 7. ✓
- gofmt debt cleaned → Task 8. ✓

**Placeholder scan:** No "TBD"/"handle edge cases"/"similar to". Moved code is given in full; relocated tests are given in full with exact rewrite rules; the one place that says "let the compiler guide import cleanup" (Task 3 Step 4) is a deterministic mechanical step, not a design gap.

**Type consistency:** `tool.Tool` (Name/Description/Schema/Execute) is the target for memory tools (Task 1) and skill tools (Task 6 `skillTools` returns `[]tool.Tool`). `NewContextManager`/`ContextManagerOptions`/`PassthroughInner` names are preserved across the move (Task 3) and used identically by the relocated tests and `runtime.go`. `skillSystemBlocks`/`skillTools` are the names introduced in Task 6 and called in `runtime.go` Step 3. `skill.Set` API used by the ACL (`Names`, `Get`, `NewSet`, `Add`, `Skill{Name,Description,SystemPrompt,Tools}`) matches `skill/skill.go`.

**Risk note:** The only behavior change is that skill-contributed tools now flow through `wrapToolsInject` (gaining the run-stream inject) and are matched against `tool.Tool` instead of `agentcore.Tool` (Task 6). Both are intended by the ADR (Decision 5 / the Phase 3b NOTE). Everything else is a mechanical relocation verified green by the existing test suite and the boundary test.
