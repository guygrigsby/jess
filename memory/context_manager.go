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
	kinds    *KindRegistry

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

	// Kinds is the registry of per-Kind policies. nil uses the
	// baked-in defaults from NewKindRegistry. The ContextManager
	// uses it to decide which Kinds bypass recall (AlwaysInclude=true)
	// and how many entries of each Kind to inject per turn.
	Kinds *KindRegistry
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
		kinds:    opts.Kinds,
		inner:    opts.Inner,
	}
	if cm.maxItems == 0 {
		cm.maxItems = 8
	}
	if cm.header == "" && opts.Header == "" {
		cm.header = "Relevant memories for this conversation:"
	}
	if cm.kinds == nil {
		cm.kinds = NewKindRegistry()
	}
	if cm.inner == nil {
		cm.inner = PassthroughInner{}
	}
	return cm
}

// Project builds the prompt view in three layers:
//
//  1. inner.Project produces the baseline (compaction etc).
//  2. AlwaysInclude Kinds (user / feedback by default) get pulled
//     directly from the Store, capped per-Kind by policy. These
//     bypass recall scoring — they're CORE memories the model
//     always operates against.
//  3. Recall fills the remaining budget with relevance-scored
//     entries from non-AlwaysInclude Kinds.
//
// The two memory blocks become ONE leading user message prepended
// to the projection (so the model sees: CORE first, then RELEVANT,
// then conversation). Memory injection never commits to the
// runtime baseline — entries appear in the prompt view for one
// call and vanish on the next.
func (m *ContextManager) Project(ctx context.Context, msgs []agentcore.AgentMessage) (agentcore.ContextProjection, error) {
	proj, err := m.inner.Project(ctx, msgs)
	if err != nil {
		return proj, err
	}

	core := m.alwaysIncludeEntries(ctx)
	relevant := m.recallForBudget(ctx, msgs, m.maxItems-len(core))
	if len(core) == 0 && len(relevant) == 0 {
		return proj, nil
	}

	memMsg := m.formatLayered(core, relevant)
	proj.Messages = append([]agentcore.AgentMessage{memMsg}, proj.Messages...)
	return proj, nil
}

// alwaysIncludeEntries pulls every entry of every AlwaysInclude
// Kind for the agent, capped per-Kind by KindPolicy.MaxEntries.
// Failures swallow — memory bugs must not block the LLM call.
func (m *ContextManager) alwaysIncludeEntries(ctx context.Context) []Entry {
	var out []Entry
	for _, kind := range m.kinds.AlwaysIncludeKinds() {
		policy := m.kinds.PolicyFor(kind)
		max := policy.MaxEntries
		if max == 0 {
			max = m.maxItems
		}
		entries, err := m.store.Recall(ctx, Query{AgentID: m.agentID, Kind: string(kind)}, max)
		if err != nil {
			continue
		}
		out = append(out, entries...)
	}
	return out
}

// recallForBudget runs the Recaller for non-AlwaysInclude Kinds
// up to the remaining budget. Returns nothing when budget <= 0
// (core entries already filled the quota).
func (m *ContextManager) recallForBudget(ctx context.Context, msgs []agentcore.AgentMessage, budget int) []Entry {
	if budget <= 0 {
		return nil
	}
	hint := lastTextContent(msgs)
	entries, err := m.recaller.Recall(ctx, m.store, m.agentID, hint, budget)
	if err != nil {
		return nil
	}
	// Drop entries whose Kind is AlwaysInclude — they're already
	// in `core` from alwaysIncludeEntries. Avoids duplicates if
	// the Recaller doesn't kind-filter (SimpleRecaller doesn't).
	out := entries[:0]
	for _, e := range entries {
		policy := m.kinds.PolicyFor(Kind(e.Kind))
		if policy.AlwaysInclude {
			continue
		}
		out = append(out, e)
	}
	return out
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

// formatLayered builds the injected memory message with two
// sub-sections: CORE (AlwaysInclude entries) and RELEVANT (recall
// results), each only emitted when non-empty. Layout keeps CORE
// at the top so the model sees stable facts before situational
// ones — matters when budget is tight and the model truncates
// from the bottom.
func (m *ContextManager) formatLayered(core, relevant []Entry) agentcore.Message {
	var b strings.Builder
	if len(core) > 0 {
		b.WriteString("Core memories (always relevant):\n\n")
		writeEntries(&b, core)
		if len(relevant) > 0 {
			b.WriteString("\n")
		}
	}
	if len(relevant) > 0 {
		if m.header != "" {
			b.WriteString(m.header)
		} else {
			b.WriteString("Relevant memories for this conversation:")
		}
		b.WriteString("\n\n")
		writeEntries(&b, relevant)
	}
	return agentcore.Message{
		Role: agentcore.Role("user"),
		Content: []agentcore.ContentBlock{
			agentcore.TextBlock(b.String()),
		},
	}
}

func writeEntries(b *strings.Builder, entries []Entry) {
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
