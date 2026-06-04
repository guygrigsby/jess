# jess ACL Translation Layer Implementation Plan (Phase 2a)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the anti-corruption layer's pure translation functions between jess domain types and agentcore, with no live agent.

**Architecture:** Phase 2a of ADR 0001. A new package `internal/acl` is the only importer of `github.com/voocel/agentcore`. It translates `message.Message`/`message.ContentBlock`/`message.Role`, wraps `tool.Tool` as `agentcore.Tool`, and flattens `agentcore.Event` into `event.Event`. All functions are pure (no goroutines, no network), so they are exhaustively table-testable with constructed values. The runtime driver and `Agent`/`Session` come in Phase 2b.

**Tech Stack:** Go 1.26, `github.com/voocel/agentcore` (aliased `ac`), jess `message`/`event`/`tool` packages (Phase 1). Stdlib `encoding/json`, `context`, `testing`.

**Key agentcore facts this layer encodes (verified against v1.6.9):**
- `ac.TextBlock(s)` -> `ContentBlock{Type: ContentText, Text: s}`; `ac.ThinkingBlock(s)` -> `{Type: ContentThinking, Thinking: s}`; `ac.ToolCallBlock(tc)` -> `{Type: ContentToolCall, ToolCall: &tc}`.
- A tool result is **not** a content block: `ac.ToolResultMsg(toolCallID, content, isError)` returns a whole `Message{Role: RoleTool, Content: [TextBlock(string(content))], Metadata: {"tool_call_id":..., "is_error":...}}`.
- `ac.Event` has fields `Type EventType`, `Delta`, `Tool`, `Args`, `Result`, `IsError`, `Summary *RunSummary`, `Err`.

---

## File structure

| File | Responsibility |
|---|---|
| `internal/acl/doc.go` | Package doc: the ACL boundary; sole agentcore importer |
| `internal/acl/translate.go` | `roleToAC`/`roleFromAC`, `messagesToAC`, `messageFromAC`, `WrapTool`, `EventFromAC` |
| `internal/acl/translate_test.go` | Table-driven tests for every function, both directions |
| `internal/acl/boundary_test.go` | Asserts agentcore is imported only under `internal/acl/` |

The package is named `acl` (anti-corruption layer). It imports agentcore as `ac` to avoid the confusing second `agentcore` identifier.

---

### Task 1: package skeleton + Role translation

**Files:**
- Create: `internal/acl/doc.go`
- Create: `internal/acl/translate.go`
- Test: `internal/acl/translate_test.go`

- [ ] **Step 1: Write the failing test** at `internal/acl/translate_test.go`:

```go
package acl

import (
	"testing"

	ac "github.com/voocel/agentcore"
	"github.com/guygrigsby/jess/message"
)

func TestRoleRoundTrip(t *testing.T) {
	tests := []struct {
		jess message.Role
		acr  ac.Role
	}{
		{message.RoleUser, ac.RoleUser},
		{message.RoleAssistant, ac.RoleAssistant},
		{message.RoleSystem, ac.RoleSystem},
		{message.RoleTool, ac.RoleTool},
	}
	for _, tt := range tests {
		t.Run(string(tt.jess), func(t *testing.T) {
			if got := roleToAC(tt.jess); got != tt.acr {
				t.Errorf("roleToAC(%q) = %q, want %q", tt.jess, got, tt.acr)
			}
			if got := roleFromAC(tt.acr); got != tt.jess {
				t.Errorf("roleFromAC(%q) = %q, want %q", tt.acr, got, tt.jess)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/acl/`
Expected: build failure (`undefined: roleToAC`).

- [ ] **Step 3: Write the implementation**

Create `internal/acl/doc.go`:

```go
// Package acl is jess's anti-corruption layer: the single place that imports
// github.com/voocel/agentcore. It translates between jess's vendor-free domain
// types (message, event, tool) and agentcore's types, so no other jess package
// depends on the harness. A boundary test enforces that agentcore is imported
// only from here.
package acl
```

Create `internal/acl/translate.go`:

```go
package acl

import (
	ac "github.com/voocel/agentcore"
	"github.com/guygrigsby/jess/message"
)

// roleToAC maps a jess Role to an agentcore Role. The string values coincide,
// but the mapping is explicit so a divergence is a compile/test failure, not a
// silent mismatch.
func roleToAC(r message.Role) ac.Role {
	switch r {
	case message.RoleUser:
		return ac.RoleUser
	case message.RoleAssistant:
		return ac.RoleAssistant
	case message.RoleSystem:
		return ac.RoleSystem
	case message.RoleTool:
		return ac.RoleTool
	default:
		return ac.Role(r)
	}
}

// roleFromAC maps an agentcore Role to a jess Role.
func roleFromAC(r ac.Role) message.Role {
	switch r {
	case ac.RoleUser:
		return message.RoleUser
	case ac.RoleAssistant:
		return message.RoleAssistant
	case ac.RoleSystem:
		return message.RoleSystem
	case ac.RoleTool:
		return message.RoleTool
	default:
		return message.Role(r)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/acl/`
Expected: PASS. Also `gofmt -l internal/acl/` (empty) and `go vet ./internal/acl/`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/doc.go internal/acl/translate.go internal/acl/translate_test.go
git commit -m "feat(acl): package skeleton + Role translation"
```

---

### Task 2: jess Message -> agentcore messages

A jess `Message` whose role is `RoleTool` becomes one agentcore message per
`BlockToolResult` (agentcore models tool results as standalone `RoleTool`
messages). Any other role becomes a single agentcore message whose content
blocks are translated in place. Image blocks have no jess equivalent and are
not produced by jess, so they do not arise here.

**Files:**
- Modify: `internal/acl/translate.go`
- Test: `internal/acl/translate_test.go`

- [ ] **Step 1: Write the failing test** (append to `translate_test.go`):

```go
func TestMessagesToAC_TextAndToolCall(t *testing.T) {
	in := []message.Message{{
		Role: message.RoleAssistant,
		Content: []message.ContentBlock{
			{Kind: message.BlockText, Text: "calling"},
			{Kind: message.BlockToolCall, ToolID: "c1", ToolName: "search", Args: []byte(`{"q":"x"}`)},
		},
	}}
	got := messagesToAC(in)
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %d", len(got))
	}
	m := got[0]
	if m.Role != ac.RoleAssistant || len(m.Content) != 2 {
		t.Fatalf("role/content wrong: %+v", m)
	}
	if m.Content[0].Type != ac.ContentText || m.Content[0].Text != "calling" {
		t.Errorf("block0 = %+v", m.Content[0])
	}
	if m.Content[1].Type != ac.ContentToolCall || m.Content[1].ToolCall == nil ||
		m.Content[1].ToolCall.ID != "c1" || m.Content[1].ToolCall.Name != "search" {
		t.Errorf("block1 = %+v", m.Content[1])
	}
}

func TestMessagesToAC_ToolResultBecomesToolMessage(t *testing.T) {
	in := []message.Message{{
		Role: message.RoleTool,
		Content: []message.ContentBlock{
			{Kind: message.BlockToolResult, ToolID: "c1", Result: []byte(`{"ok":true}`), IsError: false},
			{Kind: message.BlockToolResult, ToolID: "c2", Result: []byte(`"boom"`), IsError: true},
		},
	}}
	got := messagesToAC(in)
	if len(got) != 2 {
		t.Fatalf("two tool-result blocks -> two ac messages, got %d", len(got))
	}
	if got[0].Role != ac.RoleTool || got[0].Metadata["tool_call_id"] != "c1" || got[0].Metadata["is_error"] != false {
		t.Errorf("msg0 = %+v", got[0])
	}
	if got[1].Metadata["tool_call_id"] != "c2" || got[1].Metadata["is_error"] != true {
		t.Errorf("msg1 = %+v", got[1])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/acl/ -run TestMessagesToAC`
Expected: build failure (`undefined: messagesToAC`).

- [ ] **Step 3: Write the implementation** (append to `translate.go`; add `"encoding/json"` to imports):

```go
// messagesToAC converts jess messages to agentcore messages. A RoleTool
// message expands to one agentcore message per tool-result block (agentcore
// models each tool result as a standalone RoleTool message via ToolResultMsg).
// All other roles map to a single message with translated content blocks.
func messagesToAC(msgs []message.Message) []ac.Message {
	out := make([]ac.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == message.RoleTool {
			for _, b := range m.Content {
				if b.Kind != message.BlockToolResult {
					continue
				}
				out = append(out, ac.ToolResultMsg(b.ToolID, b.Result, b.IsError))
			}
			continue
		}
		blocks := make([]ac.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			blocks = append(blocks, blockToAC(b))
		}
		out = append(out, ac.Message{Role: roleToAC(m.Role), Content: blocks})
	}
	return out
}

// blockToAC translates a single non-tool-result content block.
func blockToAC(b message.ContentBlock) ac.ContentBlock {
	switch b.Kind {
	case message.BlockThinking:
		return ac.ThinkingBlock(b.Text)
	case message.BlockToolCall:
		return ac.ToolCallBlock(ac.ToolCall{ID: b.ToolID, Name: b.ToolName, Args: b.Args})
	default: // BlockText (and any unknown kind) render as text
		return ac.TextBlock(b.Text)
	}
}

var _ = json.RawMessage(nil) // json used via message.ContentBlock fields
```

Note: if `go vet`/compiler reports `encoding/json` unused, drop the import and the `var _` line. (`Args`/`Result` are already `json.RawMessage` from the `message` package, so the import may be unnecessary; remove it if so.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/acl/ -run TestMessagesToAC`
Expected: PASS. Then `gofmt -l internal/acl/` (empty), `go vet ./internal/acl/`. If vet flags unused `encoding/json`, remove the import and the `var _ = json.RawMessage(nil)` line and re-run.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/translate.go internal/acl/translate_test.go
git commit -m "feat(acl): translate jess messages to agentcore"
```

---

### Task 3: agentcore Message -> jess Message

**Files:**
- Modify: `internal/acl/translate.go`
- Test: `internal/acl/translate_test.go`

- [ ] **Step 1: Write the failing test** (append):

```go
func TestMessageFromAC_AssistantBlocks(t *testing.T) {
	in := ac.Message{Role: ac.RoleAssistant, Content: []ac.ContentBlock{
		ac.TextBlock("hi"),
		ac.ThinkingBlock("hmm"),
		ac.ToolCallBlock(ac.ToolCall{ID: "c1", Name: "search", Args: []byte(`{"q":"x"}`)}),
	}}
	got := messageFromAC(in)
	if got.Role != message.RoleAssistant || len(got.Content) != 3 {
		t.Fatalf("got %+v", got)
	}
	if got.Content[0].Kind != message.BlockText || got.Content[0].Text != "hi" {
		t.Errorf("b0 = %+v", got.Content[0])
	}
	if got.Content[1].Kind != message.BlockThinking || got.Content[1].Text != "hmm" {
		t.Errorf("b1 = %+v", got.Content[1])
	}
	if got.Content[2].Kind != message.BlockToolCall || got.Content[2].ToolID != "c1" ||
		got.Content[2].ToolName != "search" {
		t.Errorf("b2 = %+v", got.Content[2])
	}
}

func TestMessageFromAC_ToolResult(t *testing.T) {
	in := ac.ToolResultMsg("c1", []byte(`{"ok":true}`), true)
	got := messageFromAC(in)
	if got.Role != message.RoleTool || len(got.Content) != 1 {
		t.Fatalf("got %+v", got)
	}
	b := got.Content[0]
	if b.Kind != message.BlockToolResult || b.ToolID != "c1" || !b.IsError || string(b.Result) != `{"ok":true}` {
		t.Errorf("result block = %+v", b)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/acl/ -run TestMessageFromAC`
Expected: build failure (`undefined: messageFromAC`).

- [ ] **Step 3: Write the implementation** (append to `translate.go`):

```go
// messageFromAC translates a single agentcore message to a jess message. A
// RoleTool message is reconstructed into one BlockToolResult using the
// tool_call_id/is_error metadata that ToolResultMsg stored. Image blocks (no
// jess equivalent) are dropped.
func messageFromAC(m ac.Message) message.Message {
	if m.Role == ac.RoleTool {
		toolID, _ := m.Metadata["tool_call_id"].(string)
		isErr, _ := m.Metadata["is_error"].(bool)
		var result []byte
		for _, b := range m.Content {
			if b.Type == ac.ContentText {
				result = []byte(b.Text)
				break
			}
		}
		return message.Message{Role: message.RoleTool, Content: []message.ContentBlock{{
			Kind: message.BlockToolResult, ToolID: toolID, Result: result, IsError: isErr,
		}}}
	}
	blocks := make([]message.ContentBlock, 0, len(m.Content))
	for _, b := range m.Content {
		switch b.Type {
		case ac.ContentText:
			blocks = append(blocks, message.ContentBlock{Kind: message.BlockText, Text: b.Text})
		case ac.ContentThinking:
			blocks = append(blocks, message.ContentBlock{Kind: message.BlockThinking, Text: b.Thinking})
		case ac.ContentToolCall:
			if b.ToolCall != nil {
				blocks = append(blocks, message.ContentBlock{
					Kind: message.BlockToolCall, ToolID: b.ToolCall.ID,
					ToolName: b.ToolCall.Name, Args: b.ToolCall.Args,
				})
			}
		default: // ContentImage, ContentToolRef: no jess equivalent, drop
		}
	}
	return message.Message{Role: roleFromAC(m.Role), Content: blocks}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/acl/ -run TestMessageFromAC`
Expected: PASS. Then `gofmt -l internal/acl/`, `go vet ./internal/acl/`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/translate.go internal/acl/translate_test.go
git commit -m "feat(acl): translate agentcore messages to jess"
```

---

### Task 4: wrap jess.Tool as agentcore.Tool

`jess/tool.Tool` and `agentcore.Tool` are structurally identical (Name,
Description, Schema, Execute with the same signatures), so the wrapper is a
thin delegation. This is the only place the two interfaces meet.

**Files:**
- Modify: `internal/acl/translate.go`
- Test: `internal/acl/translate_test.go`

- [ ] **Step 1: Write the failing test** (append):

```go
type stubJessTool struct{}

func (stubJessTool) Name() string          { return "echo" }
func (stubJessTool) Description() string    { return "d" }
func (stubJessTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (stubJessTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

func TestWrapTool(t *testing.T) {
	var acTool ac.Tool = WrapTool(stubJessTool{})
	if acTool.Name() != "echo" || acTool.Description() != "d" {
		t.Fatalf("name/desc wrong: %q/%q", acTool.Name(), acTool.Description())
	}
	if acTool.Schema()["type"] != "object" {
		t.Errorf("schema = %v", acTool.Schema())
	}
	got, err := acTool.Execute(context.Background(), json.RawMessage(`{"a":1}`))
	if err != nil || string(got) != `{"a":1}` {
		t.Errorf("execute = %s, %v", got, err)
	}
}

func TestWrapTools(t *testing.T) {
	got := WrapTools([]tool.Tool{stubJessTool{}, stubJessTool{}})
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
}
```

Add imports `"context"`, `"encoding/json"`, and `"github.com/guygrigsby/jess/tool"` to the test file as needed.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/acl/ -run TestWrapTool`
Expected: build failure (`undefined: WrapTool`).

- [ ] **Step 3: Write the implementation** (append to `translate.go`; ensure imports include `"context"`, `"encoding/json"`, and `"github.com/guygrigsby/jess/tool"`):

```go
// wrappedTool adapts a jess tool.Tool to agentcore.Tool. The interfaces are
// structurally identical, so this delegates field-for-field.
type wrappedTool struct{ t tool.Tool }

func (w wrappedTool) Name() string          { return w.t.Name() }
func (w wrappedTool) Description() string    { return w.t.Description() }
func (w wrappedTool) Schema() map[string]any { return w.t.Schema() }
func (w wrappedTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	return w.t.Execute(ctx, args)
}

// WrapTool adapts a single jess tool to an agentcore.Tool.
func WrapTool(t tool.Tool) ac.Tool { return wrappedTool{t: t} }

// WrapTools adapts a slice of jess tools to agentcore.Tool.
func WrapTools(ts []tool.Tool) []ac.Tool {
	out := make([]ac.Tool, 0, len(ts))
	for _, t := range ts {
		out = append(out, WrapTool(t))
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/acl/ -run TestWrapTool`
Expected: PASS. Then `gofmt -l internal/acl/`, `go vet ./internal/acl/`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/translate.go internal/acl/translate_test.go
git commit -m "feat(acl): wrap jess.Tool as agentcore.Tool"
```

---

### Task 5: agentcore Event -> jess Event

Flatten agentcore's lifecycle events into jess's kinds. agentcore event types
with no jess equivalent (`MessageStart`, `MessageEnd`, `ToolExecUpdate`,
`Retry`) return `ok == false` and are skipped by the caller.

**Files:**
- Modify: `internal/acl/translate.go`
- Test: `internal/acl/translate_test.go`

- [ ] **Step 1: Write the failing test** (append):

```go
func TestEventFromAC(t *testing.T) {
	tests := []struct {
		name   string
		in     ac.Event
		wantOK bool
		want   event.EventKind
	}{
		{"agent start", ac.Event{Type: ac.EventAgentStart}, true, event.KindRunStart},
		{"turn start", ac.Event{Type: ac.EventTurnStart}, true, event.KindTurnStart},
		{"delta", ac.Event{Type: ac.EventMessageUpdate, Delta: "hi"}, true, event.KindMessageDelta},
		{"tool start", ac.Event{Type: ac.EventToolExecStart, Tool: "search"}, true, event.KindToolStart},
		{"tool end", ac.Event{Type: ac.EventToolExecEnd, Tool: "search", IsError: true}, true, event.KindToolEnd},
		{"turn end", ac.Event{Type: ac.EventTurnEnd}, true, event.KindTurnEnd},
		{"agent end", ac.Event{Type: ac.EventAgentEnd, Summary: &ac.RunSummary{TurnCount: 2, ToolCalls: 1, EndReason: ac.EndReasonStop}}, true, event.KindRunEnd},
		{"error", ac.Event{Type: ac.EventError}, true, event.KindError},
		{"message start dropped", ac.Event{Type: ac.EventMessageStart}, false, ""},
		{"retry dropped", ac.Event{Type: ac.EventRetry}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := EventFromAC(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.Kind != tt.want {
				t.Errorf("Kind = %q, want %q", got.Kind, tt.want)
			}
		})
	}
}

func TestEventFromAC_RunEndSummary(t *testing.T) {
	got, ok := EventFromAC(ac.Event{Type: ac.EventAgentEnd, Summary: &ac.RunSummary{TurnCount: 3, ToolCalls: 2, EndReason: ac.EndReasonMaxTurns}})
	if !ok || got.Summary == nil {
		t.Fatalf("ok=%v summary=%v", ok, got.Summary)
	}
	if got.Summary.Turns != 3 || got.Summary.ToolCalls != 2 || got.Summary.EndReason != "max_turns" {
		t.Errorf("summary = %+v", got.Summary)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/acl/ -run TestEventFromAC`
Expected: build failure (`undefined: EventFromAC`).

- [ ] **Step 3: Write the implementation** (append to `translate.go`; add `"github.com/guygrigsby/jess/event"` to imports):

```go
// EventFromAC flattens an agentcore lifecycle event into a jess event. The
// second return is false for agentcore events with no jess equivalent
// (message_start/end, tool_exec_update, retry); callers skip those.
func EventFromAC(e ac.Event) (event.Event, bool) {
	switch e.Type {
	case ac.EventAgentStart:
		return event.Event{Kind: event.KindRunStart}, true
	case ac.EventTurnStart:
		return event.Event{Kind: event.KindTurnStart}, true
	case ac.EventMessageUpdate:
		return event.Event{Kind: event.KindMessageDelta, Delta: e.Delta}, true
	case ac.EventToolExecStart:
		return event.Event{Kind: event.KindToolStart, Tool: e.Tool, Args: e.Args}, true
	case ac.EventToolExecEnd:
		return event.Event{Kind: event.KindToolEnd, Tool: e.Tool, Result: e.Result, IsError: e.IsError}, true
	case ac.EventTurnEnd:
		return event.Event{Kind: event.KindTurnEnd}, true
	case ac.EventAgentEnd:
		return event.Event{Kind: event.KindRunEnd, Summary: summaryFromAC(e.Summary)}, true
	case ac.EventError:
		return event.Event{Kind: event.KindError, Err: e.Err}, true
	default:
		return event.Event{}, false
	}
}

func summaryFromAC(s *ac.RunSummary) *event.RunSummary {
	if s == nil {
		return nil
	}
	return &event.RunSummary{Turns: s.TurnCount, ToolCalls: s.ToolCalls, EndReason: string(s.EndReason)}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/acl/ -run TestEventFromAC`
Expected: PASS. Then `gofmt -l internal/acl/`, `go vet ./internal/acl/`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/translate.go internal/acl/translate_test.go
git commit -m "feat(acl): flatten agentcore events into jess events"
```

---

### Task 6: boundary test + phase gate

**Files:**
- Create: `internal/acl/boundary_test.go`

- [ ] **Step 1: Write the boundary test** at `internal/acl/boundary_test.go`:

This test walks the module and fails if any `.go` file outside `internal/acl/` imports agentcore. It is the machine-checkable definition of the ACL holding.

```go
package acl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentcoreImportBoundary fails if any package outside internal/acl imports
// github.com/voocel/agentcore. This is the enforcement of ADR 0001's
// anti-corruption-layer boundary.
func TestAgentcoreImportBoundary(t *testing.T) {
	// Walk up to the module root (two levels above internal/acl).
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	const dep = "voocel/agentcore"
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		// The ACL is the one allowed importer.
		if strings.HasPrefix(rel, filepath.Join("internal", "acl")+string(filepath.Separator)) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), dep) {
			t.Errorf("%s imports %s; only internal/acl may", rel, dep)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the boundary test**

Run: `go test ./internal/acl/ -run TestAgentcoreImportBoundary`
Expected: At this Phase-2a point the boundary test PASSES (the domain packages added in Phase 1 do not import agentcore; only `internal/acl` does). If it FAILS, an earlier phase leaked agentcore and that must be fixed before proceeding.

Note: `memory/context_manager.go` and `skills/agentcore.go` still import agentcore on `main`, but they are not on the `arch/encapsulate-agentcore` branch's migration target yet — they are relocated into `internal/acl` in Phase 4. If this test fails because of those files, that is expected only after they are merged in; on the current branch through Phase 3 they have not moved, so confirm whether they exist on the branch. If they do and the test fails, change the test to also allow `memory/` and `skills/` until Phase 4, with a TODO referencing Phase 4 — do NOT weaken it silently.

- [ ] **Step 3: Phase gate**

Run: `go test -race ./...`
Expected: all packages PASS including `internal/acl`.

Run: `make lint`
Expected: `0 issues.`

Run: `make license-audit`
Expected: passes (no new disallowed licenses; agentcore is Apache-2.0).

- [ ] **Step 4: Commit**

```bash
git add internal/acl/boundary_test.go
git commit -m "test(acl): enforce agentcore import boundary"
```

---

## Self-review

**Spec coverage (Phase 2a of ADR 0001):**
- ACL package, sole agentcore importer — Tasks 1, 6 (boundary test). ✓
- Message/ContentBlock/Role translation both directions — Tasks 1–3. ✓
- Tool wrapping (jess.Tool -> agentcore.Tool) — Task 4. ✓
- Event flattening (agentcore.Event -> event.Event) — Task 5. ✓
- ToolResult handled as ContentBlock{BlockToolResult} <-> agentcore.ToolResultMsg — Tasks 2, 3 (per the ADR decision; no separate type). ✓
- Boundary enforcement — Task 6. ✓
- Out of scope (Phase 2b/3/4): runtime driver, Agent/Session/Run, subagent Pool, memory/skill relocation, SystemBlock translation (moves with skills in Phase 4).

**Placeholder scan:** none. Every code step shows complete code. The one conditional (`encoding/json` possibly unused in Task 2) has an explicit instruction to remove it if vet complains.

**Type consistency:** function names (`roleToAC`, `roleFromAC`, `messagesToAC`, `blockToAC`, `messageFromAC`, `WrapTool`, `WrapTools`, `EventFromAC`, `summaryFromAC`) are consistent between implementation and test steps. jess field names (`Kind`, `ToolID`, `ToolName`, `Args`, `Result`, `IsError`, `Text`) match Phase 1's `message.ContentBlock`. agentcore fields (`Type`, `Text`, `Thinking`, `ToolCall.ID/Name/Args`, event `Delta`/`Tool`/`Args`/`Result`/`IsError`/`Summary`, `RunSummary.TurnCount/ToolCalls/EndReason`) match agentcore v1.6.9.
