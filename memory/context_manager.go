package memory

import (
	"context"
	"strings"

	"github.com/voocel/agentcore"
)

// ContextManager is jess/memory's agentcore.ContextManager adapter.
// It injects recalled memory entries as a leading user message before
// each LLM call, then delegates everything else (Compact,
// RecoverOverflow, Sync, Usage, Snapshot) to an inner manager. Hosts
// that don't have their own context strategy pass nil for the inner
// manager — see PassthroughInner for what the default does.
//
// Lifecycle: Project is called once per LLM turn by agentcore. The
// adapter constructs a Query from AgentID + the conversation's
// recent user content, asks the Recaller for matching entries,
// formats them into a single inserted Message, and returns the
// inner projection with that Message prepended (after any system-
// position messages the inner produced).
//
// Memory injection never commits to the runtime baseline — entries
// appear in the prompt view for one call and vanish on the next.
// That keeps the conversation history readable and prevents
// memory text from being re-fed back into Recall via the
// conversation hint.
type ContextManager struct {
	store    Store
	recaller Recaller
	agentID  string
	maxItems int
	header   string

	inner agentcore.ContextManager
}

// ContextManagerOptions configures NewContextManager.
type ContextManagerOptions struct {
	// AgentID scopes Recall queries to one agent. Empty matches
	// memories with no AgentID (the global scope); pass an agent
	// identifier for per-agent memory.
	AgentID string
	// MaxItems caps how many memory entries get injected per call.
	// Default 8. Set to 0 for "as many as Recaller returns".
	MaxItems int
	// Header is the prefix line for the injected message. Default
	// "Relevant memories for this conversation:". Set to "" to omit
	// the header entirely (just the entries).
	Header string
	// Inner is the underlying ContextManager. May be nil — see
	// PassthroughInner for the no-op default applied in that case.
	Inner agentcore.ContextManager
}

// NewContextManager wires a Store + Recaller behind an
// agentcore.ContextManager. Returns nil on impossible config
// (nil Store or nil Recaller) — callers should construct both
// explicitly.
func NewContextManager(store Store, recaller Recaller, opts ContextManagerOptions) *ContextManager {
	if store == nil || recaller == nil {
		return nil
	}
	cm := &ContextManager{
		store:    store,
		recaller: recaller,
		agentID:  opts.AgentID,
		maxItems: opts.MaxItems,
		header:   opts.Header,
		inner:    opts.Inner,
	}
	if cm.maxItems == 0 {
		cm.maxItems = 8
	}
	if cm.header == "" && opts.Header == "" {
		cm.header = "Relevant memories for this conversation:"
	}
	if cm.inner == nil {
		cm.inner = PassthroughInner{}
	}
	return cm
}

// Project builds the prompt view: inner.Project first, then prepends
// the memory message if Recall returned anything. ShouldCommit is
// always carried through from the inner (memory never commits).
func (m *ContextManager) Project(ctx context.Context, msgs []agentcore.AgentMessage) (agentcore.ContextProjection, error) {
	proj, err := m.inner.Project(ctx, msgs)
	if err != nil {
		return proj, err
	}
	entries, err := m.recallFor(ctx, msgs)
	if err != nil {
		// Memory failures must NOT block the LLM call. Log via
		// agentcore's standard channel would be nice but we have
		// no logger handle; the host's OnMessage / event stream
		// can surface this later. For now, swallow and continue.
		return proj, nil
	}
	if len(entries) == 0 {
		return proj, nil
	}
	memMsg := m.formatEntries(entries)
	proj.Messages = append([]agentcore.AgentMessage{memMsg}, proj.Messages...)
	return proj, nil
}

// Compact / RecoverOverflow / Sync / Usage / Snapshot delegate to
// the inner manager. Memory has no opinion on compaction strategies
// — that's a separate concern, and forcing one would conflict with
// hosts that have their own.

func (m *ContextManager) Compact(ctx context.Context, msgs []agentcore.AgentMessage, reason agentcore.CompactReason) (agentcore.ContextCommitResult, error) {
	return m.inner.Compact(ctx, msgs, reason)
}

func (m *ContextManager) RecoverOverflow(ctx context.Context, msgs []agentcore.AgentMessage, cause error) (agentcore.ContextRecoveryResult, error) {
	return m.inner.RecoverOverflow(ctx, msgs, cause)
}

func (m *ContextManager) Sync(msgs []agentcore.AgentMessage) { m.inner.Sync(msgs) }

func (m *ContextManager) Usage() *agentcore.ContextUsage { return m.inner.Usage() }

func (m *ContextManager) Snapshot() *agentcore.ContextSnapshot { return m.inner.Snapshot() }

// recallFor builds a conversation hint from the trailing user turn
// (or assistant turn if no user is the last) and asks the Recaller
// for matches. Keeps the hint short — we want to bias recall toward
// the immediate context, not the whole history.
func (m *ContextManager) recallFor(ctx context.Context, msgs []agentcore.AgentMessage) ([]Entry, error) {
	hint := lastTextContent(msgs)
	return m.recaller.Recall(ctx, m.store, m.agentID, hint, m.maxItems)
}

// formatEntries turns the matched memories into one agentcore
// Message. The format is simple Markdown-ish so most models read it
// well; future iterations might use a structured XML wrapper.
func (m *ContextManager) formatEntries(entries []Entry) agentcore.Message {
	var b strings.Builder
	if m.header != "" {
		b.WriteString(m.header)
		b.WriteString("\n\n")
	}
	for _, e := range entries {
		b.WriteString("- ")
		if e.Kind != "" {
			b.WriteString("[")
			b.WriteString(e.Kind)
			b.WriteString("] ")
		}
		b.WriteString(e.Text)
		b.WriteByte('\n')
	}
	return agentcore.Message{
		Role: agentcore.Role("user"),
		Content: []agentcore.ContentBlock{
			agentcore.TextBlock(b.String()),
		},
	}
}

// lastTextContent returns the textual content of the trailing
// message — typically the new user turn. Empty when there's no
// useful text (e.g. a pure tool_result turn).
func lastTextContent(msgs []agentcore.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if t := msgs[i].TextContent(); strings.TrimSpace(t) != "" {
			return t
		}
	}
	return ""
}

// PassthroughInner is the default inner ContextManager when callers
// don't supply one. It returns the input messages unchanged and
// reports zero usage. Implements the full ContextManager interface.
type PassthroughInner struct{}

func (PassthroughInner) Project(ctx context.Context, msgs []agentcore.AgentMessage) (agentcore.ContextProjection, error) {
	return agentcore.ContextProjection{Messages: msgs}, nil
}

func (PassthroughInner) Compact(ctx context.Context, msgs []agentcore.AgentMessage, _ agentcore.CompactReason) (agentcore.ContextCommitResult, error) {
	return agentcore.ContextCommitResult{Messages: msgs, Changed: false}, nil
}

func (PassthroughInner) RecoverOverflow(ctx context.Context, msgs []agentcore.AgentMessage, _ error) (agentcore.ContextRecoveryResult, error) {
	return agentcore.ContextRecoveryResult{View: msgs, Changed: false}, nil
}

func (PassthroughInner) Sync(_ []agentcore.AgentMessage)            {}
func (PassthroughInner) Usage() *agentcore.ContextUsage             { return nil }
func (PassthroughInner) Snapshot() *agentcore.ContextSnapshot       { return nil }
