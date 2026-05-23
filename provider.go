package jess

import (
	"context"
)

// Role identifies the speaker of a message in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	// RoleTool is a tool-result message in a multi-turn loop. The Message
	// carrying it has ToolCallID set to the originating tool_use ID and
	// Content set to the tool's serialized output.
	RoleTool Role = "tool"
)

// ModelID is the canonical "<provider>/<model>" identifier used across
// jess (e.g. "openai/gpt-5.4-mini", "anthropic/claude-sonnet-4-6"). The
// provider segment routes a Request to the right Provider; the model
// segment is what the provider's wire API actually uses.
//
// Providers are not required to validate that ModelID matches their Name —
// the harness handles routing. A provider that gets handed a Request with
// the wrong prefix can either error or strip and proceed; jess does not
// dictate.
type ModelID string

// Provider returns the segment before the first "/". Empty when m has no
// slash.
func (m ModelID) Provider() string {
	for i := 0; i < len(m); i++ {
		if m[i] == '/' {
			return string(m[:i])
		}
	}
	return ""
}

// Model returns the segment after the first "/". Returns the whole string
// when m has no slash — lets providers that don't care about routing
// receive the full ID without special-casing.
func (m ModelID) Model() string {
	for i := 0; i < len(m); i++ {
		if m[i] == '/' {
			return string(m[i+1:])
		}
	}
	return string(m)
}

// Message is one turn in a conversation. Tool-related semantics:
//
//   - Role=Assistant + ToolCalls non-empty: the model chose to invoke
//     tools this turn. Content may also be non-empty if the model emitted
//     visible text alongside the calls.
//   - Role=Tool: the message carries a tool result. Content is the tool's
//     output (typically text or JSON); ToolCallID matches the originating
//     ToolCall.ID.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // assistant turns invoking tools
	ToolCallID string     // role=tool only
}

// Options carries non-required per-request tuning. Provider implementations
// translate the platform-agnostic fields and pass anything in Extra through
// to the provider's native API where supported.
type Options struct {
	// Temperature, if non-nil, overrides the provider default. Range is
	// provider-defined (commonly 0..2).
	Temperature *float64
	// MaxOutputTokens caps the streamed response length. 0 means unset —
	// let the provider's default apply.
	MaxOutputTokens int
	// Extra is the escape hatch for provider-specific options the
	// abstraction does not model (OpenAI logit_bias, DeepSeek
	// frequency_penalty, Anthropic top_k, etc). Concrete providers
	// ignore unknown keys.
	Extra map[string]any
}

// Request is the streaming-completion request handed to a Provider.
type Request struct {
	// Model is the canonical "<provider>/<model>" ID. Harness routes by
	// Model.Provider(); the provider receives the full ID and typically
	// uses Model.Model() for the wire request.
	Model ModelID
	// System is an optional system prompt prepended to Messages. Empty
	// means none. Kept separate from Messages so providers that have a
	// dedicated system field (Anthropic) can route it natively rather
	// than synthesizing a leading system Message.
	System string
	// Messages is the chat history, oldest first. Should not include the
	// system prompt — that goes in System.
	Messages []Message
	// Options is per-request tuning. Zero value is safe.
	Options Options
	// Tools advertises the function/tool surface the model may invoke
	// during this completion. Empty disables tool-calling for this turn.
	Tools []ToolSpec
}

// DeltaKind discriminates a Delta variant. Each variant uses a distinct
// subset of Delta's fields — see the Delta godoc for which apply.
type DeltaKind int

const (
	// DeltaText is a textual token chunk in the assistant response.
	// Concatenating Text across all DeltaText events produces the
	// visible reply.
	DeltaText DeltaKind = iota
	// DeltaReasoning is a chunk of the model's hidden reasoning trace
	// (o1 / Claude-thinking style). Most providers will not emit this;
	// the assembled string is informative — not part of the user-visible
	// reply.
	DeltaReasoning
	// DeltaUsage is sent at most once per stream, near the end, with
	// final input/output token counts. Usage is non-nil for this kind.
	DeltaUsage
	// DeltaToolCall is emitted once per assembled tool call when the
	// stream finalizes it (typically at finish_reason="tool_calls" or
	// end-of-stream). ToolCall is non-nil and contains the full
	// ArgumentsJSON — the provider buffers fragments internally so the
	// harness never sees partial arguments.
	//
	// A single stream may emit multiple DeltaToolCall events when the
	// model chose more than one tool to invoke this turn.
	DeltaToolCall
	// DeltaError indicates a mid-stream failure. Err is non-nil and the
	// channel is closed immediately after this delta. Setup-time
	// failures (auth, malformed request) should be returned synchronously
	// from Provider.Stream, not surfaced here.
	DeltaError
)

// String returns a human-readable label for k. Stable for logging.
func (k DeltaKind) String() string {
	switch k {
	case DeltaText:
		return "text"
	case DeltaReasoning:
		return "reasoning"
	case DeltaUsage:
		return "usage"
	case DeltaToolCall:
		return "tool_call"
	case DeltaError:
		return "error"
	default:
		return "unknown"
	}
}

// Usage is the accounting payload of a DeltaUsage event.
type Usage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int // 0 if the provider does not report it separately
	// CachedInputTokens is the subset of InputTokens served from the
	// provider's prompt cache (Anthropic prompt caching, OpenAI cached
	// input). 0 when the provider doesn't surface a cache distinction.
	CachedInputTokens int
}

// Delta is a single event in a streaming response. Which fields are set
// depends on Kind:
//
//	DeltaText:      Text
//	DeltaReasoning: Text     (reasoning chunk; not part of visible reply)
//	DeltaUsage:     Usage    (non-nil)
//	DeltaToolCall:  ToolCall (non-nil; assembled, not fragments)
//	DeltaError:     Err      (non-nil)
type Delta struct {
	Kind     DeltaKind
	Text     string
	Usage    *Usage
	ToolCall *ToolCall
	Err      error
}

// Provider is a streaming-completion source. Implementations must be safe
// for concurrent use across distinct Stream calls; a single returned
// channel must not be read by more than one consumer.
//
// Lifecycle contract:
//
//  1. Stream returns (channel, nil) for a successfully started stream, or
//     (nil, err) for setup-time failures. The harness routes setup
//     failures as run aborts.
//
//  2. The channel receives Deltas in arrival order. Producers close the
//     channel exactly once when the stream terminates — normal end (after
//     the optional DeltaUsage), DeltaError (followed immediately by close),
//     or context cancellation (close without a final delta; consumer
//     consults ctx.Err()).
//
//  3. The harness does NOT call Close on the provider; the channel-close
//     contract handles teardown.
type Provider interface {
	// Name returns the provider's stable identifier — matches the
	// provider segment of ModelID for any model this provider serves
	// (e.g. "openai", "deepseek", "anthropic"). Used for routing and
	// for log/event attribution.
	Name() string

	// Stream initiates a streaming completion. See Provider docs above
	// for the channel-close contract.
	Stream(ctx context.Context, req Request) (<-chan Delta, error)
}

// ProviderRegistry routes a Request to the right Provider by ModelID
// prefix. The harness owns one of these; hosts register their providers
// at startup. Safe for concurrent use after construction; concurrent
// Register / Get is not supported (initialize once, then read-only).
type ProviderRegistry struct {
	byName map[string]Provider
}

// NewProviderRegistry builds a registry from the given providers.
// Duplicate Name() values are rejected at construction so a config
// mistake surfaces immediately rather than as confusing routing later.
func NewProviderRegistry(providers ...Provider) (*ProviderRegistry, error) {
	r := &ProviderRegistry{byName: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		if p == nil {
			return nil, errNilProvider
		}
		name := p.Name()
		if name == "" {
			return nil, errEmptyProviderName
		}
		if _, exists := r.byName[name]; exists {
			return nil, &duplicateProviderErr{Name: name}
		}
		r.byName[name] = p
	}
	return r, nil
}

// Get returns the Provider registered under name, or nil + false if not
// found. Callers pass ModelID.Provider() to look up the right backend.
func (r *ProviderRegistry) Get(name string) (Provider, bool) {
	p, ok := r.byName[name]
	return p, ok
}

// Names returns the registered provider names. Order is not guaranteed.
// Useful for diagnostic messages ("provider X not registered; known:
// [a, b, c]") when a Request references an unknown ModelID prefix.
func (r *ProviderRegistry) Names() []string {
	out := make([]string, 0, len(r.byName))
	for name := range r.byName {
		out = append(out, name)
	}
	return out
}
