package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// JSONLStore is a Store that persists entries as line-delimited JSON.
// One file per Store; appended lines are written atomically (single
// Write call) so concurrent readers don't see partial records.
//
// File format: one Entry per line, encoded as JSON. Forget writes a
// tombstone record (ID with no other fields) — the next Recall skips
// any ID with a later tombstone. The file is never rewritten in
// place; a maintenance routine (Compact) can be run offline to drop
// tombstoned entries and shrink the file.
//
// Concurrency: a single process-wide mutex serializes file access.
// External processes writing the same file concurrently is undefined
// behavior — JSONL is not crash-safe under cross-process append
// races. For single-agent talon use that's fine.
type JSONLStore struct {
	mu sync.Mutex

	path string
	now  func() time.Time
}

// jsonlRecord is the on-disk shape. Tombstone records have ID set
// and Deleted=true, with no other fields populated.
//
// New fields (Key, Source) are appended to the schema; older files
// just lack them and decode as zero values. We never break readers
// of older files — only readers of newer files lose the new fields
// if pointed at older code, which is a non-issue for an embedded
// library.
type jsonlRecord struct {
	ID        string    `json:"id"`
	Deleted   bool      `json:"deleted,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	AgentID   string    `json:"agent,omitempty"`
	Text      string    `json:"text,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`

	// Key is the supersession identity (see Entry.Key). Stored
	// as a top-level field so readers can apply supersession
	// during file replay without parsing nested data.
	Key string `json:"key,omitempty"`
	// Source provenance, stored as a sub-object so the four
	// fields can grow without bloating every record.
	Source *sourceRecord `json:"source,omitempty"`
}

// sourceRecord mirrors Entry.Source on disk. Pointer in jsonlRecord
// so absent provenance encodes as a missing field rather than an
// empty object — keeps lines tighter for the common (manual
// Append, no Source) case.
type sourceRecord struct {
	SessionID string `json:"session_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// NewJSONLStore returns a Store backed by the file at path. The file
// is created (with parent directories) on first Append if it doesn't
// exist. Reading from a missing file returns no entries, not an
// error — a fresh install has no memories yet, that's normal.
func NewJSONLStore(path string) (*JSONLStore, error) {
	if path == "" {
		return nil, errors.New("memory: JSONLStore path is required")
	}
	return &JSONLStore{
		path: path,
		now:  time.Now,
	}, nil
}

// SetClock swaps the internal clock. Test-only helper.
func (s *JSONLStore) SetClock(fn func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = fn
}

// Append persists e. Dedupes by ID (content-address from entryID),
// same as InMemoryStore — a second Append with the same content
// adds nothing. Tags get merged via a rewrite cycle (read all, merge,
// rewrite); for the common case (no dedupe) the path is a single
// O_APPEND write.
func (s *JSONLStore) Append(ctx context.Context, e Entry) (Entry, error) {
	if e.ID == "" {
		e.ID = entryID(e)
	}
	if e.CreatedAt.IsZero() {
		s.mu.Lock()
		e.CreatedAt = s.now()
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check dedupe first. If the entry already exists with the same
	// content, surface the existing one (merged tags) without
	// writing a duplicate record.
	existing, err := s.findLatestLocked(e.ID)
	if err != nil {
		return Entry{}, err
	}
	if existing != nil {
		merged := *existing
		merged.Tags = mergeTags(existing.Tags, e.Tags)
		// Append a tag-update record (full record, dedupe-by-ID
		// at read time means newest wins).
		if !sameTags(existing.Tags, merged.Tags) {
			if err := s.appendRecordLocked(merged); err != nil {
				return Entry{}, err
			}
		}
		return merged, nil
	}
	if err := s.appendRecordLocked(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Recall reads the file, replays it (newest-wins per ID, tombstones
// remove), filters with matches(), sorts newest-first, truncates.
func (s *JSONLStore) Recall(ctx context.Context, q Query, max int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	matched := make([]Entry, 0, len(all))
	for _, e := range all {
		if matches(e, q) {
			matched = append(matched, e)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].CreatedAt.After(matched[j].CreatedAt)
	})
	if max > 0 && len(matched) > max {
		matched = matched[:max]
	}
	return matched, nil
}

// Forget writes a tombstone record. Idempotent — re-tombstoning is
// harmless.
func (s *JSONLStore) Forget(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendRecordLocked(Entry{ID: id}) // empty body + ID is interpreted as tombstone by encode
}

// readAllLocked walks the file once, replaying tombstones AND
// Key-based supersession, and returns the live entries. Caller
// holds s.mu.
//
// Two passes:
//
//  1. Scan every record into a slot map keyed by ID, applying
//     tombstones (Deleted=true marks slot deleted) and tracking
//     the latest ID per (AgentID, Key).
//  2. Walk the slot map: skip deleted slots; for entries with a
//     non-empty Key, also skip any slot whose ID isn't the
//     latest-known for that (AgentID, Key) — those are superseded.
func (s *JSONLStore) readAllLocked() ([]Entry, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: open %s: %w", s.path, err)
	}
	defer func() { _ = f.Close() }() // read-only; close error is not actionable
	scanner := bufio.NewScanner(f)
	// JSONL records can be larger than the default 64KB buffer when
	// memory text approaches the documented 8KB cap on Entry.Text
	// plus metadata. Bump to 1 MiB.
	buf := make([]byte, 1<<20)
	scanner.Buffer(buf, cap(buf))

	type slot struct {
		entry   Entry
		deleted bool
	}
	by := map[string]slot{}
	// (AgentID|Key) → latest ID seen for that key. Used in pass 2
	// to drop superseded entries.
	latestForKey := map[string]string{}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec jsonlRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("memory: parse %s: %w", s.path, err)
		}
		if rec.ID == "" {
			continue
		}
		if rec.Deleted {
			by[rec.ID] = slot{deleted: true}
			continue
		}
		entry := Entry{
			ID:        rec.ID,
			Kind:      rec.Kind,
			AgentID:   rec.AgentID,
			Text:      rec.Text,
			Tags:      rec.Tags,
			CreatedAt: rec.CreatedAt,
			Key:       rec.Key,
		}
		if rec.Source != nil {
			entry.Source = Source{
				SessionID: rec.Source.SessionID,
				MessageID: rec.Source.MessageID,
				Tool:      rec.Source.Tool,
				Reason:    rec.Source.Reason,
			}
		}
		by[rec.ID] = slot{entry: entry}
		if rec.Key != "" {
			latestForKey[agentKeyIndex(rec.AgentID, rec.Key)] = rec.ID
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("memory: scan %s: %w", s.path, err)
	}

	out := make([]Entry, 0, len(by))
	for id, sl := range by {
		if sl.deleted {
			continue
		}
		if sl.entry.Key != "" {
			if latest, ok := latestForKey[agentKeyIndex(sl.entry.AgentID, sl.entry.Key)]; ok && latest != id {
				continue // superseded by a later Append
			}
		}
		out = append(out, sl.entry)
	}
	return out, nil
}

// findLatestLocked replays the file and returns the latest non-
// tombstoned entry for the given ID, or nil if none.
func (s *JSONLStore) findLatestLocked(id string) (*Entry, error) {
	entries, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].ID == id {
			e := entries[i]
			return &e, nil
		}
	}
	return nil, nil
}

// appendRecordLocked encodes e as a single JSON line and appends it
// to the file. Empty Text + Tags == tombstone (treated as Deleted at
// read time).
func (s *JSONLStore) appendRecordLocked(e Entry) (err error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("memory: mkdir %s: %w", filepath.Dir(s.path), err)
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open %s: %w", s.path, err)
	}
	// Write path: a failed Close can mean the final flush to the OS lost
	// data, so surface it (unless an earlier error already takes priority).
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("memory: close %s: %w", s.path, cerr)
		}
	}()
	rec := jsonlRecord{
		ID:        e.ID,
		Kind:      e.Kind,
		AgentID:   e.AgentID,
		Text:      e.Text,
		Tags:      e.Tags,
		CreatedAt: e.CreatedAt,
		Key:       e.Key,
	}
	if e.Source != (Source{}) {
		rec.Source = &sourceRecord{
			SessionID: e.Source.SessionID,
			MessageID: e.Source.MessageID,
			Tool:      e.Source.Tool,
			Reason:    e.Source.Reason,
		}
	}
	// Empty Text + empty Kind + empty AgentID + no tags + zero
	// CreatedAt + no Key + no Source is a tombstone — encode the
	// Deleted flag for reader clarity.
	if e.Text == "" && e.Kind == "" && e.AgentID == "" && len(e.Tags) == 0 && e.CreatedAt.IsZero() && e.Key == "" && rec.Source == nil {
		rec.Deleted = true
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("memory: marshal: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("memory: write %s: %w", s.path, err)
	}
	return nil
}

// Compact rewrites the file dropping tombstoned IDs and old versions
// of replaced entries. Run offline (when no Append is in flight).
// Safe to call: writes to a temp file and renames atomically.
func (s *JSONLStore) Compact(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readAllLocked()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".jess-memory-*.tmp")
	if err != nil {
		return fmt.Errorf("memory: create tmp: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // best-effort cleanup; gone after a successful rename
	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		rec := jsonlRecord{
			ID: e.ID, Kind: e.Kind, AgentID: e.AgentID,
			Text: e.Text, Tags: e.Tags, CreatedAt: e.CreatedAt,
		}
		line, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("memory: marshal during compact: %w", err)
		}
		line = append(line, '\n')
		if _, err := w.Write(line); err != nil {
			return fmt.Errorf("memory: tmp write: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("memory: tmp flush: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("memory: tmp close: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("memory: rename: %w", err)
	}
	return nil
}

func sameTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// silence unused-import linter when only some helpers are used.
var _ io.Reader = (*os.File)(nil)
