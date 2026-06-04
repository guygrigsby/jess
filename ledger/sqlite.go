package ledger

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

// SQLite is the durable provenance ledger: DurableSink + Reader over a local
// SQLite file (modernc, pure-Go, no CGO).
type SQLite struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS events(
  id      TEXT PRIMARY KEY,
  run_id  TEXT NOT NULL,
  call_id TEXT,
  ts      INTEGER NOT NULL,
  kind    TEXT NOT NULL,
  tool    TEXT,
  payload BLOB
);
CREATE INDEX IF NOT EXISTS events_run  ON events(run_id);
CREATE INDEX IF NOT EXISTS events_call ON events(call_id);`

// OpenSQLite opens (creating) the ledger at path.
func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Single writer: concurrent callers queue in Go, never hit SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLite{db: db}, nil
}

// Close releases the underlying database handle.
func (s *SQLite) Close() error { return s.db.Close() }

// zeroID is the all-zero ULID, rejected as a primary key.
var zeroID EventID

func (s *SQLite) insert(e Event) error {
	if e.EventID == zeroID {
		return errors.New("ledger: zero EventID")
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	// Plain INSERT: a duplicate id is an error, never a silent overwrite.
	_, err = s.db.Exec(
		`INSERT INTO events(id,run_id,call_id,ts,kind,tool,payload) VALUES(?,?,?,?,?,?,?)`,
		e.EventID.String(), e.RunID, e.CallID, e.Time.UnixNano(), string(e.Kind), e.Tool, payload)
	return err
}

// Record is the best-effort write path (observation). Implements Sink.
func (s *SQLite) Record(e Event) error { return s.insert(e) }

// CommitAction is the durable write path. It validates that the action is
// self-explaining before persisting; a record that could only say "an action
// happened" is rejected, so the caller denies rather than store junk.
// Implements DurableSink.
func (s *SQLite) CommitAction(e Event) error {
	if e.Kind != KindAction {
		return errors.New("ledger: CommitAction requires KindAction")
	}
	if e.RunID == "" || e.CallID == "" || e.Tool == "" || len(e.Args) == 0 {
		return errors.New("ledger: action record not self-explaining (need RunID, CallID, Tool, Args)")
	}
	if len(e.Refs) == 0 {
		return errors.New("ledger: action record has no embedded why (Refs empty)")
	}
	return s.insert(e)
}

// Get resolves one event by id. Implements Reader.
func (s *SQLite) Get(id EventID) (Event, error) {
	row := s.db.QueryRow(`SELECT payload FROM events WHERE id = ?`, id.String())
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		return Event{}, err
	}
	var e Event
	return e, json.Unmarshal(payload, &e)
}

// Chain reads one run's events ordered by time then id and assembles the triad.
// Implements Reader.
func (s *SQLite) Chain(runID string) (Chain, error) {
	rows, err := s.db.Query(`SELECT payload FROM events WHERE run_id = ? ORDER BY ts, id`, runID)
	if err != nil {
		return Chain{}, err
	}
	defer func() { _ = rows.Close() }()
	var events []Event
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return Chain{}, err
		}
		var e Event
		if err := json.Unmarshal(payload, &e); err != nil {
			return Chain{}, err
		}
		events = append(events, e)
	}
	return AssembleChain(events), rows.Err()
}
