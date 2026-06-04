# ADR 0003: Provenance ledger

**Status:** Accepted
**Date:** 2026-06-04

## Context

After ADR 0002 landed the audit and gate packages, the audit trail was a flat JSONL append log: every tool request and gate decision was recorded, but there was no structure to answer "why did the agent do X?" The audit log was forensically incomplete: given a tool call you could see the args but not the memory or prior results the model drew on. For remote operation (where the operator cannot watch output directly) this is insufficient.

Three gaps drove this ADR:

1. A flat log cannot reconstruct causation. An action record needed to carry references back to the context the model was given and the request that triggered the run.
2. The JSONL sink offered no durability contract. The audit middleware could record to it and still allow the action, but JSONL offers no "I promise this write is durable before you proceed" guarantee. The gap between "recorded" and "committed" was unguarded.
3. Denied non-safe attempts (gate rejections) vanished. agentcore short-circuits a gate denial before the middleware runs, so a blocked call left no chain-visible trace.

## Decision

### Rename `audit/` to `ledger/` and build a chain ledger

The package is renamed `ledger` to reflect its new role: not a log of events, but a provenance record structured as a chain. The ledger stores three kinds of records per run: the request (what triggered the run), retrieved context (memory recall results and safe tool reads), and actions (effectful calls with embedded rationale). The triad is the "request / available / action" chain; reading it backward answers "why did the agent do X?" deterministically.

Each event carries a ULID `EventID` (time-ordered, monotonic, globally unique) as the primary key. ULID was chosen over UUID4 for natural time ordering and over integer sequences for global uniqueness without coordination.

### Split sinks by durability contract

`Sink` (`Record`) is the best-effort observation interface. `DurableSink` adds `CommitAction`, which returns nil only after the event is persisted. This split makes the durability contract explicit at the type level rather than a runtime property of any given sink.

`SQLite` (pure-Go via `modernc.org/sqlite`, no CGO) implements both `DurableSink` and `Reader`. WAL mode + `PRAGMA synchronous=FULL` gives crash-durable commits. `JSONLSink` implements `Sink` only: it is observation-only and cannot authorize actions. `DiscardSink` implements `Sink` only and silently drops everything; passing it to `WithLedger` is the explicit opt-out.

The default ledger for every `jess.New` agent is a durable SQLite file at `os.UserCacheDir()/jess/ledger.db`. If the cache dir or database cannot be opened, the fallback is `DiscardSink`, which is the safe failure: without a `DurableSink`, non-safe actions cannot run.

### Enforce "no durable record, no action" in the middleware, not the gate

The enforcement point is `auditMiddleware` in `internal/core`, not the gate. The middleware runs for every tool call after the gate allows it. For any tool not explicitly marked `gate.SafeTool`, the middleware:

1. Checks that the current sink implements `DurableSink`. If not, denies the action.
2. Builds a self-explaining `KindAction` event: `RunID`, `CallID`, `Tool`, `Args`, and at least one `Ref` back to the triggering request.
3. Calls `CommitAction`. `SQLite.CommitAction` validates that all required fields are present before inserting; a structurally incomplete record is rejected, and the tool does not run.

This placement is intentional. The gate runs first (routing decision) and the middleware runs second (recording obligation). A permissive gate (`AllowAll`) cannot bypass the record requirement because the middleware is independent of the gate's verdict. An unclassified or externally-injected tool that lacks a `SafeTool` marker is non-safe by default: fail-safe means unknown tools cannot run unaudited, never the reverse.

### Record denied non-safe attempts in the gate

When the gate denies a non-safe tool (no approver, or approver returns false), it records a `KindAction(denied)` event via `Policy.recordDeniedAction`. Because agentcore short-circuits before the audit middleware for a denied call, the gate must record denials itself. The record is best-effort: the action did not execute, so there is nothing safety-critical to fail-close on if the write fails.

### Self-explaining action requirement

`CommitAction` enforces that every persisted action record can explain itself independently: `RunID`, `CallID`, `Tool`, `Args`, and at least one `Ref` are all required. A record that says only "an action happened" is rejected at commit time, not at query time. This means every action in the ledger links back to its run and to the context that triggered it, without needing external join tables.

### Refs and content hashes for drift detection

`Ref{Source, ID, Hash}` captures the content hash of a referenced item at decision time. `ledger.Resolver.CurrentHash(source, id)` lets callers check whether the item still matches at query time. `memResolver` in `internal/core` adapts `memory.EntryGetter` to `Resolver` for memory entry refs.

### Chain assembly

`AssembleChain([]Event)` pairs `KindAction` and `KindToolResult` events by `CallID` into `Action{Intent, Result, Evidence}` structs. Safe-tool results (no matching action) and `KindRetrieved` refs land in `Chain.Available`. `SQLite.Chain(runID)` reads from an index on `run_id`, never a full scan.

## Deferred

These items are known to be valuable but out of scope:

- Evidence citation: linking each action to the specific available items (beyond the request ref) that justified it.
- Forward retrieval: querying "which actions used this memory entry?"
- Pattern mining across runs.
- The ambient agent (a background observer that reads the ledger and surfaces anomalies).

## Consequences

Positive:

- Every non-safe action in the ledger is self-explaining and durably committed before execution.
- Denied and blocked attempts are chain-visible, not silently lost.
- The enforcement is gate-independent: `AllowAll` does not bypass it.
- `SQLite.Chain(runID)` answers "why did the agent do X?" without reconstruction work.
- `DiscardSink` as the explicit opt-out means auditing is never silently off.

Negative / costs:

- `SQLite` adds `modernc.org/sqlite` to the dependency graph (Apache 2.0, pure Go, no CGO; consistent with the no-CGO constraint).
- `CommitAction` adds a synchronous SQLite write on the hot path before every non-safe tool execution. Write latency is bounded by WAL + `busy_timeout=5000ms`.
- Callers using `DiscardSink` or `JSONLSink` cannot run non-safe actions. This is a breaking behavioral change for any caller that was relying on the old `audit/` package with JSONL for authorization.
