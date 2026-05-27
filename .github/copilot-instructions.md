# Copilot instructions for jess

## What this repo is

`jess` is two extension packages on top of [`agentcore`](https://github.com/voocel/agentcore): durable agent memory (`memory/`) and registerable capability bundles (`skills/`). It is pure Go, no CGO (to preserve cross-compilation). It deliberately does not reimplement the agentcore harness, providers, tool dispatch, permission engine, or compaction.

An architectural change is in progress (ADR 0001): encapsulating agentcore behind a jess domain facade, delivered in phases. New domain packages (`message/`, `tool/`, `event/`, and later `subagent/` and the root `jess` package) are being added, with agentcore confined to `internal/acl/`.

## Review this design context before reviewing code

When reviewing a pull request, first read the design documents that correspond to the change, then review the code against them:

1. The relevant ADR under `docs/adr/` (for the current architecture work, `docs/adr/0001-encapsulate-agentcore-into-jess.md`). Understand the decision, the bounded contexts, the anti-corruption-layer boundary, and the stated invariants.
2. The matching implementation plan under `docs/superpowers/plans/`. These plans are phased; a PR usually implements one phase. Check the PR against the plan's task list and its declared scope.

Then review the code itself. Judge it against the ADR and plan: flag deviations from the documented design, scope creep beyond the phase, and any requirement in the plan that the code does not satisfy. Do not ask for work that a later phase explicitly owns.

## Conventions to enforce

- Pure Go, no CGO. Reject anything that introduces CGO without explicit discussion.
- Dependency licenses: MIT, Apache-2.0, MPL-2.0, or BSD only. No GPL or AGPL.
- Anti-corruption-layer boundary: `github.com/voocel/agentcore` must be imported only by files under `internal/acl/`. Flag any import of it elsewhere. The domain packages (`message`, `tool`, `event`, `subagent`, root `jess`) must stay vendor-free.
- Memory failures must never block an LLM call: the context-manager path degrades to no-memory, never no-agent. Preserve this when reviewing the inject path.
- Do not vendor or fork dependencies to add features; missing upstream capability is filed upstream with a local TODO referencing the issue.
- Documentation density: every exported type and function has a godoc; non-trivial design decisions get a short paragraph on why, not just what.
- Tests: prefer table-driven tests. Concurrency code must be exercised under the race detector (`go test -race`).

## Build and validation

```bash
go vet ./...
go test -race ./...
make lint           # golangci-lint, config in .golangci.yml
make license-audit  # go-licenses: fails on GPL/AGPL-class deps
```

`make lint` and `make license-audit` are what CI gates on (plus a non-blocking `govulncheck`). The embedder end-to-end test downloads model weights and is gated behind `JESS_EMBEDDER_E2E=1`; it is skipped by default.
