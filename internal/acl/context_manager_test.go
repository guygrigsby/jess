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
