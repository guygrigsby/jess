# jess

Memory and skills for [agentcore](https://github.com/voocel/agentcore)-based
Go agents. The leather strap on the falcon's leg — the bits of harness that
keep an agent grounded across runs and let it pick up new capabilities.

## What it is

`jess` is a small set of extension packages for the `agentcore` agent loop:

- `jess/memory` — durable agent memory. A `Memory` interface plus a
  `ContextManager` adapter that injects relevant memory entries into the
  conversation before each model call, and an `OnMessage` adapter that
  persists turns to a `Store`. Default stores: file-backed (JSONL) and
  in-memory (testing). The memory storage backend is pluggable.
- `jess/skills` — registerable capability bundles. A `Skill` is a name,
  description, system-prompt contribution, and zero-or-more
  `agentcore.Tool` implementations. Skills load from disk
  (filesystem layout mirrors Claude Code skills) or by direct
  registration.

Neither package re-implements anything `agentcore` already does. The
harness, provider abstraction, tool dispatch, permission engine, and
context management are all upstream's job.

## Why it exists

`agentcore` ships a solid agent loop and tool surface but leaves memory
and skills to the host. Most agents want both. `jess` provides
opinionated-but-replaceable starting points so a host can wire either
in with a few lines of options.

## Status

Pre-1.0. Both packages are skeletons today — interfaces shipped, real
implementations landing in subsequent commits. API will change before
v1.

The `agentcore` import lands in `go.mod` when the first adapter type
arrives (planned: `memory.NewContextManager`). Until then, this
module has zero non-stdlib dependencies — `go.mod` is empty by
design, and `go mod tidy` will not stick a placeholder require there.

## Earlier design exploration

The first three commits of this repo were a clean-slate harness
prototype (tool dispatch, provider abstraction, multi-turn loop)
before we discovered `agentcore`. That code is preserved in git
history (commits a558e26, 8d2eb4b, cd4d63d) but no longer ships;
it served as paid-for design exploration that informed the API
shape `jess` and `agentcore` overlap on. The repo's scope pivoted
to memory + skills once `agentcore` proved out as a viable core.

## License

MIT. See [LICENSE](./LICENSE). `agentcore` is Apache 2.0; the
license combination is fine for downstream MIT projects.
