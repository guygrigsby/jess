# jess

[![Go Reference](https://pkg.go.dev/badge/github.com/guygrigsby/jess.svg)](https://pkg.go.dev/github.com/guygrigsby/jess)
[![Go Report Card](https://goreportcard.com/badge/github.com/guygrigsby/jess)](https://goreportcard.com/report/github.com/guygrigsby/jess)
[![CI](https://github.com/guygrigsby/jess/actions/workflows/test.yml/badge.svg)](https://github.com/guygrigsby/jess/actions/workflows/test.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Memory and skills for [agentcore](https://github.com/voocel/agentcore)-based
Go agents. The leather strap on the falcon's leg.

## What it is

Two extension packages that sit on top of `agentcore`:

- **`jess/memory`** — durable agent memory. Typed `Kind` (user, feedback,
  project, reference) with per-kind retrieval policy. Three pluggable
  Stores (in-memory, JSONL, chromem-go vector). A pure-Go embedder
  (GoMLX + sentence-transformers, no CGO). Recallers compose:
  `SimpleRecaller` (token overlap) + `VectorRecaller` (cosine) fused
  via `HybridRecaller` (reciprocal rank fusion). `RememberTool` lets
  the model save to memory. `ContextManager` adapter injects layered
  memory (always-on core + relevance-recalled) into every LLM call.

- **`jess/skills`** — registerable capability bundles. A `Skill` is a
  name, description, system-prompt contribution, and zero-or-more
  `agentcore.Tool` implementations. Loads from disk (filesystem
  layout mirrors Claude Code skills) or by direct registration.

Nothing here duplicates `agentcore`. The harness, providers, tool
dispatch, permission engine, and context compaction stay upstream.

## Install

```bash
go get github.com/guygrigsby/jess
```

Go 1.26+. Pure Go — no CGO, no external runtimes. Cross-compiles to
every OS/arch combo `go build` supports.

## Quickstart

Full runnable example at [`examples/quickstart`](./examples/quickstart).
Sketch:

```go
import (
    "github.com/voocel/agentcore"
    "github.com/guygrigsby/jess/memory"
    "github.com/guygrigsby/jess/memory/embed/gomlx"
)

// 1. In-process embedder (downloads ~90MB model on first use,
//    cached after; no API key, no subprocess).
emb, _ := gomlx.NewEmbedder(gomlx.Options{})

// 2. Vector store backed by chromem-go. Path persists to gob on disk.
store, _ := memory.NewChromemStore(emb, memory.ChromemOptions{
    Path: "~/.cache/myapp/memory",
})

// 3. The "remember" tool the model can call.
tool := memory.NewRememberTool(store, memory.RememberOptions{
    AgentID: "main",
})

// 4. Hybrid retrieval: vector for semantic matches, token overlap
//    for keyword-exact, RRF-fused.
recaller := memory.NewHybridRecaller(
    memory.NewVectorRecaller(),
    memory.NewSimpleRecaller(),
)

// 5. ContextManager: injects core + relevant memories on every turn.
cm := memory.NewContextManager(store, recaller, memory.ContextManagerOptions{
    AgentID: "main",
})

// 6. Wire into agentcore.
agent := agentcore.NewAgent(
    agentcore.WithModel(yourModel),
    agentcore.WithTools(tool, /* ...your other tools... */),
    agentcore.WithContextManager(cm),
)
```

The agent now:
- Sees `user` and `feedback` memories on every turn (always-include).
- Sees `project` and `reference` memories when they're relevant.
- Can save new facts via the `remember` tool.
- Persists across restarts.

## Project layout

```
jess/
├── memory/                       # the memory subsystem
│   ├── memory.go                 # Entry, Store, Query, Recaller, VectorStore
│   ├── kind.go                   # Kind constants, KindPolicy, KindRegistry
│   ├── tool.go                   # RememberTool (agentcore.Tool)
│   ├── context_manager.go        # agentcore.ContextManager adapter
│   ├── store_inmemory.go         # InMemoryStore
│   ├── store_jsonl.go            # JSONLStore (durable, tombstones, Compact)
│   ├── store_chromem.go          # ChromemStore (vector, on chromem-go)
│   ├── recall.go                 # SimpleRecaller (token overlap)
│   ├── recall_vector.go          # VectorRecaller + HybridRecaller (RRF)
│   ├── embedder.go               # Embedder interface
│   └── embed/gomlx/              # in-process pure-Go embedder
└── skills/                       # skill bundles
    ├── skill.go                  # Skill, Set, Loader
    ├── agentcore.go              # SystemBlocks + Tools conversion
    └── filesystem.go             # SKILL.md walker (Claude Code layout)
```

## Design choices

Brief notes on the non-obvious calls:

**Typed `Kind` with policy.** `KindUser` and `KindFeedback` always inject
(stable facts the model needs every turn). `KindProject` and
`KindReference` are recall-only. Unknown kinds get a conservative
fallback. Hosts can override the registry per-agent.

**Key supersession.** Setting `Entry.Key` makes re-`Append` REPLACE
the prior entry at the same `(AgentID, Key)`. "User prefers tabs" →
later "user prefers spaces" → one entry, not two. The model doesn't
have to resolve the contradiction every turn.

**Hybrid retrieval, not pure vector.** Vector retrieval famously
misses keyword-exact hits ("what was that flag?" drifts toward
semantically related text). `HybridRecaller` fuses `SimpleRecaller`
(BM25-equivalent token overlap) with `VectorRecaller` (chromem-go
cosine) via reciprocal rank fusion (K=60). Either alone misses
cases the other catches.

**Pure-Go embedder.** `jess/memory/embed/gomlx` runs
`sentence-transformers/all-MiniLM-L6-v2` (or any BERT-family ONNX
sentence model) in-process via [GoMLX's pure-Go backend][gomlx-go].
No CGO. No subprocess. No ONNX Runtime sidecar. Cross-compile
intact. ~50ms per embedding on a modern Mac CPU (model load is
one-shot at construction).

Model weights are NOT bundled. `NewEmbedder()` downloads from
HuggingFace on first run (~90MB for MiniLM) into the standard
HuggingFace cache (`$HF_HOME` or `~/.cache/huggingface`); subsequent
runs are warm. The model's license stays the model's license; jess
takes no position. Air-gapped installs need to pre-populate the
cache or point `HF_ENDPOINT` at a mirror.

**Embedder is replaceable.** `Embedder` is an interface. Ship Ollama
and OpenAI adapters when needed; hosts that prefer those swap one
line.

[gomlx-go]: https://github.com/gomlx/gomlx#purego-backend

## Status

Pre-1.0. API may change before v1; both subpackages have shipping
implementations with test coverage. See [CHANGELOG.md](CHANGELOG.md).

Two upstream deps pinned to main (not a tagged release):

- `github.com/voocel/agentcore` — pinned for the post-PR-409
  `compute.Backend` API; falls back to a tagged version once one
  ships.
- `github.com/philippgille/chromem-go` — pinned for `ListDocuments`
  and `GetByMetadata` from main; v0.7.0 lacks both.

Both moves are temporary. Will revert to tagged versions when
upstream cuts releases.

## License

[MIT](LICENSE). Dependency licenses:

- `agentcore` — Apache 2.0
- `chromem-go` — MPL 2.0 (file-level copyleft; safe to depend on
  from MIT projects)
- `gomlx`, `onnx-gomlx`, `go-huggingface` — Apache 2.0

No GPL or AGPL anywhere in the tree.
