# Changelog

Notable changes to `jess`. Following [Keep a Changelog][kac] format;
pre-1.0 so no compatibility guarantees yet — see [SemVer][semver] for
what the rules become at v1.

[kac]: https://keepachangelog.com/en/1.1.0/
[semver]: https://semver.org/

## Unreleased

### Added
- `memory.Kind` typed string with canonical `KindUser`, `KindFeedback`,
  `KindProject`, `KindReference` constants.
- `memory.KindPolicy` with `AlwaysInclude`, `MaxEntries`, `AgeWeight`.
  `KindRegistry` holds per-Kind policies; `DefaultKindPolicies` matches
  Claude Code's auto-memory taxonomy (user/feedback always-include;
  project/reference recall-only).
- `Entry.Source { SessionID, MessageID, Tool, Reason }` for provenance.
  Round-trips through `JSONLStore` and `ChromemStore`.
- `Entry.Key` for supersession: re-Append at the same `(AgentID, Key)`
  REPLACES the prior entry. Solves the "user changed their mind"
  contradiction-in-memory problem.
- `memory.RememberTool` — `agentcore.Tool` the model calls to save
  facts. Reads provenance from ctx via `memory.WithSource`.
- `ContextManager` now produces a layered prompt: Core (AlwaysInclude
  kinds, bypasses recall) above Relevant (recalled).
- `memory/embed/gomlx` — in-process pure-Go embedder using GoMLX's
  simplego backend and `sentence-transformers/all-MiniLM-L6-v2`.
  No CGO, no subprocess, no ONNX Runtime sidecar.
- `memory.VectorStore` capability interface for nearest-neighbor
  search; `memory.NewChromemStore` wraps chromem-go.
- `memory.VectorRecaller` (semantic) and `memory.HybridRecaller`
  (RRF over multiple Recallers; K=60).
- `memory.NewSimpleRecaller`, `memory.NewInMemoryStore`,
  `memory.NewJSONLStore` (with `Compact()` to drop tombstones).
- `skills` package: `Skill`, `Set`, `Loader`, filesystem loader
  reading SKILL.md frontmatter (Claude Code skill layout).

### Changed
- `entryID` hash now includes `Entry.Key` so semantically distinct
  entries with the same text but different keys get distinct IDs.
- JSONL on-disk schema added optional `key` + `source` fields. Older
  files decode cleanly (missing = zero-valued).

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
