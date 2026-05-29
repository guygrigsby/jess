# jess Domain-Type Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build jess's vendor-free domain vocabulary (`message`, `tool`, `event`) so later phases can encapsulate agentcore behind it.

**Architecture:** Phase 1 of ADR 0001. Three new leaf packages under the existing module `github.com/guygrigsby/jess`, none importing `voocel/agentcore`. `message` and `tool` are pure types/interfaces; `event` adds a concurrency-safe `Stream` (single consumer, many producers) that the anti-corruption layer and subagent Pool will feed in later phases.

**Tech Stack:** Go 1.26, stdlib only (`encoding/json`, `sync`, `context`, `testing`). No new dependencies in this phase.

---

## File structure

| File | Responsibility |
|---|---|
| `message/message.go` | `Role`, `BlockKind`, `ContentBlock`, `Message` + small constructors |
| `message/message_test.go` | table-driven tests for `Message.Text` / `UserText` |
| `tool/tool.go` | `Tool` interface (domain capability contract) |
| `tool/tool_test.go` | compile-time + behavioral check via a fake tool |
| `event/event.go` | `EventKind` constants, `Event`, `RunSummary` |
| `event/event_test.go` | table-driven `EventKind`/`Event` shape checks |
| `event/stream.go` | `Stream`: single-consumer fan-out, race/panic-safe `Send`/`Close` |
| `event/stream_test.go` | behavioral + `-race` concurrency tests |

All four packages compile and test independently. No file in this phase imports `voocel/agentcore`.

---

### Task 1: message package

**Files:**
- Create: `message/message.go`
- Test: `message/message_test.go`

- [ ] **Step 1: Write the failing test**

Create `message/message_test.go`:

```go
package message

import (
	"encoding/json"
	"testing"
)

func TestMessage_Text(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "concatenates text blocks only",
			msg: Message{Role: RoleAssistant, Content: []ContentBlock{
				{Kind: BlockText, Text: "hello "},
				{Kind: BlockThinking, Text: "(ignored)"},
				{Kind: BlockText, Text: "world"},
			}},
			want: "hello world",
		},
		{
			name: "no text blocks yields empty",
			msg: Message{Role: RoleTool, Content: []ContentBlock{
				{Kind: BlockToolResult, ToolID: "t1", Result: json.RawMessage(`{}`)},
			}},
			want: "",
		},
		{name: "nil content", msg: Message{Role: RoleUser}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.msg.Text(); got != tt.want {
				t.Errorf("Text() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUserText(t *testing.T) {
	m := UserText("hi")
	if m.Role != RoleUser {
		t.Errorf("Role = %q, want user", m.Role)
	}
	if m.Text() != "hi" {
		t.Errorf("Text() = %q, want hi", m.Text())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./message/`
Expected: build failure (`undefined: Message`, `RoleAssistant`, etc.).

- [ ] **Step 3: Write minimal implementation**

Create `message/message.go`:

```go
// Package message defines jess's conversation vocabulary, independent of any
// agent harness. The anti-corruption layer (internal/agentcore) translates
// these to and from the harness's message types; nothing here imports
// agentcore.
package message

import "encoding/json"

// Role identifies who produced a Message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// BlockKind identifies which content variant a ContentBlock carries.
type BlockKind string

const (
	BlockText       BlockKind = "text"
	BlockThinking   BlockKind = "thinking"
	BlockToolCall   BlockKind = "tool_call"
	BlockToolResult BlockKind = "tool_result"
)

// ContentBlock is one piece of a Message's content. Fields are populated
// according to Kind; unused fields stay zero.
type ContentBlock struct {
	Kind     BlockKind
	Text     string          // BlockText, BlockThinking
	ToolID   string          // BlockToolCall, BlockToolResult
	ToolName string          // BlockToolCall
	Args     json.RawMessage // BlockToolCall
	Result   json.RawMessage // BlockToolResult
	IsError  bool            // BlockToolResult
}

// Message is the content produced by one Role in a conversation turn.
type Message struct {
	Role    Role
	Content []ContentBlock
}

// Text returns the concatenation of all text blocks. Thinking, tool-call, and
// tool-result blocks are skipped. Convenience for the common "what did the
// assistant say" case.
func (m Message) Text() string {
	var s string
	for _, b := range m.Content {
		if b.Kind == BlockText {
			s += b.Text
		}
	}
	return s
}

// UserText builds a user Message carrying a single text block.
func UserText(s string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{{Kind: BlockText, Text: s}}}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./message/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add message/message.go message/message_test.go
git commit -m "feat(message): jess conversation domain types"
```

---

### Task 2: tool package

**Files:**
- Create: `tool/tool.go`
- Test: `tool/tool_test.go`

- [ ] **Step 1: Write the failing test**

Create `tool/tool_test.go`:

```go
package tool

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeTool exercises the Tool contract and locks the method set.
type fakeTool struct{}

func (fakeTool) Name() string             { return "echo" }
func (fakeTool) Description() string       { return "echoes its args" }
func (fakeTool) Schema() map[string]any    { return map[string]any{"type": "object"} }
func (fakeTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

// Compile-time assertion that fakeTool satisfies Tool.
var _ Tool = fakeTool{}

func TestTool_Execute(t *testing.T) {
	var tl Tool = fakeTool{}
	got, err := tl.Execute(context.Background(), json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(got) != `{"x":1}` {
		t.Errorf("Execute echoed %s, want {\"x\":1}", got)
	}
	if tl.Name() != "echo" {
		t.Errorf("Name() = %q, want echo", tl.Name())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tool/`
Expected: build failure (`undefined: Tool`).

- [ ] **Step 3: Write minimal implementation**

Create `tool/tool.go`:

```go
// Package tool defines the capability contract the model invokes. The
// interface is structurally identical to the harness's tool interface by
// design: the anti-corruption layer adapts a jess tool to the harness by
// wrapping it, with no field copying. jess's own memory tools implement this
// interface; nothing here imports agentcore.
package tool

import (
	"context"
	"encoding/json"
)

// Tool is a single capability the model can call during a run.
type Tool interface {
	// Name is the stable identifier the model uses to invoke the tool.
	Name() string
	// Description tells the model when and how to use the tool.
	Description() string
	// Schema is the JSON Schema for the tool's arguments.
	Schema() map[string]any
	// Execute runs the tool against decoded-but-raw JSON args and returns a
	// raw JSON result. A non-nil error aborts the call; tool-level failures
	// the model should see are encoded in the result instead.
	Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./tool/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tool/tool.go tool/tool_test.go
git commit -m "feat(tool): jess tool capability contract"
```

---

### Task 3: event types

**Files:**
- Create: `event/event.go`
- Test: `event/event_test.go`

- [ ] **Step 1: Write the failing test**

Create `event/event_test.go`:

```go
package event

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestEvent_Shape(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
		want EventKind
	}{
		{"delta", Event{Kind: KindMessageDelta, Delta: "hi"}, KindMessageDelta},
		{"tool end error", Event{Kind: KindToolEnd, Tool: "x", IsError: true, Result: json.RawMessage(`{}`)}, KindToolEnd},
		{"error carries err", Event{Kind: KindError, Err: errors.New("boom")}, KindError},
		{
			name: "run end carries summary and agent path",
			ev:   Event{Kind: KindRunEnd, AgentPath: []string{"research/0007"}, Summary: &RunSummary{Turns: 3, ToolCalls: 2, EndReason: "stop"}},
			want: KindRunEnd,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ev.Kind != tt.want {
				t.Errorf("Kind = %q, want %q", tt.ev.Kind, tt.want)
			}
		})
	}
}

func TestEvent_IsSubagent(t *testing.T) {
	if (Event{}).IsSubagent() {
		t.Error("empty AgentPath should be root, not subagent")
	}
	if !(Event{AgentPath: []string{"research/0007"}}).IsSubagent() {
		t.Error("non-empty AgentPath should be a subagent event")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./event/`
Expected: build failure (`undefined: Event`, `KindMessageDelta`, etc.).

- [ ] **Step 3: Write minimal implementation**

Create `event/event.go`:

```go
// Package event is jess's observable run stream, independent of any harness.
// The anti-corruption layer flattens the harness's wider event taxonomy into
// these kinds; nothing here imports agentcore.
package event

import "encoding/json"

// EventKind identifies a lifecycle event in a run.
type EventKind string

const (
	KindRunStart     EventKind = "run_start"
	KindTurnStart    EventKind = "turn_start"
	KindMessageDelta EventKind = "message_delta"
	KindToolStart    EventKind = "tool_start"
	KindToolEnd      EventKind = "tool_end"
	KindTurnEnd      EventKind = "turn_end"
	KindRunEnd       EventKind = "run_end"
	KindError        EventKind = "error"
)

// RunSummary is the factual outcome of a single run.
type RunSummary struct {
	Turns     int
	ToolCalls int
	EndReason string // stop, max_turns, aborted, error
}

// Event is one observation from a run. Fields are populated according to Kind.
//
// AgentPath is nil for the root agent and carries name/instance segments for
// subagents (for example {"research/0007"}, nested for deeper trees), so a
// single stream represents an entire agent tree.
type Event struct {
	Kind      EventKind
	AgentPath []string

	Delta   string          // KindMessageDelta
	Tool    string          // KindToolStart, KindToolEnd
	Args    json.RawMessage // KindToolStart
	Result  json.RawMessage // KindToolEnd
	IsError bool            // KindToolEnd
	Err     error           // KindError
	Summary *RunSummary     // KindRunEnd
}

// IsSubagent reports whether the event came from a subagent (non-empty
// AgentPath) rather than the root agent.
func (e Event) IsSubagent() bool { return len(e.AgentPath) > 0 }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./event/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add event/event.go event/event_test.go
git commit -m "feat(event): jess run event types"
```

---

### Task 4: event Stream

**Files:**
- Create: `event/stream.go`
- Test: `event/stream_test.go`

Design note: `Stream` is single-consumer (one `range` over `Events()`) but
must accept sends from many producers (the ACL translator now, the subagent
Pool's merger later). `Send` after `Close` must be a no-op, never a panic.
Correctness is via an `RWMutex`: `Send` holds the read lock around the channel
send so `Close` (write lock) cannot close the channel mid-send. Unblocking a
producer that is blocked on a full buffer is out of scope for this phase;
producers are expected to stop (via context cancellation, wired in Phase 2/3)
before `Close`.

- [ ] **Step 1: Write the failing test**

Create `event/stream_test.go`:

```go
package event

import (
	"sync"
	"testing"
)

func TestStream_SendReceiveClose(t *testing.T) {
	s := NewStream(4)
	s.Send(Event{Kind: KindRunStart})
	s.Send(Event{Kind: KindRunEnd})
	s.Close()

	var kinds []EventKind
	for ev := range s.Events() { // range must terminate once Close ran
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) != 2 || kinds[0] != KindRunStart || kinds[1] != KindRunEnd {
		t.Fatalf("events = %v, want [run_start run_end]", kinds)
	}
}

func TestStream_SendAfterCloseIsNoop(t *testing.T) {
	s := NewStream(1)
	s.Close()
	s.Send(Event{Kind: KindError}) // must not panic
	s.Close()                      // double close must not panic
	if _, ok := <-s.Events(); ok {
		t.Error("closed stream should yield a closed channel")
	}
}

// Many producers, one consumer, then Close after producers finish. Run with
// -race to catch any data race on the channel or closed flag.
func TestStream_ConcurrentProducers(t *testing.T) {
	s := NewStream(8)

	var got int
	done := make(chan struct{})
	go func() {
		for range s.Events() {
			got++
		}
		close(done)
	}()

	const producers, each = 16, 50
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				s.Send(Event{Kind: KindMessageDelta})
			}
		}()
	}
	wg.Wait()
	s.Close()
	<-done

	if got != producers*each {
		t.Errorf("received %d events, want %d", got, producers*each)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./event/`
Expected: build failure (`undefined: NewStream`).

- [ ] **Step 3: Write minimal implementation**

Create `event/stream.go`:

```go
package event

import "sync"

// Stream is a single-consumer fan-out of run Events. Producers (the
// anti-corruption layer, and later the subagent Pool's merger) call Send; the
// host ranges over Events(). Backpressure is by blocking send against the
// buffered channel: a slow consumer slows producers, bounding memory.
//
// Send after Close is a no-op (never a panic), so a producer that outlives the
// run is harmless. Close is idempotent.
type Stream struct {
	mu     sync.RWMutex
	ch     chan Event
	closed bool
}

// NewStream returns a Stream whose channel buffers up to buffer events.
func NewStream(buffer int) *Stream {
	return &Stream{ch: make(chan Event, buffer)}
}

// Events returns the receive channel. It is closed when Close runs, so callers
// can range over it.
func (s *Stream) Events() <-chan Event { return s.ch }

// Send delivers ev to the consumer. It blocks while the buffer is full (unless
// the stream is closed). After Close it drops ev and returns.
//
// The read lock is held across the channel send so Close, which needs the
// write lock, cannot close the channel mid-send.
func (s *Stream) Send(ev Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	s.ch <- ev
}

// Close closes the stream. Idempotent. Subsequent Send calls are no-ops and the
// Events channel is closed so consumers ranging over it terminate.
func (s *Stream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}
```

- [ ] **Step 4: Run tests (with race detector) to verify they pass**

Run: `go test -race ./event/`
Expected: PASS (all three Stream tests plus Task 3's event tests).

- [ ] **Step 5: Commit**

```bash
git add event/stream.go event/stream_test.go
git commit -m "feat(event): single-consumer Stream with race-safe Send/Close"
```

---

### Task 5: phase gate (vet, race, lint)

**Files:** none (verification only).

- [ ] **Step 1: Vet all new packages**

Run: `go vet ./message/ ./tool/ ./event/`
Expected: no output, exit 0.

- [ ] **Step 2: Race-test the whole module**

Run: `go test -race ./...`
Expected: PASS for `message`, `tool`, `event` (and existing packages unaffected).

- [ ] **Step 3: Lint the new code**

Run: `make lint`
Expected: `0 issues.`

- [ ] **Step 4: Confirm the ACL boundary still holds (no agentcore leak yet)**

Run: `grep -rl "voocel/agentcore" message tool event || echo "clean"`
Expected: `clean` (these packages must never import agentcore).

- [ ] **Step 5: Commit any lint fixes if needed**

```bash
git add -A
git commit -m "chore: phase-1 vet/race/lint gate" --allow-empty
```

---

## Self-review

**Spec coverage (Phase 1 of ADR 0001):**
- `jess/message` (Message, ContentBlock, Role) — Task 1. ✓
- `jess/tool` (Tool interface) — Task 2. ✓
- `jess/event` (Event, EventKind, Stream) — Tasks 3–4. ✓
- "no agentcore type appears" / boundary holds — Task 5 Step 4. ✓
- Concurrency: Stream single-consumer, serialized producers, race-tested — Task 4. ✓
- Deferred to later phases (correctly out of scope here): `Agent`/`Session`/`Run` (Phase 2), ACL translation + runtime (Phase 2), subagent `Pool` and MPSC merge (Phase 3), memory/skill migration and `golang.org/x/sync` (Phases 3–4).

**Placeholder scan:** none. Every code step shows complete code; every run step shows the command and expected result.

**Type consistency:** `EventKind` constants (`KindMessageDelta`, `KindRunEnd`, ...) match between `event.go`, the tests, and `stream_test.go`. `RunSummary` fields (`Turns`, `ToolCalls`, `EndReason`) are consistent. `BlockKind`/`Role` constants match between `message.go` and its test. `Stream` methods (`NewStream`, `Events`, `Send`, `Close`) match between `stream.go` and `stream_test.go`.
