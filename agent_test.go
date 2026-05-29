package jess

import "testing"

func TestNew_RequiresModel(t *testing.T) {
	if _, err := New(); err == nil {
		t.Fatal("expected error without WithModel")
	}
	a, err := New(WithModel(testModel()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("expected an Agent")
	}
}
