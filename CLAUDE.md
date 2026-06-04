# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`jess` is two extension packages on top of [`agentcore`](https://github.com/voocel/agentcore): durable agent memory (`memory/`) and registerable capability bundles (`skills/`). It deliberately does NOT reimplement the agentcore harness, providers, tool dispatch, permission engine, or compaction. Pure Go, no CGO (preserves cross-compile).

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

### memory/ — the read/write/inject pipeline

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

### skills/

A `Skill` (`skill.go`) is Name + Description + SystemPrompt + zero-or-more tools. `Set` holds them (concurrency-safe). `agentcore.go` converts to `SystemBlocks` + `Tools`; `filesystem.go` walks a `SKILL.md` layout mirroring Claude Code skills. Note: `Skill.Tools` is typed `[]any` (not `[]agentcore.Tool`) to keep agentcore out of the surface API; `Set.Tools()` type-asserts.

## Non-obvious conventions (from CONTRIBUTING.md)

- **Memory failures must NEVER block LLM calls.** `ContextManager` swallows Store/Recaller errors and degrades to no-memory, not no-agent. Preserve this when editing the inject path.
- **No CGO.** The GoMLX-over-ONNX-Runtime choice exists to keep cross-compile. Reject CGO without explicit discussion.
- **No vendoring/forking deps to add features.** File upstream; local workarounds carry a TODO referencing the upstream issue. Two deps (`agentcore`, `chromem-go`) are pinned to main pending tagged releases — see README "Status".
- **No GPL/AGPL deps.** MIT/Apache-2.0/MPL-2.0/BSD only.
- **Doc density:** every exported type/func gets a godoc; non-trivial design decisions get a paragraph on *why*, not just what. Match the surrounding style.
