package ledger

// Chain is one run reconstructed as the triad. Read backward it answers "why".
type Chain struct {
	Request   Event
	Available []Ref
	Actions   []Action
}

// Action is one effectful invocation: its committed intent (target + verdict +
// embedded why), its result, and the evidence it rested on. Intent and Result
// pair by CallID.
type Action struct {
	Intent   Event
	Result   Event
	Evidence []Ref
}

// Reader is the read side of a ledger. Both methods are index-backed, never a scan.
type Reader interface {
	Get(id EventID) (Event, error)
	Chain(runID string) (Chain, error)
}

// Resolver resolves the current content hash of a referenced item, for drift
// detection. ok is false when the id is unknown (deleted) or unresolvable.
type Resolver interface {
	CurrentHash(source RefSource, id string) (hash string, ok bool)
}

// AssembleChain reconstructs the triad from a single run's events. Intent
// (KindAction) and Result (KindToolResult with the same CallID) pair into one
// Action; KindToolResult events with no matching action are safe-tool reads and
// land in Available; KindRetrieved refs also land in Available.
func AssembleChain(events []Event) Chain {
	var c Chain
	results := map[string]Event{} // callID -> result
	for _, e := range events {
		if e.Kind == KindToolResult && e.CallID != "" {
			results[e.CallID] = e
		}
	}
	actionCalls := map[string]bool{}
	for _, e := range events {
		switch e.Kind {
		case KindRequest:
			c.Request = e
		case KindRetrieved:
			c.Available = append(c.Available, e.Refs...)
		case KindAction:
			actionCalls[e.CallID] = true
			c.Actions = append(c.Actions, Action{
				Intent:   e,
				Result:   results[e.CallID],
				Evidence: e.Refs,
			})
		}
	}
	// Tool results with no matching action are safe reads -> available, by ref.
	for callID, r := range results {
		if !actionCalls[callID] {
			c.Available = append(c.Available, Ref{Source: RefTool, ID: r.EventID.String(), Hash: hashOf(r.Result)})
		}
	}
	return c
}
