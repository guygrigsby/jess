# ADR 0004: MCP adapter as a jess package

**Status:** Accepted
**Date:** 2026-09-04

## Context

gyr built `internal/mcp` to adapt config-allowlisted MCP stdio servers into agentcore tools (gyr ADR 0002). autophage needs the same adapter to reach a toolbox process inside a podman sandbox over `podman exec -i`. Go's `internal` rule forbids importing it across modules, and a copy would fork a tested anti-corruption layer.

## Decision

Move the adapter into jess as `jess/mcp`. It owns the MCP client SDK; it exposes `Server` (jess's own config type) and `Tools(ctx, servers, logf) ([]ac.Tool, io.Closer, error)`. Two additions: `Server.Bare` exposes unprefixed tool names for single-server hosts, and a text result that is valid JSON passes through unwrapped so tools that already return JSON are not double-wrapped. Adapted tools stay non-safe; the host's gate decides.

## Consequences

- gyr and autophage share one adapter. gyr deletes `internal/mcp` and maps its config to `mcp.Server`.
- jess gains a dependency on `github.com/modelcontextprotocol/go-sdk`; only `jess/mcp` imports it, and hosts that do not import `jess/mcp` do not link it.
- A JSON-shaped text result now reaches the model unwrapped. Hosts that relied on the `{"output": ...}` envelope for JSON text see a different shape.
