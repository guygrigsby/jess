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

	// Kind tags the entry's semantic category. The taxonomy is
	// caller-defined, but the common four (mirroring Claude Code's
	// auto-memory types) work well:
	//
	//   "user"      — facts about who the user is
	//   "feedback"  — explicit corrections/preferences
	//   "project"   — current goals, decisions, deadlines
	//   "reference" — pointers into external systems
	//
	// Empty Kind is valid; treat it as "untyped" in Recall scoring.
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

	// CreatedAt is set by Store.Append from time.Now if the caller
	// left it zero. Preserved on Recall so policies can prefer
	// recency.
	CreatedAt time.Time
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
