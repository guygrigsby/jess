package core

import (
	"context"

	ac "github.com/voocel/agentcore"
)

// GenerateFunc is a one-shot generation: messages + tool specs in, one response
// out. The streaming half is synthesized by Once.
type GenerateFunc func(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec) (*ac.LLMResponse, error)

// Once adapts a one-shot GenerateFunc into an agentcore.ChatModel, emitting the
// whole response as a single terminal stream event. supportsTools advertises
// tool capability to the loop.
func Once(supportsTools bool, fn GenerateFunc) ac.ChatModel {
	return &onceModel{supportsTools: supportsTools, fn: fn}
}

type onceModel struct {
	supportsTools bool
	fn            GenerateFunc
}

func (m *onceModel) SupportsTools() bool { return m.supportsTools }

func (m *onceModel) Generate(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, _ ...ac.CallOption) (*ac.LLMResponse, error) {
	return m.fn(ctx, msgs, tools)
}

// GenerateStream synthesizes a degenerate stream from the one-shot result: a
// single terminal StreamEventDone carrying the full assistant message. The
// agentcore loop only requires the done event (with the final message and stop
// reason) to advance; intermediate text deltas are an optimization Once skips.
func (m *onceModel) GenerateStream(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, _ ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	resp, err := m.fn(ctx, msgs, tools)
	if err != nil {
		return nil, err
	}
	msg := resp.Message
	stop := msg.StopReason
	if stop == "" {
		// Derive a sensible terminal reason so the loop knows whether to run
		// tools or finish: a message carrying tool calls means toolUse.
		if msg.HasToolCalls() {
			stop = ac.StopReasonToolUse
		} else {
			stop = ac.StopReasonStop
		}
	}
	ch := make(chan ac.StreamEvent, 1)
	ch <- ac.StreamEvent{Type: ac.StreamEventDone, Message: msg, StopReason: stop}
	close(ch)
	return ch, nil
}
