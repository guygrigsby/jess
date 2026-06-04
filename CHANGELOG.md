# Changelog

Notable changes to `jess`. Following [Keep a Changelog][kac] format;
pre-1.0 so no compatibility guarantees yet — see [SemVer][semver] for
what the rules become at v1.

[kac]: https://keepachangelog.com/en/1.1.0/
[semver]: https://semver.org/

## Unreleased

### Changed (refactor/jess-simplify — ADR 0002)
- **Breaking:** agentcore is now a direct, exposed dependency. `jess.New` returns `*agentcore.Agent`; `jess.Stream` takes `*agentcore.Agent`. No parallel type universe.
- **Breaking:** deleted packages `jess/tool`, `jess/message`, `jess/model`, `jess/event`. Use agentcore's types directly (`ac.Tool`, `ac.Message`, `ac.LLMResponse`, `ac.Event`).
- **Breaking:** deleted `jess.Agent`, `jess.Session`, `jess.Run` facade types.
- `internal/acl` renamed to `internal/core`; type-translation layer deleted.
- ADR 0001 boundary test dropped; agentcore is now imported throughout (ADR 0002).

### Added (refactor/jess-simplify)
- `jess/audit`: agentcore-free durable JSONL audit log. `Event`, `Sink`, `JSONLSink`, `DiscardSink`. On by default (durable sink under user cache dir); explicit `DiscardSink{}` to turn off.
- `jess/gate`: fail-closed tool gate. `SafeTool` marker, `Approver` func, `Policy`, `New(Policy) agentcore.ToolGate`, `AllowAll()`. Non-safe tools with no approver are denied; denied attempts are recorded to audit.
- `jess.WithApprover`, `jess.WithToolGate`, `jess.AllowAll` options.
- `jess.WithAudit` option; `audit.DiscardSink{}` to silence.
- `jess.Once(supportsTools, fn)` one-shot `agentcore.ChatModel` adapter.
- `jess.Stream(ctx, agent, input)` returns event channel + wait-for-summary func.
- `jess.NewMemoryManager` re-exported for Door 2 (`agentcore.NewAgent` callers).
- `examples/gated`: stdin approver example (stand-in for Telegram confirm).

### Changed (ADR 0001 — earlier)
- `jess.New` became a facade over agentcore (`jess.New` -> `Agent`/`Session`/`Run`). (Now superseded by ADR 0002 above.)
- Package `skills` renamed to `skill`.
- Memory `ContextManager` adapter moved from `memory` into `internal/acl`; hosts wire memory via `jess.WithMemory(store, recaller)`.
- `memory.RememberTool` / `memory.RecallTool` implement `agentcore.Tool`.
- `entryID` hash now includes `Entry.Key` so semantically distinct entries with the same text but different keys get distinct IDs.
- JSONL on-disk schema added optional `key` + `source` fields. Older files decode cleanly.

### Added (ADR 0001 — earlier)
- `memory.Kind` typed string with canonical `KindUser`, `KindFeedback`, `KindProject`, `KindReference` constants.
- `memory.KindPolicy` with `AlwaysInclude`, `MaxEntries`, `AgeWeight`. `KindRegistry` holds per-Kind policies; `DefaultKindPolicies` matches Claude Code's auto-memory taxonomy.
- `Entry.Source { SessionID, MessageID, Tool, Reason }` for provenance.
- `Entry.Key` for supersession: re-Append at the same `(AgentID, Key)` REPLACES the prior entry.
- `memory.RememberTool` / `memory.RecallTool` — tools the model calls to save and query facts.
- The memory inject path produces a layered prompt: Core (AlwaysInclude kinds, bypasses recall) above Relevant (recalled).
- `memory/embed/gomlx` — in-process pure-Go embedder using GoMLX's simplego backend and `sentence-transformers/all-MiniLM-L6-v2`. No CGO, no subprocess, no ONNX Runtime sidecar.
- `memory.VectorStore` capability interface; `memory.NewChromemStore` wraps chromem-go.
- `memory.VectorRecaller` (semantic) and `memory.HybridRecaller` (RRF over multiple Recallers; K=60).
- `memory.NewSimpleRecaller`, `memory.NewInMemoryStore`, `memory.NewJSONLStore` (with `Compact()` to drop tombstones).
- `skill` package: `Skill`, `Set`, `Loader`, filesystem loader reading SKILL.md frontmatter.
- `subagent` package: bounded `Pool` for fast, abundant subagents.

### Deps
- `github.com/voocel/agentcore` — pinned to main commit (post-PR-409
  `compute.Backend` migration; not yet tagged).
- `github.com/philippgille/chromem-go` — pinned to main (needs
  `ListDocuments` + `GetByMetadata` from main; v0.7.0 lacks both).
- `github.com/gomlx/gomlx`, `gomlx/compute`, `gomlx/onnx-gomlx`,
  `gomlx/go-huggingface` — pinned to main for the compute migration.

## Historical: pre-pivot harness exploration

Before adopting `agentcore` as the agent loop, the first three
commits of this repo were a clean-slate harness prototype (tool
dispatch, provider abstraction, multi-turn loop). That code is
preserved in git history but no longer ships. Commits:

- [a558e26](https://github.com/guygrigsby/jess/commit/a558e26) — scaffold
- [8d2eb4b](https://github.com/guygrigsby/jess/commit/8d2eb4b) — provider interface + registry
- [cd4d63d](https://github.com/guygrigsby/jess/commit/cd4d63d) — agent loop with event stream

The exploration informed the API shape; the implementation got
deleted when `agentcore` proved out as a viable upstream.
