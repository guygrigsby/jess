package core

import (
	"context"
	"encoding/json"
	"time"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/ledger"
)

// auditMiddleware records every tool execution: the request before running,
// then the result or error with duration. Gate denials never reach here (the
// gate short-circuits them) but the gate itself records those, so the two
// together capture every attempt.
func auditMiddleware(sink ledger.Sink, agentPath string) ac.ToolMiddleware {
	return func(ctx context.Context, call ac.ToolCall, next ac.ToolExecuteFunc) (json.RawMessage, error) {
		_ = sink.Record(ledger.Event{
			Time: time.Now(), AgentPath: agentPath, Kind: ledger.KindToolRequest,
			Tool: call.Name, Args: call.Args,
		})
		start := time.Now()
		res, err := next(ctx, call.Args)
		ev := ledger.Event{
			Time: time.Now(), AgentPath: agentPath, Kind: ledger.KindToolResult,
			Tool: call.Name, Result: res, DurationMS: time.Since(start).Milliseconds(),
		}
		if err != nil {
			ev.Err = err.Error()
		}
		_ = sink.Record(ev)
		return res, err
	}
}
