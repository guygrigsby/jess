package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

// InMemoryStore is a Store backed entirely by a map in process memory.
// Loses state on restart — intended for tests and short-lived agents,
// not for the durable-across-runs case that JSONLStore covers.
//
// Safe for concurrent use across Append / Recall / Forget.
type InMemoryStore struct {
	mu      sync.RWMutex
	entries map[string]Entry // keyed by ID

	now func() time.Time // injectable clock for deterministic tests
}

// NewInMemoryStore returns an empty InMemoryStore. Clock is time.Now;
// tests that need deterministic timestamps assign to .Now after
// construction.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		entries: make(map[string]Entry),
		now:     time.Now,
	}
}

// SetClock swaps the internal clock. Test-only helper; production
// code leaves it at the default time.Now.
func (s *InMemoryStore) SetClock(fn func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = fn
}

// Append persists e. ID is assigned by hashing (Text + AgentID +
// Kind), so semantically-identical entries dedupe: a second Append
// with the same content for the same agent returns the existing
// entry rather than creating a new one. CreatedAt is preserved
// across dedupe (the original creation wins).
func (s *InMemoryStore) Append(ctx context.Context, e Entry) (Entry, error) {
	if e.ID == "" {
		e.ID = entryID(e)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.entries[e.ID]; ok {
		// Merge tags so a re-append with new tags grows the set
		// without losing the original CreatedAt. Other fields
		// preserve the original.
		existing.Tags = mergeTags(existing.Tags, e.Tags)
		s.entries[e.ID] = existing
		return existing, nil
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = s.now()
	}
	s.entries[e.ID] = e
	return e, nil
}

// Recall returns the entries matching q in newest-first order. Scoring
// is the simple shape SimpleRecaller expects: any match on AgentID
// (including "" matching everything), Kind, all Tags, then text
// substring. Returns at most max entries.
func (s *InMemoryStore) Recall(ctx context.Context, q Query, max int) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if !matches(e, q) {
			continue
		}
		matched = append(matched, e)
	}
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].CreatedAt.After(matched[j].CreatedAt)
	})
	if max > 0 && len(matched) > max {
		matched = matched[:max]
	}
	return matched, nil
}

// Forget removes the entry with the given ID. Idempotent — no error
// for unknown IDs.
func (s *InMemoryStore) Forget(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, id)
	return nil
}

// matches is the predicate Recall uses against each stored entry.
// Empty query fields are wildcards; AgentID="" matches everything,
// AgentID="x" matches entries with AgentID="x" or empty AgentID
// (global memories are visible to all agents).
func matches(e Entry, q Query) bool {
	if q.AgentID != "" && e.AgentID != "" && e.AgentID != q.AgentID {
		return false
	}
	if q.Kind != "" && e.Kind != q.Kind {
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
	if q.Text != "" {
		if !strings.Contains(strings.ToLower(e.Text), strings.ToLower(q.Text)) {
			return false
		}
	}
	return true
}

// entryID is the content-address ID assigned when callers leave it
// empty. Stable for (AgentID, Kind, Text) — same content always
// hashes to the same ID, so Append dedupes correctly.
func entryID(e Entry) string {
	h := sha256.New()
	h.Write([]byte(e.AgentID))
	h.Write([]byte{0})
	h.Write([]byte(e.Kind))
	h.Write([]byte{0})
	h.Write([]byte(e.Text))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// mergeTags returns the union of a and b, preserving a's order and
// appending any tags from b that weren't already present.
func mergeTags(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a))
	out := append([]string(nil), a...)
	for _, t := range a {
		seen[t] = struct{}{}
	}
	for _, t := range b {
		if _, ok := seen[t]; ok {
			continue
		}
		out = append(out, t)
		seen[t] = struct{}{}
	}
	return out
}
