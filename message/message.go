// Package message defines jess's conversation vocabulary, independent of any
// agent harness. The anti-corruption layer (internal/agentcore) translates
// these to and from the harness's message types; nothing here imports
// agentcore.
package message

import (
	"encoding/json"
	"strings"
)

// Role identifies who produced a Message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// BlockKind identifies which content variant a ContentBlock carries.
type BlockKind string

const (
	BlockText       BlockKind = "text"
	BlockThinking   BlockKind = "thinking"
	BlockToolCall   BlockKind = "tool_call"
	BlockToolResult BlockKind = "tool_result"
)

// ContentBlock is one piece of a Message's content. Fields are populated
// according to Kind; unused fields stay zero.
type ContentBlock struct {
	Kind     BlockKind
	Text     string          // BlockText, BlockThinking
	ToolID   string          // BlockToolCall, BlockToolResult
	ToolName string          // BlockToolCall
	Args     json.RawMessage // BlockToolCall
	Result   json.RawMessage // BlockToolResult
	IsError  bool            // BlockToolResult
}

// Message is the content produced by one Role in a conversation turn.
type Message struct {
	Role    Role
	Content []ContentBlock
}

// Text returns the concatenation of all text blocks. Thinking, tool-call, and
// tool-result blocks are skipped. Convenience for the common "what did the
// assistant say" case.
func (m Message) Text() string {
	var b strings.Builder
	for _, blk := range m.Content {
		if blk.Kind == BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// UserText builds a user Message carrying a single text block.
func UserText(s string) Message {
	return Message{Role: RoleUser, Content: []ContentBlock{{Kind: BlockText, Text: s}}}
}
