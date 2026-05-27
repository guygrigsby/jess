package memory

import (
	"context"
	"math"
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

// normKeywordEmbedder is keywordEmbedder with L2-normalized output.
// Real embedders (MiniLM-L6-v2) emit unit vectors, and chromem only
// normalizes embeddings supplied directly to AddDocument — NOT ones
// produced by its EmbeddingFunc. So an unnormalized fixture would make
// stored-vs-query cosines wrong (dot of a unit query and a raw doc
// vector). Normalizing here makes cosines match the real-world values:
// cosine("cat kitten feline","cat") = 1/sqrt(3) ≈ 0.577.
type normKeywordEmbedder struct{ *keywordEmbedder }

func newNormKeywordEmbedder(vocab []string) *normKeywordEmbedder {
	return &normKeywordEmbedder{newKeywordEmbedder(vocab)}
}

func (e *normKeywordEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vec, err := e.keywordEmbedder.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	var sumSq float64
	for _, v := range vec {
		sumSq += float64(v) * float64(v)
	}
	if sumSq == 0 {
		return vec, nil
	}
	norm := float32(math.Sqrt(sumSq))
	for i := range vec {
		vec[i] /= norm
	}
	return vec, nil
}

func (e *normKeywordEmbedder) Name() string { return "test:normkeyword" }

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

func TestSearchVector_PopulatesScore(t *testing.T) {
	emb := newNormKeywordEmbedder([]string{"cat", "dog", "kitten", "feline"})
	s, err := NewChromemStore(emb, ChromemOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, txt := range []string{"cat kitten feline", "dog"} {
		if _, err := s.Append(context.Background(), Entry{Text: txt, AgentID: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	queryVec, err := emb.Embed(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SearchVector(context.Background(), queryVec, 2, Query{AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("SearchVector returned %d entries, want 2", len(got))
	}
	scoreByText := map[string]float32{}
	for _, e := range got {
		scoreByText[e.Text] = e.Score
	}
	// cosine("cat kitten feline", "cat") = 1/sqrt(3) ≈ 0.577
	if s := scoreByText["cat kitten feline"]; s < 0.5 {
		t.Errorf("cat entry Score = %v, want ≈ 0.577 (>0.5)", s)
	}
	// cosine("dog", "cat") = 0
	if s := scoreByText["dog"]; s != 0 {
		t.Errorf("dog entry Score = %v, want 0", s)
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

func TestVectorRecaller_MinScoreFloor(t *testing.T) {
	emb := newNormKeywordEmbedder([]string{"cat", "dog", "kitten", "feline"})
	s, err := NewChromemStore(emb, ChromemOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, txt := range []string{"cat kitten feline", "dog"} {
		if _, err := s.Append(context.Background(), Entry{Text: txt, AgentID: "a"}); err != nil {
			t.Fatal(err)
		}
	}

	// cosine("cat kitten feline","cat") ≈ 0.577, cosine("dog","cat") = 0.
	// A 0.9 floor drops both; a 0-floor keeps them.
	high := NewVectorRecaller(WithMinScore(0.9))
	got, err := high.Recall(context.Background(), s, "a", "cat", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("floor 0.9 should drop all (max score ≈ 0.577), got %d: %v", len(got), got)
	}

	none := NewVectorRecaller()
	got2, err := none.Recall(context.Background(), s, "a", "cat", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) == 0 {
		t.Fatalf("no floor should keep entries, got 0")
	}

	// A floor between the two scores keeps only the close entry.
	mid := NewVectorRecaller(WithMinScore(0.5))
	got3, err := mid.Recall(context.Background(), s, "a", "cat", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got3) != 1 || !strings.Contains(got3[0].Text, "kitten") {
		t.Fatalf("floor 0.5 should keep only the close entry, got %v", got3)
	}
}

// A vector hit dropped by the floor must contribute nothing to RRF
// fusion: its only path back into the result is the SimpleRecaller's
// lexical/recency ranking, which is exactly what an unfloored vector
// hit's RRF boost would otherwise jump it ahead of.
//
// Setup isolates the vector path. The query "cat" lexically matches
// only "cat kitten feline" (SimpleRecaller scores "dog" 0, surfacing
// it solely by recency). "cat kitten feline" is also the vector-near
// entry (cosine ≈ 0.577). With NO floor, the vector boost is real;
// with a 0.9 floor the vector hit is dropped, so the close entry's
// final rank must rest on lexical score alone — and the ordering of
// the lexically-disjoint "dog" must be unchanged by the floor (proof
// it never rode the vector path).
func TestHybridRecaller_FlooredVectorHit_NoFusionBoost(t *testing.T) {
	emb := newNormKeywordEmbedder([]string{"cat", "dog", "kitten", "feline"})
	build := func() *ChromemStore {
		s, err := NewChromemStore(emb, ChromemOptions{})
		if err != nil {
			t.Fatal(err)
		}
		// Append "dog" first so it is the OLDER entry; recency
		// tiebreaks then favor the lexically-matching entry.
		for _, txt := range []string{"dog", "cat kitten feline"} {
			if _, err := s.Append(context.Background(), Entry{Text: txt, AgentID: "a"}); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}

	texts := func(es []Entry) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.Text
		}
		return out
	}

	// No floor: vector path contributes, "cat kitten feline" leads.
	noFloor := NewHybridRecaller(NewVectorRecaller(), NewSimpleRecaller())
	open, err := noFloor.Recall(context.Background(), build(), "a", "cat", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) == 0 || open[0].Text != "cat kitten feline" {
		t.Fatalf("unfloored: close entry should lead, got %v", texts(open))
	}

	// Floor 0.9 drops the vector hit (max cosine ≈ 0.577). The close
	// entry still leads via lexical score, but "dog" — which only ever
	// came from SimpleRecaller — must remain present at the same
	// relative position, never resurrected or boosted by the vector
	// path. If the floor leaked, "dog" (vector score 0) would have
	// been dropped from the vector ranking but it was never there to
	// begin with, so its position is identical to the unfloored run.
	floored := NewHybridRecaller(NewVectorRecaller(WithMinScore(0.9)), NewSimpleRecaller())
	gated, err := floored.Recall(context.Background(), build(), "a", "cat", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(gated) == 0 || gated[0].Text != "cat kitten feline" {
		t.Fatalf("floored: lexical match should still lead, got %v", texts(gated))
	}
	// "dog" present in both, identical position => it rode only the
	// lexical path, never the (now-floored) vector path.
	posOpen, posGated := -1, -1
	for i, e := range open {
		if e.Text == "dog" {
			posOpen = i
		}
	}
	for i, e := range gated {
		if e.Text == "dog" {
			posGated = i
		}
	}
	if posOpen == -1 || posGated == -1 {
		t.Fatalf("dog should appear via SimpleRecaller in both runs: open=%v gated=%v", texts(open), texts(gated))
	}
	if posOpen != posGated {
		t.Fatalf("dog moved (%d -> %d): vector floor must not alter a lexical-only entry's rank", posOpen, posGated)
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
