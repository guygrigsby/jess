package memory

import (
	"context"
	"testing"
)

// TestSimpleRecaller_RequireMatch verifies the opt-in gate drops entries with
// no token/tag overlap instead of padding by recency. Without it, a hint that
// tokenizes to nothing returns recent entries (the "irrelevant recall" bug).
func TestSimpleRecaller_RequireMatch(t *testing.T) {
	st := NewInMemoryStore()
	for _, txt := range []string{
		"User ordered a pepperoni pizza on Friday.",
		"Deploys go out on Fridays.",
	} {
		if _, err := st.Append(context.Background(), Entry{Text: txt, AgentID: "main"}); err != nil {
			t.Fatal(err)
		}
	}

	// "oh hi" tokenizes to nothing (MinTokenLength=3) → zero lexical signal.
	plain := NewSimpleRecaller()
	if got, _ := plain.Recall(context.Background(), st, "main", "oh hi", 8); len(got) == 0 {
		t.Fatalf("default recaller should pad by recency; got 0")
	}
	gated := NewSimpleRecaller(WithRequireMatch())
	if got, _ := gated.Recall(context.Background(), st, "main", "oh hi", 8); len(got) != 0 {
		t.Fatalf("WithRequireMatch should drop zero-signal entries; got %d", len(got))
	}
	// A real lexical match still recalls.
	if got, _ := gated.Recall(context.Background(), st, "main", "what pizza did the user order", 8); len(got) == 0 {
		t.Fatalf("lexical match should survive the gate; got 0")
	}
}
