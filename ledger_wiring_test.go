package jess_test

import (
	"context"
	"testing"

	"github.com/guygrigsby/jess"
	"github.com/guygrigsby/jess/ledger"
)

// TestDefaultLedgerAllowsAuditedAction proves the no-WithLedger default is the
// durable SQLite store: a non-safe tool with an approving approver runs because
// the audit middleware can record a durable CommitAction.
func TestDefaultLedgerAllowsAuditedAction(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // route defaultLedger to a temp SQLite db
	rt := &restartTool{}
	model := callOnceModel("restart_service")
	agent := jess.New(jess.WithModel(model), jess.WithTools(rt),
		jess.WithApprover(func(context.Context, jess.Request) (bool, string) { return true, "ok" }))
	ch, wait := jess.Stream(context.Background(), agent, "restart nginx")
	for range ch {
	}
	_ = wait()
	if !rt.ran {
		t.Fatal("durable default (SQLite) ledger + approval => action should run")
	}
}

// TestDiscardLedgerDeniesAction proves a non-durable ledger denies a non-safe
// action even with an approving approver: no durable record, no action.
func TestDiscardLedgerDeniesAction(t *testing.T) {
	rt := &restartTool{}
	model := callOnceModel("restart_service")
	agent := jess.New(jess.WithModel(model), jess.WithTools(rt),
		jess.WithLedger(ledger.DiscardSink{}),
		jess.WithApprover(func(context.Context, jess.Request) (bool, string) { return true, "ok" }))
	ch, wait := jess.Stream(context.Background(), agent, "restart nginx")
	for range ch {
	}
	_ = wait()
	if rt.ran {
		t.Fatal("non-durable ledger => non-safe action denied even with approval")
	}
}
