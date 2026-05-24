package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

// ChromemStore is a Store backed by philippgille/chromem-go, the
// embedded pure-Go vector DB. Persists to gob files (chromem's
// native format) when a non-empty path is given to NewChromemStore;
// in-memory only otherwise.
//
// Implements both Store and VectorStore — Recall does keyword/tag
// filtering plus content-substring match (chromem's whereDocument);
// SearchVector does cosine-similarity nearest neighbor.
//
// Concurrency: safe across goroutines. chromem handles its own
// internal locking; the ChromemStore adds a mutex only around
// the embedder cache used for ID derivation.
type ChromemStore struct {
	db         *chromem.DB
	collection *chromem.Collection
	embedder   Embedder

	mu       sync.Mutex
	idForKey map[string]string // content-hash → chromem doc ID, for dedupe
}

// ChromemOptions configures NewChromemStore. Persistence path
// optional — empty means "in-memory only, lose state on restart."
// Compress requests gzip on persisted gob files; reduces disk by
// ~5x at the cost of ~5% extra Append latency.
type ChromemOptions struct {
	// Path is the persistence directory. Empty disables disk
	// persistence — entries vanish when the process exits.
	Path string
	// Compress enables gzip on persisted files. No effect when
	// Path is empty.
	Compress bool
	// CollectionName lets callers shard a single chromem DB
	// across multiple Stores. Default "jess-memory".
	CollectionName string
}

// NewChromemStore returns a Store backed by chromem-go. The
// Embedder is required — every Append calls it to produce the
// stored vector, and the Embedder's Name is recorded in entry
// metadata so future code can detect cross-embedder drift.
func NewChromemStore(embedder Embedder, opts ChromemOptions) (*ChromemStore, error) {
	if embedder == nil {
		return nil, errors.New("memory: NewChromemStore requires an Embedder")
	}
	if opts.CollectionName == "" {
		opts.CollectionName = "jess-memory"
	}

	var db *chromem.DB
	var err error
	if opts.Path == "" {
		db = chromem.NewDB()
	} else {
		db, err = chromem.NewPersistentDB(opts.Path, opts.Compress)
		if err != nil {
			return nil, fmt.Errorf("memory: chromem persistent db at %s: %w", opts.Path, err)
		}
	}

	// chromem's EmbeddingFunc is the same shape as our Embedder.Embed —
	// adapt one to the other.
	embedFunc := func(ctx context.Context, text string) ([]float32, error) {
		return embedder.Embed(ctx, text)
	}
	collection, err := db.GetOrCreateCollection(opts.CollectionName, nil, embedFunc)
	if err != nil {
		return nil, fmt.Errorf("memory: create collection %q: %w", opts.CollectionName, err)
	}

	return &ChromemStore{
		db:         db,
		collection: collection,
		embedder:   embedder,
		idForKey:   make(map[string]string),
	}, nil
}

// Embedder returns the embedder the Store was constructed with.
// Implements VectorStore.
func (s *ChromemStore) Embedder() Embedder { return s.embedder }

// Append persists e. ID is content-addressed via entryID (same
// dedupe semantics as InMemoryStore). The embedding is computed
// lazily by chromem on AddDocument when Embedding is nil — we
// could also pre-compute via s.embedder, but offloading lets
// chromem batch under the hood if we later use AddDocuments.
//
// chromem doesn't dedupe by ID — re-adding with the same ID
// silently shadows the prior entry. We track IDs ourselves so
// re-Appending the same content doesn't re-embed (re-embedding
// would burn the embedder's API/CPU budget for no gain).
func (s *ChromemStore) Append(ctx context.Context, e Entry) (Entry, error) {
	if e.ID == "" {
		e.ID = entryID(e)
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}

	s.mu.Lock()
	_, already := s.idForKey[e.ID]
	s.idForKey[e.ID] = e.ID
	s.mu.Unlock()
	if already {
		// Same content seen before; chromem already has it.
		// We still return the entry so callers get a populated
		// CreatedAt.
		return e, nil
	}

	doc := chromem.Document{
		ID:       e.ID,
		Metadata: entryToMetadata(e, s.embedder.Name()),
		Content:  e.Text,
		// Embedding left nil — chromem invokes our EmbeddingFunc.
	}
	if err := s.collection.AddDocument(ctx, doc); err != nil {
		return Entry{}, fmt.Errorf("memory: chromem add %s: %w", e.ID, err)
	}
	return e, nil
}

// Recall does metadata + content filtering and returns entries in
// newest-first order. Vector similarity is NOT used here — use
// SearchVector for that. This keeps Recall semantics consistent
// with the InMemoryStore so a HybridRecaller can mix VectorRecaller
// (vector path) and SimpleRecaller (keyword path) cleanly.
func (s *ChromemStore) Recall(ctx context.Context, q Query, max int) ([]Entry, error) {
	// chromem v0.7.0 doesn't expose a metadata-only filter that
	// returns Documents directly; ListDocuments + Go-side filter
	// is the available path. Cost scales with collection size,
	// which is fine for memory-volume corpora (thousands of
	// entries, not millions). Compact-or-shard when that ceases
	// to be true.
	docs, err := s.collection.ListDocuments(ctx)
	if err != nil {
		return nil, fmt.Errorf("memory: chromem ListDocuments: %w", err)
	}
	textFilter := strings.ToLower(q.Text)
	entries := make([]Entry, 0, len(docs))
	for _, doc := range docs {
		e := metadataToEntry(doc.ID, doc.Metadata, doc.Content)
		if !textMatchesAndAgentScopeOK(e, q) {
			continue
		}
		if q.Kind != "" && e.Kind != q.Kind {
			continue
		}
		if textFilter != "" && !strings.Contains(strings.ToLower(e.Text), textFilter) {
			continue
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	if max > 0 && len(entries) > max {
		entries = entries[:max]
	}
	return entries, nil
}

// SearchVector does cosine-similarity nearest-neighbor search. The
// filter narrows the candidate set the same way Query does in
// Recall — AgentID, Kind, Tags. Implements VectorStore.
func (s *ChromemStore) SearchVector(ctx context.Context, vec []float32, max int, filter Query) ([]Entry, error) {
	if len(vec) == 0 {
		return nil, errors.New("memory: SearchVector requires a non-empty vector")
	}
	if max <= 0 {
		max = 8
	}
	where := queryToWhere(filter)
	whereDoc := map[string]string(nil)
	if filter.Text != "" {
		whereDoc = map[string]string{"$contains": strings.ToLower(filter.Text)}
	}
	// chromem's QueryEmbedding rejects nResults > collection size.
	// Overfetch to give the agent-scope post-filter headroom but
	// clamp to the collection so small collections don't error.
	nResults := max * 2
	if cnt := s.collection.Count(); cnt < nResults {
		nResults = cnt
	}
	if nResults == 0 {
		return nil, nil
	}
	results, err := s.collection.QueryEmbedding(ctx, vec, nResults, where, whereDoc)
	if err != nil {
		return nil, fmt.Errorf("memory: chromem QueryEmbedding: %w", err)
	}
	entries := make([]Entry, 0, len(results))
	for _, r := range results {
		e := metadataToEntry(r.ID, r.Metadata, r.Content)
		if !textMatchesAndAgentScopeOK(e, filter) {
			continue
		}
		entries = append(entries, e)
		if len(entries) >= max {
			break
		}
	}
	return entries, nil
}

// Forget removes the entry by ID. Idempotent.
func (s *ChromemStore) Forget(ctx context.Context, id string) error {
	s.mu.Lock()
	delete(s.idForKey, id)
	s.mu.Unlock()
	if err := s.collection.Delete(ctx, nil, nil, id); err != nil {
		return fmt.Errorf("memory: chromem delete %s: %w", id, err)
	}
	return nil
}

// entryToMetadata flattens an Entry into chromem's string-only
// metadata shape. Tags become a comma-joined string; CreatedAt
// is RFC3339; the embedder name is recorded so future code can
// detect "this entry was indexed by a different embedder" and
// re-embed or refuse.
func entryToMetadata(e Entry, embedderName string) map[string]string {
	m := map[string]string{
		"agent":    e.AgentID,
		"kind":     e.Kind,
		"created":  e.CreatedAt.UTC().Format(time.RFC3339Nano),
		"embedder": embedderName,
	}
	if len(e.Tags) > 0 {
		m["tags"] = strings.Join(e.Tags, ",")
	}
	return m
}

// metadataToEntry reverses entryToMetadata for results coming back
// from chromem.
func metadataToEntry(id string, m map[string]string, content string) Entry {
	e := Entry{
		ID:      id,
		AgentID: m["agent"],
		Kind:    m["kind"],
		Text:    content,
	}
	if raw := m["tags"]; raw != "" {
		e.Tags = strings.Split(raw, ",")
	}
	if raw := m["created"]; raw != "" {
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			e.CreatedAt = t
		}
	}
	return e
}

// queryToWhere maps our Query into chromem's metadata filter.
// chromem matches exact strings only, so we encode just the
// exact-equality parts (AgentID, Kind). Tag set-membership is
// not exact — we filter in Go via textMatchesAndAgentScopeOK.
// Text is handled separately as whereDocument $contains.
func queryToWhere(q Query) map[string]string {
	where := map[string]string{}
	if q.Kind != "" {
		where["kind"] = q.Kind
	}
	// AgentID can match "" (global) or the exact ID — chromem
	// doesn't OR, so we filter in Go for that case too.
	return where
}

// textMatchesAndAgentScopeOK is the post-chromem filter that
// implements our agent-scope rule (q.AgentID="x" matches entries
// with AgentID="x" OR AgentID="") and re-applies tag set membership
// since chromem can't express "all of these tags."
func textMatchesAndAgentScopeOK(e Entry, q Query) bool {
	if q.AgentID != "" && e.AgentID != "" && e.AgentID != q.AgentID {
		return false
	}
	for _, want := range q.Tags {
		found := false
		for _, have := range e.Tags {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
