# Contributing to jess

Bug reports, design discussion, and PRs are all welcome. The repo is
small enough that there's no formal triage process.

## Reporting issues

Use [GitHub Issues](https://github.com/guygrigsby/jess/issues).
Useful issues include:

- Go version (`go version`)
- OS / arch
- Minimal reproducer (a failing test is ideal; a snippet that
  panics is fine)
- What you expected vs what happened

For embedder failures (GoMLX errors, model download issues), include
the full error chain — they often surface as "X is not implemented in
the simplego backend" and we want to file those upstream.

## Pull requests

Before opening a PR:

```bash
go vet ./...
go test -race ./...
```

`-race` catches concurrent-access bugs in the stores and recallers
that show up under real workloads.

If you touch `go.mod` / `go.sum`, also run the dependency license
audit (CI enforces it):

```bash
make license-audit
```

It fails on any GPL/AGPL-class dependency. Enable the versioned
pre-commit hook once per clone so the audit runs automatically when
you stage `go.mod` / `go.sum`:

```bash
git config core.hooksPath .githooks
```

For PRs touching the GoMLX embedder, also run:

```bash
JESS_EMBEDDER_E2E=1 go test -timeout 5m ./memory/embed/gomlx/...
```

That downloads `~90MB` of model weights on first run and validates
the full tokenize→inference→pool→normalize pipeline. Subsequent runs
hit the on-disk cache.

## Design rules

A few non-obvious conventions that have bitten us if forgotten:

- **Don't vendor or fork deps to add features.** If a dep is missing
  something, file upstream and contribute back. Local workarounds
  get a TODO referencing the upstream issue and get removed when
  it lands.
- **No GPL / AGPL deps.** Ever. MIT, Apache 2.0, MPL 2.0, BSD-2/3
  are all fine.
- **Pure Go, no CGO.** The whole point of the embedder choice (GoMLX
  over ONNX Runtime FFI) was preserving cross-compile. Reject PRs
  that introduce CGO without an explicit discussion in the issue
  thread.
- **`memory.Store` is the canonical contract.** New storage backends
  satisfy `Store`. Vector-aware ones additionally satisfy
  `VectorStore` (capability interface). Don't change the `Store`
  contract; extend via new interfaces.
- **Memory failures must NOT block LLM calls.** The `ContextManager`
  swallows errors from `Store`/`Recaller` rather than aborting the
  agent run. A broken memory layer should degrade to no-memory,
  not no-agent.

## Module layout

- `memory/` and `skills/` are top-level subpackages with their own
  godoc and independent surface area.
- `memory/embed/<name>/` subpackages hold concrete `Embedder`
  implementations. New embedders (Ollama, OpenAI, etc.) land here.
- Tests live alongside their code. There are no separate
  `internal/testdata` directories yet; if one becomes useful, add
  it under the relevant subpackage.

## Style

- Match the existing code's comment density: each exported type and
  function has a godoc; non-trivial design decisions get a paragraph
  explaining why, not just what.
- No leading "I" / "I'm" in commit messages or docs.
- Prefer the fewest words that convey the meaning.
- No em dashes or en dashes in commit messages or docs (period,
  comma, parens, or a rewrite instead).
