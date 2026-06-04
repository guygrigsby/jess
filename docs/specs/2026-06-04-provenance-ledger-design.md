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

Auditability is a hard requirement, not best-effort, so the ledger is fail-closed for actions. This is the inverse of memory's "never block the LLM call" rule, and the distinction is deliberate: memory recall is an enhancement (losing it degrades), an action record is accountability (losing it means an unaudited action happened, the exact failure this design exists to prevent).

- **Actions block on their record.** Before any effectful (non-safe) tool executes, its intent and gate decision must be durably committed to the ledger. If that commit fails, the action is denied, identical to a gate refusal. No record, no action. Enforcement point: the gate already runs before execution, so on "allow" for a non-safe tool it commits the record first and, if the commit fails, returns "deny" instead.
- **Context is best-effort.** Memory recall and the available/retrieved records (recalled-memory refs, safe-tool read outputs, run-level prompt/run-end) may fail without blocking the run. A failure there loses chain completeness, not an unaudited action. This is the only place the old "never block" rule still applies.
- **Outcomes are durable but cannot un-run.** The action's result is recorded after execution; if that write fails the action already ran, so it is an outcome-gap (logged and flagged), not an unaudited action, because the intent was committed before execution.
- A gate-denied (or record-failed) action still lands in the chain with its denial where the denial itself is recordable. The rogue-attempt-visible guarantee carries from the flat log into the chain view.
- **An action record must be self-explaining.** It carries its `Args` (the specific target, e.g. *which* file, not "a file") and its `RunID` links it to the run's request and any cited evidence. A record that reduces to "an action happened" with no recoverable target and no recoverable why is a failure, not a valid record. This is the entire difference between this ledger and a useless audit log: looking up "why was `/tmp/cache.db` deleted" must return the target, the request that drove it, the evidence, and the gate decision, not "a file was deleted." The blocking action commit must therefore include enough to reconstruct the why, or it denies.

Cost note: the blocking commit is on the action path only, which is the slow, rare, dangerous path you want gated. A local SQLite commit is sub-millisecond, so the safe/read/context path stays unblocked and the action path pays a negligible, deliberate cost.

## Package and dependencies

- Rename `audit/` to `ledger/` (it is a ledger now, not just an audit log). The types move with it; callers update the import.
- Add `modernc.org/sqlite` (pure-Go SQLite, BSD-3) and a ULID library (e.g. `github.com/oklog/ulid/v2`, Apache-2.0). Both pure-Go (no CGO, preserves cross-compile) and non-GPL.

## Testing

- SQLite `Sink`+`Reader` round-trip: `Record` several events across two runs, then `Get` one by `EventID` and assert it matches, and `Chain(runID)` assembles only that run's events into the triad.
- Integration through the real path: an agent that recalls a seeded memory, calls one safe read tool, then a non-safe (gated) action. Assert the reconstructed chain has the request as head, the recall + read in `Available` (by ref, hashes present), the action with its gate decision and result, and `Evidence = [request ref]`. A second case where the gate denies the action: assert the action still appears in the chain with a denied gate decision.
- Hash-drift: capture a ref to a memory entry, supersede that entry, and assert the chain's ref hash no longer matches the live entry (drift is detectable).
- Fail-closed ledger: wire a ledger whose action-record commit fails, then run an agent that calls a non-safe tool. Assert the tool did NOT execute and the gate returned a denial citing the record failure. This proves "no record, no action." Pair it with a safe-tool case where a context-record failure does NOT block (best-effort holds for reads).
- Self-explaining record (the "no shit, a file was deleted" test): run an agent that deletes a specific file in service of a request. Read the chain back and assert it yields the target path (from Args), the originating request, the evidence, and the gate decision. Assert that a bare action with empty Args / no RunID linkage is rejected as an incomplete record, not stored as valid.

## Deferred (designed, not built)

Evidence citation (inferred-action provenance), forward/structured/entity retrieval (the non-RAG single-user memory), pattern-mining over the ledger, ambient triggers, and the surfacing threshold. The minimal SQLite schema grows (FTS / entity tables) when those arrive; the `Reader` interface absorbs the change without touching callers.
