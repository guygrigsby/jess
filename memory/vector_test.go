package memory

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// keywordEmbedder is a deterministic test embedder. Each entry of
// its vocab gets a fixed dimension; Embed returns a vector where
// matching tokens contribute 1.0 to their slot, others 0. Vectors
// are NOT normalized — tests that care about cosine semantics
// should normalize before comparing.
//
// The fake space is small but expressive enough to verify that
// "cat" and "kitten" (if both in vocab) cosine higher than "cat"
// and "car" — semantic ordering shows up at the test layer
// without pulling a real model.
type keywordEmbedder struct {
	dim   int
	vocab map[string]int

	mu    sync.Mutex
	calls int
}

func newKeywordEmbedder(vocab []string) *keywordEmbedder {
	v := make(map[string]int, len(vocab))
	for i, w := range vocab {
		v[w] = i
	}
	return &keywordEmbedder{dim: len(vocab), vocab: v}
}

func (e *keywordEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	vec := make([]float32, e.dim)
	for _, tok := range strings.Fields(strings.ToLower(text)) {
		if i, ok := e.vocab[tok]; ok {
			vec[i] = 1
		}
	}
	return vec, nil
}

func (e *keywordEmbedder) Dim() int     { return e.dim }
func (e *keywordEmbedder) Name() string { return "test:keyword" }

// callCount races safely under -race.
func (e *keywordEmbedder) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func TestChromemStore_AppendAndRecall(t *testing.T) {
	emb := newKeywordEmbedder([]string{"cat", "dog", "kitten", "feline"})
	s, err := NewChromemStore(emb, ChromemOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, txt := range []string{"cat", "dog", "kitten"} {
		if _, err := s.Append(context.Background(), Entry{Text: txt, AgentID: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Recall(context.Background(), Query{AgentID: "a"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("Recall returned %d entries, want 3", len(got))
	}
}

func TestChromemStore_SearchVector_OrdersByCosine(t *testing.T) {
	emb := newKeywordEmbedder([]string{"cat", "dog", "kitten", "feline"})
	s, _ := NewChromemStore(emb, ChromemOptions{})

	for _, txt := range []string{
		"cat kitten feline", // close to "cat"
		"dog",               // far
	} {
		_, _ = s.Append(context.Background(), Entry{Text: txt, AgentID: "a"})
	}

	queryVec, _ := emb.Embed(context.Background(), "cat kitten")
	got, err := s.SearchVector(context.Background(), queryVec, 2, Query{AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("SearchVector returned nothing")
	}
	// Cosine-nearest should be the entry that shares more tokens.
	if !strings.Contains(got[0].Text, "kitten") {
		t.Errorf("nearest entry should contain 'kitten'; got %q", got[0].Text)
	}
}

func TestChromemStore_Dedupe_AvoidsReEmbed(t *testing.T) {
	emb := newKeywordEmbedder([]string{"x"})
	s, _ := NewChromemStore(emb, ChromemOptions{})

	first, _ := s.Append(context.Background(), Entry{Text: "x", AgentID: "a"})
	before := emb.callCount()
	second, _ := s.Append(context.Background(), Entry{Text: "x", AgentID: "a"})
	after := emb.callCount()

	if first.ID != second.ID {
		t.Errorf("dedupe by ID failed: first=%q second=%q", first.ID, second.ID)
	}
	if after != before {
		t.Errorf("re-Append should NOT re-embed (calls before=%d, after=%d)", before, after)
	}
}

func TestChromemStore_Forget(t *testing.T) {
	emb := newKeywordEmbedder([]string{"x"})
	s, _ := NewChromemStore(emb, ChromemOptions{})
	e, _ := s.Append(context.Background(), Entry{Text: "x", AgentID: "a"})
	if err := s.Forget(context.Background(), e.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Recall(context.Background(), Query{AgentID: "a"}, 0)
	if len(got) != 0 {
		t.Errorf("entry should be gone after Forget; got %v", got)
	}
}

func TestChromemStore_AgentScopeIncludesGlobal(t *testing.T) {
	emb := newKeywordEmbedder([]string{"x"})
	s, _ := NewChromemStore(emb, ChromemOptions{})
	_, _ = s.Append(context.Background(), Entry{Text: "global x"}) // no AgentID
	_, _ = s.Append(context.Background(), Entry{Text: "coding x", AgentID: "coding"})
	_, _ = s.Append(context.Background(), Entry{Text: "research x", AgentID: "research"})

	got, _ := s.Recall(context.Background(), Query{AgentID: "coding"}, 0)
	if len(got) != 2 {
		t.Fatalf("coding agent should see its own + global, got %d: %v", len(got), got)
	}
	for _, e := range got {
		if e.AgentID == "research" {
			t.Errorf("research entry leaked: %v", e)
		}
	}
}

func TestVectorRecaller_UsesStoreEmbedderByDefault(t *testing.T) {
	emb := newKeywordEmbedder([]string{"cat", "kitten", "dog"})
	s, _ := NewChromemStore(emb, ChromemOptions{})
	_, _ = s.Append(context.Background(), Entry{Text: "cat kitten", AgentID: "a"})
	_, _ = s.Append(context.Background(), Entry{Text: "dog", AgentID: "a"})

	r := NewVectorRecaller()
	got, err := r.Recall(context.Background(), s, "a", "tell me about a kitten", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Text, "cat") {
		t.Errorf("nearest entry should be cat-related; got %v", got)
	}
}

func TestVectorRecaller_RejectsNonVectorStore(t *testing.T) {
	r := NewVectorRecaller()
	_, err := r.Recall(context.Background(), NewInMemoryStore(), "a", "anything", 1)
	if err == nil {
		t.Fatal("VectorRecaller should refuse non-VectorStore — no silent fallback")
	}
}

// HybridRecaller's RRF: an entry appearing high in both recallers
// should rank higher than entries appearing high in only one.
func TestHybridRecaller_RRF_FusesRankings(t *testing.T) {
	emb := newKeywordEmbedder([]string{"alpha", "beta", "gamma", "delta"})
	s, _ := NewChromemStore(emb, ChromemOptions{})

	// Stored entries:
	//   "alpha beta" — vector-near to "alpha", contains "alpha"
	//   "alpha gamma" — vector-near to "alpha", contains "alpha"
	//   "delta" — neither
	_, _ = s.Append(context.Background(), Entry{Text: "alpha beta", AgentID: "a"})
	_, _ = s.Append(context.Background(), Entry{Text: "alpha gamma", AgentID: "a"})
	_, _ = s.Append(context.Background(), Entry{Text: "delta", AgentID: "a"})

	hybrid := NewHybridRecaller(NewVectorRecaller(), NewSimpleRecaller())
	got, err := hybrid.Recall(context.Background(), s, "a", "alpha", 3)
	if err != nil {
		t.Fatal(err)
	}
	// "delta" should rank last — it loses in both rankings.
	if len(got) == 0 || got[len(got)-1].Text != "delta" {
		t.Errorf("delta should be ranked last by RRF; got %v", got)
	}
}

func TestHybridRecaller_PartialFailure_Tolerated(t *testing.T) {
	emb := newKeywordEmbedder([]string{"x"})
	s, _ := NewChromemStore(emb, ChromemOptions{})
	_, _ = s.Append(context.Background(), Entry{Text: "x", AgentID: "a"})

	// SimpleRecaller works against any Store. Pair it with a
	// recaller that's guaranteed to fail (VectorRecaller against
	// an InMemoryStore). The hybrid should still return results
	// from the working contributor.
	failing := NewVectorRecaller()
	working := NewSimpleRecaller()

	mem := NewInMemoryStore()
	_, _ = mem.Append(context.Background(), Entry{Text: "hello world", AgentID: "a"})

	hybrid := NewHybridRecaller(failing, working)
	got, err := hybrid.Recall(context.Background(), mem, "a", "hello", 5)
	if err != nil {
		t.Errorf("partial failure should not bubble up: %v", err)
	}
	if len(got) == 0 {
		t.Error("working contributor should still return results")
	}
}

func TestHybridRecaller_AllFail_ReturnsError(t *testing.T) {
	mem := NewInMemoryStore()
	hybrid := NewHybridRecaller(NewVectorRecaller())
	_, err := hybrid.Recall(context.Background(), mem, "a", "anything", 1)
	if err == nil {
		t.Error("when every contributor fails, the error should surface")
	}
}

func TestRRF_StableOrdering(t *testing.T) {
	// Three rankings that put A first, B second; RRF should
	// rank A above B above any single-ranking entry.
	a := Entry{ID: "A", Text: "a"}
	b := Entry{ID: "B", Text: "b"}
	c := Entry{ID: "C", Text: "c"}
	rankings := [][]Entry{
		{a, b, c},
		{a, b},
	}
	got := rrf(rankings, 60, 3)
	want := []string{"A", "B", "C"}
	gotIDs := []string{got[0].ID, got[1].ID, got[2].ID}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("RRF ordering: got %v want %v", gotIDs, want)
	}
}
