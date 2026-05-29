// Package tool defines the capability contract the model invokes. The
// interface is structurally identical to the harness's tool interface by
// design: the anti-corruption layer adapts a jess tool to the harness by
// wrapping it, with no field copying. jess's own memory tools implement this
// interface; nothing here imports agentcore.
package tool

import (
	"context"
	"encoding/json"
)

// Tool is a single capability the model can call during a run.
type Tool interface {
	// Name is the stable identifier the model uses to invoke the tool.
	Name() string
	// Description tells the model when and how to use the tool.
	Description() string
	// Schema is the JSON Schema for the tool's arguments.
	Schema() map[string]any
	// Execute runs the tool against decoded-but-raw JSON args and returns a
	// raw JSON result. A non-nil error aborts the call; tool-level failures
	// the model should see are encoded in the result instead.
	Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}
