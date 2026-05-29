package jess

import (
	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/internal/acl"
	"github.com/guygrigsby/jess/message"
)

// Result is the outcome of a finished Run: the messages the run produced and a
// factual summary.
type Result struct {
	Messages []message.Message
	Summary  *event.RunSummary
}

// Run is the handle for one Prompt/Continue cycle. Range over Events for live
// progress, or call Wait for the final result. Both observe the same run.
type Run struct {
	inner *acl.Run
}

// Events returns the live event channel for this run, closed when the run ends.
func (r *Run) Events() <-chan event.Event { return r.inner.Events() }

// Wait blocks until the run finishes and returns its result and any run error.
func (r *Run) Wait() (Result, error) {
	res, err := r.inner.Wait()
	return Result{Messages: res.Messages, Summary: res.Summary}, err
}
