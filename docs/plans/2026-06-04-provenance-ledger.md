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
- `gate/gate.go` — human approval; records DENIED non-safe attempts as `KindAction(denied)`. Does NOT enforce durability (a custom gate would bypass it).
- `internal/core/audit_mw.go` — the unbypassable enforcement point: durable `KindAction` commit-or-deny for non-safe tools before execution, plus `KindToolResult` outcomes.
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
	if a.Compare(b) >= 0 {
		t.Fatalf("ids must increase monotonically: %s !< %s", a, b)
	}
	if len(a.String()) != 26 {
		t.Fatalf("ulid string should be 26 chars, got %q", a.String())
	}
	// 1000 in a tight loop must stay strictly increasing (same-ms stress).
	prev := NewEventID()
	for i := 0; i < 1000; i++ {
		cur := NewEventID()
		if cur.Compare(prev) <= 0 {
			t.Fatalf("non-monotonic at %d: %s <= %s", i, cur, prev)
		}
		prev = cur
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

Add imports `"github.com/oklog/ulid/v2"`, `"crypto/rand"`, `"sync"`, `"time"`. Add to the `Event` struct (keep existing fields): `EventID ulid.ULID`, `RunID string`, `CallID string`. Add the new kinds and ref types:

```go
// EventID is a ULID: time-ordered, globally unique, the ledger primary key.
type EventID = ulid.ULID

// monotonic entropy so ids minted in the same millisecond still sort in
// creation order. ulid.Monotonic is not goroutine-safe, so guard it.
var (
	entMu   sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// NewEventID mints a fresh, monotonically increasing time-ordered id.
func NewEventID() ulid.ULID {
	entMu.Lock()
	defer entMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy)
}

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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLite{db: db}, nil
}

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

// Record is the best-effort write path (observation).
func (s *SQLite) Record(e Event) error { return s.insert(e) }

// CommitAction is the durable write path. It validates that the action is
// self-explaining before persisting; a record that could only say "an action
// happened" is rejected, so the gate/middleware deny rather than store junk.
// A successful INSERT means the row is on disk.
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
	rows, err := s.db.Query(`SELECT payload FROM events WHERE run_id = ? ORDER BY ts, id`, runID)
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

import (
	"testing"

	"github.com/guygrigsby/jess/ledger"
)

func TestRunStateBeginEnd(t *testing.T) {
	rs := &runState{}
	reqID := ledger.NewEventID()
	if err := rs.begin("run-abc", reqID, "restart nginx"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if rs.runID() != "run-abc" {
		t.Fatalf("got %q", rs.runID())
	}
	if id, txt := rs.request(); id != reqID || txt != "restart nginx" {
		t.Fatalf("request() = %s, %q", id, txt)
	}
	// a second begin while active must fail (one-active-run).
	if err := rs.begin("run-xyz", ledger.NewEventID(), "x"); err != ErrRunActive {
		t.Fatalf("concurrent begin should fail with ErrRunActive, got %v", err)
	}
	// a stale end from a different owner must not clear.
	rs.end("not-owner")
	if rs.runID() != "run-abc" {
		t.Fatal("stale end wiped the active run")
	}
	rs.end("run-abc")
	if rs.runID() != "" {
		t.Fatal("owner end should clear runID")
	}
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/core/ -run RunState`
Expected: FAIL (undefined `runState`).

- [ ] **Step 3: Create `internal/core/runstate.go`**

```go
package core

import (
	"errors"
	"sync"

	"github.com/guygrigsby/jess/ledger"
)

// ErrRunActive is returned by begin when a run is already in flight on this
// agent. It enforces the one-active-run invariant instead of assuming it.
var ErrRunActive = errors.New("core: a run is already active on this agent")

// runState carries the current run's id plus the request id/text the gate and
// middleware need to make an action self-explaining. The gate, audit middleware,
// and context manager capture a *runState at build time; jess.Stream begins/ends
// it per run.
type runState struct {
	mu          sync.RWMutex
	id          string
	requestID   ledger.EventID
	requestText string
}

// begin starts a run, failing if one is already active (no silent overwrite of
// a live run's id by a concurrent Stream).
func (r *runState) begin(id string, reqID ledger.EventID, reqText string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.id != "" {
		return ErrRunActive
	}
	r.id, r.requestID, r.requestText = id, reqID, reqText
	return nil
}

// end clears the run only if id matches the owner, so a late end from a previous
// run cannot wipe a newer one.
func (r *runState) end(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.id == id {
		r.id, r.requestText, r.requestID = "", "", ledger.EventID{}
	}
}

func (r *runState) runID() string { r.mu.RLock(); defer r.mu.RUnlock(); return r.id }

// request returns the current run's request id and text (the embedded why).
func (r *runState) request() (ledger.EventID, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.requestID, r.requestText
}
```

- [ ] **Step 4: Wire it in `build.go` and `stream.go`**

In `build.go` `Agent(cfg)`: create `rs := &runState{}` before assembling options; pass `rs` into the audit middleware, the gate construction, and the context manager (each gains a `*runState` parameter — Tasks 8, 9 use it). Store `rs` in the agent registry alongside the sink (extend the existing registry value struct in `build.go` to include `rs`).

In `stream.go` `Stream`: look up the agent's `rs` from the registry. Before `agent.Prompt(input)`:
- mint the request event id `reqID := ledger.NewEventID()` and the run id `runID := ledger.NewEventID().String()`;
- `rs.begin(runID, reqID, input)` — if it returns `ErrRunActive`, abort this Stream (emit an error event and return; a run is already live on this agent);
- best-effort `Record` the `KindRequest` head: `ledger.Event{EventID: reqID, RunID: runID, Kind: ledger.KindRequest, Args: jsonString(input)}`.

The gate/middleware later read `rs.request()` to embed the request id (`reqID`) and text into the action, so the action is self-explaining even if this best-effort `KindRequest` head was lost. After the run completes (existing defer), best-effort `Record` a `KindRunEnd` and call `rs.end(runID)`.

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/core/`
Expected: PASS (runState test + existing core tests still green).

- [ ] **Step 6: Commit**

```bash
git add internal/core/
git commit -m "feat(core): per-agent runState carries RunID to gate/middleware/CM; Stream records request head"
```

---

## Task 8: Gate records denied attempts (approval only; durable enforcement is Task 9)

**Files:**
- Modify: `gate/gate.go` (Policy gains `RunID func() string` and `RequestRef func() ledger.Ref`; on DENY of a non-safe tool, record a best-effort `KindAction` with `Verdict=denied`)
- Test: `gate/gate_test.go`

**Context:** The gate is human approval, and it is swappable (`AllowAll`, `WithToolGate`), so it is NOT where "no record, no action" can be enforced — a custom gate would bypass it. That enforcement moves to the audit middleware in Task 9, which agentcore runs for every tool that is about to execute regardless of which gate allowed it. The gate's one ledger job here is the thing the middleware cannot do: record DENIED non-safe attempts, because agentcore short-circuits denials before the middleware runs (loop.go:840), so without this a rogue attempt would vanish from the chain. The denied record is best-effort (the action did not run, so there is nothing dangerous to fail-close on).

- [ ] **Step 1: Write the failing test**

```go
func TestDeniedNonSafeRecordsKindAction(t *testing.T) {
	rs := &recSink{}
	g := New(Policy{
		Audit:      rs,
		RunID:      func() string { return "run1" },
		RequestRef: func() ledger.Ref { return ledger.Ref{Source: ledger.RefTool, ID: "req1"} },
		// no approver => non-safe denied (fail-closed default)
	})
	d, _ := g(context.Background(), req(dangerTool{}))
	if d == nil || d.Allowed {
		t.Fatal("non-safe with no approver must be denied")
	}
	var sawDeniedAction bool
	for _, e := range rs.events {
		if e.Kind == ledger.KindAction && e.Verdict == ledger.VerdictDenied && e.RunID == "run1" {
			sawDeniedAction = true
		}
	}
	if !sawDeniedAction {
		t.Fatalf("denied non-safe attempt must land in the chain as a KindAction(denied): %+v", rs.events)
	}
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./gate/ -run DeniedNonSafe`
Expected: FAIL (Policy has no RunID/RequestRef; no KindAction recorded on denial).

- [ ] **Step 3: Edit `gate/gate.go`**

Add to `Policy`: `RunID func() string` and `RequestRef func() ledger.Ref` (both nil-safe). In `New`, whenever a NON-safe call is DENIED (the no-approver path and the approver-says-no path), in addition to the existing best-effort gate-decision `rec(...)`, record a best-effort denied action so it reconstructs into the chain:

```go
func (p Policy) recordDeniedAction(gr ac.GateRequest, reason string) {
	if p.Audit == nil {
		return
	}
	runID, ref := "", ledger.Ref{}
	if p.RunID != nil {
		runID = p.RunID()
	}
	if p.RequestRef != nil {
		ref = p.RequestRef()
	}
	_ = p.Audit.Record(ledger.Event{
		EventID: ledger.NewEventID(),
		RunID:   runID,
		CallID:  gr.Call.ID,
		Kind:    ledger.KindAction,
		Tool:    gr.Call.Name,
		Args:    gr.Call.Args,
		Verdict: ledger.VerdictDenied,
		Reason:  reason,
		Refs:    []ledger.Ref{ref},
	})
}
```

Call `p.recordDeniedAction(gr, reason)` on each non-safe denial. The gate does NOT commit allowed actions; the middleware does (Task 9). Safe tools still short-circuit to allow with no KindAction.

- [ ] **Step 4: Run tests**

Run: `go test -race ./gate/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add gate/
git commit -m "feat(gate): record denied non-safe attempts as KindAction(denied) so they stay visible"
```

---

## Task 9: Middleware enforces "no record, no action" (gate-independent) + ContextManager capture

**Files:**
- Modify: `internal/core/audit_mw.go` (durable KindAction commit + deny for non-safe; tag RunID/CallID; KindToolResult)
- Modify: `internal/core/build.go` (compute the non-safe tool-name set; pass durable sink + runState + set into the middleware)
- Modify: `internal/core/context_manager.go` (emit KindRetrieved refs)
- Test: `internal/core/audit_mw_test.go`, `internal/core/build_test.go`

**Context:** The middleware is where "no durable record, no action" is actually enforced, because agentcore runs it for every tool that is about to execute, regardless of which gate (default, `AllowAll`, custom `WithToolGate`) allowed it. For a non-safe tool the middleware commits one durable, self-explaining `KindAction` BEFORE calling `next`, and if the sink is not a `DurableSink` or `CommitAction` fails, it returns an error so the tool never runs. This closes the gate-bypass: a permissive gate can skip approval, but nothing skips the durable record. Safe tools just get a best-effort `KindToolResult`. The non-safe set is computed in `build.go` from the registered tools (a tool is safe only if it implements `gate.SafeTool` and `Safe()` is true).

- [ ] **Step 1: Write the failing tests**

```go
package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/ledger"
)

type durRec struct {
	recSink              // embeds Record (pointer receiver) — use *durRec
	commitErr error
	committed []ledger.Event
}

func (d *durRec) CommitAction(e ledger.Event) error {
	if d.commitErr != nil {
		return d.commitErr
	}
	d.committed = append(d.committed, e)
	return nil
}

func runMW(sink ledger.Sink, nonSafe map[string]bool, rs *runState, name string) (json.RawMessage, error) {
	mw := auditMiddleware(sink, nonSafe, rs, "root")
	call := ac.ToolCall{ID: "c1", Name: name, Args: []byte(`{"path":"/tmp/x"}`)}
	return mw(context.Background(), call, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return []byte(`"ran"`), nil
	})
}

func TestMWNonSafeDeniedWhenSinkNotDurable(t *testing.T) {
	rs := &runState{}
	_ = rs.begin("r1", ledger.NewEventID(), "do it")
	_, err := runMW(&recSink{}, map[string]bool{"delete_file": true}, rs, "delete_file")
	if err == nil {
		t.Fatal("non-safe + non-durable sink must error (tool denied, never ran)")
	}
}

func TestMWNonSafeDeniedWhenCommitFails(t *testing.T) {
	rs := &runState{}
	_ = rs.begin("r1", ledger.NewEventID(), "do it")
	d := &durRec{commitErr: errors.New("disk full")}
	_, err := runMW(d, map[string]bool{"delete_file": true}, rs, "delete_file")
	if err == nil {
		t.Fatal("commit failure must deny the action")
	}
}

func TestMWNonSafeCommitsThenRuns(t *testing.T) {
	rs := &runState{}
	_ = rs.begin("r1", ledger.NewEventID(), "do it")
	d := &durRec{}
	out, err := runMW(d, map[string]bool{"delete_file": true}, rs, "delete_file")
	if err != nil || string(out) != `"ran"` {
		t.Fatalf("durable commit should allow the run: out=%s err=%v", out, err)
	}
	if len(d.committed) != 1 || d.committed[0].Kind != ledger.KindAction || d.committed[0].CallID != "c1" {
		t.Fatalf("expected one committed KindAction: %+v", d.committed)
	}
	if len(d.committed[0].Args) == 0 || len(d.committed[0].Refs) == 0 {
		t.Fatal("committed action must be self-explaining (Args + embedded why)")
	}
}

func TestMWSafeToolBestEffort(t *testing.T) {
	rs := &runState{}
	_ = rs.begin("r1", ledger.NewEventID(), "look")
	// "list" is not in the non-safe set -> safe -> runs even with a plain sink.
	out, err := runMW(&recSink{}, map[string]bool{}, rs, "list")
	if err != nil || string(out) != `"ran"` {
		t.Fatalf("safe tool must run best-effort: out=%s err=%v", out, err)
	}
}
```

- [ ] **Step 2: Run, confirm fail**

Run: `go test ./internal/core/ -run 'MWNonSafe|MWSafe'`
Expected: FAIL (auditMiddleware signature differs; no enforcement).

- [ ] **Step 3: Rewrite `auditMiddleware` in `audit_mw.go`**

```go
func auditMiddleware(sink ledger.Sink, nonSafe map[string]bool, rs *runState, agentPath string) ac.ToolMiddleware {
	return func(ctx context.Context, call ac.ToolCall, next ac.ToolExecuteFunc) (json.RawMessage, error) {
		runID := rs.runID()
		if nonSafe[call.Name] {
			// Enforcement: a non-safe tool may not run without a durable,
			// self-explaining action record. Gate-independent.
			durable, ok := sink.(ledger.DurableSink)
			if !ok {
				return nil, fmt.Errorf("ledger not durable; action %q denied (no record, no action)", call.Name)
			}
			reqID, reqText := rs.request()
			action := ledger.Event{
				EventID: ledger.NewEventID(),
				RunID:   runID,
				CallID:  call.ID,
				Kind:    ledger.KindAction,
				Tool:    call.Name,
				Args:    call.Args, // the target (which file, etc.)
				Verdict: ledger.VerdictAllowed,
				// embedded why: request id + text, so the action is
				// self-explaining even if the KindRequest head was lost.
				Reason: reqText,
				Refs:   []ledger.Ref{{Source: ledger.RefTool, ID: reqID.String(), Hash: ""}},
			}
			if err := durable.CommitAction(action); err != nil {
				return nil, fmt.Errorf("action %q denied: record not durable: %w", call.Name, err)
			}
		}
		res, err := next(ctx, call.Args)
		ev := ledger.Event{EventID: ledger.NewEventID(), RunID: runID, CallID: call.ID,
			Kind: ledger.KindToolResult, Tool: call.Name, Result: res}
		if err != nil {
			ev.Err = err.Error()
		}
		_ = sink.Record(ev) // outcome: best-effort
		return res, err
	}
}
```

(Add `"fmt"` to imports.)

- [ ] **Step 4: Compute the non-safe set and wire in `build.go`**

In `core.Agent(cfg)`, after collecting all tools (`cfg.Tools` + skill tools + subagent tool), build `nonSafe := map[string]bool{}`: for each tool, `safe := false; if st, ok := tool.(gate.SafeTool); ok { safe = st.Safe() }; if !safe { nonSafe[tool.Name()] = true }`. Pass `cfg.Audit, nonSafe, rs, cfg.AgentID` into `auditMiddleware`.

- [ ] **Step 5: Edit `context_manager.go`**

After the ContextManager selects entries to inject, best-effort `Record` one `KindRetrieved` event: `RunID: rs.runID()`, `Refs` = one `ledger.Ref{Source: RefMemory, ID: entry.ID, Hash: sha256hex(entry.Text)}` per injected entry. The CM gains the `*runState` and the `Sink` (passed from build.go). Memory failure still never blocks (best-effort).

- [ ] **Step 6: Run tests**

Run: `go test -race ./internal/core/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/core/
git commit -m "feat(core): middleware enforces durable action record gate-independently; CM emits KindRetrieved"
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

First re-export `Request` so the public approver signature compiles: in `gate_opts.go` add `type Request = gate.Request` next to the existing `type Approver = gate.Approver`.

`defaultLedger()` opens `ledger.OpenSQLite(filepath.Join(cacheDir, "jess", "ledger.db"))`, falling back to `ledger.DiscardSink{}` only if it cannot open — and that fallback then denies non-safe actions (the middleware sees a non-durable sink), which is the safe failure.

Move the default-gate construction into `core.Agent` (it already holds `rs`), since the gate now needs the run lookup. `jess.New` passes `cfg.Approver` (from `st.approver`) and `cfg.Audit` into `cfg`; `core.Agent` assembles `gate.New(gate.Policy{ Approver: cfg.Approver, Audit: cfg.Audit, AgentPath: cfg.AgentID, RunID: rs.runID, RequestRef: func() ledger.Ref { id, _ := rs.request(); return ledger.Ref{Source: ledger.RefTool, ID: id.String()} } })` unless the caller supplied a custom gate via `WithToolGate`/`AllowAll`. The durable sink does NOT go to the gate; it reaches the middleware (Task 9), so a custom gate cannot bypass the durable-action enforcement.

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
- Review-driven corrections folded in (codex pass on the plan): (1) enforcement of "no record, no action" lives in the MIDDLEWARE, not the gate, so `AllowAll`/`WithToolGate` cannot bypass it (T9); (2) the gate records denied non-safe attempts so they stay visible, since agentcore short-circuits denials before the middleware (T8); (3) `NewEventID` is monotonic and `Chain` orders by `ts, id` (T2/T5); (4) `CommitAction` validates a self-explaining action and uses plain `INSERT`, not `INSERT OR REPLACE` (T5); (5) `runState.begin` fails on an active run and `end` is owner-checked (T7); (6) the action embeds the request id+text from `runState` so it is self-explaining even if the head is lost (T7/T9); (7) `jess.Request` is re-exported and the test fakes use pointer receivers (T10/T9).
- Green-between-tasks: T1-T6 are additive/green per task (ledger package builds in isolation). T7-T10 interlock through `runState`/gate/sink; each ends with `go test ./internal/core/ ./gate/ ./.` green, and T10 restores full-module green. T11 is the end-to-end proof.
