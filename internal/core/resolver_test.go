package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/memory"
)

func TestMemResolver(t *testing.T) {
	st := memory.NewInMemoryStore()
	e, err := st.Append(context.Background(), memory.Entry{AgentID: "a", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	r := memResolver{g: st}

	// Known id, RefMemory → stable hash.
	sum := sha256.Sum256([]byte("hello"))
	want := hex.EncodeToString(sum[:])

	hash, ok := r.CurrentHash(ledger.RefMemory, e.ID)
	if !ok {
		t.Fatal("expected ok=true for known id")
	}
	if hash != want {
		t.Fatalf("hash = %q, want %q", hash, want)
	}

	// Call again — hash must be stable.
	hash2, ok2 := r.CurrentHash(ledger.RefMemory, e.ID)
	if !ok2 || hash2 != want {
		t.Fatalf("second call: hash=%q ok=%v, want %q true", hash2, ok2, want)
	}

	// Unknown id → ok=false.
	if _, ok := r.CurrentHash(ledger.RefMemory, "nope"); ok {
		t.Fatal("unknown id should return ok=false")
	}

	// RefTool → ok=false regardless of id.
	if _, ok := r.CurrentHash(ledger.RefTool, e.ID); ok {
		t.Fatal("RefTool should return ok=false")
	}
}
