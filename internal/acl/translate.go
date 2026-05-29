package acl

import (
	"context"
	"encoding/json"

	"github.com/guygrigsby/jess/event"
	"github.com/guygrigsby/jess/message"
	"github.com/guygrigsby/jess/tool"
	ac "github.com/voocel/agentcore"
)

// roleToAC maps a jess Role to an agentcore Role. The string values coincide,
// but the mapping is explicit so a divergence is a compile/test failure, not a
// silent mismatch.
func roleToAC(r message.Role) ac.Role {
	switch r {
	case message.RoleUser:
		return ac.RoleUser
	case message.RoleAssistant:
		return ac.RoleAssistant
	case message.RoleSystem:
		return ac.RoleSystem
	case message.RoleTool:
		return ac.RoleTool
	default:
		return ac.Role(r)
	}
}

// roleFromAC maps an agentcore Role to a jess Role.
func roleFromAC(r ac.Role) message.Role {
	switch r {
	case ac.RoleUser:
		return message.RoleUser
	case ac.RoleAssistant:
		return message.RoleAssistant
	case ac.RoleSystem:
		return message.RoleSystem
	case ac.RoleTool:
		return message.RoleTool
	default:
		return message.Role(r)
	}
}

// messagesToAC converts jess messages to agentcore messages. A RoleTool
// message expands to one agentcore message per tool-result block (agentcore
// models each tool result as a standalone RoleTool message via ToolResultMsg).
// All other roles map to a single message with translated content blocks.
func messagesToAC(msgs []message.Message) []ac.Message {
	out := make([]ac.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == message.RoleTool {
			for _, b := range m.Content {
				if b.Kind != message.BlockToolResult {
					continue
				}
				out = append(out, ac.ToolResultMsg(b.ToolID, b.Result, b.IsError))
			}
			continue
		}
		blocks := make([]ac.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			// A tool result only belongs in a RoleTool message (handled
			// above). If one appears here the message is malformed; skip it
			// rather than emit an empty text block that silently drops the
			// result's semantics.
			if b.Kind == message.BlockToolResult {
				continue
			}
			blocks = append(blocks, blockToAC(b))
		}
		out = append(out, ac.Message{Role: roleToAC(m.Role), Content: blocks})
	}
	return out
}

// messageFromAC translates a single agentcore message to a jess message. A
// RoleTool message is reconstructed into one BlockToolResult using the
// tool_call_id/is_error metadata that ToolResultMsg stored. Image and
// tool-reference blocks (no jess equivalent) are dropped.
func messageFromAC(m ac.Message) message.Message {
	if m.Role == ac.RoleTool {
		toolID, _ := m.Metadata["tool_call_id"].(string)
		isErr, _ := m.Metadata["is_error"].(bool)
		var result []byte
		for _, b := range m.Content {
			if b.Type == ac.ContentText {
				result = []byte(b.Text)
				break
			}
		}
		return message.Message{Role: message.RoleTool, Content: []message.ContentBlock{{
			Kind: message.BlockToolResult, ToolID: toolID, Result: result, IsError: isErr,
		}}}
	}
	blocks := make([]message.ContentBlock, 0, len(m.Content))
	for _, b := range m.Content {
		switch b.Type {
		case ac.ContentText:
			blocks = append(blocks, message.ContentBlock{Kind: message.BlockText, Text: b.Text})
		case ac.ContentThinking:
			blocks = append(blocks, message.ContentBlock{Kind: message.BlockThinking, Text: b.Thinking})
		case ac.ContentToolCall:
			if b.ToolCall != nil {
				blocks = append(blocks, message.ContentBlock{
					Kind: message.BlockToolCall, ToolID: b.ToolCall.ID,
					ToolName: b.ToolCall.Name, Args: b.ToolCall.Args,
				})
			}
		default: // ContentImage, ContentToolRef: no jess equivalent, drop
		}
	}
	return message.Message{Role: roleFromAC(m.Role), Content: blocks}
}

// blockToAC translates a single non-tool-result content block.
func blockToAC(b message.ContentBlock) ac.ContentBlock {
	switch b.Kind {
	case message.BlockThinking:
		return ac.ThinkingBlock(b.Text)
	case message.BlockToolCall:
		return ac.ToolCallBlock(ac.ToolCall{ID: b.ToolID, Name: b.ToolName, Args: b.Args})
	default: // BlockText (and any unknown kind) render as text
		return ac.TextBlock(b.Text)
	}
}

// wrappedTool adapts a jess tool.Tool to agentcore.Tool. The interfaces are
// structurally identical, so this delegates field-for-field. inject, when
// non-nil, is applied to the ctx before every Execute call — used to inject
// the current run's event stream so tools can forward events into the parent.
type wrappedTool struct {
	t      tool.Tool
	inject func(context.Context) context.Context
}

func (w wrappedTool) Name() string           { return w.t.Name() }
func (w wrappedTool) Description() string    { return w.t.Description() }
func (w wrappedTool) Schema() map[string]any { return w.t.Schema() }
func (w wrappedTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	if w.inject != nil {
		ctx = w.inject(ctx)
	}
	return w.t.Execute(ctx, args)
}

// WrapTool adapts a single jess tool to an agentcore.Tool.
func WrapTool(t tool.Tool) ac.Tool { return wrappedTool{t: t} }

// WrapTools adapts a slice of jess tools to agentcore.Tool.
func WrapTools(ts []tool.Tool) []ac.Tool {
	out := make([]ac.Tool, 0, len(ts))
	for _, t := range ts {
		out = append(out, WrapTool(t))
	}
	return out
}

// wrapToolsInject adapts jess tools to agentcore.Tool, applying inject to each
// Execute's context. inject is called on every tool invocation to thread in
// dynamic state (e.g. the current run's event stream).
func wrapToolsInject(ts []tool.Tool, inject func(context.Context) context.Context) []ac.Tool {
	out := make([]ac.Tool, 0, len(ts))
	for _, t := range ts {
		out = append(out, wrappedTool{t: t, inject: inject})
	}
	return out
}

// EventFromAC flattens an agentcore lifecycle event into a jess event. The
// second return is false for agentcore events with no jess equivalent
// (message_start/end, tool_exec_update, retry); callers skip those.
func EventFromAC(e ac.Event) (event.Event, bool) {
	switch e.Type {
	case ac.EventAgentStart:
		return event.Event{Kind: event.KindRunStart}, true
	case ac.EventTurnStart:
		return event.Event{Kind: event.KindTurnStart}, true
	case ac.EventMessageUpdate:
		return event.Event{Kind: event.KindMessageDelta, Delta: e.Delta, DeltaKind: deltaKindFromAC(e.DeltaKind)}, true
	case ac.EventToolExecStart:
		return event.Event{Kind: event.KindToolStart, Tool: e.Tool, ToolCallID: e.ToolID, Args: e.Args}, true
	case ac.EventToolExecEnd:
		return event.Event{Kind: event.KindToolEnd, Tool: e.Tool, ToolCallID: e.ToolID, Result: e.Result, IsError: e.IsError}, true
	case ac.EventTurnEnd:
		return event.Event{Kind: event.KindTurnEnd}, true
	case ac.EventAgentEnd:
		return event.Event{Kind: event.KindRunEnd, Summary: runSummaryFromAC(e)}, true
	case ac.EventError:
		return event.Event{Kind: event.KindError, Err: e.Err}, true
	default:
		return event.Event{}, false
	}
}

// deltaKindFromAC maps agentcore's message-delta classification to jess's.
// Thinking and tool-call deltas map explicitly; everything else (plain text
// today, plus any delta kind agentcore adds later) maps to DeltaText, so a new
// upstream kind degrades to text rather than breaking translation.
func deltaKindFromAC(d ac.DeltaKind) event.DeltaKind {
	switch d {
	case ac.DeltaThinking:
		return event.DeltaThinking
	case ac.DeltaToolCall:
		return event.DeltaToolCall
	default:
		return event.DeltaText
	}
}

// messagesToACAgent translates jess messages to the []ac.AgentMessage shape
// Agent.SetMessages expects. messagesToAC returns []ac.Message; since Go slices
// are not covariant we copy each (ac.Message implements ac.AgentMessage) into
// an interface slice.
func messagesToACAgent(msgs []message.Message) []ac.AgentMessage {
	acMsgs := messagesToAC(msgs)
	out := make([]ac.AgentMessage, len(acMsgs))
	for i := range acMsgs {
		out[i] = acMsgs[i]
	}
	return out
}

func summaryFromAC(s *ac.RunSummary) *event.RunSummary {
	if s == nil {
		return nil
	}
	return &event.RunSummary{Turns: s.TurnCount, ToolCalls: s.ToolCalls, EndReason: string(s.EndReason)}
}

// runSummaryFromAC builds the run summary for an EventAgentEnd, including the
// per-run token usage aggregated from the run's new messages. Used by BOTH the
// run_end event (EventFromAC) and the Wait() result (captureEnd), so the
// streamed summary and the returned summary always agree.
func runSummaryFromAC(e ac.Event) *event.RunSummary {
	s := summaryFromAC(e.Summary)
	if s != nil {
		s.Usage = usageFromACMessages(e.NewMessages)
	}
	return s
}
