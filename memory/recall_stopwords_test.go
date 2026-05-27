package memory

import (
	"context"
	"testing"
)

func TestSimpleRecaller_StopwordsFilterTokens(t *testing.T) {
	r := NewSimpleRecaller(WithStopwords(DefaultStopwords...))
	got := r.tokenize("What did the user order for dinner?")
	for _, tok := range got {
		switch tok {
		case "what", "did", "the", "for":
			t.Errorf("stopword %q not filtered", tok)
		}
	}
	want := map[string]bool{"user": false, "order": false, "dinner": false}
	for _, tok := range got {
		if _, ok := want[tok]; ok {
			want[tok] = true
		}
	}
	for w, seen := range want {
		if !seen {
			t.Errorf("content word %q was dropped", w)
		}
	}
}

// Stopwords + RequireMatch: a query whose only overlap is a stopword must
// recall nothing, instead of false-matching on "the".
func TestSimpleRecaller_StopwordsReduceFalseMatch(t *testing.T) {
	st := NewInMemoryStore()
	for _, txt := range []string{"Remember the milk.", "Deploys run on Fridays."} {
		if _, err := st.Append(context.Background(), Entry{Text: txt, AgentID: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	plain := NewSimpleRecaller(WithRequireMatch())
	if got, _ := plain.Recall(context.Background(), st, "a", "what is the plan", 8); len(got) == 0 {
		t.Fatalf("without stopwords, 'the' should false-match 'Remember the milk'")
	}
	gated := NewSimpleRecaller(WithRequireMatch(), WithStopwords(DefaultStopwords...))
	if got, _ := gated.Recall(context.Background(), st, "a", "what is the plan", 8); len(got) != 0 {
		t.Fatalf("with stopwords, a stopword-only query should match nothing; got %d", len(got))
	}
}
