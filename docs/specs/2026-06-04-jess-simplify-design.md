# jess simplification: drop the anti-corruption layer, expose agentcore (2026-06-04)

## What and why

jess stops wrapping agentcore behind a parallel type universe and instead speaks agentcore's types directly. `jess.New` becomes an option-assembler that returns a real `*agentcore.Agent`, pre-wired with memory, skills, tools, a tool gate, and subagents. jess is an ergonomics + memory/skills layer on agentcore, not a harness abstraction over it.

This supersedes ADR 0001 (encapsulate agentcore behind a jess facade) and the "Path 1" recommendation in docs/audits/2026-06-04-jess-usability-and-pinion.md. The audit assumed the facade was worth keeping if jess had a real consumer. It does now, and the consumer's needs (below) are simpler than the facade serves. Reading agentcore's interfaces directly settled it: Tool is identical to jess/tool, Message is richer than jess/message, and the only things the wrapper bought were two small adapters and the memory ContextManager. Everything else translated agentcore into a near-copy of itself.

Surveyed the alternatives (eino, langchaingo, Google ADK, openai-agents-go, AgenticGoKit, Genkit, Jetify). Nothing lighter fits better: jess leans on two agentcore seams most light frameworks lack, a per-turn ContextManager.Project (memory injection without mutating history) and a pre-tool ToolGate (per-call allow/deny). agentcore stays the base.

## Motivating consumer

A personal agent server on this Mac (the openclaw/talon lineage). Telegram in, Telegram out, runs as a launchd daemon, no web UI. It restarts services, sets reminders, handles small ops, and asks for confirmation before destructive actions. It is a separate service repo scaffolded from rookery; jess is a dependency. Its needs of jess are exactly: construct an agent in a few lines, register a handful of specific tools, gate dangerous ones, drive it per inbound message. Memory comes later. That shapes this refactor: make the easy construction path and the gate first-class, delete everything that does not serve it.

Security tenet carried into the daemon (recorded here because it shapes what jess must make easy): trust comes from a surface of narrow, specific tools whose effect is obvious by construction (`restart_service(name)`), not from labeling arbitrary tools' effects (pinion) or gating a firehose (bash). Gated bash is the v1 scaffold; the trajectory is toward many specific tools. jess's job is to make registering many small tools plus a gate trivial.

## Target API

Door 1, easy. jess assembles agentcore options and returns the agent:

```go
agent := jess.New(
    jess.WithModel(model),              // agentcore.ChatModel, or jess.Once(fn)
    jess.WithMemory(store, recaller),    // wires the ContextManager (optional)
    jess.WithSkills(set),                // -> system blocks + tools
    jess.WithTools(tools...),            // agentcore.Tool, no wrapper
    jess.WithSubagents(specs...),        // builds + owns the pool, wires the tool
    jess.WithApprover(telegramConfirm),  // gate: dangerous ops ask here; SAFE DEFAULT if omitted
    // audit is ON by default to a durable JSONL log; jess.WithAudit(sink) to redirect
    jess.WithAgentcoreOptions(           // passthrough for the long tail
        agentcore.WithMaxTurns(20),
    ),
)
ch, wait := jess.Stream(ctx, agent, "restart the nginx service")  // ctx cancel == kill switch
for ev := range ch { /* agentcore.Event */ }
res := wait()
```

`jess.New` keeps a thin Option type (not raw agentcore options) because it does cross-cutting wiring loose option-helpers cannot: subagents inheriting the parent's model/store, integrating the Stream helper, and installing the audit + gate defaults (next section). Everything else agentcore offers reaches through `WithAgentcoreOptions` so jess does not re-declare it.

Door 2, compose your own. The pieces are agentcore-native and public, so no facade is needed:

```go
agent := agentcore.NewAgent(
    agentcore.WithModel(model),
    agentcore.WithContextManager(jess.NewMemoryManager(store, recaller)),
    agentcore.WithTools(tools...),
    agentcore.WithToolGate(gate),
)
```

## First-class safety constructs (audit, gate, abort)

This is the load-bearing part, not a feature flag. Local, at the keyboard, the human has three controls: they watch output, they refuse or approve the dangerous thing, and they Ctrl-C. Over a remote channel (Telegram) all three are gone. jess bakes back exactly those three, on by default. A rogue or confused agent reachable over Telegram with no human watching is the threat being designed against.

These are net additions, justified because agentcore provides neither audit nor a fail-closed gate policy, and they are jess's actual reason to exist beyond memory. The wrapper deletion still dwarfs them.

### Audit (detective control): know everything

New package `jess/audit`, agentcore-free and portable like `memory/`.

- `audit.Event`: Time, AgentPath, Kind (`prompt | model_response | tool_request | gate_decision | tool_result | abort | run_end`), Tool, Args, Label, Preview, Gate outcome (allowed/denied/needs-approval + reason + how it was approved), Result, Err, Duration.
- `audit.Sink` interface: `Record(Event)`. Default implementation is durable append-only JSONL at a standard path (mirrors `memory.JSONLStore` and codebot's storage). Queryable after the fact ("what did you do today").
- ON by default. `jess.New` installs the default sink unless `jess.WithAudit(sink)` redirects it. Turning audit off requires an explicit discard sink, never silent.
- Wiring (in the root `jess` package, which is agentcore-aware): an audit `ToolMiddleware` records every execution with result/error/timing; the gate hook records every decision including denials, BEFORE execution, so attempted-but-blocked dangerous calls are captured; run events record prompts, responses, and aborts. The request is logged even on deny. That is how a rogue agent's blocked attempts become visible instead of vanishing.

### Gate (preventive control): fail-closed by default

- Omitting a gate does NOT mean allow-all. `jess.New` installs a default policy: a tool that declares itself safe runs frictionlessly; anything unmarked or dangerous routes to the approver; deny if no approver is set. Permissiveness is opt-in via an explicit, greppable `jess.AllowAll()`.
- Tool risk is declared by a marker interface (the narrow-tool tenet): a tool implements `jess.SafeTool` (or returns a risk level) to be auto-approved. This is what makes safe-by-default also friction-free: most tools are safe by construction, so only the dangerous few interrupt.
- `jess.WithApprover(fn)` supplies the human decision for non-safe calls. The approver receives the resolved label and `Previewer` output (the exact command), so the daemon's Telegram prompt shows `launchctl kickstart -k system/nginx` and waits for YES/NO. Under the hood this is an `agentcore.ToolGate` plus the default classifier; `jess.WithToolGate` remains available for a fully custom gate.
- codebot's `internal/approval` (bash_classify, dangerous_paths, destructive_warning) is the classifier reference, reimplemented here since it is not importable.

### Abort (interventive control): reachable kill switch

- `ctx` threaded through `jess.Stream` and `Abort` wired so cancelling the context stops the run and in-flight tools. The daemon binds a Telegram "stop" to this. jess's responsibility is only to guarantee ctx/abort are reliable and threaded; process-group kill for spawned shells is the daemon's job.

### Convenience is preserved

You wire nothing to be safe. Audit is automatic, safe tools run without prompting, only dangerous ops ping you, and stop is one message. Safe-by-default and convenient-by-default are the same default here, because the tool surface is narrow and specific.

## Deletes

- `jess/tool` (use `agentcore.Tool`).
- `jess/message` (use `agentcore.Message`).
- `jess/model` (use `agentcore.ChatModel`; keep the `jess.Once` adapter, see below).
- `jess/event` (use `agentcore.Event`; keep the `jess.Stream` helper).
- `internal/acl/translate.go`, `internal/acl/model.go`, the `Runtime` wrapper in `internal/acl/runtime.go`.
- Root facade types: `Agent`, `Session`, `Run` and their files (`agent.go`, `session.go`, `run.go`), `errors.go`'s wrapped error.
- `internal/acl/boundary_test.go` (the boundary it enforces is gone by design).

Estimate: 1,500 to 2,500 LOC removed.

## Survives, lifted public where needed

- `memory/*` unchanged and stays agentcore-free. Store/Recaller/Entry/Kind remain reusable primitives. This is the portability insurance: if agentcore ever dies, memory and skills survive and only `jess.New` plus the adapters re-point.
- The memory ContextManager adapter moves from `internal/acl/context_manager.go` to the root `jess` package as `jess.NewMemoryManager(store, recaller, opts) agentcore.ContextManager`. It already imports only `memory` + agentcore, so the move is mechanical. `jess.New(WithMemory(...))` uses it internally; Door 2 calls it directly.
- `skill/*` stays. Its system-block and tool builders (currently `internal/acl/skills.go`) become public, producing `[]agentcore.SystemBlock` and `[]agentcore.Tool`.
- `subagent/*` survives but is rewired (its own section below).
- New: `jess.New` + its options, `jess.Once`, `jess.Stream`, `jess.WithApprover` / `jess.WithToolGate` / `jess.AllowAll` + the `jess.SafeTool` marker + the default classifier, `jess.WithAgentcoreOptions`.
- New package `jess/audit` (agentcore-free): `audit.Event`, `audit.Sink`, default JSONL sink. On by default via `jess.New`.

## The two kept adapters

- `jess.Once(fn GenerateFunc) agentcore.ChatModel`. agentcore's ChatModel asks for both `Generate` and `GenerateStream`; this keeps a one-shot local model a single function. Lifted from the existing `internal/acl/model.go` streamAdapter, minus the jess types.
- `jess.Stream(ctx, agent, input) (<-chan agentcore.Event, func() *agentcore.RunSummary)`. Turns agentcore's callback `Subscribe` + ctx-less `Prompt` into a channel plus a `Wait`. Lifted from the existing `internal/acl/runtime.go` event capture, minus the translation. Optional sugar; raw `agent.Subscribe` / `agent.Prompt` still work.

## Subagent rewiring (the meaty part)

Today `subagent.Pool` builds an `acl.Config` per task and runs it through `acl.Runtime`. After the refactor there is no `acl.Config` or `Runtime`. Changes:

- `subagent.Spec` holds the jess construction inputs (model, tools, skills, system prompt, agentID, maxTurns). Empty fields inherit from the parent `jess.New` options (so a spec is usually just name + system prompt).
- The pool builds each subagent as `*agentcore.Agent` via the same internal construction `jess.New` uses, and drives it with `agent.Prompt` + a `Subscribe` sink.
- Event merging tags each subagent's events by `AgentPath` and forwards them into the parent run's event channel, same behavior as today, but the events are `agentcore.Event` rather than `jess/event.Event`.
- `subagent.Tool` stays; it is now an `agentcore.Tool`.

This is the largest code churn and gets verified hardest (the existing pool tests for bounded concurrency, graceful shutdown, and nesting must stay green after the type swap).

## Non-goals

- Not dropping memory or skills. Long-term memory stays a jess feature; it is simply not in the daemon's v1.
- Not specifying the daemon here. The daemon (Telegram channel, ops tools, confirm gate, rookery scaffold, reminders store + scheduler) is a separate repo and a separate spec, written after this refactor lands.
- No behavior change to the memory pipeline (recall, injection, Kind policies) or skills loading. Same logic, fewer types around it.

## Risks

- Deepens lock-in to agentcore (its types in jess's public signatures, and it is a small single-maintainer dep pinned to main). Mitigation is structural and already in the plan: keep `memory/` and `skill/` agentcore-free so the genuinely portable value never touches agentcore. Wrapping was the expensive form of this insurance and it did not pay out (the adapter was buried in `internal/`); clean primitives are the cheap form.
- Subagent rewiring is the place a regression could hide. Mitigated by keeping the existing pool test suite and making it pass against the new types before anything else.

## Verification

- `go build ./...`, `go vet ./...`, `go test -race ./...` green.
- `examples/quickstart` still runs offline and still shows injected memory in the echo reply, rewritten against the new API (proves Door 1 + memory end to end through the real path).
- Gate: a test proving the DEFAULT (no approver) denies an unmarked tool with a reason, that a `jess.SafeTool` runs without prompting, and that `WithApprover` returning no routes the call to a blocked result. Fail-closed is the default, verified.
- Audit: a test proving a gate-DENIED call still produces a `tool_request` + `gate_decision` audit record (the rogue-attempt-is-visible guarantee), and that a normal call records request + result + duration. Default sink writes durable JSONL.
- Abort: a test proving ctx cancellation stops an in-flight tool and emits an `abort` audit record.
- The subagent pool tests pass unchanged in intent against agentcore types.
- Docs: CLAUDE.md and README rewritten so jess is described as an easy agent harness over agentcore (not "two extension packages that do NOT reimplement the harness"). `docs/superpowers/` moves to neutral `docs/plans`. A new ADR records the agentcore-as-direct-dependency decision and supersedes 0001.
