# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`jess` is an easy agent harness over [`agentcore`](https://github.com/voocel/agentcore) with durable memory, skills, subagents, a fail-closed tool gate, and a durable provenance ledger. `jess.New(opts...)` returns a real `*agentcore.Agent`; drive it with `jess.Stream` or directly with agentcore's API. Pure Go, no CGO (preserves cross-compile).

## Commands

```bash
go vet ./...                  # required before any PR
go test -race ./...           # required before any PR; -race catches store/recaller concurrency bugs
go test -race -run TestName ./memory/   # single test
go build ./...
```

Embedder E2E test downloads ~90MB of model weights and is gated behind an env var (skipped by default and in CI):

```bash
JESS_EMBEDDER_E2E=1 go test -timeout 5m ./memory/embed/gomlx/...
```

CI (`.github/workflows/test.yml`) runs `go vet`, `go test -race`, and a non-blocking `govulncheck`. Go 1.26+.

## Architecture

### Package layout

- **`jess` (root)** — `New`, `Stream`, `Once`, `Option`, `With*`. Assembles a `*agentcore.Agent` from functional options; bakes in the provenance ledger and the fail-closed gate.
- **`internal/core`** — `Config`, `Agent(cfg)` builder, `Once` model adapter, `Stream` capture, audit middleware (enforcement), memory `ContextManager` wiring, skill block/tool helpers, `runState`, `memResolver`. Imported by root `jess` and `subagent`; never by callers.
- **`ledger/`** — agentcore-free provenance ledger. `Event` (ULID `EventID`, `RunID`, `CallID`, typed `Ref`/`RefSource`, `Kind` set incl. `KindRequest`/`KindRetrieved`/`KindAction`/`KindToolResult`/`KindGateDecision`/`KindAbort`/`KindRunEnd`), `Sink`, `DurableSink` (`CommitAction`), `DiscardSink`, `JSONLSink`, `SQLite` (pure-Go modernc, implements `DurableSink`+`Reader`), `Chain`/`Action`, `AssembleChain`, `Reader`, `Resolver`. See Provenance + enforcement below.
- **`gate/`** — fail-closed tool gate. `SafeTool` marker, `Approver` func, `Policy`, `New(Policy) agentcore.ToolGate`, `AllowAll()`. Non-safe tools with no approver are denied; denied attempts are recorded as `KindAction(denied)` so they stay visible in the ledger chain.
- **`mcp/`**, MCP stdio servers adapted into agentcore tools. `Tools(ctx, servers, logf)` launches each allowlisted server, lists its tools and returns them as `[]ac.Tool` plus a closer. Names are `<server>__<tool>` or bare (`Server.Bare`); JSON results pass through unwrapped. Only this package imports the MCP SDK.
- **`memory/`** — agentcore-free read/write/inject pipeline (see below). Portability insurance: stays importable without pulling in agentcore.
- **`skill/`** — agentcore-free capability bundles (see below). Same portability guarantee.
- **`subagent/`** — bounded `Pool` for fan-out subagents. Each subagent is a `*agentcore.Agent` built via `internal/core`.

### Provenance ledger + enforcement model

The ledger (`ledger/`) is the detective control for remote operation. Every run produces a chain of three kinds of records: the request (what triggered the run), the available context (memory recalled and safe tool results), and the actions (effectful calls with embedded rationale). This "request/retrieved/action" triad means reading a chain backward answers "why did the agent do X?" without needing separate documentation.

**Sinks.** `Sink` is the basic write interface (`Record`). `DurableSink` adds `CommitAction`, which only returns nil once the event is persisted. `SQLite` (pure-Go via modernc, no CGO) implements both `DurableSink` and `Reader`. `JSONLSink` and `DiscardSink` implement `Sink` only: they are observation-only and cannot authorize actions. Turning audit off is explicit: pass `ledger.DiscardSink{}` to `WithLedger`; the ledger is never silently absent.

**Enforcement ("no durable record, no action").** The audit middleware in `internal/core` runs for every tool call, gate-independently. Before calling any tool not explicitly marked `gate.SafeTool`, it: (a) checks that the sink is a `DurableSink`; (b) builds a self-explaining `KindAction` event carrying `RunID`, `CallID`, `Tool`, `Args`, and `Refs` back to the triggering request; (c) calls `CommitAction`, which validates the event is fully populated before persisting. If any step fails, the tool does not run. A permissive gate (`AllowAll`) cannot bypass this: the record happens in the middleware, after the gate decision. Tools without a `SafeTool` marker are non-safe by default (fail-safe: an unclassified or externally-injected tool cannot run unaudited). The gate itself records denied non-safe attempts as `KindAction(denied)` so blocked/rogue attempts are visible in the chain, not silently dropped.

**RunID.** Flows via a per-agent `runState` (not `context.Context`). `runState.begin` / `runState.end` bracket each `Stream` call. The gate and middleware capture `*runState` at build time and read it under a lock per call. `WithLedger(sink)` overrides the default; the default is a durable SQLite ledger at `os.UserCacheDir()/jess/ledger.db`.

**Chain assembly.** `AssembleChain([]Event)` pairs `KindAction` and `KindToolResult` events by `CallID`, groups `KindRetrieved` refs as available context, and populates the `Chain.Request`. Safe-tool results (no matching action) land in `Chain.Available`. `SQLite.Chain(runID)` does this query-backed, never a full scan.

**Drift detection.** `Ref` carries `{Source, ID, Hash}`: a content hash captured at decision time. `ledger.Resolver` provides `CurrentHash(source, id)` to check whether the referenced item still matches. `memResolver` in `internal/core` adapts `memory.EntryGetter` to `Resolver`.

**What is deferred.** Evidence citation (linking actions to the specific available items that justified them beyond the request ref), forward retrieval of actions by evidence, pattern mining across runs, and the ambient agent are all out of scope for this release.

Three layers cooperate; understanding their division of labor is the key to the package:

- **`Store`** (`memory.go`) is the persistence contract: `Append` / `Recall` / `Forget`, concurrency-safe. Three implementations: `InMemoryStore`, `JSONLStore` (durable, tombstones, `Compact`), `ChromemStore` (vector, on chromem-go). New backends satisfy `Store`; vector-aware ones additionally satisfy the `VectorStore` capability interface (`SearchVector` + `Embedder`) — extend via new interfaces, never change `Store`.

- **`Recaller`** (`recall.go`, `recall_vector.go`) is the read-side strategy that turns raw Store lookup into "the right N entries for this turn." `SimpleRecaller` (token overlap) + `VectorRecaller` (cosine, requires a `VectorStore`) fuse via `HybridRecaller` (reciprocal rank fusion, K=60). The split exists so hosts swap retrieval strategy without touching the Store.

- **`ContextManager`** (`context_manager.go`) is the `agentcore.ContextManager` adapter. On each turn `Project` builds the prompt in layers: inner manager baseline → AlwaysInclude Kinds pulled directly from Store (bypass recall) → recall fills remaining budget. Memory is prepended as ONE leading user message that never commits to the runtime baseline (appears for one call, vanishes next — keeps it out of the recall conversation hint). Wraps an `inner` ContextManager; `PassthroughInner` is the nil default.

### Kind taxonomy (`kind.go`)

`Entry.Kind` is an untyped string, but `KindUser` / `KindFeedback` / `KindProject` / `KindReference` are the canonical categories, each with a `KindPolicy` in the `KindRegistry`. `user`/`feedback` are `AlwaysInclude=true` (core memories, injected every turn, bypass recall scoring). `project`/`reference` are recall-only. Unknown Kinds get `FallbackKindPolicy`. Hosts override per-agent via `KindRegistry.Set`.

### Key supersession

Setting `Entry.Key` makes a re-`Append` REPLACE the prior entry at the same `(AgentID, Key)` rather than accumulate. "User prefers tabs" → later "spaces" → one entry. Without `Key`, Appends are independent (subject only to content-hash dedupe).

### Tools the model calls

`RememberTool` (`tool.go`) and `RecallTool` (`recall_tool.go`) are `agentcore.Tool`s that let the model write/read memory. `Source` on an Entry records provenance (session/message/tool/reason) — set it for tool-written entries so "why do you remember X?" and "forget session Y" are answerable.

### memory/embed/gomlx — the pure-Go embedder

Runs BERT-family sentence-transformers ONNX models in-process via GoMLX's pure-Go backend (no CGO, no ONNX Runtime sidecar, no Python/`huggingface-cli`). `NewEmbedder` downloads weights from HuggingFace on first use into the standard HF cache (`$HF_HOME` / `~/.cache/huggingface`); `$HF_ENDPOINT` redirects to mirrors / air-gapped installs, `HF_TOKEN` authenticates. `models.go` holds known-good `Model` constants (Dim+SeqLen pre-filled to avoid the footgun of setting `ModelID` alone with a stale `Dim`); `DefaultModel` is MiniLM-L6-v2. Or pass `Options{ModelID: "org/model"}` and `resolve.go` auto-detects Dim+SeqLen from the repo's `config.json`. New embedder backends (Ollama, OpenAI) land under `memory/embed/<name>/`.

### skill/

A `Skill` (`skill.go`) is Name + Description + SystemPrompt + zero-or-more tools. `Set` holds them (concurrency-safe). `agentcore.go` converts to `SystemBlocks` + `Tools`; `filesystem.go` walks a `SKILL.md` layout mirroring Claude Code skills. Note: `Skill.Tools` is typed `[]any` (not `[]agentcore.Tool`) to keep agentcore out of the surface API; `Set.Tools()` type-asserts.

## Non-obvious conventions (from CONTRIBUTING.md)

- **Memory failures must NEVER block LLM calls.** `ContextManager` swallows Store/Recaller errors and degrades to no-memory, not no-agent. Preserve this when editing the inject path.
- **No CGO.** The GoMLX-over-ONNX-Runtime choice exists to keep cross-compile. Reject CGO without explicit discussion.
- **No vendoring/forking deps to add features.** File upstream; local workarounds carry a TODO referencing the upstream issue. Two deps (`agentcore`, `chromem-go`) are pinned to main pending tagged releases — see README "Status".
- **No GPL/AGPL deps.** MIT/Apache-2.0/MPL-2.0/BSD only.
- **Doc density:** every exported type/func gets a godoc; non-trivial design decisions get a paragraph on *why*, not just what. Match the surrounding style.
