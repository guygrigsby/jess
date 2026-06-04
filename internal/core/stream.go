package core

import (
	"context"
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
	rec := func(ev ledger.Event) {
		if sink != nil {
			ev.Time = time.Now()
			ev.AgentPath = path
			_ = sink.Record(ev)
		}
	}

	out := make(chan ac.Event, 64)
	var summary atomic.Pointer[ac.RunSummary]
	done := make(chan struct{})

	unsub := agent.Subscribe(func(ev ac.Event) {
		if ev.Summary != nil {
			summary.Store(ev.Summary)
		}
		select {
		case out <- ev:
		case <-done:
		}
	})

	rec(ledger.Event{Kind: ledger.KindPrompt, Preview: input})

	go func() {
		defer close(done)
		defer close(out)
		defer unsub()

		// agentcore's Prompt is asynchronous: it kicks off the run on a
		// background goroutine and returns immediately. The run is finished only
		// after WaitForIdle. So start the prompt, then wait for idle on a
		// goroutine and race it against ctx cancellation (the kill switch).
		if err := agent.Prompt(input); err != nil {
			ev := ledger.Event{Kind: ledger.KindRunEnd, Err: err.Error()}
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
			rec(ledger.Event{Kind: ledger.KindAbort, Reason: ctx.Err().Error()})
			return
		}
		ev := ledger.Event{Kind: ledger.KindRunEnd}
		if s := summary.Load(); s != nil {
			ev.Reason = string(s.EndReason)
		}
		rec(ev)
	}()

	return out, func() *ac.RunSummary { <-done; return summary.Load() }
}
