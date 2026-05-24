package memory

import (
	"context"
	"errors"
	"time"
)

// Entry is one memory record. The shape borrows from the auto-memory
// pattern Claude Code uses (typed snippets with descriptions) but
// without the markdown-file requirement — Entry is the in-process
// type; serialization is the Store's problem.
//
// Fields are intentionally small. Memory entries are short prose
// snippets ("user prefers tabs over spaces", "the merge freeze
// starts 2026-03-05") plus enough metadata to find them again.
// Larger structured artifacts belong in a domain-specific store,
// not in agent memory.
type Entry struct {
	// ID is a Store-assigned identifier. Empty on Append; populated
	// by the Store before return so callers can Forget specific
	// entries later. Stable across reads — implementations should
	// not renumber.
	ID string

	// Kind tags the entry's semantic category. See the Kind type
	// (kind.go) for the canonical taxonomy + per-Kind retrieval
	// policy. Stored as a plain string so raw literals work
	// without conversion. Empty Kind picks up FallbackKindPolicy.
	Kind string

	// AgentID scopes the entry. A multi-agent host (jess's design
	// expects this) stores per-agent memory so the "Coding" agent
	// doesn't surface "Research" agent preferences. Empty means
	// global (visible to all agents).
	AgentID string

	// Text is the memory content. Short prose. Implementations are
	// free to truncate at a sensible bound (suggested: 8KB) — the
	// goal is "things the model can read without burning context".
	Text string

	// Tags are optional searchable labels. Recallers use them for
	// fast filtering before scoring; a Tag-only Recall is a
	// reasonable cheap strategy.
	Tags []string

	// Key is an optional semantic identity. When set, Append
	// REPLACES any prior Entry with the same (AgentID, Key) pair:
	// the old entry is removed from the Store and the new entry
	// takes its place. Use for facts that update over time —
	// "user prefers tabs" → user changes mind → re-Append with
	// the same Key and the new Text supersedes the old. Empty
	// Key disables supersession (each Append is independent,
	// subject only to content-hash dedupe).
	Key string

	// Source records provenance: which session / message / tool
	// caused this entry to be saved. Useful for audit, "show me
	// why you remember this," and bulk-Forget by session. Zero-
	// valued Source is fine for manual programmatic Appends.
	Source Source

	// CreatedAt is set by Store.Append from time.Now if the caller
	// left it zero. Preserved on Recall so policies can prefer
	// recency.
	CreatedAt time.Time
}

// Source captures where an Entry came from. Optional but strongly
// recommended for entries written by the RememberTool — without
// it, a user who asks "why do you remember X?" can't be answered
// and "forget everything from session Y" can't be implemented.
type Source struct {
	// SessionID is the conversation/agent-run identifier that
	// originated the save. Caller-defined shape; jess doesn't
	// enforce a format. Talon uses sessionKey (e.g.
	// "agent:main:main").
	SessionID string
	// MessageID is the specific message within SessionID that
	// triggered the save (typically the assistant turn whose
	// tool_call invoked the RememberTool).
	MessageID string
	// Tool names the tool that performed the save. Set to
	// "remember" for the standard RememberTool path; hosts that
	// wire their own save flow set their own identifier here.
	Tool string
	// Reason is free-form: "user said /remember", "model decided
	// this was important", etc. Useful for audit; not interpreted
	// by the recall pipeline.
	Reason string
}

// Store is the persistence interface for memory entries. Implementations
// must be safe for concurrent use across Append / Recall / Forget; the
// agent host calls Append from the OnMessage hook and Recall from the
// ContextManager prepare path, which can interleave.
type Store interface {
	// Append persists e and returns its assigned ID. Implementations
	// may dedupe by Text + AgentID; in that case ID identifies the
	// existing entry. The returned entry carries the persisted form
	// (ID set, CreatedAt populated if it was zero).
	Append(ctx context.Context, e Entry) (Entry, error)

	// Recall returns at most max entries matching q. Ordering is
	// implementation-defined but should be "most relevant first"
	// — Recallers may re-rank on top of this.
	Recall(ctx context.Context, q Query, max int) ([]Entry, error)

	// Forget removes the entry identified by id. Returns no error
	// for unknown IDs — Forget is idempotent. Callers that need to
	// distinguish "removed" from "never existed" should Recall first.
	Forget(ctx context.Context, id string) error
}

// Query is the input to Store.Recall. Empty fields are wildcards;
// AgentID="" matches all agents including the global scope (entries
// with no AgentID), AgentID="x" matches "x" plus global.
type Query struct {
	AgentID string
	Kind    string
	Tags    []string

	// Text is a free-text snippet to match against Entry.Text. The
	// matching strategy is Store-defined: substring for simple
	// stores, vector similarity for embedding-backed ones.
	Text string
}

// Recaller is the read-side strategy: given the current conversation
// state, pick the entries to inject into the next LLM call.
//
// Stores expose raw lookup. Recallers turn lookup into the right N
// entries for the moment. Separating them lets a host swap, say, a
// TF-IDF recaller for a semantic-search one without changing the
// Store.
type Recaller interface {
	// Recall returns at most max entries to inject. ConversationHint
	// is the last few messages of the running conversation —
	// implementations decide how much context they want; the
	// caller (ContextManager adapter) supplies as much as the
	// host's CompactionStrategy already keeps live.
	Recall(ctx context.Context, store Store, agentID string, conversationHint string, max int) ([]Entry, error)
}

// ErrUnsupported is returned by Stores that don't implement an optional
// capability (e.g. semantic Text matching when the backing Store only
// supports tag lookup). Callers check with errors.Is.
var ErrUnsupported = errors.New("memory: operation unsupported by this Store")

// VectorStore is an optional capability interface for Stores that
// support nearest-neighbor vector search. Recallers (like
// VectorRecaller) type-assert their Store argument to this
// interface; non-vector Stores fail the assertion and fall through
// to their text-only path.
//
// Implementations document the embedder they index against; callers
// MUST pass a query vector produced by the same embedder model the
// Store was constructed with, or results are nonsense. The simplest
// way: every Store that satisfies VectorStore exposes an Embedder
// via Embedder() so callers don't have to track two configurations.
type VectorStore interface {
	Store

	// SearchVector returns the entries closest to vec, in
	// nearest-first order. Distance metric is implementation-
	// defined (chromem-go uses cosine). filter narrows the
	// candidate set the same way Query does in Store.Recall —
	// AgentID, Kind, Tags. Empty filter matches everything.
	SearchVector(ctx context.Context, vec []float32, max int, filter Query) ([]Entry, error)

	// Embedder returns the Embedder this Store was built with.
	// Lets Recallers produce query vectors with the same model
	// the stored vectors were produced with, without the caller
	// threading both through.
	Embedder() Embedder
}
