# jess Model Layer Implementation Plan (Phase 2b-model)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A vendor-free, streaming-first `model.Model` interface and the ACL adapters that bridge it to agentcore's `ChatModel`, supporting local/custom and cloud models with ctx-based interruption.

**Architecture:** Phase 2b-model of ADR 0001. New vendor-free package `model/` (interface + `Chunk`/`ToolSpec`/`Usage`/`Response` + `Once` helper). New ACL files in `internal/acl/` translate both directions: `model.Model` -> `agentcore.ChatModel` (for the loop) and `agentcore.ChatModel` -> `model.Model` (for jess-provided cloud models). `jess.LiteLLM` (root package) builds a litellm-backed model inside the ACL and returns it as a `model.Model` (native passthrough).

**Tech Stack:** Go 1.26, `github.com/voocel/agentcore` (`ac`) and its `agentcore/llm` subpackage — only inside `internal/acl`. jess `message`/`event`/`model` packages. Stdlib `context`.

**Verified agentcore facts (v1.6.9):**
- `ChatModel`: `Generate(ctx, []Message, []ToolSpec, ...CallOption) (*LLMResponse, error)`, `GenerateStream(...) (<-chan StreamEvent, error)`, `SupportsTools() bool`. `LLMResponse` is `{Message Message}`.
- `StreamEvent{Type, Delta, Message, StopReason, Err, CompletedToolCall, ...}`. Types: `*_start/*_delta/*_end` for text/thinking/toolcall, `StreamEventDone`, `StreamEventError`. The loop (`loop.go:680`) treats `StreamEventDone.Message` as the authoritative final message; a stream that closes without `Done` is a `PartialStreamError`.
- `ToolSpec{Name, Description string, Parameters any}`. `Usage{Input, Output, TotalTokens int, ...}`.
- `llm.NewModel(provider, model string, ...ModelOption) (*LiteLLMAdapter, error)` builds a `ChatModel`.
- Interruption: the loop's ctx (cancelled by `Agent.Abort`) is passed to `GenerateStream`.

---

## File structure

| File | Responsibility |
|---|---|
| `model/model.go` | `Model` interface, `ToolSpec`, `Usage`, `Response`, `Chunk` |
| `model/once.go` | `Once` helper: one-shot fn -> single-`Done`-chunk `Model` |
| `model/model_test.go` | `Once` behavior + interface compliance |
| `internal/acl/model.go` | `ToAC` (model.Model -> ac.ChatModel, native unwrap), `streamAdapter`, `nativeModel`, helpers |
| `internal/acl/model_test.go` | both bridge directions, ctx interruption, native passthrough |
| `litellm.go` (root) | `LiteLLM(provider, model, ...)` -> `model.Model` via the ACL |
| `litellm_test.go` (root) | constructor returns a usable model.Model (no network) |

`model/` imports only `context`, `message`, `event` — never agentcore. The boundary test (Phase 2a) keeps the root `jess` package and `model/` agentcore-free; only `internal/acl` imports agentcore.

---

### Task 1: model package types + Model interface

**Files:**
- Create: `model/model.go`
- Test: `model/model_test.go` (interface compliance only here; `Once` tested in Task 2)

- [ ] **Step 1: Write the failing test** at `model/model_test.go`:

```go
package model

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess/message"
)

// fakeModel proves the interface is implementable and locks the method set.
type fakeModel struct{ chunks []Chunk }

func (f fakeModel) SupportsTools() bool { return true }
func (f fakeModel) Stream(_ context.Context, _ []message.Message, _ []ToolSpec) (<-chan Chunk, error) {
	ch := make(chan Chunk, len(f.chunks))
	for _, c := range f.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

var _ Model = fakeModel{}

func TestModel_StreamYieldsChunks(t *testing.T) {
	m := fakeModel{chunks: []Chunk{
		{Delta: "hi", DeltaKind: ""},
		{Done: true, Message: message.Message{Role: message.RoleAssistant}, StopReason: "stop"},
	}}
	ch, err := m.Stream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []Chunk
	for c := range ch {
		got = append(got, c)
	}
	if len(got) != 2 || got[0].Delta != "hi" || !got[1].Done || got[1].StopReason != "stop" {
		t.Fatalf("chunks = %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./model/`
Expected: build failure (`undefined: Model`, `Chunk`, `ToolSpec`).

- [ ] **Step 3: Write the implementation** at `model/model.go`:

```go
// Package model defines jess's vendor-free, streaming-first LLM contract.
// Implement Model for a local or custom model; use a jess-provided constructor
// (such as jess.LiteLLM) for a cloud provider. The anti-corruption layer
// (internal/acl) adapts a Model to the agent harness, so nothing here imports
// the harness.
package model

import (
	"context"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
)

// ToolSpec describes a tool to the model: name, description, and JSON schema.
type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Usage reports token consumption for a generation.
type Usage struct {
	Input       int
	Output      int
	TotalTokens int
}

// Response is the outcome of a one-shot generation: the assistant message
// (which may contain tool-call blocks), token usage, and the provider's stop
// reason. Used by the Once helper; streaming models emit Chunks instead.
type Response struct {
	Message    message.Message
	Usage      Usage
	StopReason string
}

// Chunk is one streaming increment from a Model.
//
//   - A delta chunk carries incremental Delta text of the given DeltaKind.
//   - The final chunk has Done=true with Message set to the complete assistant
//     message (text plus any tool-call blocks), and Usage/StopReason filled.
//   - An error chunk has Err set.
//
// A Stream must terminate with exactly one Done chunk or one Err chunk.
type Chunk struct {
	Delta      string
	DeltaKind  event.DeltaKind
	Done       bool
	Message    message.Message
	Usage      Usage
	StopReason string
	Err        error
}

// Model is jess's streaming-first LLM interface.
//
// Stream emits Chunks until it sends a final Done chunk (carrying the complete
// assistant Message) or an Err chunk, then closes the channel. Stream MUST
// honor ctx: on ctx.Done() it stops producing and closes the channel promptly
// (it may send a final Err chunk with ctx.Err()). Context cancellation is how
// interruption (Session.Abort) reaches the model mid-stream.
type Model interface {
	Stream(ctx context.Context, msgs []message.Message, tools []ToolSpec) (<-chan Chunk, error)
	SupportsTools() bool
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./model/`
Expected: PASS. Then `gofmt -l model/` (empty), `go vet ./model/`.

- [ ] **Step 5: Commit**

```bash
git add model/model.go model/model_test.go
git commit -m "feat(model): streaming-first vendor-free Model interface"
```

---

### Task 2: model.Once helper

**Files:**
- Create: `model/once.go`
- Test: `model/model_test.go` (append)

- [ ] **Step 1: Append the failing tests** to `model/model_test.go`:

```go
import "errors" // add to the existing import block

func TestOnce_EmitsSingleDoneChunk(t *testing.T) {
	m := Once(true, func(_ context.Context, _ []message.Message, _ []ToolSpec) (*Response, error) {
		return &Response{
			Message:    message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "ok"}}},
			Usage:      Usage{Input: 1, Output: 2, TotalTokens: 3},
			StopReason: "stop",
		}, nil
	})
	if !m.SupportsTools() {
		t.Error("SupportsTools should be true")
	}
	ch, _ := m.Stream(context.Background(), nil, nil)
	var got []Chunk
	for c := range ch {
		got = append(got, c)
	}
	if len(got) != 1 || !got[0].Done || got[0].Message.Text() != "ok" || got[0].Usage.TotalTokens != 3 {
		t.Fatalf("chunks = %+v", got)
	}
}

func TestOnce_EmitsErrChunk(t *testing.T) {
	m := Once(false, func(_ context.Context, _ []message.Message, _ []ToolSpec) (*Response, error) {
		return nil, errors.New("boom")
	})
	ch, _ := m.Stream(context.Background(), nil, nil)
	c := <-ch
	if c.Err == nil || c.Done {
		t.Fatalf("want error chunk, got %+v", c)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./model/ -run TestOnce`
Expected: build failure (`undefined: Once`).

- [ ] **Step 3: Write the implementation** at `model/once.go`:

```go
package model

import (
	"context"

	"github.com/guygrigsby/jess/message"
)

// GenerateFunc is a one-shot, non-streaming generation. It should honor ctx so
// the resulting Model is interruptible.
type GenerateFunc func(ctx context.Context, msgs []message.Message, tools []ToolSpec) (*Response, error)

// Once adapts a one-shot generation function into a Model: Stream calls fn and
// emits its result as a single Done chunk (or an Err chunk on failure).
// supportsTools is reported by SupportsTools. This keeps trivial local models
// trivial; a model that can stream tokens should implement Model directly.
func Once(supportsTools bool, fn GenerateFunc) Model {
	return onceModel{supportsTools: supportsTools, fn: fn}
}

type onceModel struct {
	supportsTools bool
	fn            GenerateFunc
}

func (m onceModel) SupportsTools() bool { return m.supportsTools }

func (m onceModel) Stream(ctx context.Context, msgs []message.Message, tools []ToolSpec) (<-chan Chunk, error) {
	ch := make(chan Chunk, 1)
	go func() {
		defer close(ch)
		resp, err := m.fn(ctx, msgs, tools)
		if err != nil {
			ch <- Chunk{Err: err}
			return
		}
		ch <- Chunk{Done: true, Message: resp.Message, Usage: resp.Usage, StopReason: resp.StopReason}
	}()
	return ch, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./model/`
Expected: PASS (all model tests). Then `gofmt -l model/`, `go vet ./model/`.

- [ ] **Step 5: Commit**

```bash
git add model/once.go model/model_test.go
git commit -m "feat(model): Once helper for one-shot models"
```

---

### Task 3: ACL helpers — ToolSpec, assistant message, delta-event mapping

Small pure helpers the bridges need. Added to a new `internal/acl/model.go`.

**Files:**
- Create: `internal/acl/model.go`
- Test: `internal/acl/model_test.go`

- [ ] **Step 1: Write the failing test** at `internal/acl/model_test.go`:

```go
package acl

import (
	"testing"

	ac "github.com/voocel/agentcore"
	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

func TestToolSpecFromAC(t *testing.T) {
	in := ac.ToolSpec{Name: "search", Description: "d", Parameters: map[string]any{"type": "object"}}
	got := toolSpecFromAC(in)
	if got.Name != "search" || got.Description != "d" || got.Schema["type"] != "object" {
		t.Fatalf("got %+v", got)
	}
}

func TestToolSpecFromAC_NonMapParameters(t *testing.T) {
	got := toolSpecFromAC(ac.ToolSpec{Name: "x", Parameters: "not-a-map"})
	if got.Schema != nil {
		t.Errorf("non-map Parameters should yield nil Schema, got %v", got.Schema)
	}
}

func TestDeltaEventType(t *testing.T) {
	tests := []struct {
		in   event.DeltaKind
		want ac.StreamEventType
	}{
		{event.DeltaText, ac.StreamEventTextDelta},
		{event.DeltaThinking, ac.StreamEventThinkingDelta},
		{event.DeltaToolCall, ac.StreamEventToolCallDelta},
	}
	for _, tt := range tests {
		if got := deltaEventType(tt.in); got != tt.want {
			t.Errorf("deltaEventType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAssistantMessageToAC(t *testing.T) {
	jm := message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "hi"}}}
	got := assistantMessageToAC(jm, model.Usage{Input: 1, Output: 2, TotalTokens: 3}, "stop")
	if got.Role != ac.RoleAssistant || len(got.Content) != 1 || got.Content[0].Text != "hi" {
		t.Fatalf("message = %+v", got)
	}
	if got.StopReason != ac.StopReason("stop") || got.Usage == nil || got.Usage.TotalTokens != 3 {
		t.Errorf("stop/usage = %v / %+v", got.StopReason, got.Usage)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/acl/ -run 'TestToolSpecFromAC|TestDeltaEventType|TestAssistantMessageToAC'`
Expected: build failure (`undefined: toolSpecFromAC`).

- [ ] **Step 3: Write the implementation** at `internal/acl/model.go`:

```go
package acl

import (
	ac "github.com/voocel/agentcore"
	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
)

// toolSpecFromAC converts an agentcore ToolSpec to a jess ToolSpec. agentcore's
// Parameters is an untyped JSON schema; jess models it as a map. A non-map
// Parameters yields a nil Schema (the model gets no schema rather than a bogus
// one).
func toolSpecFromAC(s ac.ToolSpec) model.ToolSpec {
	schema, _ := s.Parameters.(map[string]any)
	return model.ToolSpec{Name: s.Name, Description: s.Description, Schema: schema}
}

// deltaEventType maps a jess delta classification to the agentcore stream event
// type the loop expects for that delta.
func deltaEventType(k event.DeltaKind) ac.StreamEventType {
	switch k {
	case event.DeltaThinking:
		return ac.StreamEventThinkingDelta
	case event.DeltaToolCall:
		return ac.StreamEventToolCallDelta
	default:
		return ac.StreamEventTextDelta
	}
}

// assistantMessageToAC builds the agentcore assistant Message for a Done chunk:
// the translated content blocks plus usage and stop reason, which the loop
// reads from StreamEventDone.Message as authoritative.
func assistantMessageToAC(m message.Message, u model.Usage, stop string) ac.Message {
	acMsg := messagesToAC([]message.Message{m})[0]
	acMsg.StopReason = ac.StopReason(stop)
	acMsg.Usage = &ac.Usage{Input: u.Input, Output: u.Output, TotalTokens: u.TotalTokens}
	return acMsg
}
```

Note: `messagesToAC` (Phase 2a) maps a single assistant message 1:1, so `[0]` is safe for a non-tool-result message. If a future change makes assistant messages expand, revisit.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/acl/ -run 'TestToolSpecFromAC|TestDeltaEventType|TestAssistantMessageToAC'`
Expected: PASS. Then `go test ./internal/acl/`, `gofmt -l internal/acl/`, `go vet ./internal/acl/`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/model.go internal/acl/model_test.go
git commit -m "feat(acl): model-call translation helpers"
```

---

### Task 4: streamAdapter — model.Model -> agentcore.ChatModel

Implements `ac.ChatModel` from a jess `model.Model`: `GenerateStream` bridges
jess `Chunk`s to agentcore `StreamEvent`s (cancellation-aware on both ends),
`Generate` drains the stream to the final message, `SupportsTools` delegates.

**Files:**
- Modify: `internal/acl/model.go`
- Test: `internal/acl/model_test.go` (append)

- [ ] **Step 1: Append the failing tests** to `internal/acl/model_test.go` (add imports `"context"`, `"errors"`):

```go
// scriptedModel is a model.Model that emits a fixed Chunk script.
type scriptedModel struct{ chunks []model.Chunk }

func (scriptedModel) SupportsTools() bool { return true }
func (m scriptedModel) Stream(ctx context.Context, _ []message.Message, _ []model.ToolSpec) (<-chan model.Chunk, error) {
	ch := make(chan model.Chunk)
	go func() {
		defer close(ch)
		for _, c := range m.chunks {
			select {
			case <-ctx.Done():
				return
			case ch <- c:
			}
		}
	}()
	return ch, nil
}

func TestStreamAdapter_GenerateStreamMapsChunks(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{
		{Delta: "he", DeltaKind: event.DeltaText},
		{Delta: "llo", DeltaKind: event.DeltaText},
		{Done: true, Message: message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "hello"}}}, StopReason: "stop"},
	}}
	var cm ac.ChatModel = ToAC(m)
	stream, err := cm.GenerateStream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var types []ac.StreamEventType
	var final ac.Message
	for ev := range stream {
		types = append(types, ev.Type)
		if ev.Type == ac.StreamEventDone {
			final = ev.Message
		}
	}
	// two text deltas then done
	if len(types) != 3 || types[0] != ac.StreamEventTextDelta || types[2] != ac.StreamEventDone {
		t.Fatalf("event types = %v", types)
	}
	if final.Content[0].Text != "hello" || final.StopReason != ac.StopReason("stop") {
		t.Errorf("final = %+v", final)
	}
}

func TestStreamAdapter_GenerateDrainsToFinal(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{
		{Delta: "x", DeltaKind: event.DeltaText},
		{Done: true, Message: message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.BlockText, Text: "done"}}}, StopReason: "stop"},
	}}
	resp, err := ToAC(m).Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content[0].Text != "done" {
		t.Errorf("final message = %+v", resp.Message)
	}
}

func TestStreamAdapter_ErrChunkBecomesError(t *testing.T) {
	m := scriptedModel{chunks: []model.Chunk{{Err: errors.New("boom")}}}
	_, err := ToAC(m).Generate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("want error from Err chunk")
	}
}

func TestStreamAdapter_ContextCancelStops(t *testing.T) {
	// A model that would block forever sending its second chunk; cancelling ctx
	// must terminate the bridged stream.
	m := scriptedModel{chunks: []model.Chunk{{Delta: "a", DeltaKind: event.DeltaText}, {Delta: "b", DeltaKind: event.DeltaText}}}
	ctx, cancel := context.WithCancel(context.Background())
	stream, _ := ToAC(m).GenerateStream(ctx, nil, nil)
	<-stream // first event
	cancel()
	// drain; must close without hanging
	for range stream {
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/acl/ -run TestStreamAdapter`
Expected: build failure (`undefined: ToAC`).

- [ ] **Step 3: Append the implementation** to `internal/acl/model.go` (add imports `"context"`, `"errors"`):

```go
// ToAC adapts a jess model.Model into an agentcore.ChatModel for the loop. If m
// is a jess-provided cloud model (nativeModel), its underlying ChatModel is
// returned directly — zero translation. Otherwise a translating streamAdapter
// is returned.
func ToAC(m model.Model) ac.ChatModel {
	if nm, ok := m.(nativeModel); ok {
		return nm.cm
	}
	return streamAdapter{m: m}
}

// streamAdapter implements ac.ChatModel by bridging to a jess model.Model.
type streamAdapter struct{ m model.Model }

func (a streamAdapter) SupportsTools() bool { return a.m.SupportsTools() }

func (a streamAdapter) GenerateStream(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, _ ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	jmsgs := make([]message.Message, len(msgs))
	for i := range msgs {
		jmsgs[i] = messageFromAC(msgs[i])
	}
	jtools := make([]model.ToolSpec, len(tools))
	for i := range tools {
		jtools[i] = toolSpecFromAC(tools[i])
	}
	chunks, err := a.m.Stream(ctx, jmsgs, jtools)
	if err != nil {
		return nil, err
	}
	out := make(chan ac.StreamEvent)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-chunks:
				if !ok {
					return
				}
				var ev ac.StreamEvent
				switch {
				case c.Err != nil:
					ev = ac.StreamEvent{Type: ac.StreamEventError, Err: c.Err}
				case c.Done:
					msg := assistantMessageToAC(c.Message, c.Usage, c.StopReason)
					ev = ac.StreamEvent{Type: ac.StreamEventDone, Message: msg, StopReason: msg.StopReason}
				default:
					ev = ac.StreamEvent{Type: deltaEventType(c.DeltaKind), Delta: c.Delta}
				}
				select {
				case <-ctx.Done():
					return
				case out <- ev:
				}
				if c.Done || c.Err != nil {
					return
				}
			}
		}
	}()
	return out, nil
}

func (a streamAdapter) Generate(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, opts ...ac.CallOption) (*ac.LLMResponse, error) {
	stream, err := a.GenerateStream(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	var final ac.Message
	var done bool
	for ev := range stream {
		switch ev.Type {
		case ac.StreamEventError:
			return nil, ev.Err
		case ac.StreamEventDone:
			final, done = ev.Message, true
		}
	}
	if !done {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("acl: model stream ended without a done chunk")
	}
	return &ac.LLMResponse{Message: final}, nil
}
```

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./internal/acl/ -run TestStreamAdapter`
Expected: PASS (including the context-cancel test, which must not hang). Then `go test ./internal/acl/`, `gofmt -l internal/acl/`, `go vet ./internal/acl/`.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/model.go internal/acl/model_test.go
git commit -m "feat(acl): streamAdapter bridges model.Model to agentcore.ChatModel"
```

---

### Task 5: nativeModel + cloud stream-to-chunks + jess.LiteLLM

`nativeModel` wraps an `agentcore.ChatModel` as a `model.Model` (for
jess-provided cloud models). Its `Stream` bridges the harness stream back to
jess `Chunk`s so it is a valid standalone `Model`; `ToAC` short-circuits it for
the loop. `acl.NewLiteLLMModel` builds a litellm model; the root `jess.LiteLLM`
is the public, agentcore-free entry point.

**Files:**
- Modify: `internal/acl/model.go`
- Test: `internal/acl/model_test.go` (append)
- Create: `litellm.go` (root package `jess`)
- Create: `litellm_test.go` (root package `jess`)

- [ ] **Step 1: Append the failing ACL tests** to `internal/acl/model_test.go`:

```go
// fakeChatModel is an agentcore.ChatModel emitting a fixed StreamEvent script.
type fakeChatModel struct{ events []ac.StreamEvent }

func (fakeChatModel) SupportsTools() bool { return true }
func (m fakeChatModel) GenerateStream(ctx context.Context, _ []ac.Message, _ []ac.ToolSpec, _ ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	ch := make(chan ac.StreamEvent, len(m.events))
	for _, e := range m.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}
func (m fakeChatModel) Generate(context.Context, []ac.Message, []ac.ToolSpec, ...ac.CallOption) (*ac.LLMResponse, error) {
	return &ac.LLMResponse{}, nil
}

func TestNativeModel_ToACUnwrapsToUnderlying(t *testing.T) {
	cm := fakeChatModel{}
	nm := newNativeModel(cm)
	if ToAC(nm) != ac.ChatModel(cm) {
		t.Error("ToAC(nativeModel) must return the underlying ChatModel (passthrough)")
	}
}

func TestNativeModel_StreamBridgesToChunks(t *testing.T) {
	cm := fakeChatModel{events: []ac.StreamEvent{
		{Type: ac.StreamEventTextDelta, Delta: "hi"},
		{Type: ac.StreamEventDone, Message: ac.Message{Role: ac.RoleAssistant, Content: []ac.ContentBlock{ac.TextBlock("hi")}}, StopReason: ac.StopReason("stop")},
	}}
	nm := newNativeModel(cm)
	ch, err := nm.Stream(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []model.Chunk
	for c := range ch {
		got = append(got, c)
	}
	if len(got) != 2 || got[0].Delta != "hi" || got[0].DeltaKind != event.DeltaText {
		t.Fatalf("chunks = %+v", got)
	}
	if !got[1].Done || got[1].Message.Text() != "hi" || got[1].StopReason != "stop" {
		t.Errorf("final chunk = %+v", got[1])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/acl/ -run TestNativeModel`
Expected: build failure (`undefined: newNativeModel`).

- [ ] **Step 3a: Append the ACL implementation** to `internal/acl/model.go` (add `"github.com/voocel/agentcore/llm"` to imports):

```go
// nativeModel is a model.Model backed directly by an agentcore.ChatModel
// (jess-provided cloud models). ToAC unwraps it for zero-overhead passthrough;
// its Stream bridges the harness stream to jess Chunks so it is also a valid
// standalone model.Model.
type nativeModel struct{ cm ac.ChatModel }

func newNativeModel(cm ac.ChatModel) nativeModel { return nativeModel{cm: cm} }

func (n nativeModel) SupportsTools() bool { return n.cm.SupportsTools() }

func (n nativeModel) Stream(ctx context.Context, msgs []message.Message, tools []model.ToolSpec) (<-chan model.Chunk, error) {
	acMsgs := messagesToAC(msgs)
	acTools := make([]ac.ToolSpec, len(tools))
	for i, t := range tools {
		acTools[i] = ac.ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.Schema}
	}
	stream, err := n.cm.GenerateStream(ctx, acMsgs, acTools)
	if err != nil {
		return nil, err
	}
	out := make(chan model.Chunk)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-stream:
				if !ok {
					return
				}
				c, emit := chunkFromAC(ev)
				if !emit {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case out <- c:
				}
				if c.Done || c.Err != nil {
					return
				}
			}
		}
	}()
	return out, nil
}

// chunkFromAC maps an agentcore StreamEvent to a jess Chunk. start/end framing
// events have no jess equivalent and return emit=false.
func chunkFromAC(ev ac.StreamEvent) (model.Chunk, bool) {
	switch ev.Type {
	case ac.StreamEventTextDelta:
		return model.Chunk{Delta: ev.Delta, DeltaKind: event.DeltaText}, true
	case ac.StreamEventThinkingDelta:
		return model.Chunk{Delta: ev.Delta, DeltaKind: event.DeltaThinking}, true
	case ac.StreamEventToolCallDelta:
		return model.Chunk{Delta: ev.Delta, DeltaKind: event.DeltaToolCall}, true
	case ac.StreamEventDone:
		return model.Chunk{Done: true, Message: messageFromAC(ev.Message), StopReason: string(ev.Message.StopReason)}, true
	case ac.StreamEventError:
		return model.Chunk{Err: ev.Err}, true
	default:
		return model.Chunk{}, false
	}
}

// NewLiteLLMModel builds a litellm-backed cloud model and returns it as a
// model.Model (a native passthrough). provider/model are litellm identifiers
// (e.g. "openai","gpt-4o"); options configure the underlying adapter.
func NewLiteLLMModel(provider, modelID string, opts ...llm.ModelOption) (model.Model, error) {
	cm, err := llm.NewModel(provider, modelID, opts...)
	if err != nil {
		return nil, err
	}
	return newNativeModel(cm), nil
}
```

- [ ] **Step 3b: Write the root constructor** at `litellm.go`:

```go
package jess

import (
	"github.com/guygrigsby/jess/internal/acl"
	"github.com/guygrigsby/jess/model"
)

// LiteLLM builds a cloud model backed by agentcore's litellm adapter and
// returns it as a vendor-free model.Model, suitable for jess.WithModel.
// provider and modelID are litellm identifiers, e.g. LiteLLM("openai","gpt-4o").
// This is a convenience for cloud providers; for a local or custom model,
// implement model.Model directly (or use model.Once).
//
// The agentcore dependency stays inside internal/acl; this package does not
// import the harness.
func LiteLLM(provider, modelID string) (model.Model, error) {
	return acl.NewLiteLLMModel(provider, modelID)
}
```

Note: `litellm.go` is the first file in the root `jess` package. It declares `package jess`. Provider option passthrough (API key, base URL) is deferred; add jess-typed options here in a later task when `jess.New` options land (Phase 2b-runtime).

- [ ] **Step 3c: Write the root test** at `litellm_test.go`:

```go
package jess

import (
	"testing"

	"github.com/guygrigsby/jess/model"
)

// LiteLLM should return a usable model.Model without performing any network
// I/O at construction (litellm adapter construction is lazy).
func TestLiteLLM_ReturnsModel(t *testing.T) {
	m, err := LiteLLM("openai", "gpt-4o")
	if err != nil {
		t.Fatalf("LiteLLM: %v", err)
	}
	var _ model.Model = m
	if m == nil {
		t.Fatal("expected a non-nil model.Model")
	}
}
```

If `llm.NewModel` returns an error for an unknown provider without network, adjust the test to a provider/model pair that constructs cleanly; the intent is "constructor wiring works, no network." Verify by running it.

- [ ] **Step 4: Run tests (race) to verify they pass**

Run: `go test -race ./internal/acl/ -run TestNativeModel` then `go test -race ./...`
Expected: PASS. Then `gofmt -l .`, `go vet ./...`.

If `TestLiteLLM_ReturnsModel` fails because `llm.NewModel` needs an API key or network at construction, change it to assert only that the wiring compiles and the error (if any) is a construction error, not a panic — keep it network-free.

- [ ] **Step 5: Commit**

```bash
git add internal/acl/model.go internal/acl/model_test.go litellm.go litellm_test.go
git commit -m "feat: jess.LiteLLM cloud model + ACL native passthrough"
```

---

### Task 6: phase gate

**Files:** none (verification only).

- [ ] **Step 1: Boundary still holds**

Run: `go test ./internal/acl/ -run TestAgentcoreImportBoundary`
Expected: PASS. The new agentcore-importing code (`internal/acl/model.go`) is under `internal/acl`; the root `litellm.go` imports `internal/acl` and `model`, NOT agentcore. If the boundary test flags `litellm.go`, the import is wrong — fix it to go through the ACL.

- [ ] **Step 2: Full race suite**

Run: `go test -race ./...`
Expected: all PASS (`model`, `internal/acl`, root `jess`, and existing packages).

- [ ] **Step 3: Lint + license**

Run: `make lint` (expect `0 issues.`) and `make license-audit` (expect pass; agentcore/litellm are Apache-2.0/MIT).

- [ ] **Step 4: Commit (if any fixes)**

```bash
git add -A
git commit -m "chore: phase 2b-model gate" --allow-empty
```

---

## Self-review

**Spec coverage (Phase 2b-model of ADR 0001):**
- Vendor-free streaming-first `model.Model` + `Chunk`/`ToolSpec`/`Usage`/`Response` — Task 1. ✓
- `model.Once` one-shot helper — Task 2. ✓
- ACL `model.Model` -> `agentcore.ChatModel` (`GenerateStream` maps Chunks incl. authoritative `Done`; `Generate` drains; ctx-aware both ends) — Tasks 3–4. ✓
- ACL native passthrough + cloud `ChatModel` -> `model.Model` bridge — Task 5. ✓
- `jess.LiteLLM` public constructor, agentcore confined to ACL — Task 5. ✓
- Interruption via ctx (cancel test) — Task 4. ✓
- Boundary intact — Task 6. ✓
- Out of scope (Phase 2b-runtime): `Agent`/`Session`/`Run`, `jess.New`/`WithModel`, steering/follow-up/abort delegation, the event Stream wiring. Provider option passthrough (API key/base URL) deferred to when `jess.New` options land.

**Placeholder scan:** none. Two tests (`TestLiteLLM_ReturnsModel`, and the cancel test) carry explicit "verify/adjust if" notes with concrete fallback instructions, not vague TODOs.

**Type consistency:** `ToAC`, `streamAdapter`, `nativeModel`, `newNativeModel`, `chunkFromAC`, `toolSpecFromAC`, `deltaEventType`, `assistantMessageToAC`, `NewLiteLLMModel` are consistent across impl and tests. jess `model` fields (`Delta`, `DeltaKind`, `Done`, `Message`, `Usage`, `StopReason`, `Err`) match between `model.go` and the ACL bridge. agentcore fields (`StreamEvent.Type/Delta/Message/StopReason/Err`, `Message.StopReason/Usage`, `ToolSpec.Parameters`, `LLMResponse.Message`) match v1.6.9. Reuses Phase 2a `messagesToAC`/`messageFromAC`.
