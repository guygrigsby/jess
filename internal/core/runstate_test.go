package core

import (
	"errors"
	"sync"
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

// TestRunContextAtomicUnderConcurrentEnd verifies that runContext never returns
// a non-empty id paired with a zero requestID — the torn-read that would occur
// if id and requestID were read under separate lock acquisitions while end()
// races between them. Run under -race to catch data races simultaneously.
func TestRunContextAtomicUnderConcurrentEnd(t *testing.T) {
	const goroutines = 50
	const iters = 200

	for i := 0; i < iters; i++ {
		rs := &runState{}
		reqID := ledger.NewEventID()
		if err := rs.begin("run-x", reqID, "some text"); err != nil {
			t.Fatalf("begin: %v", err)
		}

		var wg sync.WaitGroup
		// One goroutine calls end() concurrently with readers.
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs.end("run-x")
		}()

		// N goroutines hammer runContext and check the atomicity invariant.
		for j := 0; j < goroutines; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id, rID, _ := rs.runContext()
				// If we see a non-empty run id the request id must also be
				// non-zero — a zero EventID paired with a non-empty id is the
				// torn read this test guards against.
				if id != "" && rID == (ledger.EventID{}) {
					t.Errorf("torn read: non-empty id=%q but zero requestID", id)
				}
			}()
		}
		wg.Wait()
	}
}
