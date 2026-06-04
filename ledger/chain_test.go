package ledger

import "testing"

func ev(kind Kind, callID string) Event {
	return Event{EventID: NewEventID(), RunID: "run1", CallID: callID, Kind: kind}
}

func TestAssembleChainPairsByCallID(t *testing.T) {
	req := ev(KindRequest, "")
	req.Args = []byte(`"clean tmp"`)
	a1 := ev(KindAction, "call-1")
	a1.Tool = "delete_file"
	a1.Args = []byte(`{"path":"/tmp/x"}`)
	r1 := ev(KindToolResult, "call-1")
	a2 := ev(KindAction, "call-2")
	a2.Tool = "delete_file"
	r2 := ev(KindToolResult, "call-2")

	c := AssembleChain([]Event{req, a1, r1, a2, r2})
	if c.Request.Kind != KindRequest {
		t.Fatal("missing request head")
	}
	if len(c.Actions) != 2 {
		t.Fatalf("want 2 actions, got %d", len(c.Actions))
	}
	if c.Actions[0].Intent.CallID != c.Actions[0].Result.CallID {
		t.Fatal("intent/result not paired by CallID")
	}
	if c.Actions[0].Intent.CallID == c.Actions[1].Intent.CallID {
		t.Fatal("two distinct calls collapsed into one")
	}
}

func TestAssembleChainCollectsAvailable(t *testing.T) {
	retr := ev(KindRetrieved, "")
	retr.Refs = []Ref{{Source: RefMemory, ID: "m1", Hash: "h1"}}
	read := ev(KindToolResult, "read-1") // a safe read result, no matching action => available
	c := AssembleChain([]Event{ev(KindRequest, ""), retr, read})
	if len(c.Available) == 0 {
		t.Fatal("retrieved refs should appear in Available")
	}
}
