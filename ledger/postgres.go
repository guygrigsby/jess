package ledger

import (
	"database/sql"
	"encoding/json"

	// Postgres driver, database/sql interface only.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Postgres is the durable provenance ledger backed by PostgreSQL: DurableSink +
// Reader over a shared server, for deployments where the ledger must outlive
// any single host or process. Same write contract as SQLite: plain INSERT,
// duplicate ids error, actions must be self-explaining before they persist.
type Postgres struct{ db *sql.DB }

const pgSchema = `
CREATE TABLE IF NOT EXISTS events(
  id      TEXT PRIMARY KEY,
  run_id  TEXT NOT NULL,
  call_id TEXT,
  ts      BIGINT NOT NULL,
  kind    TEXT NOT NULL,
  tool    TEXT,
  payload JSONB
);
CREATE INDEX IF NOT EXISTS events_run  ON events(run_id);
CREATE INDEX IF NOT EXISTS events_call ON events(call_id);`

// OpenPostgres connects to dsn, verifies the connection and ensures the schema.
func OpenPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	p, err := NewPostgres(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return p, nil
}

// NewPostgres wraps a caller-owned pool (callers sharing the database with
// e.g. a job queue keep one pool and one transaction domain) and ensures the
// schema. Close on the returned Postgres closes the pool.
func NewPostgres(db *sql.DB) (*Postgres, error) {
	if err := db.Ping(); err != nil {
		return nil, err
	}
	if _, err := db.Exec(pgSchema); err != nil {
		return nil, err
	}
	return &Postgres{db: db}, nil
}

// Close releases the underlying pool.
func (p *Postgres) Close() error { return p.db.Close() }

func (p *Postgres) insert(e Event) error {
	e, payload, err := prepareInsert(e)
	if err != nil {
		return err
	}
	// Plain INSERT: a duplicate id is a unique violation, never a silent overwrite.
	_, err = p.db.Exec(
		`INSERT INTO events(id,run_id,call_id,ts,kind,tool,payload) VALUES($1,$2,$3,$4,$5,$6,$7)`,
		e.EventID.String(), e.RunID, e.CallID, e.Time.UnixNano(), string(e.Kind), e.Tool, string(payload))
	return err
}

// Record is the best-effort write path (observation). Implements Sink.
func (p *Postgres) Record(e Event) error { return p.insert(e) }

// CommitAction is the durable write path. It validates that the action is
// self-explaining before persisting; a record that could only say "an action
// happened" is rejected, so the caller denies rather than store junk.
// Implements DurableSink.
func (p *Postgres) CommitAction(e Event) error {
	if err := validateAction(e); err != nil {
		return err
	}
	return p.insert(e)
}

// Get resolves one event by id. Implements Reader.
func (p *Postgres) Get(id EventID) (Event, error) {
	row := p.db.QueryRow(`SELECT payload FROM events WHERE id = $1`, id.String())
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		return Event{}, err
	}
	var e Event
	return e, json.Unmarshal(payload, &e)
}

// Chain reads one run's events ordered by time then id and assembles the triad.
// Implements Reader.
func (p *Postgres) Chain(runID string) (Chain, error) {
	rows, err := p.db.Query(`SELECT payload FROM events WHERE run_id = $1 ORDER BY ts, id`, runID)
	if err != nil {
		return Chain{}, err
	}
	events, err := scanEvents(rows)
	if err != nil {
		return Chain{}, err
	}
	return AssembleChain(events), nil
}
