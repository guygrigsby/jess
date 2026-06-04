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
- `CallID string` — the agentcore `ToolCall.ID` (tool.go:84) for tool-related events. This is what pairs a call, its gate verdict, and its result deterministically, even with concurrent or repeated calls to the same tool; `RunID` alone cannot.
- existing fields stay: `Time, AgentPath, Kind, Tool, Args, Verdict, Reason, Result, Err, DurationMS`.

`Kind`s: `KindRequest` (chain head, the run input), `KindRetrieved` (memory recall, by ref), `KindAction` (the atomic action intent + gate verdict, committed at the gate before execution, see wiring), `KindToolResult` (the outcome of a tool, safe or not), plus existing kinds. Safe-tool reads land as `KindToolResult` marked available; a non-safe tool produces one `KindAction` (intent) then one `KindToolResult` (outcome), paired by `CallID`.

`Ref{ Source RefSource; ID string; Hash string }` addresses an available item. `Source` is an explicit enum: `RefTool` (ID is a ledger `EventID`) or `RefMemory` (ID is a `memory.Entry.ID`). No guessing from id shape. `Hash` is the item's content hash captured at decision time, so a later read can detect the referenced thing changed or was deleted (memory mutates via supersession and Forget). Refs, not copies.

`Chain` (a read-side view, assembled by `Reader.Chain(RunID)`):
```
Chain{
    Request   Event          // the KindRequest head
    Available []Ref           // safe-tool (read) outputs + recalled memory, by ref
    Actions   []Action
}
Action{
    Intent   Event            // the KindAction event: args/target + gate verdict + embedded why
    Result   Event            // the KindToolResult (present even on denial: the denial is the result)
    Evidence []Ref            // the available items that justified it
}
```
Call, intent, and result are matched by `CallID`.

Available vs action is decided by the `SafeTool` marker at capture: a safe (read, bounded) tool's output is *available*; a non-safe tool call is an *action*. Evidence is populated trivially now: for a direct command, `Evidence = [ref to the Request]`, and the request text is embedded in the `KindAction` event so the action stays self-explaining even if the separate `KindRequest` record is lost. The citation mechanism for inferred actions is deferred.

## Storage, behind interfaces

- `Sink` (exists): `Record(Event) error` — the best-effort write path (observation: reads, recall, results, run-end).
- `CommitAction(Event) error` (new) — the durable write path. Returns nil ONLY on real persistence. This is what makes "no record, no action" enforceable; `Record` alone cannot, because a `Record` that returns nil without persisting (DiscardSink) would let an action run unrecorded. A non-durable store returns a non-nil error here so the gate denies.
- `Reader` (new): `Get(id ulid.ULID) (Event, error)` and `Chain(runID string) (Chain, error)`. Both index-backed, never a scan.
- `Resolver` (new, for drift detection): `Get(id string) (memory.Entry, error)`. `memory.Store` has only Append/Recall/Forget, no indexed `Get`, so add a small `EntryGetter` interface that `InMemoryStore`/`JSONLStore`/`ChromemStore` satisfy. A `RefMemory` hash is verified against the resolver when the store implements it; otherwise the ref is recorded but flagged unverifiable, never silently scanned.

A SQLite store (`modernc.org/sqlite`, pure-Go, no CGO, BSD-licensed) implements `Sink`, `CommitAction`, and `Reader`. Minimal schema:
```
events(
  id      TEXT PRIMARY KEY,   -- ULID
  run_id  TEXT NOT NULL,
  call_id TEXT,                -- agentcore ToolCall.ID, for call/intent/result pairing
  ts      INTEGER NOT NULL,
  kind    TEXT NOT NULL,
  tool    TEXT,
  payload BLOB                 -- the rest of the Event as JSON
);
CREATE INDEX events_run  ON events(run_id);
CREATE INDEX events_call ON events(call_id);
```
ULID PK gives O(log n) ref resolution and time ordering; the `run_id` index makes `Chain` an indexed lookup; the `call_id` index pairs intent/result. `CommitAction` is a synchronous INSERT + commit; success means the row is on disk. No FTS or entity tables yet.

`JSONLSink` survives as an optional, greppable, write-only mirror (it satisfies `Sink` but NOT `CommitAction`, so it cannot back actions on its own). `DiscardSink` is the explicit off switch: its `Record` drops observation, and its `CommitAction` returns `ErrNotDurable`, so with auditing off the agent may observe but is denied non-safe actions (see Invariants). The default ledger (`jess.New` with no `WithLedger`/`WithAudit`) is the SQLite store at the user cache dir.

## Capture wiring

RunID travels by a **jess-owned run registry keyed by the agent**, NOT by `ctx`. `agentcore.Agent.Prompt(input string)` takes no context and starts the run from `context.Background()` (agent.go:115, :287), so a value stashed in `jess.Stream`'s ctx never reaches the gate, middleware, or context manager. Instead `jess.Stream` registers `(agent -> RunID, request)` before calling `Prompt` and releases it after (this mirrors the existing `agentRegistry` sink lookup in `internal/core`). The gate/middleware/CM look up their `RunID` from that registry by the running agent.

- `jess.Stream` mints a `RunID`, registers it, and records the `KindRequest` head (durable attempt) and a `KindRunEnd` at close.
- The memory `ContextManager` (`internal/core`) records a `KindRetrieved` event listing the *refs* of injected memory (ids + hashes, not text). Best-effort.
- A safe-tool read records a `KindToolResult` marked available. Best-effort.
- A non-safe tool is gated, and **the gate is the enforcement point**. On "allow", the gate assembles one atomic `KindAction` event (`CallID` from `ToolCall.ID`, `Args`/target, the gate verdict, `RunID`, and the embedded request/evidence — the why) and calls `CommitAction`. It returns "allow" only if the commit is durable; otherwise it returns "deny". The middleware then records the `KindToolResult` outcome, paired by `CallID`. This replaces the old middleware-owned `tool_request`, which fired after approval and was best-effort: intent and verdict are now one durable event, committed before execution, at the gate.

## Invariants

Auditability is a hard requirement, not best-effort, so the ledger is fail-closed for actions. This is the inverse of memory's "never block the LLM call" rule, and the distinction is deliberate: memory recall is an enhancement (losing it degrades), an action record is accountability (losing it means an unaudited action happened, the exact failure this design exists to prevent).

- **Actions block on a durable, self-explaining record.** Before a non-safe tool executes, the gate commits one `KindAction` event (CallID, Args/target, gate verdict, RunID, embedded why) via `CommitAction`, which succeeds only on real persistence. Commit fails, action denied, identical to a gate refusal. No durable record, no action.
- **The why travels inside the action.** The request and cited evidence are embedded in the `KindAction` event, so an action is self-explaining even if the separate `KindRequest` head was lost. The request is load-bearing, not observation, and it rides with the action rather than being relegated to best-effort. (Earlier draft wrongly filed the request under best-effort context; that would let an action record with no recoverable why.)
- **Only pure observation is best-effort.** Reads, recall refs, and run-end, events nothing acts on. A failure there loses chain completeness, not an unaudited action, and keeps a ledger hiccup from blinding the overseer.
- **Auditing off disables the hands, not just the pen.** A non-durable ledger (`DiscardSink`, or `JSONLSink` alone) makes `CommitAction` fail, so non-safe actions are denied. gyr can run with auditing off, but then it can only observe; it cannot act. There is no mode where dangerous actions run unrecorded.
- **Call/intent/result pair by `CallID`.** A tool call, its `KindAction` intent, and its `KindToolResult` share the agentcore `ToolCall.ID`, so concurrent or repeated calls to the same tool reconstruct into distinct, correct actions. `RunID` groups the chain; `CallID` groups one invocation within it.
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
- Auditing-off disables the hands: wire `DiscardSink` (CommitAction returns ErrNotDurable), run an agent that calls a non-safe tool and a safe tool. Assert the non-safe tool is denied and the safe tool runs. Proves there is no mode where a dangerous action runs unrecorded.
- Why survives a lost head: commit a `KindAction` whose separate `KindRequest` write was dropped; assert the action is still self-explaining (request/evidence recoverable from the embedded fields), proving the why does not depend on the head surviving.
- CallID pairing: drive two concurrent (or back-to-back) calls to the same tool in one run; assert `Chain` reconstructs two distinct `Action`s, each pairing its own intent and result by `CallID`, with no cross-contamination.

## Deferred (designed, not built)

Evidence citation (inferred-action provenance), forward/structured/entity retrieval (the non-RAG single-user memory), pattern-mining over the ledger, ambient triggers, and the surfacing threshold. The minimal SQLite schema grows (FTS / entity tables) when those arrive; the `Reader` interface absorbs the change without touching callers.
