# jess

[![Go Reference](https://pkg.go.dev/badge/github.com/guygrigsby/jess.svg)](https://pkg.go.dev/github.com/guygrigsby/jess)
[![Go Report Card](https://goreportcard.com/badge/github.com/guygrigsby/jess)](https://goreportcard.com/report/github.com/guygrigsby/jess)
[![CI](https://github.com/guygrigsby/jess/actions/workflows/test.yml/badge.svg)](https://github.com/guygrigsby/jess/actions/workflows/test.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A memory- and skill-augmented agent facade for Go. The leather strap
on the falcon's leg.

## What it is

`jess` is an easy agent harness over [agentcore](https://github.com/voocel/agentcore) with durable memory, skills, subagents, and baked-in audit + a fail-closed tool gate. `jess.New(opts...)` returns a real `*agentcore.Agent`; drive it with `jess.Stream` or directly with agentcore's API.

- **`jess` (root)** — `New(opts...) *agentcore.Agent`, `Stream(ctx, agent, input)`, `Once(supportsTools, fn)`. Options: `WithModel`, `WithAgentID`, `WithSystemPrompt`, `WithTools`, `WithMemory`, `WithSkills`, `WithMaxTurns`, `WithApprover`, `WithAudit`, `AllowAll`, `WithToolGate`, `WithSubagents`, `WithAgentcoreOptions`.

- **`jess/memory`** — durable agent memory. Typed `Kind` (user, feedback, project, reference) with per-kind retrieval policy. Three pluggable Stores (in-memory, JSONL, chromem-go vector). A pure-Go embedder (GoMLX + sentence-transformers, no CGO). Recallers compose: `SimpleRecaller` (token overlap) + `VectorRecaller` (cosine) fused via `HybridRecaller` (reciprocal rank fusion). `RememberTool` and `RecallTool` let the model write and read memory. Wired via `jess.WithMemory(store, recaller)`; recalled entries are injected on every turn.

- **`jess/skill`** — registerable capability bundles. A `Skill` is a name, description, system-prompt contribution, and zero-or-more tools. Loads from disk (filesystem layout mirrors Claude Code skills) or by direct registration. Wired via `jess.WithSkills(set)`.

- **`jess/audit`** — durable JSONL audit log (agentcore-free). Every tool request, gate decision, and run boundary is recorded. `DiscardSink{}` turns audit off explicitly; it is never off silently.

- **`jess/gate`** — fail-closed tool gate. Tools implementing `SafeTool` are auto-approved. Everything else goes to the `Approver` if one is wired; without an approver, non-safe tools are denied. `AllowAll()` is the explicit opt-out.

## Install

```bash
go get github.com/guygrigsby/jess
```

Go 1.26+. Pure Go — no CGO, no external runtimes. Cross-compiles to
every OS/arch combo `go build` supports.

## Quickstart

Full runnable, offline example at
[`examples/quickstart`](./examples/quickstart). Sketch:

```go
import (
    ac "github.com/voocel/agentcore"
    "github.com/guygrigsby/jess"
    "github.com/guygrigsby/jess/audit"
    "github.com/guygrigsby/jess/memory"
)

// 1. Durable memory. Seed a core (always-include) user fact.
store := memory.NewInMemoryStore()
store.Append(ctx, memory.Entry{
    AgentID: "demo",
    Kind:    string(memory.KindUser), // AlwaysInclude: injected every turn
    Text:    "User prefers concise, example-first answers.",
})

// 2. Build the agent. jess.New returns a *agentcore.Agent.
agent := jess.New(
    jess.WithModel(yourModel),  // any agentcore.ChatModel; jess.Once for local fns
    jess.WithAgentID("demo"),
    jess.WithMemory(store, memory.NewSimpleRecaller()),
    jess.WithAudit(audit.DiscardSink{}), // or omit for durable JSONL default
)

// 3. Drive a run and observe its event channel.
ch, wait := jess.Stream(ctx, agent, "What kind of answers do I like?")
for ev := range ch {
    switch ev.Type {
    case ac.EventToolExecStart:
        fmt.Printf("-> tool %s\n", ev.Tool)
    case ac.EventError:
        fmt.Printf("! error: %v\n", ev.Err)
    }
}
sum := wait() // *agentcore.RunSummary; nil on abort
```

The agent:
- Sees `user` and `feedback` memories on every turn (always-include).
- Sees `project` and `reference` memories when they're relevant.
- Can save and query facts via the `remember` / `recall` tools.
- Persists across restarts (with a durable Store).

## Safety: audit + fail-closed gate

Every `jess.New` agent is safe by default. Two controls are baked in:

**Audit.** Every tool request, gate decision (allow *or* deny), and run boundary is appended to a JSONL log under the user cache dir (`~/.cache/jess/audit.jsonl` on macOS/Linux). Blocked attempts are recorded before the call is denied, so rogue tool calls stay visible. Pass `jess.WithAudit(audit.DiscardSink{})` to turn it off explicitly.

**Fail-closed gate.** A tool not implementing `gate.SafeTool` (or returning `Safe() == false`) is denied unless an `Approver` is wired via `jess.WithApprover`. No approver means deny, not allow. `jess.AllowAll()` is the explicit, greppable opt-out. See [`examples/gated`](./examples/gated) for a stdin approver stand-in for an async Telegram confirm flow.

## Project layout

```
jess/
├── jess.go, adapters.go, gate_opts.go, audit_opts.go, subagent_opts.go
│                                 # New, Stream, Once, With* options
├── audit/                        # durable JSONL audit log (agentcore-free)
├── gate/                         # fail-closed tool gate (SafeTool, Approver, Policy)
├── memory/                       # the memory subsystem (agentcore-free)
│   ├── memory.go                 # Entry, Store, Query, Recaller, VectorStore
│   ├── kind.go                   # Kind constants, KindPolicy, KindRegistry
│   ├── tool.go                   # RememberTool (agentcore.Tool)
│   ├── recall_tool.go            # RecallTool (agentcore.Tool)
│   ├── store_inmemory.go         # InMemoryStore
│   ├── store_jsonl.go            # JSONLStore (durable, tombstones, Compact)
│   ├── store_chromem.go          # ChromemStore (vector, on chromem-go)
│   ├── recall.go                 # SimpleRecaller (token overlap)
│   ├── recall_vector.go          # VectorRecaller + HybridRecaller (RRF)
│   ├── embedder.go               # Embedder interface
│   └── embed/gomlx/              # in-process pure-Go embedder
├── skill/                        # skill bundles (agentcore-free)
│   ├── skill.go                  # Skill, Set, Loader
│   └── filesystem.go             # SKILL.md walker (Claude Code layout)
├── subagent/                     # bounded Pool for fan-out subagents
├── internal/core/                # Config + Agent builder, Once, Stream, audit middleware
└── examples/
    ├── quickstart/               # offline echo-model demo with memory
    └── gated/                    # stdin approver stand-in for Telegram confirm
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
runs are warm. Cache layout matches the Python `huggingface_hub`
client's, so a user who already has the model cached from another
tool gets a warm start for free.

The download path is pure Go HTTP via `gomlx/go-huggingface/hub` —
no `huggingface-cli` install, no Python, no `pip`. Just stdlib
`net/http` against `huggingface.co` (or `$HF_ENDPOINT` for
mirrors / air-gapped installs).

The model's license stays the model's license; jess takes no
position on redistribution.

**Embedder is replaceable.** `Embedder` is an interface. Ship Ollama
and OpenAI adapters when needed; hosts that prefer those swap one
line.

[gomlx-go]: https://github.com/gomlx/gomlx#purego-backend

## Development

Make targets wrap the checks CI runs:

```bash
make test           # go test -race ./...
make vet            # go vet ./...
make lint           # golangci-lint (config in .golangci.yml)
make license-audit  # go-licenses: fail on any GPL/AGPL-class dep
```

`lint` and `license-audit` need nothing installed beyond the Go
toolchain (both fall back to `go run` at a pinned version).

CI (`.github/workflows/test.yml`) runs all four on every push and PR
to `main`, plus a non-blocking `govulncheck`. Tests, lint, and the
license audit are blocking; a GPL/AGPL dependency fails the build.

A versioned pre-commit hook runs lint when `.go` files are staged and
the license audit when `go.mod` / `go.sum` change. Opt in once per
clone:

```bash
git config core.hooksPath .githooks
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow.

## Status

Pre-1.0. API may change before v1; the facade and its subpackages have
shipping implementations with test coverage. See
[CHANGELOG.md](CHANGELOG.md).

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

No GPL or AGPL anywhere in the tree. CI enforces this via
`make license-audit` (see Development).
