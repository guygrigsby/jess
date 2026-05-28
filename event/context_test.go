package event

import (
	"context"
	"testing"
)

func TestStreamContext_RoundTrip(t *testing.T) {
	s := NewStream(1)
	ctx := ContextWithStream(context.Background(), s)
	got, ok := StreamFromContext(ctx)
	if !ok || got != s {
		t.Fatalf("StreamFromContext = %v, %v; want the injected stream", got, ok)
	}
}

func TestStreamFromContext_Absent(t *testing.T) {
	if _, ok := StreamFromContext(context.Background()); ok {
		t.Error("expected no stream in a bare context")
	}
	if _, ok := StreamFromContext(ContextWithStream(context.Background(), nil)); ok {
		t.Error("a nil stream should report not-ok")
	}
}
