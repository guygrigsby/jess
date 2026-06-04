# ADR 0002: agentcore as a direct, exposed dependency

**Status:** Accepted
**Date:** 2026-06-04
**Supersedes:** ADR 0001 (Encapsulate agentcore behind a jess facade)

## Context

ADR 0001 hid agentcore behind an anti-corruption layer (`internal/acl`) and a parallel type universe (`jess/tool`, `jess/message`, `jess/model`, `jess/event`). The stated goal was harness-swappability: if agentcore's API changed or a better harness emerged, jess could swap the internal plumbing without breaking callers.

In practice, the ACL became a tax with no payout. The parallel types duplicated agentcore's types without adding anything, callers had to translate between worlds, and every agentcore addition required a parallel shim. The harness-swappability thesis also weakened: jess is purpose-built for agentcore's model; abstracting over it produced worse ergonomics for callers, not better.

Meanwhile, the work that needed doing was safety controls: audit trails for remote operation, a fail-closed tool gate, and a clean abort path. Those are first-class concerns, not afterthoughts behind a wrapper.

## Decision

Drop the anti-corruption layer and expose agentcore's types directly. `jess.New` returns `*agentcore.Agent`. `jess.Stream` takes `*agentcore.Agent`. The parallel type packages are deleted. Callers import `agentcore` alongside `jess` and the types match.

Three safety controls are baked in as first-class packages: `jess/audit` (durable JSONL event log), `jess/gate` (fail-closed tool gate with a SafeTool marker and an Approver hook), and abort via `context.Context` cancellation passed to `jess.Stream`. The gate records denied calls to audit so blocked attempts stay visible.

## Portability insurance

Harness-swappability is preserved exactly where it can be: `jess/memory` and `jess/skill` remain agentcore-free. They have no agentcore imports. A future swap would need to re-wire the thin `internal/core` adapter layer, not rewrite the memory or skill subsystems.

## Consequences

- The module is simpler: no translation layer, no duplicate types, no boundary test.
- Callers get the full agentcore API surface directly. They can use `agentcore.NewAgent` with `jess.NewMemoryManager` for cases where the `jess.New` option set doesn't cover their needs.
- The safety controls are explicit and greppable: `AllowAll()` is a named function, not an absent config key. Turning off audit requires passing `audit.DiscardSink{}` explicitly.
- `jess/memory` and `jess/skill` stay importable without pulling in agentcore.
