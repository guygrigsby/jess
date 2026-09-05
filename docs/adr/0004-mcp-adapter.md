# ADR 0004: MCP adapter as a jess package

**Status:** Accepted
**Date:** 2026-09-04

## Context

gyr built `internal/mcp` to adapt config-allowlisted MCP stdio servers into agentcore tools (gyr ADR 0002). autophage needs the same adapter to reach a toolbox process inside a podman sandbox over `podman exec -i`. Go's `internal` rule forbids importing it across modules, and a copy would fork a tested anti-corruption layer.

## Decision

Move the adapter into jess as `jess/mcp`. It owns the MCP client SDK; it exposes `Server` (jess's own config type) and `Tools(ctx, servers, logf) ([]ac.Tool, io.Closer, error)`. Two additions: `Server.Bare` exposes unprefixed tool names for single-server hosts, and a text result that is valid JSON passes through unwrapped so tools that already return JSON are not double-wrapped. Adapted tools stay non-safe; the host's gate decides.

Addendum, 2026-09-05, autophage's first real use surfaced three refinements:

- `Tools` no longer always returns a nil error. When at least one server was requested and every one failed to dial or list, it returns an error naming each server and its failure, with no tools and a closer already safe to close. A partial failure (at least one server dials and lists) still returns a nil error with the working servers' tools, unchanged from before.
- An MCP `IsError` tool result is the tool's own reported failure, meant for the model to read and self-correct. `callTool` now returns it as ordinary text (prefixed `error: `) with a nil error instead of a Go error, so agentcore's consecutive-failure breaker does not disable the tool over it. A Go error from `callTool` now means a transport or protocol failure, including a closed server connection, which wraps the package's own `ErrServerGone` sentinel.
- The JSON passthrough only applies to JSON containers: a result whose first non-space byte is `{` or `[`. A scalar or quoted string that happens to be valid JSON still gets the `{"output": ...}` envelope, since unwrapping it would hand the model a bare value it did not ask for.

## Consequences

- gyr and autophage share one adapter. gyr deletes `internal/mcp` and maps its config to `mcp.Server`.
- jess gains a dependency on `github.com/modelcontextprotocol/go-sdk`; only `jess/mcp` imports it, and hosts that do not import `jess/mcp` do not link it.
- A JSON-shaped text result now reaches the model unwrapped. Hosts that relied on the `{"output": ...}` envelope for JSON text see a different shape.
