package core

import (
	"context"
	"errors"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	ac "github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

// toolSpecFromAC converts an agentcore ToolSpec to a jess ToolSpec. agentcore's
// Parameters is an untyped JSON schema; jess models it as a map. A non-map
// Parameters yields a nil Schema (the model gets no schema rather than a bogus
// one).
func toolSpecFromAC(s ac.ToolSpec) model.ToolSpec {
	schema, _ := s.Parameters.(map[string]any)
	return model.ToolSpec{Name: s.Name, Description: s.Description, Schema: schema}
}

// deltaEventType maps a jess delta classification to the agentcore stream event
// type the loop expects for that delta.
func deltaEventType(k event.DeltaKind) ac.StreamEventType {
	switch k {
	case event.DeltaThinking:
		return ac.StreamEventThinkingDelta
	case event.DeltaToolCall:
		return ac.StreamEventToolCallDelta
	default:
		return ac.StreamEventTextDelta
	}
}

// assistantMessageToAC builds the agentcore assistant Message for a Done chunk:
// the translated content blocks plus usage and stop reason, which the loop
// reads from StreamEventDone.Message as authoritative.
func assistantMessageToAC(m message.Message, u model.Usage, stop string) ac.Message {
	acMsg := messagesToAC([]message.Message{m})[0]
	acMsg.StopReason = ac.StopReason(stop)
	acMsg.Usage = &ac.Usage{Input: u.Input, Output: u.Output, TotalTokens: u.TotalTokens}
	return acMsg
}

// nativeModel is a model.Model backed directly by an agentcore.ChatModel
// (jess-provided cloud models). ToAC unwraps it for zero-overhead passthrough;
// its Stream bridges the harness stream to jess Chunks so it is also a valid
// standalone model.Model.
type nativeModel struct{ cm ac.ChatModel }

func newNativeModel(cm ac.ChatModel) nativeModel { return nativeModel{cm: cm} }

func (n nativeModel) SupportsTools() bool { return n.cm.SupportsTools() }

func (n nativeModel) Stream(ctx context.Context, msgs []message.Message, tools []model.ToolSpec) (<-chan model.Chunk, error) {
	acMsgs := messagesToAC(msgs)
	acTools := make([]ac.ToolSpec, len(tools))
	for i, t := range tools {
		acTools[i] = ac.ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.Schema}
	}
	stream, err := n.cm.GenerateStream(ctx, acMsgs, acTools)
	if err != nil {
		return nil, err
	}
	out := make(chan model.Chunk)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-stream:
				if !ok {
					return
				}
				c, emit := chunkFromAC(ev)
				if !emit {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case out <- c:
				}
				if c.Done || c.Err != nil {
					return
				}
			}
		}
	}()
	return out, nil
}

// chunkFromAC maps an agentcore StreamEvent to a jess Chunk. start/end framing
// events have no jess equivalent and return emit=false.
func chunkFromAC(ev ac.StreamEvent) (model.Chunk, bool) {
	switch ev.Type {
	case ac.StreamEventTextDelta:
		return model.Chunk{Delta: ev.Delta, DeltaKind: event.DeltaText}, true
	case ac.StreamEventThinkingDelta:
		return model.Chunk{Delta: ev.Delta, DeltaKind: event.DeltaThinking}, true
	case ac.StreamEventToolCallDelta:
		return model.Chunk{Delta: ev.Delta, DeltaKind: event.DeltaToolCall}, true
	case ac.StreamEventDone:
		// Prefer the event-level StopReason (populated by all agentcore adapters);
		// fall back to the message's own StopReason for callers that only set one.
		stop := ev.StopReason
		if stop == "" {
			stop = ev.Message.StopReason
		}
		return model.Chunk{Done: true, Message: messageFromAC(ev.Message), Usage: usageFromAC(ev.Message.Usage), StopReason: string(stop)}, true
	case ac.StreamEventError:
		return model.Chunk{Err: ev.Err}, true
	default:
		return model.Chunk{}, false
	}
}

// usageFromAC maps agentcore token usage to jess Usage (nil-safe).
func usageFromAC(u *ac.Usage) model.Usage {
	if u == nil {
		return model.Usage{}
	}
	return model.Usage{Input: u.Input, Output: u.Output, TotalTokens: u.TotalTokens}
}

// cappedChatModel wraps an ac.ChatModel to cap max output tokens per call.
// agentcore's WithMaxTokens is a per-call option (not a construction option) and
// the agent loop builds its own options, so capping must be applied on each
// Generate/GenerateStream call here. max <= 0 should never reach this type
// (NewLiteLLMModel only wraps when max > 0).
type cappedChatModel struct {
	cm  ac.ChatModel
	max int
}

func (c cappedChatModel) SupportsTools() bool { return c.cm.SupportsTools() }

func (c cappedChatModel) Generate(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, opts ...ac.CallOption) (*ac.LLMResponse, error) {
	return c.cm.Generate(ctx, msgs, tools, append(opts, ac.WithMaxTokens(c.max))...)
}

func (c cappedChatModel) GenerateStream(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, opts ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	return c.cm.GenerateStream(ctx, msgs, tools, append(opts, ac.WithMaxTokens(c.max))...)
}

// LiteLLMConfig configures a litellm-backed model. Plain fields (no agentcore
// types) so the root jess package can build it without importing the harness.
type LiteLLMConfig struct {
	APIKey    string
	BaseURL   string
	MaxTokens int
}

// NewLiteLLMModel builds a litellm-backed cloud model from provider/modelID and
// an optional config, returning it as a model.Model (native passthrough).
func NewLiteLLMModel(provider, modelID string, cfg LiteLLMConfig) (model.Model, error) {
	var opts []llm.ModelOption
	if cfg.APIKey != "" {
		opts = append(opts, llm.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, llm.WithBaseURL(cfg.BaseURL))
	}
	raw, err := llm.NewModel(provider, modelID, opts...)
	if err != nil {
		return nil, err
	}
	var cm ac.ChatModel = raw
	if cfg.MaxTokens > 0 {
		cm = cappedChatModel{cm: cm, max: cfg.MaxTokens}
	}
	return newNativeModel(cm), nil
}

// ToAC adapts a jess model.Model into an agentcore.ChatModel for the loop. If m
// is a jess-provided cloud model (nativeModel), its underlying ChatModel is
// returned directly — zero translation. Otherwise a translating streamAdapter
// is returned.
func ToAC(m model.Model) ac.ChatModel {
	if nm, ok := m.(nativeModel); ok {
		return nm.cm
	}
	return streamAdapter{m: m}
}

// streamAdapter implements ac.ChatModel by bridging to a jess model.Model.
type streamAdapter struct{ m model.Model }

func (a streamAdapter) SupportsTools() bool { return a.m.SupportsTools() }

func (a streamAdapter) GenerateStream(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, _ ...ac.CallOption) (<-chan ac.StreamEvent, error) {
	jmsgs := make([]message.Message, len(msgs))
	for i := range msgs {
		jmsgs[i] = messageFromAC(msgs[i])
	}
	jtools := make([]model.ToolSpec, len(tools))
	for i := range tools {
		jtools[i] = toolSpecFromAC(tools[i])
	}
	chunks, err := a.m.Stream(ctx, jmsgs, jtools)
	if err != nil {
		return nil, err
	}
	out := make(chan ac.StreamEvent)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-chunks:
				if !ok {
					return
				}
				var ev ac.StreamEvent
				switch {
				case c.Err != nil:
					ev = ac.StreamEvent{Type: ac.StreamEventError, Err: c.Err}
				case c.Done:
					msg := assistantMessageToAC(c.Message, c.Usage, c.StopReason)
					ev = ac.StreamEvent{Type: ac.StreamEventDone, Message: msg, StopReason: msg.StopReason}
				default:
					ev = ac.StreamEvent{Type: deltaEventType(c.DeltaKind), Delta: c.Delta}
				}
				select {
				case <-ctx.Done():
					return
				case out <- ev:
				}
				if c.Done || c.Err != nil {
					return
				}
			}
		}
	}()
	return out, nil
}

func (a streamAdapter) Generate(ctx context.Context, msgs []ac.Message, tools []ac.ToolSpec, opts ...ac.CallOption) (*ac.LLMResponse, error) {
	stream, err := a.GenerateStream(ctx, msgs, tools, opts...)
	if err != nil {
		return nil, err
	}
	var final ac.Message
	var done bool
	for ev := range stream {
		switch ev.Type {
		case ac.StreamEventError:
			return nil, ev.Err
		case ac.StreamEventDone:
			final, done = ev.Message, true
		}
	}
	if !done {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("acl: model stream ended without a done chunk")
	}
	return &ac.LLMResponse{Message: final}, nil
}
