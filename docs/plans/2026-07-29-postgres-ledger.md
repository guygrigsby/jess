# Postgres Ledger Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Postgres-backed `DurableSink` + `Reader` in `jess/ledger`, feature-equivalent to the SQLite backend, for deployments where the ledger must outlive any single host (driven by scanduit ADR 0003: Postgres from the start).

**Architecture:** Mirror `ledger/sqlite.go` on `database/sql` with the pgx stdlib driver, so the two backends differ only in driver import, schema dialect and placeholder style. Shared write-path validation is extracted first so the CommitAction contract lives in exactly one place. Tests run against a real Postgres in a throwaway container, gated on an env var so `go test ./...` stays green without one.

**Tech Stack:** Go 1.26, `github.com/jackc/pgx/v5/stdlib` (MIT, passes the repo license policy: MIT/Apache-2.0/MPL-2.0/BSD only), `postgres:17-alpine` for tests.

## Global Constraints

- Repo license policy: MIT/Apache-2.0/MPL-2.0/BSD deps only (`make license-audit` is a blocking CI gate).
- All tests run with `-race` (`make test` = `go test -race ./...`).
- Go style: `for range n` for plain counts, never three-clause count loops.
- Commit prefixes follow repo convention: `ledger:`, `ci:`, `docs:`. No AI attribution, no Co-Authored-By trailers.
- Postgres tests skip (not fail) when `JESS_TEST_POSTGRES_DSN` is unset.
- Ledger invariants (from `sink.go` and `sqlite.go`, must hold identically in Postgres): plain INSERT (duplicate id errors, never overwrites), zero EventID rejected, `CommitAction` rejects non-`KindAction` and non-self-explaining events BEFORE persisting anything.

---

### Task 1: Extract shared write-path helpers

The SQLite backend inlines CommitAction validation and insert preparation. The Postgres backend needs both; duplicating them forks the contract. Pure refactor, existing tests stay green.

**Files:**
- Modify: `ledger/event.go` (add `prepareInsert`, `validateAction`, move `zeroID`)
- Modify: `ledger/sqlite.go:51-90` (rewire `insert` and `CommitAction`, delete moved code)

**Interfaces:**
- Consumes: existing `Event`, `Kind`, `KindAction` from `ledger/event.go`.
- Produces (Task 2 relies on these exact signatures):
  - `func prepareInsert(e Event) (Event, []byte, error)` — rejects zero `EventID`, defaults `e.Time` to `time.Now()`, returns the (possibly updated) event plus its JSON payload.
  - `func validateAction(e Event) error` — the CommitAction self-explaining contract.
  - package var `zeroID EventID` now lives in `event.go`.

- [ ] **Step 1: Confirm the current tests pass (baseline)**

Run: `cd ~/projects/jess && go test -race ./ledger/...`
Expected: PASS

- [ ] **Step 2: Add helpers to `ledger/event.go`**

Append to `ledger/event.go` (add `"errors"` to its imports; `zeroID` moves here from `sqlite.go`):

```go
// zeroID is the all-zero ULID, rejected as a primary key.
var zeroID EventID

// prepareInsert validates and normalizes an Event for the write path shared by
// every durable backend: zero ids are rejected, a zero Time defaults to now,
// and the JSON payload is what the backend persists.
func prepareInsert(e Event) (Event, []byte, error) {
	if e.EventID == zeroID {
		return e, nil, errors.New("ledger: zero EventID")
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	payload, err := json.Marshal(e)
	return e, payload, err
}

// validateAction enforces the CommitAction contract: a stored action must be
// self-explaining on its own row, or the caller denies rather than store junk.
func validateAction(e Event) error {
	if e.Kind != KindAction {
		return errors.New("ledger: CommitAction requires KindAction")
	}
	if e.RunID == "" || e.CallID == "" || e.Tool == "" || len(e.Args) == 0 {
		return errors.New("ledger: action record not self-explaining (need RunID, CallID, Tool, Args)")
	}
	if len(e.Refs) == 0 {
		return errors.New("ledger: action record has no embedded why (Refs empty)")
	}
	return nil
}
```

- [ ] **Step 3: Rewire `ledger/sqlite.go`**

Delete the `var zeroID EventID` line and replace `insert` and `CommitAction` bodies:

```go
func (s *SQLite) insert(e Event) error {
	e, payload, err := prepareInsert(e)
	if err != nil {
		return err
	}
	// Plain INSERT: a duplicate id is an error, never a silent overwrite.
	_, err = s.db.Exec(
		`INSERT INTO events(id,run_id,call_id,ts,kind,tool,payload) VALUES(?,?,?,?,?,?,?)`,
		e.EventID.String(), e.RunID, e.CallID, e.Time.UnixNano(), string(e.Kind), e.Tool, payload)
	return err
}
```

```go
func (s *SQLite) CommitAction(e Event) error {
	if err := validateAction(e); err != nil {
		return err
	}
	return s.insert(e)
}
```

Keep the existing doc comments on both methods. Remove now-unused imports from `sqlite.go` (`errors`, `time`, `encoding/json` may all drop; let the compiler tell you).

- [ ] **Step 4: Run tests, verify still green**

Run: `go test -race ./ledger/...`
Expected: PASS (same tests as baseline; this task adds none)

- [ ] **Step 5: Commit**

```bash
git add ledger/event.go ledger/sqlite.go
git commit -m "ledger: extract shared write-path validation for multi-backend use"
```

---

### Task 2: Postgres backend

**Files:**
- Create: `ledger/postgres.go`
- Create: `ledger/postgres_test.go`
- Modify: `go.mod` / `go.sum` (add `github.com/jackc/pgx/v5`)
- Modify: `ledger/sqlite.go:105-124` (extract `scanEvents` row loop so `Chain` isn't duplicated twice)

**Interfaces:**
- Consumes: `prepareInsert`, `validateAction`, `zeroID` from Task 1; `Event`, `EventID`, `Chain`, `AssembleChain`, `DurableSink`, `Reader` from the existing package.
- Produces:
  - `type Postgres struct{ db *sql.DB }` satisfying `DurableSink` and `Reader`, with `Close() error`.
  - `func OpenPostgres(dsn string) (*Postgres, error)`
  - `func NewPostgres(db *sql.DB) (*Postgres, error)` — wraps a caller-owned pool (callers sharing the database with e.g. a job queue).
  - `func scanEvents(rows *sql.Rows) ([]Event, error)` shared by both backends' `Chain`.

- [ ] **Step 1: Start a throwaway Postgres for the TDD loop**

```bash
docker run -d --rm --name jess-pg-dev -e POSTGRES_PASSWORD=jess -p 127.0.0.1:5439:5432 postgres:17-alpine
until docker exec jess-pg-dev pg_isready -U postgres -q; do sleep 0.5; done
export JESS_TEST_POSTGRES_DSN='postgres://postgres:jess@127.0.0.1:5439/postgres?sslmode=disable'
```

(podman works identically if docker isn't on the machine; same flags.)

- [ ] **Step 2: Write the failing tests**

Create `ledger/postgres_test.go`. Tests mirror the SQLite suite; isolation comes from unique run ids (shared table, no truncation needed). Note the concurrent test has NO single-writer cap, that's the point of Postgres.

```go
package ledger

import (
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
)

// openTestPostgres skips unless JESS_TEST_POSTGRES_DSN is set (make
// test-postgres provides it; plain `go test ./...` stays green without one).
func openTestPostgres(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("JESS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("JESS_TEST_POSTGRES_DSN not set; run `make test-postgres`")
	}
	pg, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

func TestPostgresCommitGetChain(t *testing.T) {
	pg := openTestPostgres(t)

	var _ DurableSink = pg // must satisfy DurableSink
	var _ Reader = pg      // must satisfy Reader

	runID := "r-" + NewEventID().String()
	req := Event{EventID: NewEventID(), RunID: runID, Kind: KindRequest, Args: []byte(`"do it"`)}
	if err := pg.Record(req); err != nil {
		t.Fatalf("Record req: %v", err)
	}
	act := Event{EventID: NewEventID(), RunID: runID, CallID: "c1", Kind: KindAction, Tool: "delete_file",
		Args: []byte(`{"path":"/tmp/x"}`), Refs: []Ref{{Source: RefTool, ID: "req"}}}
	if err := pg.CommitAction(act); err != nil {
		t.Fatalf("CommitAction: %v", err)
	}
	res := Event{EventID: NewEventID(), RunID: runID, CallID: "c1", Kind: KindToolResult, Result: []byte(`"ok"`)}
	if err := pg.Record(res); err != nil {
		t.Fatalf("Record res: %v", err)
	}

	got, err := pg.Get(act.EventID)
	if err != nil || got.Tool != "delete_file" {
		t.Fatalf("Get: %+v err=%v", got, err)
	}
	chain, err := pg.Chain(runID)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if chain.Request.Kind != KindRequest || len(chain.Actions) != 1 || chain.Actions[0].Result.CallID != "c1" {
		t.Fatalf("chain wrong: %+v", chain)
	}
}

func TestPostgresCommitActionRejectsNonSelfExplaining(t *testing.T) {
	pg := openTestPostgres(t)
	runID := "r-" + NewEventID().String()
	// missing Tool/Args/Refs => not self-explaining => must error, must NOT store.
	bad := Event{EventID: NewEventID(), RunID: runID, CallID: "c1", Kind: KindAction}
	if err := pg.CommitAction(bad); err == nil {
		t.Fatal("CommitAction must reject a non-self-explaining action")
	}
	if _, err := pg.Get(bad.EventID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rejected event must not be stored; Get returned %v", err)
	}
	// wrong kind
	if err := pg.CommitAction(Event{EventID: NewEventID(), RunID: runID, Kind: KindToolResult}); err == nil {
		t.Fatal("CommitAction must require KindAction")
	}
}

func TestPostgresInsertRejectsDuplicateAndZeroID(t *testing.T) {
	pg := openTestPostgres(t)
	runID := "r-" + NewEventID().String()
	if err := pg.Record(Event{RunID: runID, Kind: KindRunEnd}); err == nil {
		t.Fatal("zero EventID must be rejected")
	}
	e := Event{EventID: NewEventID(), RunID: runID, Kind: KindRunEnd}
	if err := pg.Record(e); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := pg.Record(e); err == nil {
		t.Fatal("duplicate EventID must error (plain INSERT, unique violation)")
	}
}

func TestPostgresDurabilityAcrossReconnect(t *testing.T) {
	pg := openTestPostgres(t)
	act := Event{EventID: NewEventID(), RunID: "r-" + NewEventID().String(), CallID: "c1", Kind: KindAction,
		Tool: "delete_file", Args: []byte(`{"path":"/tmp/x"}`), Refs: []Ref{{Source: RefTool, ID: "req"}}}
	if err := pg.CommitAction(act); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := pg.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	pg2, err := OpenPostgres(os.Getenv("JESS_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer func() { _ = pg2.Close() }()
	got, err := pg2.Get(act.EventID)
	if err != nil || got.Tool != "delete_file" {
		t.Fatalf("event not durable across reconnect: got=%+v err=%v", got, err)
	}
}

func TestPostgresConcurrentCommit(t *testing.T) {
	pg := openTestPostgres(t)
	runID := "r-" + NewEventID().String()
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := Event{EventID: NewEventID(), RunID: runID, CallID: NewEventID().String(), Kind: KindAction,
				Tool: "t", Args: []byte(`{}`), Refs: []Ref{{Source: RefTool, ID: "req"}}}
			if err := pg.CommitAction(e); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent CommitAction failed: %v", err)
	}
	chain, err := pg.Chain(runID)
	if err != nil || len(chain.Actions) != 50 {
		t.Fatalf("want 50 actions, got %d err=%v", len(chain.Actions), err)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test -race ./ledger/ -run TestPostgres -v`
Expected: FAIL to compile — `Postgres`, `OpenPostgres` undefined.

- [ ] **Step 4: Add the dependency**

```bash
go get github.com/jackc/pgx/v5@latest
```

- [ ] **Step 5: Implement `ledger/postgres.go`**

```go
package ledger

import (
	"database/sql"

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
```

Add `"encoding/json"` to the imports (used by `Get`).

- [ ] **Step 6: Extract `scanEvents` and rewire SQLite's `Chain`**

In `ledger/sqlite.go`, replace the body of `Chain` and add the shared helper (helper lives in `sqlite.go`; it's `database/sql` plumbing both backends use):

```go
// Chain reads one run's events ordered by time then id and assembles the triad.
// Implements Reader.
func (s *SQLite) Chain(runID string) (Chain, error) {
	rows, err := s.db.Query(`SELECT payload FROM events WHERE run_id = ? ORDER BY ts, id`, runID)
	if err != nil {
		return Chain{}, err
	}
	events, err := scanEvents(rows)
	if err != nil {
		return Chain{}, err
	}
	return AssembleChain(events), nil
}

// scanEvents drains rows of single-column JSON payloads into Events. Shared by
// every database/sql backend's Chain.
func scanEvents(rows *sql.Rows) ([]Event, error) {
	defer func() { _ = rows.Close() }()
	var events []Event
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var e Event
		if err := json.Unmarshal(payload, &e); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
```

- [ ] **Step 7: Run the full ledger suite, verify green**

Run: `go test -race ./ledger/... -v`
Expected: PASS, including all five `TestPostgres*` tests (not skipped; DSN is exported from Step 1) and the untouched SQLite suite.

- [ ] **Step 8: Commit**

```bash
git add ledger/postgres.go ledger/postgres_test.go ledger/sqlite.go go.mod go.sum
git commit -m "ledger: Postgres DurableSink/Reader backend"
```

---

### Task 3: Makefile target and CI wiring

**Files:**
- Modify: `Makefile` (add `test-postgres` target)
- Modify: `.github/workflows/test.yml` (Postgres service on the `test` job, DSN env on the race-test step)

**Interfaces:**
- Consumes: `JESS_TEST_POSTGRES_DSN` gate from Task 2's `openTestPostgres`.
- Produces: `make test-postgres` (local, container-managed) and CI coverage on every push/PR.

- [ ] **Step 1: Add the Makefile target**

Append to `Makefile` (it already defines `$(GO)`; add `test-postgres` to the existing `.PHONY` line):

```make
PG_TEST_IMG  ?= postgres:17-alpine
PG_TEST_PORT ?= 5439
PG_TEST_DSN   = postgres://postgres:jess@127.0.0.1:$(PG_TEST_PORT)/postgres?sslmode=disable

# Spin a throwaway Postgres, run the ledger suite against it, tear it down.
# Container is removed even when tests fail; exit status is the test status.
test-postgres:
	docker run -d --rm --name jess-pg-test -e POSTGRES_PASSWORD=jess \
		-p 127.0.0.1:$(PG_TEST_PORT):5432 $(PG_TEST_IMG)
	@until docker exec jess-pg-test pg_isready -U postgres -q; do sleep 0.5; done
	@JESS_TEST_POSTGRES_DSN="$(PG_TEST_DSN)" $(GO) test -race ./ledger/...; \
		status=$$?; docker stop jess-pg-test >/dev/null; exit $$status
```

- [ ] **Step 2: Verify the target works end to end**

Run: `docker stop jess-pg-dev; make test-postgres`
Expected: container starts, ledger suite PASSes (Postgres tests not skipped), container is gone afterward (`docker ps` shows no jess-pg-test).

- [ ] **Step 3: Wire CI**

In `.github/workflows/test.yml`, add a `services` block to the `test` job (after `timeout-minutes: 10`, before `steps`):

```yaml
    services:
      postgres:
        image: postgres:17-alpine
        env:
          POSTGRES_PASSWORD: jess
        ports:
          - 5439:5432
        options: >-
          --health-cmd pg_isready
          --health-interval 5s
          --health-timeout 5s
          --health-retries 10
```

And on the `go test (race)` step, add the env so the Postgres tests stop skipping in CI:

```yaml
      - name: go test (race)
        # E2E test in memory/embed/gomlx downloads ~90MB; gated on
        # JESS_EMBEDDER_E2E env var so CI doesn't pay it. Set in a
        # separate workflow when we want pre-release verification.
        env:
          JESS_TEST_POSTGRES_DSN: postgres://postgres:jess@127.0.0.1:5439/postgres?sslmode=disable
        run: go test -race -timeout 5m ./...
```

- [ ] **Step 4: Commit**

```bash
git add Makefile .github/workflows/test.yml
git commit -m "ci: run ledger postgres tests against a service container"
```

---

### Task 4: Docs and preflight

**Files:**
- Modify: `README.md` (ledger bullet)
- Modify: `CHANGELOG.md` (unreleased entry)

**Interfaces:**
- Consumes: `OpenPostgres` / `NewPostgres` names from Task 2 (docs must match exactly).
- Produces: nothing downstream; this closes the feature.

- [ ] **Step 1: Update the README ledger bullet**

In `README.md`, the `jess/ledger` bullet ends with "`SQLite` is the default durable backend (pure-Go, no CGO). `DiscardSink{}` turns recording off explicitly; it is never off silently." Extend it (keep the existing sentences):

```markdown
`SQLite` is the default durable backend (pure-Go, no CGO); `OpenPostgres`/`NewPostgres` provide the same DurableSink + Reader on PostgreSQL for shared or replicated deployments. `DiscardSink{}` turns recording off explicitly; it is never off silently.
```

- [ ] **Step 2: Add the CHANGELOG entry**

Match the file's existing format (check its top entry style first) and add under the unreleased/next section:

```markdown
- ledger: Postgres backend (`OpenPostgres`, `NewPostgres`), same DurableSink/Reader contract as SQLite; `make test-postgres` runs the suite against a throwaway container.
```

- [ ] **Step 3: Full local preflight (matches CI)**

Run: `make vet && make test && make lint && make license-audit`
Expected: all PASS. license-audit must accept pgx (MIT) and its deps; if it flags anything, stop and report rather than allowlisting.

- [ ] **Step 4: Commit**

```bash
git add README.md CHANGELOG.md
git commit -m "docs: ledger postgres backend"
```

---

## Self-review notes

- Spec coverage: ADR 0003 needs a Postgres `DurableSink` jess can hand to scanduit; `NewPostgres(db *sql.DB)` is the pool-sharing seam river/scanduit will use. Covered by Task 2.
- The SQLite suite guards the Task 1 refactor; no new tests needed there.
- Types cross-checked: `prepareInsert`/`validateAction`/`scanEvents` signatures match between Task 1, Task 2 code and both call sites.
- Not in scope, deliberate: context.Context on the ledger interfaces (upstream API change, separate conversation with a real design question about `Sink` compatibility), retention/pruning, migrations tooling (schema is CREATE IF NOT EXISTS, same posture as SQLite).
