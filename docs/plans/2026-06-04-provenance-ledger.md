# Provenance Ledger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Evolve the `audit/` package into a provenance `ledger/`: every run is a causal chain stored in a keyed SQLite store, reconstructable as the triad (available / action / evidence), with actions fail-closed on a durable, self-explaining record.

**Architecture:** Rename `audit/` to `ledger/` and enrich `Event` (ULID id, RunID, CallID, typed Refs). Split the write path into best-effort `Sink.Record` (observation) and durable `DurableSink.CommitAction` (actions). A SQLite store (`modernc.org/sqlite`, pure-Go) implements `Sink` + `CommitAction` + `Reader`. RunID reaches the gate/middleware/ContextManager through a per-agent `runState` set by `jess.Stream` per run (not ctx — `agentcore.Prompt` takes none). The gate is the enforcement point: it durably commits one `KindAction` (target + verdict + embedded why) before a non-safe tool runs, or denies.

**Tech Stack:** Go 1.26, `github.com/voocel/agentcore` v1.6.9, `modernc.org/sqlite` (pure-Go, no CGO, BSD-3), `github.com/oklog/ulid/v2` (Apache-2.0).

**Spec:** `docs/specs/2026-06-04-provenance-ledger-design.md`

**Key invariant being built:** no durable, self-explaining record => no action. Auditing off (`DiscardSink`/`JSONLSink`, which are not `DurableSink`) denies non-safe actions; the overseer can observe blind but cannot act unrecorded.

**File structure (end state):**
- `ledger/event.go` — `Event`, `EventID`, `RunID`, `CallID`, `Kind` constants, `Verdict`, `Ref`, `RefSource`, `NewEventID()`.
- `ledger/sink.go` — `Sink`, `DurableSink`, `DiscardSink`, errors.
- `ledger/jsonl.go` — `JSONLSink` (Sink only, the greppable mirror).
- `ledger/chain.go` — `Chain`, `Action`, `AssembleChain([]Event) Chain` (pure), `Reader`, `Resolver`.
- `ledger/sqlite.go` — `SQLite` store implementing `DurableSink` + `Reader`.
- `ledger/*_test.go` — per-file tests.
- `internal/core/runstate.go` — per-agent `runState`, the agent->runState registry, `CurrentRunID(agent)`.
- `internal/core/build.go`, `audit_mw.go`, `context_manager.go`, `stream.go` — wiring changes.
- `gate/gate.go` — gate commits `KindAction` via `DurableSink`.
- `memory/resolver.go` — `EntryGetter` on the stores (drift resolution), memory stays agentcore-free.
- `jess.go`, `audit_opts.go` (→ `ledger_opts.go`) — `WithLedger`, default SQLite ledger, wiring.

---

## Task 1: Rename audit → ledger

**Files:**
- Rename: `audit/` → `ledger/` (git mv each file), `package audit` → `package ledger`
- Modify: every importer (update the import path and the `audit.` selector to `ledger.`)
- Rename: `audit_opts.go` → `ledger_opts.go`; `jess.WithAudit` → `jess.WithLedger`; `defaultAudit` → `defaultLedger`

**Context:** Pure mechanical rename, ends green. The importers are: `audit_opts.go`, `examples/gated/main.go`, `examples/quickstart/main.go`, `gate_integration_test.go`, `gate/gate*.go`, `internal/core/audit_mw.go`, `internal/core/build*.go`, `internal/core/stream.go`, `jess_test.go`, `subagent_opts_test.go`, `subagent/pool.go`, `subagent/spec.go`.

- [ ] **Step 1: Move and rewrite package clause**

```bash
git mv audit ledger
git mv ledger/audit.go ledger/event.go
git mv ledger/audit_test.go ledger/event_test.go
grep -rl '^package audit' ledger | xargs sed -i '' 's/^package audit/package ledger/'
git mv audit_opts.go ledger_opts.go
```

- [ ] **Step 2: Rewrite imports and selectors across the module**

```bash
grep -rl '"github.com/guygrigsby/jess/audit"' --include='*.go' . | xargs sed -i '' 's#github.com/guygrigsby/jess/audit#github.com/guygrigsby/jess/ledger#g'
grep -rl 'audit\.' --include='*.go' . | xargs sed -i '' 's/\baudit\./ledger./g'
```
Then in `ledger_opts.go` and `jess.go`/examples rename the option: `WithAudit` → `WithLedger`, `defaultAudit` → `defaultLedger`, and any `c.Audit`/`cfg.Audit`/`Policy{Audit:` field referencing the sink. (The `core.Config.Audit` field and `gate.Policy.Audit` field keep their names for now; only the public option renames. Do a `grep -rn 'WithAudit\|defaultAudit'` and update every hit.)

- [ ] **Step 3: Build and test**

Run: `go build ./... && go vet ./... && go test -race ./...`
Expected: all PASS (pure rename).

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor: rename audit package to ledger; WithAudit -> WithLedger"
```

---

## Task 2: Enrich Event (ULID id, RunID, CallID, typed Refs, new kinds)

**Files:**
- Modify: `ledger/event.go`
- Test: `ledger/event_test.go`
- Modify: `go.mod` (add ulid)

- [ ] **Step 1: Add the ulid dependency**

Run: `go get github.com/oklog/ulid/v2@latest`
Expected: `go.mod` gains the require.

- [ ] **Step 2: Write the failing test (append to `ledger/event_test.go`)**

```go
func TestNewEventIDMonotonicAndParsable(t *testing.T) {
	a := NewEventID()
	b := NewEventID()
	if a == b {
		t.Fatal("two ids should differ")
	}
	if a.String() == "" || len(a.String()) != 26 {
		t.Fatalf("ulid string should be 26 chars, got %q", a.String())
	}
}

func TestRefSourceValues(t *testing.T) {
	if RefTool == RefMemory {
		t.Fatal("ref sources must be distinct")
	}
	r := Ref{Source: RefMemory, ID: "m1", Hash: "abc"}
	if r.Source != RefMemory || r.ID != "m1" {
		t.Fatalf("ref fields: %+v", r)
	}
}
```

- [ ] **Step 3: Run it, confirm fail**

Run: `go test ./ledger/ -run 'NewEventID|RefSource'`
Expected: FAIL (undefined `NewEventID`, `Ref`, `RefTool`, `RefMemory`).

- [ ] **Step 4: Edit `ledger/event.go`**

Add imports `"github.com/oklog/ulid/v2"` and `"crypto/rand"`. Add to the `Event` struct (keep existing fields): `EventID ulid.ULID`, `RunID string`, `CallID string`. Add the new kinds and ref types:

```go
// EventID is a ULID: time-ordered, globally unique, the ledger primary key.
type EventID = ulid.ULID

// NewEventID mints a fresh time-ordered id.
func NewEventID() ulid.ULID { return ulid.MustNew(ulid.Now(), rand.Reader) }

const (
	KindRequest    Kind = "request"     // chain head: the run input
	KindRetrieved  Kind = "retrieved"   // memory recall, by ref
	KindAction     Kind = "action"      // atomic intent + gate verdict, committed at the gate
	// KindToolResult, KindRunEnd, KindAbort already exist from the audit package.
)

// RefSource names what a Ref points at, so resolution never guesses from id shape.
type RefSource string

const (
	RefTool   RefSource = "tool"   // ID is a ledger EventID
	RefMemory RefSource = "memory" // ID is a memory.Entry.ID
)

// Ref addresses an available item by id plus a content hash captured at decision
// time, so drift or deletion is detectable. Refs, not copies.
type Ref struct {
	Source RefSource `json:"source"`
	ID     string    `json:"id"`
	Hash   string    `json:"hash"`
}
```

Add `EventID ulid.ULID `json:"event_id"``, `RunID string `json:"run_id,omitempty"``, `CallID string `json:"call_id,omitempty"``, and `Refs []Ref `json:"refs,omitempty"`` (used by KindRetrieved and embedded-evidence on KindAction) to the `Event` struct.

- [ ] **Step 5: Run tests**

Run: `go test -race ./ledger/`
Expected: PASS (existing event tests + new ones).

- [ ] **Step 6: Commit**

```bash
git add ledger/ go.mod go.sum
git commit -m "feat(ledger): Event gains ULID id, RunID, CallID, typed Refs, chain kinds"
```

---

## Task 3: Sink / DurableSink / DiscardSink

**Files:**
- Create: `ledger/sink.go` (move `Sink`, `DiscardSink` here from event.go; add `DurableSink`)
- Modify: `ledger/jsonl.go` (unchanged behavior, just confirm it does NOT implement CommitAction)
- Test: `ledger/sink_test.go`

**Context:** `Sink.Record` is best-effort (observation). `DurableSink` adds `CommitAction`, the path the gate uses for actions; only a store that truly persists implements it. `DiscardSink` and `JSONLSink` are plain `Sink`s, so the gate (Task 8) denies non-safe actions when the ledger is not a `DurableSink`. This is the clean Go form of the spec's "no record, no action" (type assertion, not an always-erroring method).

- [ ] **Step 1: Write the failing test**

```go
package ledger

import "testing"

func TestDiscardSinkIsNotDurable(t *testing.T) {
	var s Sink = DiscardSink{}
	if _, ok := s.(DurableSink); ok {
		t.Fatal("DiscardSink must NOT be a DurableSink (auditing off denies actions)")
	}
}

func TestJSONLSinkIsNotDurable(t *testing.T) {
	var s Sink = &JSONLSink{}
	if _, ok := s.(DurableSink); ok {
		t.Fatal("JSONLSink is a mirror, not a durable ledger; must not be DurableSink")
	}
}
```

- [ ] **Step 2: Run it, confirm fail**

Run: `go test ./ledger/ -run Durable`
Expected: FAIL (undefined `DurableSink`).

- [ ] **Step 3: Create `ledger/sink.go`**

Move `Sink` and `DiscardSink` out of `event.go` into `sink.go` (cut/paste), and add:

```go
package ledger

// DurableSink is a Sink that can durably commit an action record. CommitAction
// returns nil only when the event is persisted (e.g. fsync'd to SQLite). The
// gate requires a DurableSink before allowing a non-safe tool: no durable
// record, no action. Plain Sinks (DiscardSink, JSONLSink) are observation-only.
type DurableSink interface {
	Sink
	CommitAction(Event) error
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race ./ledger/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ledger/
git commit -m "feat(ledger): DurableSink (CommitAction) split from best-effort Sink"
```

---

## Task 4: Chain assembly (pure)

**Files:**
- Create: `ledger/chain.go` (`Chain`, `Action`, `AssembleChain`, `Reader`, `Resolver` interfaces)
- Test: `ledger/chain_test.go`

**Context:** `AssembleChain` turns a run's events into the triad, pairing `KindAction` intent with its `KindToolResult` by `CallID`. Pure function, no store, so it tests in isolation. `Reader` and `Resolver` are interfaces implemented later (SQLite in Task 5, memory in Task 6).

- [ ] **Step 1: Write the failing test**

```go
package ledger

import "testing"

func ev(kind Kind, callID string) Event {
	return Event{EventID: NewEventID(), RunID: "run1", CallID: callID, Kind: kind}
}

func TestAssembleChainPairsByCallID(t *testing.T) {
	req := ev(KindRequest, "")
	req.Args = []byte(`"clean tmp"`)
	a1 := ev(KindAction, "call-1")
	a1.Tool = "delete_file"
	a1.Args = []byte(`{"path":"/tmp/x"}`)
	r1 := ev(KindToolResult, "call-1")
	a2 := ev(KindAction, "call-2")
	a2.Tool = "delete_file"
	r2 := ev(KindToolResult, "call-2")

	c := AssembleChain([]Event{req, a1, r1, a2, r2})
	if c.Request.Kind != KindRequest {
		t.Fatal("missing request head")
	}
	if len(c.Actions) != 2 {
		t.Fatalf("want 2 actions, got %d", len(c.Actions))
	}
	if c.Actions[0].Intent.CallID != c.Actions[0].Result.CallID {
		t.Fatal("intent/result not paired by CallID")
	}
	if c.Actions[0].Intent.CallID == c.Actions[1].Intent.CallID {
		t.Fatal("two distinct calls collapsed into one")
	}
}

func TestAssembleChainCollectsAvailable(t *testing.T) {
	retr := ev(KindRetrieved, "")
	retr.Refs = []Ref{{Source: RefMemory, ID: "m1", Hash: "h1"}}
	read := ev(KindToolResult, "read-1") // a safe read result, no matching action => available
	c := AssembleChain([]Event{ev(KindRequest, ""), retr, read})
	if len(c.Available) == 0 {
		t.Fatal("retrieved refs should appear in Available")
	}
}
```

- [ ] **Step 2: Run it, confirm fail**

Run: `go test ./ledger/ -run AssembleChain`
Expected: FAIL (undefined).

- [ ] **Step 3: Create `ledger/chain.go`**

```go
package ledger

// Chain is one run reconstructed as the triad. Read backward it answers "why".
type Chain struct {
	Request   Event
	Available []Ref
	Actions   []Action
}

// Action is one effectful invocation: its committed intent (target + verdict +
// embedded why), its result, and the evidence it rested on. Intent and Result
// pair by CallID.
type Action struct {
	Intent   Event
	Result   Event
	Evidence []Ref
}

// Reader is the read side of a ledger. Both methods are index-backed, never a scan.
type Reader interface {
	Get(id EventID) (Event, error)
	Chain(runID string) (Chain, error)
}

// Resolver resolves the current content hash of a referenced item, for drift
// detection. Implemented by a memory-backed adapter (Task 6). ok is false when
// the id is unknown (deleted) or the store cannot resolve it.
type Resolver interface {
	CurrentHash(source RefSource, id string) (hash string, ok bool)
}

// AssembleChain reconstructs the triad from a single run's events. Intent
// (KindAction) and Result (KindToolResult with the same CallID) pair into one
// Action; KindToolResult events with no matching action are safe-tool reads and
// land in Available; KindRetrieved refs also land in Available.
func AssembleChain(events []Event) Chain {
	var c Chain
	results := map[string]Event{} // callID -> result
	for _, e := range events {
		if e.Kind == KindToolResult && e.CallID != "" {
			results[e.CallID] = e
		}
	}
	actionCalls := map[string]bool{}
	for _, e := range events {
		switch e.Kind {
		case KindRequest:
			c.Request = e
		case KindRetrieved:
			c.Available = append(c.Available, e.Refs...)
		case KindAction:
			actionCalls[e.CallID] = true
			c.Actions = append(c.Actions, Action{
				Intent:   e,
				Result:   results[e.CallID],
				Evidence: e.Refs,
			})
		}
	}
	// Tool results with no matching action are safe reads -> available, by ref.
	for callID, r := range results {
		if !actionCalls[callID] {
			c.Available = append(c.Available, Ref{Source: RefTool, ID: r.EventID.String(), Hash: hashOf(r.Result)})
		}
	}
	return c
}
```

Add a small `hashOf([]byte) string` helper (sha256 hex of the bytes; empty for nil) in `chain.go` or a new `ledger/hash.go`:

```go
import (
	"crypto/sha256"
	"encoding/hex"
)

func hashOf(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race ./ledger/ -run AssembleChain`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ledger/
git commit -m "feat(ledger): pure AssembleChain (triad reconstruction, CallID pairing)"
```

---

## Task 5: SQLite store (DurableSink + Reader)

**Files:**
- Create: `ledger/sqlite.go`
- Test: `ledger/sqlite_test.go`
- Modify: `go.mod` (add modernc.org/sqlite)

- [ ] **Step 1: Add the dependency**

Run: `go get modernc.org/sqlite@latest`
Expected: `go.mod` gains the require (pure-Go, no CGO).

- [ ] **Step 2: Write the failing test**

```go
package ledger

import (
	"path/filepath"
	"testing"
)

func TestSQLiteCommitGetChain(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()

	var _ DurableSink = db // must satisfy DurableSink

	req := Event{EventID: NewEventID(), RunID: "r1", Kind: KindRequest, Args: []byte(`"do it"`)}
	if err := db.Record(req); err != nil {
		t.Fatalf("Record req: %v", err)
	}
	act := Event{EventID: NewEventID(), RunID: "r1", CallID: "c1", Kind: KindAction, Tool: "delete_file", Args: []byte(`{"path":"/tmp/x"}`)}
	if err := db.CommitAction(act); err != nil {
		t.Fatalf("CommitAction: %v", err)
	}
	res := Event{EventID: NewEventID(), RunID: "r1", CallID: "c1", Kind: KindToolResult, Result: []byte(`"ok"`)}
	if err := db.Record(res); err != nil {
		t.Fatalf("Record res: %v", err)
	}

	got, err := db.Get(act.EventID)
	if err != nil || got.Tool != "delete_file" {
		t.Fatalf("Get: %+v err=%v", got, err)
	}
	chain, err := db.Chain("r1")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if chain.Request.Kind != KindRequest || len(chain.Actions) != 1 || chain.Actions[0].Result.CallID != "c1" {
		t.Fatalf("chain wrong: %+v", chain)
	}
}
```

- [ ] **Step 3: Run it, confirm fail**

Run: `go test ./ledger/ -run SQLite`
Expected: FAIL (undefined `OpenSQLite`).

- [ ] **Step 4: Create `ledger/sqlite.go`**

```go
package ledger

import (
	"database/sql"
	"encoding/json"

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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLite{db: db}, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) insert(e Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO events(id,run_id,call_id,ts,kind,tool,payload) VALUES(?,?,?,?,?,?,?)`,
		e.EventID.String(), e.RunID, e.CallID, e.Time.UnixNano(), string(e.Kind), e.Tool, payload)
	return err
}

// Record is the best-effort write path.
func (s *SQLite) Record(e Event) error { return s.insert(e) }

// CommitAction is the durable write path. A successful INSERT means the row is
// persisted; the gate treats success as "safe to run the action".
func (s *SQLite) CommitAction(e Event) error { return s.insert(e) }

// Get resolves one event by id.
func (s *SQLite) Get(id EventID) (Event, error) {
	row := s.db.QueryRow(`SELECT payload FROM events WHERE id = ?`, id.String())
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		return Event{}, err
	}
	var e Event
	return e, json.Unmarshal(payload, &e)
}

// Chain reads one run's events (ordered by id == time) and assembles the triad.
func (s *SQLite) Chain(runID string) (Chain, error) {
	rows, err := s.db.Query(`SELECT payload FROM events WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return Chain{}, err
	}
	defer rows.Close()
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
```

(`Event.Time` must be set by producers; if a producer leaves it zero, set `e.Time = time.Now()` in `insert` before marshaling so `ts` is meaningful. Add that guard.)

- [ ] **Step 5: Run tests**

Run: `go test -race ./ledger/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add ledger/ go.mod go.sum
git commit -m "feat(ledger): pure-Go SQLite store (DurableSink + Reader, ULID/run/call indexes)"
```

---

## Task 6: Memory resolver (drift detection)

**Files:**
- Create: `memory/resolver.go` (`EntryGetter`, and a `Get` method on the stores if absent)
- Create: `ledger/resolver_mem.go` is NOT allowed (would couple ledger to memory). Instead the adapter lives in `internal/core` or root jess. Put it in `internal/core/resolver.go`.
- Test: `memory/resolver_test.go`

**Context:** `memory/` stays agentcore-free and ledger-free. It exposes an `EntryGetter` so something can fetch an entry by id. The ledger's `Resolver` is implemented by a tiny adapter in `internal/core` that wraps an `EntryGetter` and hashes the entry text. Drift = stored ref hash != current hash.

- [ ] **Step 1: Write the failing test**

```go
package memory

import (
	"context"
	"testing"
)

func TestInMemoryStoreGet(t *testing.T) {
	st := NewInMemoryStore()
	e, _ := st.Append(context.Background(), Entry{AgentID: "a", Text: "hello"})
	got, ok := st.Get(e.ID)
	if !ok || got.Text != "hello" {
		t.Fatalf("Get(%q) = %+v, %v", e.ID, got, ok)
	}
	if _, ok := st.Get("nope"); ok {
		t.Fatal("unknown id should return ok=false")
	}
}
```

- [ ] **Step 2: Run it, confirm fail**

Run: `go test ./memory/ -run InMemoryStoreGet`
Expected: FAIL (no `Get` method).

- [ ] **Step 3: Add `EntryGetter` and implement `Get`**

In `memory/resolver.go`:

```go
package memory

// EntryGetter fetches a stored entry by id. Stores that can resolve an id
// implement it; the provenance ledger uses it to verify a memory ref's hash
// (drift / deletion detection). Stores that cannot resolve simply do not
// implement it, and refs to them are recorded but flagged unverifiable.
type EntryGetter interface {
	Get(id string) (Entry, bool)
}
```

Implement `Get(id string) (Entry, bool)` on `InMemoryStore` (look up its id->entry map; the store already indexes by id internally — read `memory/store_inmemory.go` to find the map and add the method). Do the same for `JSONLStore` and `ChromemStore` if they keep an id index; if a store cannot cheaply resolve by id, do NOT add the method (it just won't satisfy `EntryGetter`).

- [ ] **Step 4: Add the ledger Resolver adapter in `internal/core/resolver.go`**

```go
package core

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/guygrigsby/jess/ledger"
	"github.com/guygrigsby/jess/memory"
)

// memResolver adapts a memory.EntryGetter to ledger.Resolver for drift checks.
type memResolver struct{ g memory.EntryGetter }

func (m memResolver) CurrentHash(src ledger.RefSource, id string) (string, bool) {
	if src != ledger.RefMemory || m.g == nil {
		return "", false
	}
	e, ok := m.g.Get(id)
	if !ok {
		return "", false
	}
	sum := sha256.Sum256([]byte(e.Text))
	return hex.EncodeToString(sum[:]), true
}
```

- [ ] **Step 5: Run tests**

Run: `go test -race ./memory/ ./internal/core/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add memory/ internal/core/resolver.go
git commit -m "feat(memory,core): EntryGetter + ledger Resolver adapter for ref drift detection"
```

---

## Task 7: Per-agent runState + RunID plumbing

**Files:**
- Create: `internal/core/runstate.go`
- Modify: `internal/core/build.go` (create a runState per agent, register it, capture it in gate/middleware/CM)
- Modify: `internal/core/stream.go` (set runState.RunID per run, record KindRequest, clear after)
- Test: `internal/core/runstate_test.go`

**Context:** The gate/middleware/CM closures are built in `core.Agent(cfg)` before the agent exists and never receive the agent or a usable ctx (`Prompt` takes none). So they capture a shared `*runState` at build time; `jess.Stream` sets `runState.RunID` before each `Prompt` and clears it after. The one-active-run-per-agent invariant (already documented on Stream) makes this race-free. Reuse the existing agent registry pattern: extend the registry value to hold the `*runState`.

- [ ] **Step 1: Write the failing test**

```go
package core

import "testing"

func TestRunStateRoundTrip(t *testing.T) {
	rs := &runState{}
	rs.set("run-abc")
	if rs.runID() != "run-abc" {
		t.Fatalf("got %q", rs.runID())
	}
	rs.clear()
	if rs.runID() != "" {
		t.Fatal("clear should empty runID")
	}
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/core/ -run RunState`
Expected: FAIL (undefined `runState`).

- [ ] **Step 3: Create `internal/core/runstate.go`**

```go
package core

import "sync"

// runState carries the current RunID for one agent. The gate, audit middleware,
// and context manager capture a *runState at build time; jess.Stream sets it per
// run. Safe because at most one run is active per agent (documented on Stream).
type runState struct {
	mu sync.RWMutex
	id string
}

func (r *runState) set(id string) { r.mu.Lock(); r.id = id; r.mu.Unlock() }
func (r *runState) clear()        { r.mu.Lock(); r.id = ""; r.mu.Unlock() }
func (r *runState) runID() string { r.mu.RLock(); defer r.mu.RUnlock(); return r.id }
```

- [ ] **Step 4: Wire it in `build.go` and `stream.go`**

In `build.go` `Agent(cfg)`: create `rs := &runState{}` before assembling options; pass `rs` into the audit middleware, the gate construction, and the context manager (each gains a `*runState` parameter — Tasks 8, 9 use it). Store `rs` in the agent registry alongside the sink (extend the existing registry value struct in `build.go` to include `rs`).

In `stream.go` `Stream`: look up the agent's `rs` from the registry; before `agent.Prompt(input)`, mint `runID := ledger.NewEventID().String()`, call `rs.set(runID)`, and `Record` a `KindRequest` event (`EventID: NewEventID, RunID: runID, Kind: KindRequest, Args: json input`) to the sink (best-effort). After the run completes (in the existing defer), `Record` a `KindRunEnd` and call `rs.clear()`.

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/core/`
Expected: PASS (runState test + existing core tests still green).

- [ ] **Step 6: Commit**

```bash
git add internal/core/
git commit -m "feat(core): per-agent runState carries RunID to gate/middleware/CM; Stream records request head"
```

---

## Task 8: Gate commits the action (fail-closed enforcement)

**Files:**
- Modify: `gate/gate.go` (Policy gains a `DurableSink` + `*runState`-equivalent; on allow for non-safe, commit KindAction or deny)
- Modify: `internal/core/build.go` (build the gate with the durable sink + runState)
- Test: `gate/gate_test.go`

**Context:** This is the enforcement point. `gate.Policy` gains a `Ledger ledger.DurableSink` (nil if the configured sink is not durable) and a `RunID func() string` (reads the runState). On a non-safe call that is allowed (safe tools skip this), the gate assembles one `KindAction` event — CallID (`gr.Call.ID`), Tool, Args (the target), the verdict reason, RunID, and `Refs` embedding the request/why — and calls `Ledger.CommitAction`. If `Ledger` is nil (not durable) or `CommitAction` errors, it returns DENY. No durable record, no action.

- [ ] **Step 1: Write the failing test**

```go
type denyDurable struct{ recSink }

func (denyDurable) CommitAction(ledger.Event) error { return errors.New("boom") }

func TestNonSafeDeniedWhenLedgerNotDurable(t *testing.T) {
	rs := &recSink{} // plain Sink, not DurableSink
	g := New(Policy{Audit: rs, Approver: func(context.Context, Request) (bool, string) { return true, "ok" }})
	d, _ := g(context.Background(), req(dangerTool{}))
	if d == nil || d.Allowed {
		t.Fatal("approver allowed but ledger is not durable => must deny (no record, no action)")
	}
}

func TestNonSafeDeniedWhenCommitFails(t *testing.T) {
	dd := denyDurable{}
	g := New(Policy{Audit: dd, Ledger: dd, RunID: func() string { return "r1" },
		Approver: func(context.Context, Request) (bool, string) { return true, "ok" }})
	d, _ := g(context.Background(), req(dangerTool{}))
	if d == nil || d.Allowed {
		t.Fatal("commit failed => must deny")
	}
}
```

(Define `RunID func() string` and `Ledger ledger.DurableSink` on `Policy`. `recSink` from the existing gate test is a plain Sink.)

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./gate/ -run 'NonSafeDenied'`
Expected: FAIL (Policy has no Ledger/RunID; approver path currently allows).

- [ ] **Step 3: Edit `gate/gate.go`**

Add to `Policy`: `Ledger ledger.DurableSink` and `RunID func() string`. In `New`, in the non-safe branch AFTER the approver says allow (and for the no-approver fail-closed path leave as-is), before returning allow:

```go
// Durable, self-explaining record before the action runs. No durable
// record, no action.
if p.Ledger == nil {
	rec(ledger.VerdictDenied, "no durable ledger; non-safe action denied")
	return &ac.GateDecision{Allowed: false, Reason: "denied: auditing not durable"}, nil
}
runID := ""
if p.RunID != nil {
	runID = p.RunID()
}
action := ledger.Event{
	EventID: ledger.NewEventID(),
	RunID:   runID,
	CallID:  gr.Call.ID,
	Kind:    ledger.KindAction,
	Tool:    gr.Call.Name,
	Args:    gr.Call.Args, // the target: which file, etc.
	Verdict: ledger.VerdictAllowed,
	Reason:  reason,
	// embedded why: the request/evidence travels with the action so it is
	// self-explaining even if the KindRequest head is lost.
	Refs: []ledger.Ref{{Source: ledger.RefTool, ID: runID, Hash: ""}},
}
if err := p.Ledger.CommitAction(action); err != nil {
	rec(ledger.VerdictDenied, "action record not durable: "+err.Error())
	return &ac.GateDecision{Allowed: false, Reason: "denied: could not record action"}, nil
}
```

Then return the existing allow decision. (Safe tools still short-circuit to allow at the top of `New`, no KindAction.) Keep the existing audit `rec(...)` for the gate decision as before.

- [ ] **Step 4: Build the gate with the durable sink in `build.go`**

In `core.Agent(cfg)`, when constructing the gate (currently `gate.New(gate.Policy{...})` happens in root `jess.go:44`, but move/extend it so the durable sink + runState are passed): set `Ledger:` to `cfg.Audit` type-asserted to `ledger.DurableSink` (nil if not durable), and `RunID: rs.runID`. Update `jess.go` where the default gate is built to thread these through (Task 10 finalizes the root wiring; here just make the types compile and the gate test pass).

- [ ] **Step 5: Run tests**

Run: `go test -race ./gate/ ./internal/core/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add gate/ internal/core/
git commit -m "feat(gate): commit durable KindAction before non-safe execution; deny if not durable"
```

---

## Task 9: Middleware + ContextManager capture

**Files:**
- Modify: `internal/core/audit_mw.go` (tag RunID + CallID; safe vs non-safe result)
- Modify: `internal/core/context_manager.go` (emit KindRetrieved refs)
- Test: `internal/core/build_test.go` (extend)

**Context:** The middleware now reads RunID from the captured `*runState` and tags every `KindToolResult` with `CallID` (from `call.ID`) so it pairs with the gate's `KindAction`. The ContextManager emits a `KindRetrieved` event listing refs of injected memory.

- [ ] **Step 1: Write the failing test (extend build_test.go)**

```go
func TestRetrievedAndResultTaggedWithRun(t *testing.T) {
	rs := &recSink{}
	// build an agent with memory + a tool, run it, then inspect rs.events:
	// assert at least one KindToolResult carries a non-empty RunID and CallID,
	// and (if memory injected) a KindRetrieved with Refs.
	// (Use the existing build_test harness pattern; drive via core.Stream.)
}
```

(Write it concretely against the existing `build_test.go` harness: seed an `InMemoryStore`, register a safe tool the echo model calls, run via `core.Stream`, then scan `rs.events` for a `KindToolResult` with `RunID != ""` and `CallID != ""`.)

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/core/ -run RetrievedAndResult`
Expected: FAIL (events untagged / no KindRetrieved).

- [ ] **Step 3: Edit `audit_mw.go`**

The middleware closure gains a `*runState` param (captured in build.go). Change the recorded events to set `EventID: ledger.NewEventID()`, `RunID: rs.runID()`, `CallID: call.ID`, and use `KindToolResult` for the outcome (drop the separate `KindToolRequest` for non-safe tools, since the gate's `KindAction` is the intent record; safe tools still record a single `KindToolResult`).

- [ ] **Step 4: Edit `context_manager.go`**

After the ContextManager selects entries to inject, record one `KindRetrieved` event: `Refs` = one `Ref{Source: RefMemory, ID: entry.ID, Hash: sha256(entry.Text)}` per injected entry, `RunID` from the captured `*runState`, via the best-effort `Record`. The CM gains the `*runState` and the `Sink` (passed from build.go). Memory failure still never blocks (best-effort).

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/core/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/core/
git commit -m "feat(core): middleware tags RunID/CallID; ContextManager emits KindRetrieved refs"
```

---

## Task 10: Root jess wiring (default SQLite ledger + gate)

**Files:**
- Modify: `jess.go`, `ledger_opts.go`
- Test: `jess_test.go`

**Context:** Finalize the public wiring. `defaultLedger()` opens the SQLite store at the user cache dir. `jess.New` passes the sink as `cfg.Audit`, and the gate is built with `Ledger:` (the sink as `DurableSink` when it is one) and `RunID:`. `WithLedger(sink)` overrides; passing a non-durable sink (DiscardSink/JSONLSink) means non-safe actions are denied (the documented behavior).

- [ ] **Step 1: Write the failing test**

```go
func TestDefaultLedgerAllowsAuditedAction(t *testing.T) {
	// with the default SQLite ledger (durable), an approved non-safe tool runs.
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir) // route defaultLedger somewhere temporary
	rt := &restartTool{} // from gate_integration_test.go (non-safe)
	model := callOnceModel("restart_service")
	agent := jess.New(jess.WithModel(model), jess.WithTools(rt),
		jess.WithApprover(func(context.Context, jess.Request) (bool, string) { return true, "ok" }))
	ch, wait := jess.Stream(context.Background(), agent, "restart nginx")
	for range ch {
	}
	_ = wait()
	if !rt.ran {
		t.Fatal("durable ledger + approval => action should run")
	}
}

func TestDiscardLedgerDeniesAction(t *testing.T) {
	rt := &restartTool{}
	model := callOnceModel("restart_service")
	agent := jess.New(jess.WithModel(model), jess.WithTools(rt),
		jess.WithLedger(ledger.DiscardSink{}),
		jess.WithApprover(func(context.Context, jess.Request) (bool, string) { return true, "ok" }))
	ch, wait := jess.Stream(context.Background(), agent, "restart nginx")
	for range ch {
	}
	_ = wait()
	if rt.ran {
		t.Fatal("non-durable ledger => non-safe action must be denied even with approval")
	}
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./ -run 'DefaultLedger|DiscardLedger'`
Expected: FAIL until wiring is complete.

- [ ] **Step 3: Edit `ledger_opts.go` and `jess.go`**

`defaultLedger()` opens `ledger.OpenSQLite(filepath.Join(os.UserCacheDir()/"jess"/"ledger.db"))`, falling back to `ledger.DiscardSink{}` only if it cannot open (and that fallback then denies actions, which is the safe failure). In `jess.New`, build the gate with `gate.Policy{Approver: st.approver, Audit: cfg.Audit, AgentPath: cfg.AgentID, Ledger: asDurable(cfg.Audit), RunID: <agent runState lookup>}`. Provide `asDurable(s ledger.Sink) ledger.DurableSink` returning the type-assertion or nil. The RunID func resolves through the agent's runState (exposed by `core` via a helper, e.g. `core.RunIDFunc(agent)` returning `func() string`, since the gate is built before the agent — instead pass the `*runState` the build created; thread it from `core.Agent`). Simplest: move the default-gate construction into `core.Agent` (it already has `rs`), and have `jess.New` pass the approver + durable sink into `cfg`, letting `core.Agent` assemble the gate with `rs.runID`.

- [ ] **Step 4: Run tests**

Run: `go test -race ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(jess): default SQLite ledger; gate wired durable + RunID; DiscardSink denies actions"
```

---

## Task 11: Integration tests + drift + final verification

**Files:**
- Create: `ledger_integration_test.go` (root package)

- [ ] **Step 1: Write the integration tests**

Write, in `package jess_test`, against the real path (build an agent with a seeded `InMemoryStore`, a safe read tool, and a non-safe gated action with an approver that allows):
- `TestChainReconstructsTriad`: after a run, open the same SQLite ledger and `Chain(runID)`; assert `Request` head present, `Available` contains the recalled memory ref and the safe read, one `Action` with target Args and its result, `Evidence` non-empty.
- `TestWhySurvivesLostHead`: commit a `KindAction` whose `KindRequest` was never recorded; assert the action's embedded `Refs`/Args still make the action self-explaining (target + why recoverable from the action event alone).
- `TestCallIDPairingTwoCalls`: a model that calls the same non-safe tool twice in one run; assert `Chain` yields two distinct `Action`s paired correctly by `CallID`.
- `TestMemoryRefDriftDetected`: capture a `KindRetrieved` ref to a memory entry, supersede the entry (Append with same Key), resolve via the `memResolver`, assert the stored hash != current hash.

(Get the runID for assertions by having the test wrap the sink, or expose the last runID via a test hook; simplest is to scan the ledger for the single run's id.)

- [ ] **Step 2: Run them, fix until green**

Run: `go test -race ./ -run 'Chain|WhySurvives|CallIDPairing|Drift'`
Expected: PASS.

- [ ] **Step 3: Full gate**

Run: `go vet ./... && go test -race ./... && make lint`
Expected: all PASS, `0 issues`.

- [ ] **Step 4: Real-path smoke**

Run: `go run ./examples/quickstart` (still prints its success line) and `go build ./examples/gated`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "test(ledger): chain reconstruction, why-survives-lost-head, CallID pairing, drift"
```

---

## Task 12: Docs

**Files:**
- Modify: `CLAUDE.md` (ledger package + the three-control + fail-closed-action note), `README.md`, `CHANGELOG.md`
- Create: `docs/adr/0003-provenance-ledger.md`

- [ ] **Step 1:** Update `CLAUDE.md` package layout: `audit/` becomes `ledger/` (Event/Chain/Sink/DurableSink/SQLite/Reader); note the fail-closed-action rule (no durable record, no action) and that `DiscardSink`/`JSONLSink` are observation-only so they deny non-safe actions.
- [ ] **Step 2:** Write `docs/adr/0003-provenance-ledger.md`: the chain/triad model, SQLite store, the fail-closed-for-actions / best-effort-for-observation split, deferred forward-retrieval. Plain prose, no footers.
- [ ] **Step 3:** Update `README.md` and `CHANGELOG.md`.
- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "docs: ledger package, ADR 0003 (provenance ledger)"
```

---

## Self-Review notes

- Spec coverage: rename (T1), Event+Refs+CallID (T2), Sink/DurableSink (T3), Chain/AssembleChain (T4), SQLite Sink+CommitAction+Reader (T5), Resolver/EntryGetter drift (T6), RunID-via-runState not ctx (T7), gate fail-closed CommitAction + DiscardSink-denies (T8), middleware/CM capture + KindRetrieved (T9), default SQLite + root wiring (T10), integration incl self-explaining/why-survives/CallID/drift (T11), docs/ADR (T12). All spec sections mapped.
- The single trickiest mechanism is the per-agent `runState` (T7) replacing the spec's ctx wiring; it is called out because the gate/middleware/CM are built before the agent exists. Verify the one-active-run invariant holds (Stream sets/clears around one Prompt) so the shared runState is race-free; `-race` on the integration tests guards it.
- Agentcore field names to confirm against the cache before writing: `GateRequest.Call.ID` and `ToolCall.ID` (T8), `ToolMiddleware`'s `call.ID` (T9). The plan assumes `ToolCall.ID` exists (tool.go:84, confirmed in the spec review).
- Green-between-tasks: T1-T6 are additive/green per task (ledger package builds in isolation). T7-T10 interlock through `runState`/gate/sink; each ends with `go test ./internal/core/ ./gate/ ./.` green, and T10 restores full-module green. T11 is the end-to-end proof.
