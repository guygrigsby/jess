// Package model defines jess's vendor-free, streaming-first LLM contract.
// Implement Model for a local or custom model; use a jess-provided constructor
// (such as jess.LiteLLM) for a cloud provider. The anti-corruption layer
// (internal/acl) adapts a Model to the agent harness, so nothing here imports
// the harness.
package model

import (
	"context"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
)

// ToolSpec describes a tool to the model: name, description, and JSON schema.
type ToolSpec struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Usage reports token consumption for a generation.
type Usage struct {
	Input       int
	Output      int
	TotalTokens int
}

// Response is the outcome of a one-shot generation: the assistant message
// (which may contain tool-call blocks), token usage, and the provider's stop
// reason. Used by the Once helper; streaming models emit Chunks instead.
type Response struct {
	Message    message.Message
	Usage      Usage
	StopReason string
}

// Chunk is one streaming increment from a Model.
//
// - A delta chunk carries incremental Delta text of the given DeltaKind.
// - The final chunk has Done=true with Message set to the complete assistant
// message (text plus any tool-call blocks), and Usage/StopReason filled.
// - An error chunk has Err set.
//
// A Stream must terminate with exactly one Done chunk or one Err chunk.
type Chunk struct {
	Delta      string
	DeltaKind  event.DeltaKind
	Done       bool
	Message    message.Message
	Usage      Usage
	StopReason string
	Err        error
}

// Model is jess's streaming-first LLM interface.
//
// Stream emits Chunks until it sends a final Done chunk (carrying the complete
// assistant Message) or an Err chunk, then closes the channel. Stream MUST
// honor ctx: on ctx.Done() it stops producing and closes the channel promptly
// (it may send a final Err chunk with ctx.Err()). Context cancellation is how
// interruption (Session.Abort) reaches the model mid-stream.
type Model interface {
	Stream(ctx context.Context, msgs []message.Message, tools []ToolSpec) (<-chan Chunk, error)
	SupportsTools() bool
}
