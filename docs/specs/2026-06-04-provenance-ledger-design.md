# Provenance ledger (2026-06-04)

## Scope

Evolve `audit/` into a provenance ledger: every agent run is a causal chain, stored in a keyed embedded store, reconstructable as the triad (available info / action / evidence). Build the accountability half (read a chain backward to answer "why did it do this"). Defer the forward half (retrieval as memory), evidence citation, pattern-mining, and the ambient agent.

This builds on the just-merged `audit/` + `gate/` work. The `Sink` interface stays; this adds the chain model, a read-side `Reader`, and a SQLite-backed store.

## Why now, and why this much

The near-term consumer is the reactive daemon (you type "restart nginx"). By the autonomy-dial rule, that consumer needs almost no provenance: evidence is just the request. So the *full* ledger (citation, pattern-mining) would be building ahead of its consumer. What is justified now: the chain model and a fast keyed store, because the chain structure with references is the substrate every later piece reads, and "fast, no scanning" is a stated requirement, not a guess. RAG operates on embedded blobs; the chain ledger is structured references, which is what non-RAG structured retrieval queries later. Building it does not lock us into RAG.

## Data model

`Event` (in the ledger package) gains:
- `EventID ulid.ULID` — primary key. Time-ordered, globally unique. Refs point at it.
- `RunID string` — correlates all events of one `jess.Stream` run into a chain.
- existing fields stay: `Time, AgentPath, Kind, Tool, Args, Verdict, Reason, Result, Err, DurationMS`.

New `Kind`s: `KindRequest` (the chain head, the run input) and `KindRetrieved` (memory recall, carried by ref).

`Ref{ ID string; Hash string }` addresses an available item. `ID` namespaces its source: an audit `EventID` for a tool output, or a `memory.Entry.ID` for recalled memory. `Hash` is the item's content hash captured at decision time, so a later read can detect that the referenced thing changed or was deleted (memory mutates via supersession and Forget). Refs, not copies: the content lives once in its store.

`Chain` (a read-side view, assembled by `Reader.Chain(RunID)`):
```
Chain{
    Request   Event          // the KindRequest head
    Available []Ref           // safe-tool (read) outputs + recalled memory, by ref
    Actions   []Action
}
Action{
    Call     Event            // the non-safe tool request
    Gate     Event            // its gate decision (allowed / denied + reason)
    Result   Event            // the tool result (present even on denial: the denial is the result)
    Evidence []Ref            // the available items that justified it
}
```

Available vs action is decided by the `SafeTool` marker at capture: a safe (read, bounded) tool's output is *available*; a non-safe tool call is an *action*. Evidence is populated trivially now: for a direct command, `Evidence = [ref to the Request]`. The citation mechanism for inferred actions is deferred.

## Storage, behind interfaces

- `Sink` (exists): `Record(Event) error` — the write path.
- `Reader` (new): `Get(id ulid.ULID) (Event, error)` and `Chain(runID string) (Chain, error)`. Both index-backed, never a scan.

A SQLite store (`modernc.org/sqlite`, pure-Go, no CGO, BSD-licensed) implements both. Minimal schema:
```
events(
  id      TEXT PRIMARY KEY,   -- ULID
  run_id  TEXT NOT NULL,
  ts      INTEGER NOT NULL,
  kind    TEXT NOT NULL,
  tool    TEXT,
  payload BLOB                 -- the rest of the Event as JSON
);
CREATE INDEX events_run ON events(run_id);
```
ULID PK gives O(log n) ref resolution and time ordering for free; the `run_id` index makes `Chain` an indexed lookup. No FTS or entity tables yet; they land with the deferred structured retrieval.

`JSONLSink` survives as an optional, greppable, write-only mirror. `DiscardSink` is the explicit off switch. The default ledger (what `jess.New` wires when you do not pass `WithLedger`/`WithAudit`) becomes the SQLite store at the user cache dir, replacing the JSONL default.

## Capture wiring

- `jess.Stream` mints a `RunID` and a request `EventID`, puts the `RunID` in `ctx`, records a `KindRequest` event (the input) and a `KindRunEnd` at the close.
- The audit middleware (`internal/core`) reads `RunID` from `ctx`, tags `KindToolRequest`/`KindToolResult`; safe-tool results are available, non-safe are actions.
- The gate reads `RunID` from `ctx`, tags `KindGateDecision`.
- The memory `ContextManager` (`internal/core`) records a `KindRetrieved` event listing the *refs* (memory entry ids + content hashes) of what it injected this turn, tagged with `RunID`. This is the one genuinely new capture.

`RunID` travels by `ctx` because agentcore already threads `ctx` through tools and the context manager, so no signature changes ripple out.

## Invariants

- Ledger writes never block the run. A SQLite write failure is swallowed and the run continues (same rule memory already follows). Reader errors are returned to the caller; queries are off the run path.
- A gate-denied action still lands in the chain with its denial. The rogue-attempt-visible guarantee carries from the flat log into the chain view.

## Package and dependencies

- Rename `audit/` to `ledger/` (it is a ledger now, not just an audit log). The types move with it; callers update the import.
- Add `modernc.org/sqlite` (pure-Go SQLite, BSD-3) and a ULID library (e.g. `github.com/oklog/ulid/v2`, Apache-2.0). Both pure-Go (no CGO, preserves cross-compile) and non-GPL.

## Testing

- SQLite `Sink`+`Reader` round-trip: `Record` several events across two runs, then `Get` one by `EventID` and assert it matches, and `Chain(runID)` assembles only that run's events into the triad.
- Integration through the real path: an agent that recalls a seeded memory, calls one safe read tool, then a non-safe (gated) action. Assert the reconstructed chain has the request as head, the recall + read in `Available` (by ref, hashes present), the action with its gate decision and result, and `Evidence = [request ref]`. A second case where the gate denies the action: assert the action still appears in the chain with a denied gate decision.
- Hash-drift: capture a ref to a memory entry, supersede that entry, and assert the chain's ref hash no longer matches the live entry (drift is detectable).

## Deferred (designed, not built)

Evidence citation (inferred-action provenance), forward/structured/entity retrieval (the non-RAG single-user memory), pattern-mining over the ledger, ambient triggers, and the surfacing threshold. The minimal SQLite schema grows (FTS / entity tables) when those arrive; the `Reader` interface absorbs the change without touching callers.
