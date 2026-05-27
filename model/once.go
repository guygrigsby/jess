package model

import (
	"context"

	"github.com/guygrigsby/jess/message"
)

// GenerateFunc is a one-shot, non-streaming generation. It should honor ctx so
// the resulting Model is interruptible.
type GenerateFunc func(ctx context.Context, msgs []message.Message, tools []ToolSpec) (*Response, error)

// Once adapts a one-shot generation function into a Model: Stream calls fn and
// emits its result as a single Done chunk (or an Err chunk on failure).
// supportsTools is reported by SupportsTools. This keeps trivial local models
// trivial; a model that can stream tokens should implement Model directly.
func Once(supportsTools bool, fn GenerateFunc) Model {
	return onceModel{supportsTools: supportsTools, fn: fn}
}

type onceModel struct {
	supportsTools bool
	fn            GenerateFunc
}

func (m onceModel) SupportsTools() bool { return m.supportsTools }

func (m onceModel) Stream(ctx context.Context, msgs []message.Message, tools []ToolSpec) (<-chan Chunk, error) {
	ch := make(chan Chunk, 1)
	go func() {
		defer close(ch)
		resp, err := m.fn(ctx, msgs, tools)
		if err != nil {
			ch <- Chunk{Err: err}
			return
		}
		ch <- Chunk{Done: true, Message: resp.Message, Usage: resp.Usage, StopReason: resp.StopReason}
	}()
	return ch, nil
}
