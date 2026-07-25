package core

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

// Race regression: ContextManager.Project must be safe to call concurrently —
// agentcore would never do that in production, but hosts that share a
// ContextManager across goroutines shouldn't hit data races.
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
	// Core leads (stable prefix), the turn follows, Relevant trails (volatile).
	if len(proj.Messages) != 3 {
		t.Fatalf("expected core + original + relevant, got %d msgs", len(proj.Messages))
	}
	memContent := proj.Messages[0].TextContent()
	if !strings.Contains(memContent, "senior engineer") {
		t.Errorf("user fact (AlwaysInclude) should appear regardless of relevance; got: %s", memContent)
	}
	if !strings.Contains(memContent, "Core memories") {
		t.Errorf("core block should be labeled; got: %s", memContent)
	}
}

// TestContextManager_Project_VolatileRecallGoesLast: the Relevant block is
// rescored against the latest turn, so its bytes differ every turn. Prepending it
// puts volatile content ahead of the whole conversation, which defeats a
// provider's prefix cache: nothing after it can ever match, so the entire
// transcript is re-uploaded every turn. It belongs after the conversation, with
// the last stable message carrying the cache breakpoint.
func TestContextManager_Project_VolatileRecallGoesLast(t *testing.T) {
	store := memory.NewInMemoryStore()
	// project is a recall kind (not AlwaysInclude), so this lands in Relevant.
	_, _ = store.Append(context.Background(), memory.Entry{
		AgentID: "main", Kind: string(memory.KindProject),
		Text: "Go generics use type parameters",
	})
	cm := NewContextManager(store, memory.NewSimpleRecaller(), ContextManagerOptions{AgentID: "main"})

	turn := ac.Message{Role: ac.Role("user"), Content: []ac.ContentBlock{ac.TextBlock("Explain Go generics.")}}
	proj, err := cm.Project(context.Background(), []ac.AgentMessage{turn})
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.Messages) != 2 {
		t.Fatalf("expected the turn + a trailing memory message, got %d", len(proj.Messages))
	}
	if got := proj.Messages[0].TextContent(); got != "Explain Go generics." {
		t.Errorf("conversation turn should come first, got %q", got)
	}
	tail := proj.Messages[len(proj.Messages)-1].TextContent()
	if !strings.Contains(tail, "Go generics use type parameters") {
		t.Errorf("recalled memories should be the last message, got %q", tail)
	}

	// The breakpoint has to land on the last stable message, not on the volatile
	// tail: an entry covering the tail is never a prefix of the next request.
	stable, ok := proj.Messages[0].(ac.Message)
	if !ok {
		t.Fatalf("message 0 is not an ac.Message: %T", proj.Messages[0])
	}
	if stable.Metadata["cache_control"] == nil {
		t.Errorf("last stable message carries no cache_control marker: %+v", stable.Metadata)
	}
	if tailMsg, ok := proj.Messages[1].(ac.Message); ok && tailMsg.Metadata["cache_control"] != nil {
		t.Errorf("volatile tail must not carry the breakpoint: %+v", tailMsg.Metadata)
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
	// With no AlwaysInclude entries there is no Core block, so the only injected
	// message is the volatile Relevant block, which trails the conversation.
	memContent := proj.Messages[len(proj.Messages)-1].TextContent()
	if strings.Contains(memContent, "Core memories") {
		t.Errorf("no AlwaysInclude entries exist; should NOT see Core block; got: %s", memContent)
	}
	if !strings.Contains(memContent, "Relevant memories") {
		t.Errorf("expected Relevant block header; got: %s", memContent)
	}
}
