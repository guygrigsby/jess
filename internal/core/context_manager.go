package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/memory"
)

// ContextManager is the agentcore.ContextManager adapter for jess/memory. It
// injects recalled memory entries as a leading user message before each LLM
// call, then delegates everything else (Compact, RecoverOverflow, Sync, Usage,
// Snapshot) to an inner manager. Hosts that don't have their own context
// strategy get PassthroughInner (the default applied when Inner is nil).
//
// It lives in the ACL (not in jess/memory) because it speaks agentcore types;
// the root jess package wires it via the facade's WithMemory option. Memory
// failures never block the LLM call: Store/Recaller errors are swallowed and
// the adapter degrades to no-memory, never no-agent (ADR Decision 7).
//
// Lifecycle: Project is called once per LLM turn by agentcore. The adapter
// constructs a Query from AgentID + the conversation's recent user content,
// asks the Recaller for matching entries, formats them into a single inserted
// Message, and returns the inner projection with that Message prepended.
//
// Memory injection never commits to the runtime baseline — entries appear in
// the prompt view for one call and vanish on the next. That keeps the
// conversation history readable and prevents memory text from being re-fed back
// into Recall via the conversation hint.
type ContextManager struct {
	store    memory.Store
	recaller memory.Recaller
	agentID  string
	maxItems int
	header   string
	kinds    *memory.KindRegistry

	// audit + rs let Project record a KindRetrieved provenance event for each
	// injected entry (by ref, with the text hash). Both may be nil; recording is
	// always best-effort and never blocks the LLM call.
	audit ledger.Sink
	rs    *runState

	inner ac.ContextManager
}

// ContextManagerOptions configures NewContextManager.
type ContextManagerOptions struct {
	// AgentID scopes Recall queries to one agent. Empty matches memories with
	// no AgentID (the global scope); pass an agent identifier for per-agent
	// memory.
	AgentID string
	// MaxItems caps how many memory entries get injected per call. Default 8.
	// Set to 0 for "as many as Recaller returns".
	MaxItems int
	// Header is the prefix line for the injected message. Default "Relevant
	// memories for this conversation:". Set to "" to omit the header entirely.
	Header string
	// Inner is the underlying ContextManager. May be nil — see PassthroughInner
	// for the no-op default applied in that case.
	Inner ac.ContextManager

	// Kinds is the registry of per-Kind policies. nil uses the baked-in
	// defaults from memory.NewKindRegistry. The ContextManager uses it to decide
	// which Kinds bypass recall (AlwaysInclude=true) and how many entries of
	// each Kind to inject per turn.
	Kinds *memory.KindRegistry

	// Audit is the provenance sink. When set, Project records one KindRetrieved
	// event per turn referencing every injected memory entry (by id + text hash).
	// nil disables retrieval provenance. Recording is best-effort.
	Audit ledger.Sink

	// RunState supplies the current RunID so a KindRetrieved event correlates with
	// the run that injected the memory. nil yields an empty RunID.
	RunState *runState
}

// NewContextManager wires a Store + Recaller behind an agentcore.ContextManager.
// Returns nil on impossible config (nil Store or nil Recaller) — callers should
// construct both explicitly.
func NewContextManager(store memory.Store, recaller memory.Recaller, opts ContextManagerOptions) *ContextManager {
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
		audit:    opts.Audit,
		rs:       opts.RunState,
		inner:    opts.Inner,
	}
	if cm.maxItems == 0 {
		cm.maxItems = 8
	}
	if cm.header == "" && opts.Header == "" {
		cm.header = "Relevant memories for this conversation:"
	}
	if cm.kinds == nil {
		cm.kinds = memory.NewKindRegistry()
	}
	if cm.inner == nil {
		cm.inner = PassthroughInner{}
	}
	return cm
}

// Project builds the prompt view in three layers:
//
//  1. inner.Project produces the baseline (compaction etc).
//  2. AlwaysInclude Kinds (user / feedback by default) get pulled directly from
//     the Store, capped per-Kind by policy. These bypass recall scoring.
//  3. Recall fills the remaining budget with relevance-scored entries from
//     non-AlwaysInclude Kinds.
//
// The two blocks are placed by how stable they are, because providers cache on a
// byte prefix. CORE is the same every turn until a memory is added, so it leads
// the projection and extends the cacheable prefix. RELEVANT is rescored against
// the latest turn, so its bytes differ every turn: it goes LAST, after the
// conversation, and the final stable message is marked as the cache breakpoint.
// Leading with RELEVANT would put volatile bytes ahead of the whole transcript, so
// nothing after them could ever match and the conversation would be re-uploaded at
// full price every turn.
//
// Memory injection never commits to the runtime baseline.
func (m *ContextManager) Project(ctx context.Context, msgs []ac.AgentMessage) (ac.ContextProjection, error) {
	proj, err := m.inner.Project(ctx, msgs)
	if err != nil {
		return proj, err
	}

	core := m.alwaysIncludeEntries(ctx)
	relevant := m.recallForBudget(ctx, msgs, m.maxItems-len(core))
	if len(core) == 0 && len(relevant) == 0 {
		return proj, nil
	}

	m.recordRetrieved(core, relevant)

	if len(core) > 0 {
		proj.Messages = append([]ac.AgentMessage{m.formatCore(core)}, proj.Messages...)
	}
	if len(relevant) > 0 {
		proj.Messages = markCacheBreakpoint(proj.Messages)
		proj.Messages = append(proj.Messages, m.formatRelevant(relevant))
	}
	return proj, nil
}

// cacheBreakpoint is the marker written to Message.Metadata["cache_control"], the
// key agentcore already uses for provider cache placement. It means "everything up
// to and including this message is stable, cache it here"; the provider adapter
// picks the actual TTL.
const cacheBreakpoint = "ephemeral"

// markCacheBreakpoint stamps the last message as the end of the stable prefix, so
// an adapter places its breakpoint there instead of on the volatile block appended
// after it. Returns the input unchanged when the last message is not an ac.Message
// and there is nothing to stamp: a missing marker costs a cache read, never
// correctness.
func markCacheBreakpoint(msgs []ac.AgentMessage) []ac.AgentMessage {
	if len(msgs) == 0 {
		return msgs
	}
	last, ok := msgs[len(msgs)-1].(ac.Message)
	if !ok {
		return msgs
	}
	md := make(map[string]any, len(last.Metadata)+1)
	for k, v := range last.Metadata {
		md[k] = v
	}
	md["cache_control"] = cacheBreakpoint
	last.Metadata = md

	out := make([]ac.AgentMessage, len(msgs))
	copy(out, msgs)
	out[len(out)-1] = last
	return out
}

// alwaysIncludeEntries pulls every entry of every AlwaysInclude Kind for the
// agent, capped per-Kind by KindPolicy.MaxEntries. Failures swallow — memory
// bugs must not block the LLM call.
func (m *ContextManager) alwaysIncludeEntries(ctx context.Context) []memory.Entry {
	var out []memory.Entry
	for _, kind := range m.kinds.AlwaysIncludeKinds() {
		policy := m.kinds.PolicyFor(kind)
		max := policy.MaxEntries
		if max == 0 {
			max = m.maxItems
		}
		entries, err := m.store.Recall(ctx, memory.Query{AgentID: m.agentID, Kind: string(kind)}, max)
		if err != nil {
			continue
		}
		out = append(out, entries...)
	}
	return out
}

// recallForBudget runs the Recaller for non-AlwaysInclude Kinds up to the
// remaining budget. Returns nothing when budget <= 0 (core entries already
// filled the quota).
func (m *ContextManager) recallForBudget(ctx context.Context, msgs []ac.AgentMessage, budget int) []memory.Entry {
	if budget <= 0 {
		return nil
	}
	hint := lastTextContent(msgs)
	entries, err := m.recaller.Recall(ctx, m.store, m.agentID, hint, budget)
	if err != nil {
		return nil
	}
	// Drop entries whose Kind is AlwaysInclude — they're already in `core` from
	// alwaysIncludeEntries. Avoids duplicates if the Recaller doesn't
	// kind-filter (SimpleRecaller doesn't).
	out := entries[:0]
	for _, e := range entries {
		policy := m.kinds.PolicyFor(memory.Kind(e.Kind))
		if policy.AlwaysInclude {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Compact / RecoverOverflow / Sync / Usage / Snapshot delegate to the inner
// manager. Memory has no opinion on compaction strategies.

func (m *ContextManager) Compact(ctx context.Context, msgs []ac.AgentMessage, reason ac.CompactReason) (ac.ContextCommitResult, error) {
	return m.inner.Compact(ctx, msgs, reason)
}

func (m *ContextManager) RecoverOverflow(ctx context.Context, msgs []ac.AgentMessage, cause error) (ac.ContextRecoveryResult, error) {
	return m.inner.RecoverOverflow(ctx, msgs, cause)
}

func (m *ContextManager) Sync(msgs []ac.AgentMessage) { m.inner.Sync(msgs) }

func (m *ContextManager) Usage() *ac.ContextUsage { return m.inner.Usage() }

func (m *ContextManager) Snapshot() *ac.ContextSnapshot { return m.inner.Snapshot() }

// recordRetrieved emits one best-effort KindRetrieved provenance event for the
// turn, with one Ref per injected entry (source=memory, id=entry id, hash of the
// entry text so later drift or deletion is detectable). Recording never blocks
// the LLM call: a nil audit sink, a nil runState, or a Record error all degrade
// silently. The event ties injected memory to the run that consumed it, so "why
// did the agent know X?" is answerable from the ledger.
func (m *ContextManager) recordRetrieved(core, relevant []memory.Entry) {
	if m.audit == nil {
		return
	}
	refs := make([]ledger.Ref, 0, len(core)+len(relevant))
	for _, group := range [][]memory.Entry{core, relevant} {
		for _, e := range group {
			refs = append(refs, ledger.Ref{
				Source: ledger.RefMemory,
				ID:     e.ID,
				Hash:   sha256hex(e.Text),
			})
		}
	}
	if len(refs) == 0 {
		return
	}
	var runID string
	if m.rs != nil {
		runID = m.rs.runID()
	}
	_ = m.audit.Record(ledger.Event{
		EventID:   ledger.NewEventID(),
		RunID:     runID,
		Time:      time.Now(),
		AgentPath: m.agentID,
		Kind:      ledger.KindRetrieved,
		Refs:      refs,
	})
}

// sha256hex returns the lowercase hex sha256 of s.
func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// formatLayered builds the injected memory message with two sub-sections: CORE
// (AlwaysInclude entries) and RELEVANT (recall results), each only emitted when
// non-empty. CORE stays at the top so the model sees stable facts before
// situational ones — matters when budget is tight and the model truncates from
// the bottom.
func (m *ContextManager) formatCore(core []memory.Entry) ac.Message {
	var b strings.Builder
	b.WriteString("Core memories (always relevant):\n\n")
	writeEntries(&b, core)
	return memoryMessage(b.String())
}

func (m *ContextManager) formatRelevant(relevant []memory.Entry) ac.Message {
	var b strings.Builder
	if m.header != "" {
		b.WriteString(m.header)
	} else {
		b.WriteString("Relevant memories for this conversation:")
	}
	b.WriteString("\n\n")
	writeEntries(&b, relevant)
	return memoryMessage(b.String())
}

func memoryMessage(text string) ac.Message {
	return ac.Message{
		Role: ac.Role("user"),
		Content: []ac.ContentBlock{
			ac.TextBlock(text),
		},
	}
}

func writeEntries(b *strings.Builder, entries []memory.Entry) {
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

// lastTextContent returns the textual content of the trailing message —
// typically the new user turn. Empty when there's no useful text (e.g. a pure
// tool_result turn).
func lastTextContent(msgs []ac.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if t := msgs[i].TextContent(); strings.TrimSpace(t) != "" {
			return t
		}
	}
	return ""
}

// PassthroughInner is the default inner ContextManager when callers don't supply
// one. It returns the input messages unchanged and reports zero usage.
// Implements the full ContextManager interface.
type PassthroughInner struct{}

func (PassthroughInner) Project(ctx context.Context, msgs []ac.AgentMessage) (ac.ContextProjection, error) {
	return ac.ContextProjection{Messages: msgs}, nil
}

func (PassthroughInner) Compact(ctx context.Context, msgs []ac.AgentMessage, _ ac.CompactReason) (ac.ContextCommitResult, error) {
	return ac.ContextCommitResult{Messages: msgs, Changed: false}, nil
}

func (PassthroughInner) RecoverOverflow(ctx context.Context, msgs []ac.AgentMessage, _ error) (ac.ContextRecoveryResult, error) {
	return ac.ContextRecoveryResult{View: msgs, Changed: false}, nil
}

func (PassthroughInner) Sync(_ []ac.AgentMessage)      {}
func (PassthroughInner) Usage() *ac.ContextUsage       { return nil }
func (PassthroughInner) Snapshot() *ac.ContextSnapshot { return nil }
