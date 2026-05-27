package acl

import (
	"context"
	"errors"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/model"
	ac "github.com/voocel/agentcore"
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

// nativeModel is fleshed out in the next task (cloud passthrough). The stub
// here satisfies model.Model so ToAC's type assertion compiles; the next task
// replaces these with real implementations.
type nativeModel struct{ cm ac.ChatModel }

func (nativeModel) SupportsTools() bool { return true }
func (nativeModel) Stream(_ context.Context, _ []message.Message, _ []model.ToolSpec) (<-chan model.Chunk, error) {
	ch := make(chan model.Chunk)
	close(ch)
	return ch, nil
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
