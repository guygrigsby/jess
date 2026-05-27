package acl

import (
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
