package memory

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

// fixedClock returns deterministic times so dedupe and recency tests
// don't race the wall clock.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestInMemoryStore_Append_AssignsID(t *testing.T) {
	s := NewInMemoryStore()
	got, err := s.Append(context.Background(), Entry{Text: "tabs > spaces", AgentID: "coding"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" {
		t.Error("Append should populate ID")
	}
	if got.CreatedAt.IsZero() {
		t.Error("Append should populate CreatedAt")
	}
}

func TestInMemoryStore_Append_DedupesByContent(t *testing.T) {
	s := NewInMemoryStore()
	first, _ := s.Append(context.Background(), Entry{Text: "x", AgentID: "a"})
	second, _ := s.Append(context.Background(), Entry{Text: "x", AgentID: "a"})
	if first.ID != second.ID {
		t.Errorf("dedupe failed: first=%q second=%q", first.ID, second.ID)
	}
}

func TestInMemoryStore_Append_MergesTagsOnDedupe(t *testing.T) {
	s := NewInMemoryStore()
	_, _ = s.Append(context.Background(), Entry{Text: "x", AgentID: "a", Tags: []string{"alpha"}})
	merged, _ := s.Append(context.Background(), Entry{Text: "x", AgentID: "a", Tags: []string{"beta"}})
	if len(merged.Tags) != 2 || merged.Tags[0] != "alpha" || merged.Tags[1] != "beta" {
		t.Errorf("tags = %v, want [alpha beta]", merged.Tags)
	}
}

func TestInMemoryStore_Recall_AgentScopeIncludesGlobal(t *testing.T) {
	s := NewInMemoryStore()
	_, _ = s.Append(context.Background(), Entry{Text: "global-fact"}) // no AgentID
	_, _ = s.Append(context.Background(), Entry{Text: "coding-pref", AgentID: "coding"})
	_, _ = s.Append(context.Background(), Entry{Text: "research-pref", AgentID: "research"})

	got, _ := s.Recall(context.Background(), Query{AgentID: "coding"}, 0)
	// Coding agent should see its own + global, but not research.
	if len(got) != 2 {
		t.Fatalf("recall returned %d entries, want 2: %v", len(got), got)
	}
	for _, e := range got {
		if e.AgentID == "research" {
			t.Errorf("research entry leaked to coding agent: %v", e)
		}
	}
}

func TestInMemoryStore_Recall_TagFilter(t *testing.T) {
	s := NewInMemoryStore()
	_, _ = s.Append(context.Background(), Entry{Text: "a", Tags: []string{"x", "y"}})
	_, _ = s.Append(context.Background(), Entry{Text: "b", Tags: []string{"x"}})
	_, _ = s.Append(context.Background(), Entry{Text: "c"})

	got, _ := s.Recall(context.Background(), Query{Tags: []string{"x", "y"}}, 0)
	if len(got) != 1 || got[0].Text != "a" {
		t.Errorf("tag filter wrong: %v", got)
	}
}

func TestInMemoryStore_Forget(t *testing.T) {
	s := NewInMemoryStore()
	e, _ := s.Append(context.Background(), Entry{Text: "ephemeral"})
	if err := s.Forget(context.Background(), e.ID); err != nil {
		t.Fatal(err)
	}
	// Forgetting again is a no-op.
	if err := s.Forget(context.Background(), e.ID); err != nil {
		t.Fatal("re-forget should be idempotent")
	}
	got, _ := s.Recall(context.Background(), Query{}, 0)
	if len(got) != 0 {
		t.Errorf("entry still present after Forget: %v", got)
	}
}

func TestJSONLStore_RoundTripsAcrossReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.jsonl")
	s1, err := NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s1.Append(context.Background(), Entry{Text: "first", AgentID: "a"})
	_, _ = s1.Append(context.Background(), Entry{Text: "second", AgentID: "a", Tags: []string{"important"}})

	// Reopen — same file, fresh Store instance. Entries must
	// survive.
	s2, err := NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s2.Recall(context.Background(), Query{}, 0)
	if len(got) != 2 {
		t.Fatalf("after reopen got %d entries, want 2", len(got))
	}
}

func TestJSONLStore_ForgetSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.jsonl")
	s1, _ := NewJSONLStore(path)
	e, _ := s1.Append(context.Background(), Entry{Text: "tombstone me"})
	_ = s1.Forget(context.Background(), e.ID)

	s2, _ := NewJSONLStore(path)
	got, _ := s2.Recall(context.Background(), Query{}, 0)
	if len(got) != 0 {
		t.Errorf("tombstone not honored after reopen: %v", got)
	}
}

func TestJSONLStore_Compact_DropsTombstones(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.jsonl")
	s, _ := NewJSONLStore(path)
	keep, _ := s.Append(context.Background(), Entry{Text: "keep"})
	drop, _ := s.Append(context.Background(), Entry{Text: "drop"})
	_ = s.Forget(context.Background(), drop.ID)

	if err := s.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Recall(context.Background(), Query{}, 0)
	if len(got) != 1 || got[0].ID != keep.ID {
		t.Errorf("after compact: %v", got)
	}
}

func TestSimpleRecaller_TokenizationAndScore(t *testing.T) {
	s := NewInMemoryStore()
	now := time.Now()
	s.SetClock(fixedClock(now))
	_, _ = s.Append(context.Background(), Entry{Text: "prefers tabs over spaces", AgentID: "a"})
	_, _ = s.Append(context.Background(), Entry{Text: "merge freeze ends march", AgentID: "a"})

	r := NewSimpleRecaller()
	got, err := r.Recall(context.Background(), s, "a", "Should I use tabs or spaces?", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one recall hit")
	}
	if !strings.Contains(got[0].Text, "tabs") {
		t.Errorf("most relevant entry should be the tabs one; got %q", got[0].Text)
	}
}

func TestSimpleRecaller_IncludeKindsFilter(t *testing.T) {
	s := NewInMemoryStore()
	_, _ = s.Append(context.Background(), Entry{Text: "user prefers tabs", Kind: "user"})
	_, _ = s.Append(context.Background(), Entry{Text: "merge freeze", Kind: "project"})
	_, _ = s.Append(context.Background(), Entry{Text: "lookup at oncall.example", Kind: "reference"})

	r := &SimpleRecaller{IncludeKinds: []string{"user", "project"}, MinTokenLength: 3}
	got, _ := r.Recall(context.Background(), s, "", "merge freeze tabs", 5)
	for _, e := range got {
		if e.Kind == "reference" {
			t.Errorf("reference entry leaked through IncludeKinds: %v", e)
		}
	}
}

// ContextManager wiring: Project should prepend the recalled
// memories as a user message. Compact/Sync delegate to the inner
// manager (the PassthroughInner default returns input unchanged).
func TestContextManager_Project_PrependsMemoryMessage(t *testing.T) {
	store := NewInMemoryStore()
	_, _ = store.Append(context.Background(), Entry{Text: "user prefers concise replies", Kind: "user"})

	cm := NewContextManager(store, NewSimpleRecaller(), ContextManagerOptions{})
	if cm == nil {
		t.Fatal("NewContextManager returned nil with valid inputs")
	}

	last := agentcore.Message{
		Role:    agentcore.Role("user"),
		Content: []agentcore.ContentBlock{agentcore.TextBlock("Tell me about Go modules concisely.")},
	}
	proj, err := cm.Project(context.Background(), []agentcore.AgentMessage{last})
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
	cm := NewContextManager(NewInMemoryStore(), NewSimpleRecaller(), ContextManagerOptions{})
	input := []agentcore.AgentMessage{
		agentcore.Message{Role: agentcore.Role("user"), Content: []agentcore.ContentBlock{agentcore.TextBlock("hi")}},
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
	if cm := NewContextManager(nil, NewSimpleRecaller(), ContextManagerOptions{}); cm != nil {
		t.Error("nil Store should yield nil ContextManager")
	}
	if cm := NewContextManager(NewInMemoryStore(), nil, ContextManagerOptions{}); cm != nil {
		t.Error("nil Recaller should yield nil ContextManager")
	}
}

// Race regression: ContextManager.Project must be safe to call
// concurrently — agentcore would never do that in production, but
// hosts that share a ContextManager across goroutines shouldn't
// hit data races.
func TestContextManager_ConcurrentProject_RaceClean(t *testing.T) {
	store := NewInMemoryStore()
	_, _ = store.Append(context.Background(), Entry{Text: "concurrent fact"})
	cm := NewContextManager(store, NewSimpleRecaller(), ContextManagerOptions{})

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cm.Project(context.Background(), []agentcore.AgentMessage{
				agentcore.Message{Role: agentcore.Role("user"), Content: []agentcore.ContentBlock{agentcore.TextBlock("hi")}},
			})
		}()
	}
	wg.Wait()
}
