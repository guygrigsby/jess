package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/ledger"
)

// auditMiddleware enforces "no durable record, no action" gate-independently,
// then records every tool execution's result.
//
// agentcore runs this middleware for EVERY tool about to execute, regardless of
// which gate (default, AllowAll, custom WithToolGate) allowed it. So this is the
// unbypassable enforcement point: for a non-safe tool, the middleware commits one
// durable, self-explaining KindAction BEFORE calling next. If the sink is not a
// ledger.DurableSink, or CommitAction fails, it returns an error and next is
// never called (the tool does not run). A permissive gate cannot bypass the
// durable record because the record happens here, not in the gate.
//
// Safe tools (and the result leg of non-safe tools) get a best-effort
// KindToolResult: those writes never block the run.
func auditMiddleware(sink ledger.Sink, nonSafe map[string]bool, rs *runState, agentPath string) ac.ToolMiddleware {
	return func(ctx context.Context, call ac.ToolCall, next ac.ToolExecuteFunc) (json.RawMessage, error) {
		runID := rs.runID()
		if nonSafe[call.Name] {
			durable, ok := sink.(ledger.DurableSink)
			if !ok {
				return nil, fmt.Errorf("ledger not durable; action %q denied (no record, no action)", call.Name)
			}
			reqID, reqText := rs.request()
			action := ledger.Event{
				EventID:   ledger.NewEventID(),
				RunID:     runID,
				CallID:    call.ID,
				Time:      time.Now(),
				AgentPath: agentPath,
				Kind:      ledger.KindAction,
				Tool:      call.Name,
				Args:      call.Args,
				Verdict:   ledger.VerdictAllowed,
				Reason:    reqText, // embedded why
				Refs:      []ledger.Ref{{Source: ledger.RefTool, ID: reqID.String()}},
			}
			if err := durable.CommitAction(action); err != nil {
				return nil, fmt.Errorf("action %q denied: record not durable: %w", call.Name, err)
			}
		}
		start := time.Now()
		res, err := next(ctx, call.Args)
		ev := ledger.Event{
			EventID: ledger.NewEventID(), RunID: runID, CallID: call.ID,
			Time: time.Now(), AgentPath: agentPath,
			Kind: ledger.KindToolResult, Tool: call.Name, Result: res,
			DurationMS: time.Since(start).Milliseconds(),
		}
		if err != nil {
			ev.Err = err.Error()
		}
		_ = sink.Record(ev)
		return res, err
	}
}
