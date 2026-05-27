# ADR 0001: Encapsulate agentcore behind a jess domain facade

- Status: Proposed
- Date: 2026-05-27
- Deciders: Guy Grigsby

## Context

Today jess is a *library of parts*. It hands the host a `memory.ContextManager`,
a `skills.Set`, and a set of `agentcore.Tool`s, and the host constructs and
drives the `agentcore.Agent` itself. agentcore types appear in jess's public
API across four files (`doc.go`, `memory/context_manager.go`,
`skills/agentcore.go`, `examples/quickstart`). jess never owns an agent run.

We want to invert this. jess should be the thing the host calls: all agent
calls go through jess, and jess owns memory, skills, and subagent
orchestration. jess also needs an event stream of its own that wraps
agentcore's event stream, so a host can observe a run (including subagent
activity) without ever touching agentcore.

This is a large architectural change, so it gets domain-driven design: explicit
bounded contexts, a named ubiquitous language, aggregates with stated
invariants, and an anti-corruption layer at the vendor boundary.

Constraints carried from the project:

- Pure Go, no CGO (preserve cross-compile).
- No GPL/AGPL deps; MIT/Apache-2.0/MPL-2.0/BSD only.
- Do not reimplement what agentcore already does well (providers, the loop,
  permission engine, compaction). Wrap, do not fork.
- Memory failures must never block an LLM call.

A late but load-bearing requirement: subagents must be **fast, lightweight, and
abundant** (thousands if necessary) with **airtight** concurrency (no leaked
goroutines, bounded resources, clean cancellation). This requirement overrides
an earlier inclination to wrap agentcore's `subagent.Tool` (see Decision 5).

## Decision

### 1. Anti-corruption layer: agentcore is a hidden implementation detail

jess defines its own domain types (`Agent`, `Session`, `Run`, `Message`,
`Event`, `Tool`) and translates to and from agentcore at a single boundary.
agentcore is importable only inside `internal/acl/`. No agentcore type
appears in any jess public API.

This is enforced mechanically: a test asserts that `voocel/agentcore` is
imported only by files under `internal/acl/`. That grep is the
machine-checkable definition of "fully encapsulated"; if it fails, the ACL has
leaked.

### 2. Bounded contexts and package layout (full reorg)

| Context | Responsibility | Aggregate / key types |
|---|---|---|
| Session (root `jess`) | Drive a conversation run; the host's facade | `Agent` (root), `Session`, `Run` |
| Conversation (`jess/message`) | The messages the domain speaks in | `Message`, `ContentBlock`, `Role` |
| Events (`jess/event`) | The observable stream wrapping a run | `Event`, `EventKind`, `Stream` |
| Tools (`jess/tool`) | The capability the model invokes | `Tool` |
| Memory (`jess/memory`) | Durable recall (existing) | `Store`, `Recaller`, `Entry`, `Kind` |
| Skills (`jess/skill`) | Capability bundles (existing) | `Skill`, `Set` |
| Orchestration (`jess/subagent`) | Bounded subagent scheduling | `Spec`, `Pool`, `Task` |
| ACL (`internal/acl`) | The only importer of agentcore | runtime, translate, pool runner |

```
jess/
├── agent.go            # Agent aggregate root: New, Prompt, Continue, Events, NewSession
├── session.go          # Session: Prompt, Continue, Steer, FollowUp, Events, Abort
├── run.go              # Run: result handle + Summary
├── options.go          # functional options (model, memory, skills, subagents, pool limits)
├── message/            # Message, ContentBlock, Role
├── event/              # Event, EventKind, Stream
├── tool/               # Tool interface
├── model/              # vendor-free Model interface, ToolSpec, Response, Usage
├── memory/             # existing; agentcore adapter removed
├── skill/              # renamed from skills/; agentcore.go removed
├── subagent/           # Spec, Pool, Task (orchestration)
└── internal/
    └── acl/            # anti-corruption layer: sole import of voocel/agentcore
        ├── runtime.go       # constructs & drives agentcore.Agent (top-level Session)
        ├── looprunner.go    # runs agentcore.AgentLoop per subagent Task
        ├── translate.go     # Event/Message/Tool/SystemBlock translation (pure fns)
        └── subagenttool.go  # LLM-facing subagent tool, backed by the Pool
```

Note on tool results: there is no separate `ToolResult` domain type. A tool
result is a `message.ContentBlock{Kind: BlockToolResult}` (carrying `ToolID`,
`Result`, `IsError`). The ACL maps that to and from agentcore's `ToolResult`
(and its `RoleTool` message encoding), which is the only place the two shapes
meet. (agentcore represents a tool call as a `ContentBlock` but a tool result
as a whole `RoleTool` message, so the ACL translation is real field mapping,
not a pass-through wrap. The wrap-without-copying property holds only for the
`Tool` interface itself.)

### 3. Aggregates and ubiquitous language

Three aggregates with distinct lifetimes:

- **Agent** is *who*: identity and capabilities (model, skills, subagent specs,
  and the `AgentID` that scopes memory). Long-lived, reusable, goroutine-safe.
  Memory belongs to the Agent and persists across conversations.
- **Session** is *a conversation with that Agent*: holds message history, runs
  `Prompt`/`Continue`, emits `Events`, can `Abort`. Short-lived. Message history
  belongs to the Session.
- **Run** is one `Prompt`-to-stop cycle inside a Session, carrying a `Summary`
  (turn count, tool calls, end reason; jess's own type).

Separating Agent from Session is the key modeling call. It gives two aggregates
with different invariants and lifetimes (memory vs message history), and it
makes subagents fall out cleanly: a subagent is an Agent spawned into its own
run.

Ergonomics: `Agent.Prompt/Continue/Events` delegate to a lazily-created default
Session, so the common single-conversation case is one line. Hosts that need
parallel or per-user conversations call `Agent.NewSession()` explicitly.

```go
agent, _ := jess.New(opts...)        // configure once
run, _   := agent.Prompt(ctx, "hi")  // default session
for ev := range agent.Events() { ... }

sess   := agent.NewSession()          // advanced: explicit sessions
run, _ := sess.Prompt(ctx, "hi")
```

**The model crosses the ACL as a vendor-free `model.Model` interface.** A raw
`agentcore.ChatModel` cannot enter jess's public API: its methods speak
agentcore message types, so accepting one would force the root `jess` package
to import agentcore and break the boundary. Instead jess defines `model.Model`
in jess-domain terms. This supports *all* model kinds, local and custom
included: a host implements `model.Model` directly for an in-process or private
model, while jess ships cloud constructors (e.g. `jess.LiteLLM(provider, model,
…)`) that build agentcore's litellm-backed model inside `internal/acl` and
return it as a `model.Model`.

`model.Model` is streaming-first; `Stream` is the only primitive:

    Stream(ctx, []message.Message, []ToolSpec) (<-chan Chunk, error)
    SupportsTools() bool

A `Chunk` carries an incremental `Delta` + `DeltaKind` (text/thinking/toolcall)
and, on the final chunk, `Done=true` with the complete assistant `Message`.
This keeps token-level streaming for local models (the event stream's whole
point), not just cloud. A `model.Once(genFn)` helper wraps a one-shot function
as a single-`Done`-chunk stream so trivial local models stay trivial.

The agent loop is agentcore's, so the ACL wraps any `model.Model` into an
`agentcore.ChatModel`:

  - `ChatModel.GenerateStream` maps each jess `Chunk` to an agentcore
    `StreamEvent` (`text_delta`/`thinking_delta`/`toolcall_delta`, then
    `StreamEventDone{Message, StopReason}` — the shape agentcore's own litellm
    adapter emits and the loop consumes).
  - `ChatModel.Generate` is derived by draining the stream to the final message.
  - For a jess-provided cloud model the wrapper is a native passthrough (the
    underlying value already is an `agentcore.ChatModel`, so the ACL
    type-asserts and uses it directly — zero translation).

### 4. Event stream: a thin domain stream, not go-eventlogger

`jess/event.Stream` is a fan-out over a channel. `Session.Events()` returns
`<-chan event.Event`, closed on `run_end`. `Event` is kind-tagged and flattened
from agentcore's wide `Event` struct in the ACL translator. An `AgentPath`
field (`nil` for the root, `["research/0007"]` for a subagent) makes subagent
aggregation first-class: one ordered stream where `AgentPath` says which agent
in the tree emitted each event.

**go-eventlogger was evaluated and rejected.** It is MPL-2.0 (license-compatible)
and is a capable Broker -> Pipeline (Filter -> Formatter -> Sink) router. But its
value is runtime-configurable routing to *multiple pluggable sinks*
(file/syslog/OTel), and the stated consumers are only the host UI and subagent
aggregation. For "one stream the host ranges over," its formatter/sink ceremony
is pure overhead. The `event` package is designed so a go-eventlogger bridge
could be added later as an opt-in sink if pluggable telemetry ever becomes a
real requirement; we do not pay for it now.

### 5. Subagent orchestration: a jess-owned bounded Pool

agentcore's `subagent.Tool` is **not** wrapped for the general case. It hard-caps
at `maxConcurrency = 4` / `maxParallelTasks = 8` (unexported), and it is an
LLM-call-driven abstraction. Neither fits the "thousands, programmatic,
airtight" requirement.

Instead, `jess/subagent.Pool` is the orchestration aggregate:

- **Lightweight runs.** Each subagent `Task` runs via `agentcore.AgentLoop`
  (the channel-returning loop function), not the heavyweight `agentcore.Agent`
  struct. One cheap goroutine per *running* task; a queued task is just a struct.
- **Bounded concurrency** via `golang.org/x/sync/errgroup` with `SetLimit(N)`
  (BSD-licensed, standard-adjacent; no hand-rolled pool). errgroup gives bounded
  concurrency, leak-free lifecycle (`Wait` blocks until every task exits), and
  context-cancellation propagation in one primitive.
- **Bounded queue, blocking submit.** `Submit(ctx, task)` blocks when the queue
  is full (and returns on ctx cancellation). Hard cap on pending + running, so
  "spawn thousands" cannot blow up memory.
- **MPSC event fan-in, no mutex.** Every child run sends tagged events to one
  shared buffered channel; a single merger goroutine drains it onto the parent
  Stream. Lock-free on the hot path, serialized by construction. Backpressure is
  automatic: a slow host consumer fills the channel and child sends block.
- **Airtight cancellation.** Parent `ctx` -> errgroup `ctx` -> every child loop.
  `Session.Abort()` cancels the parent; `Pool.Wait()` guarantees all children
  exit before teardown. No orphans.
- **Identity and safety at scale.** `AgentPath` carries name + instance id so
  thousands are distinguishable and nesting is preserved. A `MaxDepth` guard
  caps recursion so a runaway tree cannot fork-bomb.

The same Pool also backs an **LLM-facing subagent tool** (in the ACL), so the
model can self-delegate; that path feeds the identical scheduler rather than
agentcore's capped tool.

Configurable, with sane defaults: `MaxConcurrent`, `MaxQueued`, `MaxDepth`.

### 6. Concurrency model (what jess guarantees vs the caller)

jess owns the hard concurrency. The caller's only contract is the natural one:
do not drive a single Session from two goroutines at once.

- **Agent**: goroutine-safe and shareable. Config is immutable after `New`;
  `Store` and `skill.Set` are already concurrency-safe. One Agent can back many
  Sessions across goroutines.
- **Session**: one active Run at a time. A second `Prompt` while a Run is in
  flight returns `ErrRunInProgress`. Intentional in-run input uses
  `Session.Steer(msg)` (inject into the running loop) or `Session.FollowUp(msg)`
  (queue for after), surfacing agentcore's real capabilities as first-class jess
  methods. Reads are safe under lock.
- **Stream**: single consumer per Session (idiomatic Go). Every producer (main
  translator and all subagent mergers) is serialized inside jess.
- **Subagents**: fully owned by jess as described in Decision 5.

### 7. Error handling (preserve the cardinal invariant)

- **Config errors** (`jess.New`, bad option/model) return synchronously from
  `New`/`NewSession`, never via the Stream.
- **Run-time errors** flow as `event.Event{Kind: error}` and end the run.
- **Memory errors** stay invisible by design: the memory context manager
  adapter (moved into `internal/acl/`) keeps swallowing Store/Recaller
  errors and degrades to no-memory, never no-agent.
- **Subagent failures** are isolated: a dying child produces a `tool_end` event
  with `IsError` plus an `error`-kind event tagged with its `AgentPath`; the
  parent Session continues.

### 8. Migration: clean break (pre-1.0)

- `skills/` -> `skill/`; `skills/agentcore.go` deleted from the public package
  (logic moves to the ACL).
- `memory/`: `context_manager.go` moves to `internal/acl/`;
  `Store`/`Recaller`/`Entry`/`Kind` and the tools stay, with tools implementing
  `jess/tool.Tool` (structurally unchanged).
- New packages: `jess` root, `jess/message`, `jess/event`, `jess/tool`,
  `jess/subagent`, `internal/acl`.
- `examples/quickstart` rewritten to the facade.
- `README.md` rewritten: it currently describes jess as a "library of parts"
  (the host wires the `agentcore.Agent`); the cutover replaces that with the
  facade model (`jess.New` -> `Agent`/`Session`), the new package map, and the
  subagent Pool. Done in Phase 4 so it never documents an unbuilt API.
- CHANGELOG records the break. No deprecation shims (pre-1.0).

## Alternatives considered

- **Thin facade / type aliasing.** Re-export agentcore types instead of mirroring
  them. Rejected: does not achieve encapsulation; agentcore stays in the public
  API and cannot be swapped.
- **Wrap agentcore `subagent.Tool`.** Rejected: capped at 4/8 concurrency
  (unexported) and LLM-call-driven; cannot meet the thousands/airtight
  requirement. (Contributing configurable limits upstream would help the
  LLM-driven case but still not the programmatic fan-out case.)
- **go-eventlogger as the internal bus.** Rejected: its sink-routing model
  serves pluggable telemetry, which is not a stated consumer. Kept as a possible
  future opt-in bridge.
- **Merge Agent and Session into one type (agentcore-style).** Rejected: conflates
  memory and message lifetimes and forces a separate Agent (and memory scope)
  per parallel conversation.
- **Block-on-busy Session, or leave concurrency to the user.** Rejected: hidden
  blocking invites deadlock and hides Steer/FollowUp; "leave to the user"
  contradicts "jess owns concurrency."
- **Mutex-based subagent event merge.** Rejected: lock contention at thousands of
  producers. Replaced with MPSC fan-in.

## Consequences

Positive:

- agentcore is swappable behind a tested boundary; the domain model is clean and
  vendor-free.
- Subagents scale to thousands with bounded resources and airtight teardown.
- Concurrency guarantees are explicit and owned, not delegated to callers.
- The memory-never-blocks invariant is preserved, just relocated.

Negative / costs:

- Translation code to write and maintain (message/event/tool), and a single
  place that must track agentcore's `Event` shape across upgrades.
- More packages and a one-time import-path break for any existing consumer.
- New dependency `golang.org/x/sync` (BSD; license-clean).

Risks:

- agentcore `AgentLoop`/`Event` semantics could change upstream; mitigated by
  concentrating all coupling in `internal/acl/` and the table-driven
  translation tests.
- "Thousands of subagents" still implies thousands of LLM calls over time;
  `MaxConcurrent` plus provider rate limits, not the Pool alone, govern real
  throughput. The Pool bounds resources, not provider cost.

## Testing and enforcement

- ACL translation: exhaustive table-driven tests over every `EventKind` and
  `ContentBlock` variant, including error cases (pure functions, cheap to total).
- Boundary test: assert `voocel/agentcore` is imported only under
  `internal/acl/`.
- Session/runtime: a scripted fake `agentcore.ChatModel` drives a real loop
  through the ACL; assert the jess `Event` sequence and `Run.Summary`.
- Subagent Pool: assert bounded concurrency (never exceeds `MaxConcurrent`),
  blocking submit at the queue cap, `AgentPath` tagging/ordering on the parent
  Stream, and leak-free teardown under `Abort` (goroutine count returns to
  baseline after `Wait`).
- `-race` throughout; the Stream fan-out and Pool are concurrent by nature.
