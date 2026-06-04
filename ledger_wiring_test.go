package jess_test

import (
	"context"
	"encoding/json"
	"testing"

	ac "github.com/voocel/agentcore"

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

// TestExtraMiddlewareCannotBypassEnforcement proves that a caller passing
// ac.WithMiddlewares via WithAgentcoreOptions cannot clobber jess's audit
// enforcement. With a non-durable ledger the non-safe tool must still be
// denied even though the caller's (no-op) middleware ran.
func TestExtraMiddlewareCannotBypassEnforcement(t *testing.T) {
	rt := &restartTool{}
	model := callOnceModel("restart_service")
	// A caller tries to install their own (no-op) middleware via the escape hatch,
	// which would replace the audit middleware if order were wrong. With a non-durable
	// ledger + approver-allows, the non-safe tool must STILL be denied.
	noop := ac.ToolMiddleware(func(ctx context.Context, call ac.ToolCall, next ac.ToolExecuteFunc) (json.RawMessage, error) {
		return next(ctx, call.Args)
	})
	agent := jess.New(jess.WithModel(model), jess.WithTools(rt),
		jess.WithLedger(ledger.DiscardSink{}),
		jess.WithApprover(func(context.Context, jess.Request) (bool, string) { return true, "ok" }),
		jess.WithAgentcoreOptions(ac.WithMiddlewares(noop)))
	ch, wait := jess.Stream(context.Background(), agent, "restart nginx")
	for range ch {
	}
	_ = wait()
	if rt.ran {
		t.Fatal("Extra middleware must NOT bypass audit enforcement; non-safe tool ran unaudited")
	}
}
