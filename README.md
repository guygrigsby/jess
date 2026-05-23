# jess

A Go agent harness. The leather strap on the falcon's leg — the runtime that
holds the loop, dispatches tools, and streams the model's work back to
whoever's flying the bird.

## What it is

`jess` is the streaming model-loop runtime extracted from
[talon](https://github.com/guygrigsby/talon). It owns:

- the multi-turn iterate-until-no-more-tool-calls loop
- provider abstraction (any backend that can stream chat completions)
- tool dispatch (any function the host wires up)
- usage accounting and per-iteration cap enforcement
- structured event streaming to a caller-supplied sink

It does **not** own:

- transport (WebSocket / HTTP / RPC framing — caller's job)
- configuration (caller injects providers, tools, caps)
- credential resolution (caller hands `jess` already-authed providers)
- session persistence (caller supplies a store interface)

## Status

Pre-1.0. API will change. Versioning will pin once the boundary settles.

## License

MIT. See [LICENSE](./LICENSE).
