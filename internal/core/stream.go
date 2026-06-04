package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/ledger"
)

// Stream drives one prompt on agent and exposes its events as a channel plus a
// Wait for the final RunSummary. Cancelling ctx aborts the run (the kill
// switch). Run-level audit (prompt, run_end, abort) is recorded to the agent's
// audit sink, which Agent registered at build time; tool-level audit is already
// covered by the middleware and gate. A nil/unknown sink degrades to no
// run-level audit, never to no run.
//
// Only one active Stream per agent at a time; a second concurrent call races
// the first run's events.
func Stream(ctx context.Context, agent *ac.Agent, input string) (<-chan ac.Event, func() *ac.RunSummary) {
	sink := sinkFor(agent)
	path := pathFor(agent)
	rs := runStateFor(agent)
	rec := func(ev ledger.Event) {
		if sink != nil {
			ev.Time = time.Now()
			ev.AgentPath = path
			_ = sink.Record(ev)
		}
	}

	// Mint the run and request ids before touching the agent so the ledger
	// entries use consistent ids across all recorded events for this run.
	reqID := ledger.NewEventID()
	runID := ledger.NewEventID().String()

	out := make(chan ac.Event, 64)
	var summary atomic.Pointer[ac.RunSummary]
	done := make(chan struct{})

	// Enforce the one-active-run-per-agent invariant. A second concurrent
	// Stream call would race the first run's events; fail it explicitly.
	if rs != nil {
		if err := rs.begin(runID, reqID, input); err != nil {
			go func() {
				defer close(done)
				defer close(out)
				out <- ac.Event{} // unblock any select on the channel
				rec(ledger.Event{Kind: ledger.KindRunEnd, RunID: runID, Err: err.Error()})
			}()
			return out, func() *ac.RunSummary { <-done; return nil }
		}
	}

	// Best-effort: record the request head before the model touches input.
	argsJSON, _ := json.Marshal(input)
	rec(ledger.Event{
		EventID: reqID,
		RunID:   runID,
		Kind:    ledger.KindRequest,
		Args:    argsJSON,
	})

	unsub := agent.Subscribe(func(ev ac.Event) {
		if ev.Summary != nil {
			summary.Store(ev.Summary)
		}
		select {
		case out <- ev:
		case <-done:
		}
	})

	rec(ledger.Event{Kind: ledger.KindPrompt, RunID: runID, Preview: input})

	go func() {
		defer close(done)
		defer close(out)
		defer unsub()
		defer func() {
			if rs != nil {
				rs.end(runID)
			}
		}()

		// agentcore's Prompt is asynchronous: it kicks off the run on a
		// background goroutine and returns immediately. The run is finished only
		// after WaitForIdle. So start the prompt, then wait for idle on a
		// goroutine and race it against ctx cancellation (the kill switch).
		if err := agent.Prompt(input); err != nil {
			ev := ledger.Event{Kind: ledger.KindRunEnd, RunID: runID, Err: err.Error()}
			rec(ev)
			return
		}
		idle := make(chan struct{})
		go func() { agent.WaitForIdle(); close(idle) }()

		var aborted bool
		select {
		case <-ctx.Done():
			agent.Abort()
			<-idle // wait for the loop to actually unwind after the abort
			aborted = true
		case <-idle:
		}

		if aborted {
			rec(ledger.Event{Kind: ledger.KindAbort, RunID: runID, Reason: ctx.Err().Error()})
			return
		}
		ev := ledger.Event{Kind: ledger.KindRunEnd, RunID: runID}
		if s := summary.Load(); s != nil {
			ev.Reason = string(s.EndReason)
		}
		rec(ev)
	}()

	return out, func() *ac.RunSummary { <-done; return summary.Load() }
}
