package memory

import (
	"context"
	"testing"
)

func TestInMemoryStoreGet(t *testing.T) {
	st := NewInMemoryStore()
	e, _ := st.Append(context.Background(), Entry{AgentID: "a", Text: "hello"})
	got, ok := st.Get(e.ID)
	if !ok || got.Text != "hello" {
		t.Fatalf("Get(%q) = %+v, %v", e.ID, got, ok)
	}
	if _, ok := st.Get("nope"); ok {
		t.Fatal("unknown id should return ok=false")
	}
}
