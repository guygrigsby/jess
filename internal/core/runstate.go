package core

import (
	"errors"
	"sync"

	"github.com/guygrigsby/jess/ledger"
)

// ErrRunActive is returned by begin when a run is already in flight on this
// agent. It enforces the one-active-run invariant instead of assuming it.
var ErrRunActive = errors.New("core: a run is already active on this agent")

// runState carries the current run's id plus the request id/text the gate and
// middleware need to make an action self-explaining. The gate, audit middleware,
// and context manager capture a *runState at build time; jess.Stream begins/ends
// it per run.
type runState struct {
	mu          sync.RWMutex
	id          string
	requestID   ledger.EventID
	requestText string
}

func (r *runState) begin(id string, reqID ledger.EventID, reqText string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.id != "" {
		return ErrRunActive
	}
	r.id, r.requestID, r.requestText = id, reqID, reqText
	return nil
}

func (r *runState) end(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.id == id {
		r.id, r.requestText, r.requestID = "", "", ledger.EventID{}
	}
}

func (r *runState) runID() string { r.mu.RLock(); defer r.mu.RUnlock(); return r.id }

func (r *runState) request() (ledger.EventID, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.requestID, r.requestText
}

// runContext returns the run id, request id, and request text under one lock,
// so a concurrent end() cannot tear them apart.
func (r *runState) runContext() (string, ledger.EventID, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.id, r.requestID, r.requestText
}
