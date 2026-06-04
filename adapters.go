package jess

import (
	"context"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/internal/core"
	"github.com/guygrigsby/jess/memory"
)

// GenerateFunc is a one-shot generation function (see Once).
type GenerateFunc = core.GenerateFunc

// Once adapts a one-shot function into an agentcore.ChatModel, so a local model
// stays a one-liner. supportsTools advertises tool capability to the loop.
func Once(supportsTools bool, fn GenerateFunc) ac.ChatModel { return core.Once(supportsTools, fn) }

// Stream drives one prompt and returns the event channel plus a Wait for the
// final summary. Cancelling ctx aborts the run (the kill switch).
func Stream(ctx context.Context, agent *ac.Agent, input string) (<-chan ac.Event, func() *ac.RunSummary) {
	return core.Stream(ctx, agent, input)
}

// NewMemoryManager builds the agentcore.ContextManager that injects recalled
// memory each turn. Use it when driving a raw agentcore.NewAgent directly.
func NewMemoryManager(store memory.Store, recaller memory.Recaller) ac.ContextManager {
	return core.NewContextManager(store, recaller, core.ContextManagerOptions{})
}
