package core

import (
	"errors"
	"testing"

	"github.com/guygrigsby/jess/ledger"
)

func TestRunStateBeginEnd(t *testing.T) {
	rs := &runState{}
	reqID := ledger.NewEventID()
	if err := rs.begin("run-abc", reqID, "restart nginx"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if rs.runID() != "run-abc" {
		t.Fatalf("got %q", rs.runID())
	}
	if id, txt := rs.request(); id != reqID || txt != "restart nginx" {
		t.Fatalf("request() = %s, %q", id, txt)
	}
	if err := rs.begin("run-xyz", ledger.NewEventID(), "x"); !errors.Is(err, ErrRunActive) {
		t.Fatalf("concurrent begin should fail with ErrRunActive, got %v", err)
	}
	rs.end("not-owner")
	if rs.runID() != "run-abc" {
		t.Fatal("stale end wiped the active run")
	}
	rs.end("run-abc")
	if rs.runID() != "" {
		t.Fatal("owner end should clear runID")
	}
}
