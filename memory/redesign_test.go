package memory

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
)

// ---- Kind + KindRegistry -------------------------------------------------

func TestKindRegistry_Defaults(t *testing.T) {
	r := NewKindRegistry()
	if !r.PolicyFor(KindUser).AlwaysInclude {
		t.Error("KindUser should default to AlwaysInclude=true")
	}
	if !r.PolicyFor(KindFeedback).AlwaysInclude {
		t.Error("KindFeedback should default to AlwaysInclude=true")
	}
	if r.PolicyFor(KindProject).AlwaysInclude {
		t.Error("KindProject should default to AlwaysInclude=false (recall-only)")
	}
	if r.PolicyFor(KindReference).AlwaysInclude {
		t.Error("KindReference should default to AlwaysInclude=false (recall-only)")
	}
}

func TestKindRegistry_UnknownKindFallsBack(t *testing.T) {
	r := NewKindRegistry()
	got := r.PolicyFor("incident") // unknown
	if got.AlwaysInclude || got.MaxEntries == 0 {
		t.Errorf("unknown kind should get FallbackKindPolicy, got %+v", got)
	}
}

func TestKindRegistry_AlwaysIncludeKinds(t *testing.T) {
	r := NewKindRegistry()
	always := map[Kind]bool{}
	for _, k := range r.AlwaysIncludeKinds() {
		always[k] = true
	}
	if !always[KindUser] || !always[KindFeedback] {
		t.Errorf("AlwaysIncludeKinds should contain user + feedback, got %v", always)
	}
	if always[KindProject] || always[KindReference] {
		t.Errorf("AlwaysIncludeKinds should NOT contain project/reference, got %v", always)
	}
}

// ---- Key supersession across all three stores ----------------------------

func TestInMemoryStore_Key_SupersedesPriorEntry(t *testing.T) {
	s := NewInMemoryStore()
	first, _ := s.Append(context.Background(), Entry{
		AgentID: "a", Kind: "user", Key: "user.indent",
		Text: "user prefers tabs",
	})
	second, _ := s.Append(context.Background(), Entry{
		AgentID: "a", Kind: "user", Key: "user.indent",
		Text: "user prefers spaces",
	})
	if first.ID == second.ID {
		t.Fatalf("different content should produce different IDs even with same Key; first=%q second=%q", first.ID, second.ID)
	}
	got, _ := s.Recall(context.Background(), Query{AgentID: "a"}, 0)
	if len(got) != 1 {
		t.Fatalf("supersession should leave exactly 1 live entry, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Text, "spaces") {
		t.Errorf("surviving entry should be the new one; got %q", got[0].Text)
	}
}

func TestInMemoryStore_Key_SameContentDedupes(t *testing.T) {
	s := NewInMemoryStore()
	a, _ := s.Append(context.Background(), Entry{
		AgentID: "a", Key: "k", Text: "x",
	})
	b, _ := s.Append(context.Background(), Entry{
		AgentID: "a", Key: "k", Text: "x",
	})
	if a.ID != b.ID {
		t.Errorf("identical content + Key should hash to same ID; a=%q b=%q", a.ID, b.ID)
	}
}

func TestJSONLStore_Key_SupersedesAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.jsonl")
	s1, _ := NewJSONLStore(path)
	_, _ = s1.Append(context.Background(), Entry{
		AgentID: "a", Key: "user.indent", Text: "tabs",
	})
	_, _ = s1.Append(context.Background(), Entry{
		AgentID: "a", Key: "user.indent", Text: "spaces",
	})

	s2, _ := NewJSONLStore(path)
	got, _ := s2.Recall(context.Background(), Query{AgentID: "a"}, 0)
	if len(got) != 1 {
		t.Fatalf("after reopen + supersession should see 1 entry, got %d: %+v", len(got), got)
	}
	if got[0].Text != "spaces" {
		t.Errorf("post-reopen surviving entry should be the latest; got %q", got[0].Text)
	}
}

func TestChromemStore_Key_Supersedes(t *testing.T) {
	emb := newKeywordEmbedder([]string{"tabs", "spaces"})
	s, _ := NewChromemStore(emb, ChromemOptions{})
	_, _ = s.Append(context.Background(), Entry{
		AgentID: "a", Key: "k", Text: "tabs",
	})
	_, _ = s.Append(context.Background(), Entry{
		AgentID: "a", Key: "k", Text: "spaces",
	})
	got, _ := s.Recall(context.Background(), Query{AgentID: "a"}, 0)
	if len(got) != 1 {
		t.Fatalf("supersession should leave 1 entry, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Text, "spaces") {
		t.Errorf("surviving entry should be the new one; got %q", got[0].Text)
	}
}

// ---- Source provenance round-trips through stores ------------------------

func TestSource_RoundTripsJSONLStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.jsonl")
	s1, _ := NewJSONLStore(path)
	src := Source{
		SessionID: "session-123",
		MessageID: "msg-456",
		Tool:      "remember",
		Reason:    "user said /remember",
	}
	_, _ = s1.Append(context.Background(), Entry{
		AgentID: "a", Text: "x", Source: src,
	})

	s2, _ := NewJSONLStore(path)
	got, _ := s2.Recall(context.Background(), Query{AgentID: "a"}, 0)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry post-reopen, got %d", len(got))
	}
	if got[0].Source != src {
		t.Errorf("Source did not round-trip: got %+v want %+v", got[0].Source, src)
	}
}

func TestSource_RoundTripsChromemStore(t *testing.T) {
	emb := newKeywordEmbedder([]string{"x"})
	s, _ := NewChromemStore(emb, ChromemOptions{})
	src := Source{
		SessionID: "session-123",
		MessageID: "msg-456",
		Tool:      "remember",
		Reason:    "user said /remember",
	}
	_, _ = s.Append(context.Background(), Entry{
		AgentID: "a", Text: "x", Source: src,
	})
	got, _ := s.Recall(context.Background(), Query{AgentID: "a"}, 0)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Source != src {
		t.Errorf("Source did not round-trip: got %+v want %+v", got[0].Source, src)
	}
}

// ---- RememberTool --------------------------------------------------------

func TestRememberTool_SavesViaStore(t *testing.T) {
	store := NewInMemoryStore()
	tool := NewRememberTool(store, RememberOptions{AgentID: "main"})

	args := json.RawMessage(`{"kind":"user","text":"user prefers concise replies"}`)
	resp, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(resp, &decoded); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if decoded["id"] == "" {
		t.Error("response should include the saved entry's ID")
	}
	if decoded["kind"] != "user" {
		t.Errorf("response kind = %v, want user", decoded["kind"])
	}

	// Confirm the entry actually landed in the store.
	got, _ := store.Recall(context.Background(), Query{AgentID: "main"}, 0)
	if len(got) != 1 {
		t.Fatalf("store should contain 1 entry, got %d", len(got))
	}
	if got[0].Source.Tool != "remember" {
		t.Errorf("Source.Tool should default to 'remember', got %q", got[0].Source.Tool)
	}
}

func TestRememberTool_PicksUpSourceFromContext(t *testing.T) {
	store := NewInMemoryStore()
	tool := NewRememberTool(store, RememberOptions{AgentID: "main"})

	ctx := WithSource(context.Background(), Source{
		SessionID: "s1", MessageID: "m1",
	})
	args := json.RawMessage(`{"kind":"feedback","text":"prefer short responses"}`)
	_, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got, _ := store.Recall(context.Background(), Query{AgentID: "main"}, 0)
	if len(got) != 1 {
		t.Fatal("expected 1 entry")
	}
	if got[0].Source.SessionID != "s1" || got[0].Source.MessageID != "m1" {
		t.Errorf("Source from ctx not propagated: %+v", got[0].Source)
	}
	if got[0].Source.Tool != "remember" {
		t.Errorf("Source.Tool should still default to 'remember' even when ctx Source was set without one; got %q", got[0].Source.Tool)
	}
}

func TestRememberTool_RejectsEmptyText(t *testing.T) {
	tool := NewRememberTool(NewInMemoryStore(), RememberOptions{AgentID: "a"})
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"kind":"user"}`))
	if err == nil {
		t.Fatal("empty text should be rejected")
	}
}

func TestRememberTool_AppliesKey_Supersedes(t *testing.T) {
	store := NewInMemoryStore()
	tool := NewRememberTool(store, RememberOptions{AgentID: "main"})

	_, err := tool.Execute(context.Background(),
		json.RawMessage(`{"kind":"user","text":"user prefers tabs","key":"user.indent"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(),
		json.RawMessage(`{"kind":"user","text":"user prefers spaces","key":"user.indent"}`))
	if err != nil {
		t.Fatal(err)
	}

	got, _ := store.Recall(context.Background(), Query{AgentID: "main"}, 0)
	if len(got) != 1 {
		t.Fatalf("Key should supersede, leaving 1 entry; got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "spaces") {
		t.Errorf("surviving entry should be the latest; got %q", got[0].Text)
	}
}

// ---- ContextManager layered formatting -----------------------------------

func TestContextManager_AlwaysIncludeBypassesRecall(t *testing.T) {
	store := NewInMemoryStore()
	// One user fact + one project fact. user is AlwaysInclude;
	// project is recall-only with low age weight.
	_, _ = store.Append(context.Background(), Entry{
		AgentID: "main", Kind: string(KindUser),
		Text: "user is a senior engineer",
	})
	_, _ = store.Append(context.Background(), Entry{
		AgentID: "main", Kind: string(KindProject),
		Text: "current goal: ship feature X by Friday",
	})

	cm := NewContextManager(store, NewSimpleRecaller(), ContextManagerOptions{
		AgentID: "main",
	})

	// Prompt the agent with text UNRELATED to either memory.
	// The user fact should still appear (AlwaysInclude); the
	// project fact may or may not (recall-only, unrelated text).
	last := agentcore.Message{
		Role: agentcore.Role("user"),
		Content: []agentcore.ContentBlock{
			agentcore.TextBlock("How do I write a Go map?"),
		},
	}
	proj, err := cm.Project(context.Background(), []agentcore.AgentMessage{last})
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
	store := NewInMemoryStore()
	// Only project facts. With no AlwaysInclude entries, ALL
	// surfaced memory should come from the Relevant block.
	_, _ = store.Append(context.Background(), Entry{
		AgentID: "main", Kind: string(KindProject),
		Text: "Go generics use type parameters",
	})
	_, _ = store.Append(context.Background(), Entry{
		AgentID: "main", Kind: string(KindProject),
		Text: "the Rust ownership model is stricter than Go's",
	})

	cm := NewContextManager(store, NewSimpleRecaller(), ContextManagerOptions{
		AgentID: "main",
	})

	last := agentcore.Message{
		Role: agentcore.Role("user"),
		Content: []agentcore.ContentBlock{
			agentcore.TextBlock("Explain Go generics."),
		},
	}
	proj, _ := cm.Project(context.Background(), []agentcore.AgentMessage{last})
	if len(proj.Messages) < 2 {
		// The unrelated entry might not survive scoring; that's OK.
		// What matters: the relevant block is what appears, not core.
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

func TestSourceFromContext_ZeroWhenNotSet(t *testing.T) {
	got := SourceFromContext(context.Background())
	if got != (Source{}) {
		t.Errorf("missing ctx Source should yield zero Source; got %+v", got)
	}
}

// Sanity: jsonl tombstone shape after Key supersession — we tombstone
// in-process via the latestForKey replay logic, not by writing
// tombstone records. Confirm Forget still produces a tombstone record
// (those are distinct from supersession).
func TestJSONLStore_Forget_StillTombstones(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem.jsonl")
	s1, _ := NewJSONLStore(path)
	e, _ := s1.Append(context.Background(), Entry{
		AgentID: "a", Text: "x", CreatedAt: time.Now(),
	})
	_ = s1.Forget(context.Background(), e.ID)
	s2, _ := NewJSONLStore(path)
	got, _ := s2.Recall(context.Background(), Query{}, 0)
	if len(got) != 0 {
		t.Errorf("Forget should still tombstone after redesign; got %d entries", len(got))
	}
}
