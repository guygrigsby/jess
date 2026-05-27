package acl

import (
	"github.com/guygrigsby/jess/message"
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
