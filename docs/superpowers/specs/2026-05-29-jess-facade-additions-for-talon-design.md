# jess facade additions for the talon port — Design

- Status: Approved (brainstormed 2026-05-29; revised per pre-implementation review 2026-05-29)
- Repo: `github.com/guygrigsby/jess`
- Branch: `feat/facade-additions-for-talon`
- Review: `2026-05-29-jess-facade-additions-for-talon-design-review.md` (all findings incorporated)

## Context

talon (`github.com/guygrigsby/talon`) is being ported from driving `agentcore`
directly to consuming the new jess facade (`jess.New` → `Agent`/`Session`/`Run`),
the end-state ADR 0001 set up. This is **sub-project 1 of 2**: the small set of
capabilities the facade must add before talon can fully adopt it. Sub-project 2
(the talon rewrite) is a separate spec/plan and is out of scope here.

talon today uses jess only for memory and builds/drives an `agentcore.Agent`
itself: `agentcore.NewAgent(WithModel, WithMaxTurns, WithContextManager,
WithTools, WithSystemPrompt, WithMiddlewares)`, then `Subscribe` / `SetMessages`
/ `Prompt` / `WaitForIdle` / `Abort` / `TotalUsage`. Four facade gaps block a
full port (verified against the current jess source):

1. **Run token usage is not surfaced.** `event.RunSummary` carries
   `Turns`/`ToolCalls`/`EndReason` only. The ACL already propagates per-message
   `model.Usage`, but nothing aggregates it to the run result. talon reports
   input/output tokens per turn (`agent.TotalUsage()` today).
2. **No history seeding.** The facade has `Agent.NewSession()` (empty) and
   `Session.Prompt/Continue`, but no way to pre-load prior messages. talon keeps
   a long-lived `Session` per conversation (the chosen state model) and must
   replay history from its durable `ChatStore` on cold start / eviction / gateway
   restart.
3. **No per-run memory provenance hook.** talon stamps memory writes with
   `memory.Source{SessionID, MessageID, Tool, Reason}` (via an agentcore tool
   middleware today) so "why do you remember X" / "forget session Y" work. The
   facade hides the per-tool execution ctx, so talon can no longer inject this.
4. **No model token cap.** `jess.LiteLLM` exposes only `WithLLMAPIKey` /
   `WithLLMBaseURL`. talon caps max output tokens per model (its `model_cap.go`)
   to avoid provider 400s.

A non-gap, confirmed here so the talon spec can rely on it: agentcore's
filesystem tools (`agentcore/tools`) structurally satisfy `jess/tool.Tool` (same
`Name/Description/Schema/Execute` set — the memory tools already double-implement
both interfaces), so talon passes them through `jess.WithTools` unchanged. No
jess change needed for tools.

## Constraints (carried from the project)

- Pure Go, no CGO.
- agentcore stays imported only under `internal/acl/` (boundary test must stay
  green with its empty allowlist). No agentcore type enters jess's public API.
- Memory failures never block an LLM call.
- Domain packages (`event`, `message`, `tool`, `model`, `subagent`, root `jess`)
  stay vendor-free.
- No GPL/AGPL deps; MIT/Apache-2.0/MPL-2.0/BSD only.

## Decision: four additions

### 1. Run token usage in the result

- New type in `event` (NOT `model`, to avoid an `event → model` import cycle —
  `model` already imports `event`):

  ```go
  // Usage reports token consumption aggregated over a run.
  type Usage struct {
      Input  int
      Output int
      Total  int
  }
  ```

- `event.RunSummary` gains `Usage Usage`.
- **Source of truth (corrected for agentcore v1.6.9):** agentcore's `RunSummary`
  has no usage field, and `Agent.TotalUsage()` is CUMULATIVE across the session,
  so it is wrong for a per-run result once Sessions are long-lived (it would leak
  earlier turns' tokens into a later run). The ACL therefore aggregates
  `ac.Message.Usage` over the current run's assistant messages in `captureEnd`,
  reading `EventAgentEnd.NewMessages` (the same payload `captureEnd` already
  consumes at `internal/acl/runtime.go:159-163`) BEFORE `messagesFromACAgent`
  drops usage. Zero-valued when the model reports none.
- Surfaced unchanged through `jess.Result` (`Result.Summary.Usage`) and the
  `event.KindRunEnd` event's `Summary`.
- **Field scope (deliberate):** `event.Usage` carries `Input`/`Output`/`Total`
  only. agentcore's usage also has cache-read / cache-write / cost; these are
  omitted for now (talon needs input/output/total). Adding struct fields later is
  backward-compatible, so this stays minimal (YAGNI).

**Invariant:** usage is best-effort and never fails a run; if agentcore reports
nothing, `Usage` is the zero value.

### 2. History seeding

- New constructor on the Agent aggregate:

  ```go
  // NewSessionWithHistory returns a Session pre-loaded with prior messages, so a
  // host can resume a conversation whose history lives in its own store. The
  // messages seed the underlying run's message list; the next Prompt continues
  // from them. history is copied; the caller may reuse the slice.
  func (a *Agent) NewSessionWithHistory(history []message.Message) (*Session, error)
  ```

- ACL: the runtime translates `history` via the existing `messagesToAC` (which
  returns `[]ac.Message`) and calls `agentcore.Agent.SetMessages`, which accepts
  `[]ac.AgentMessage` (`agent.go:251`). Go slices are not covariant, so a small
  ACL helper converts each `ac.Message` (it implements `ac.AgentMessage`) into an
  `[]ac.AgentMessage` before the call.
- `NewSession()` stays the empty case (equivalent to
  `NewSessionWithHistory(nil)`).
- Roles supported by `messagesToAC` already cover user/assistant/system and
  tool-result blocks, which is what talon's `ChatStore` holds.
- **Seeded system messages vs `WithSystemPrompt`:** seeded history is
  conversation turns. The system prompt is configured separately via
  `WithSystemPrompt`, and agentcore prepends it on every LLM call, so callers
  must NOT also include the configured system prompt as a seeded system message
  (it would be duplicated). The seeding API doc states this rule; how talon's
  `ChatStore` maps to it is settled in sub-project 2.

**Invariant:** seeding is a construction-time operation; it does not start a run
and emits no events.

### 3. Per-run memory provenance (ACL snapshots Source, re-applies onto the tool ctx)

The host stamps the per-run Source on the ctx it passes to
`Session.Prompt`/`Continue` (jess accepts a ctx there, even though agentcore's
`Agent.Prompt` does not):

```go
run, _ := sess.Prompt(memory.WithSource(ctx, memory.Source{
    SessionID: sessionKey, MessageID: runID, Tool: "remember", Reason: "model decided",
}), userText)
```

The memory `RememberTool`/`RecallTool` read `memory.SourceFromContext(ctx)` as
they do today, so writes during the run are stamped.

**Mechanism (corrected for agentcore v1.6.9):** the run ctx cannot be threaded to
tools generically. `agentcore.Agent.Prompt` takes no context; it starts the loop
with `context.Background()` (`agent.go:287`,`308`) and tools receive agentcore's
own `progressCtx` (`loop.go:874-934`), which carries the cancellation/progress
state `Session.Abort()` depends on. So the ACL must NOT substitute its ctx for
agentcore's (that would drop cancellation, and Go contexts are not enumerable so
arbitrary values cannot be copied across). Instead:

- At run start the runtime snapshots `memory.SourceFromContext(promptCtx)` from
  the jess-level ctx it already holds (the one wired to the abort bridge) onto the
  active run.
- The existing per-Execute inject seam (today `event.ContextWithStream`) is
  extended to also apply `memory.WithSource(toolCtx, snapshot)` when a Source was
  set, keeping agentcore's incoming `progressCtx` as the BASE. Cancellation,
  progress, and `Session.Abort()` are therefore unaffected.

This narrows the feature to the concrete need (memory provenance) rather than a
general ctx pass-through, which is not implementable against v1.6.9 without an
agentcore API that accepts the run ctx. The ACL already imports `jess/memory`, so
referencing `memory.Source` here introduces no boundary issue. If agentcore later
adds a ctx-accepting `Prompt`, this can generalize.

### 4. LiteLLM token cap

- New option:

  ```go
  // WithLLMMaxTokens caps the model's max output tokens per call (0 = provider
  // default). Prevents over-long generations / provider 400s.
  func WithLLMMaxTokens(n int) LiteLLMOption
  ```

- `LiteLLMConfig` gains `MaxTokens int`.

**Mechanism (corrected for agentcore v1.6.9):** `agentcore.WithMaxTokens` is a
per-CALL `CallOption` consumed by `Generate`/`GenerateStream` (`llm/litellm.go:557-592`);
it is NOT a `llm.NewModel` construction option (`llm/litellm.go:70-124`), and the
agent loop builds its own call options (appending only thinking options,
`loop.go:589-599`). So setting it at construction has nowhere to take effect.
Therefore, when `MaxTokens > 0`, `acl.NewLiteLLMModel` wraps the native
`ac.ChatModel` in an ACL-local capping model that appends `ac.WithMaxTokens(n)`
to the options on every `Generate` and `GenerateStream` call before delegating
(talon's current `cappedChatModel` behavior, moved into the ACL).
`MaxTokens == 0` returns the native model unwrapped.

- `jess.LiteLLM(provider, modelID, WithLLMAPIKey, WithLLMBaseURL,
  WithLLMMaxTokens)` then covers talon's full model-construction need (provider,
  model, per-provider key/base-url override, cap).

## Out of scope (sub-project 2: the talon port)

The talon rewrite gets its own spec: a long-lived `jess.Agent`+`Session` manager
keyed by session, eviction + replay-from-ChatStore on cold start, the runner
rewrite (`session.Prompt` → range `run.Events()` → `run.Wait()`), the
`event.Event → EventSink` adapter, model wiring via `jess.LiteLLM`, the
onboarding tool reimplemented as a `tool.Tool`, filesystem tools passed through
`WithTools`, and removal of talon's direct agentcore usage in
`internal/agentcore_chat/` + `cmd/talon/gateway_agentcore.go`. talon will use a
local `replace github.com/guygrigsby/jess => ../jess` during development and bump
to a released/pseudo version once these additions land.

## Testing and enforcement

- **Usage:** a scripted fake `model.Model` returns chunks carrying `model.Usage`;
  assert `Result.Summary.Usage` aggregates Input/Output/Total and is zero when the
  model reports none. AND a two-run, same-`Session` test asserts the second run's
  usage reflects only that run (guards against cumulative leak).
- **History seeding:** `NewSessionWithHistory(msgs)`; drive one Prompt through a
  fake model that echoes the messages it received; assert the seeded history is
  present ahead of the new turn. Cover user/assistant/tool-result roles.
- **Provenance:** (a) a source-probe `tool.Tool` records
  `memory.SourceFromContext`; call `Prompt(memory.WithSource(ctx, src), ...)`
  with a fake model that emits a tool call; assert the probe saw `src`. (b) a
  blocking tool plus a direct `Session.Abort()` asserts the tool's ctx IS
  cancelled, proving agentcore's progress/cancel ctx was preserved (not replaced).
- **Max tokens:** a fake `ac.ChatModel` captures the resolved `CallOption`s;
  assert `WithMaxTokens(n)` is appended on BOTH `Generate` and `GenerateStream`
  when set, and absent when `MaxTokens == 0`.
- All under `-race`; the `TestAgentcoreImportBoundary` test stays green (empty
  allowlist); `go vet`, `make lint`, `make license-audit` clean.

## Consequences

- Positive: talon (and any host) gets token accounting, resumable sessions,
  per-run provenance, and model caps without touching agentcore. The facade
  becomes sufficient to fully replace direct agentcore use for a real app.
- Cost: four small public-API additions to maintain; one more agentcore field
  the ACL must track (run usage).
- Risk: low. The two agentcore unknowns (per-run usage source, tool-ctx
  semantics) were resolved against v1.6.9 during review: usage aggregates from
  `EventAgentEnd.NewMessages`, and provenance re-applies `memory.Source` onto
  agentcore's preserved tool ctx (no ctx substitution). All coupling stays in
  `internal/acl`. The main maintenance surface is a future agentcore upgrade that
  changes message-usage or call-option plumbing.

## Resolved during review

- **`event.Usage` fields:** Input/Output/Total only, deliberately (addition 1).
  Cache and cost fields are omitted now; struct fields can be added later without
  breaking callers.
- **Seeded system messages vs `WithSystemPrompt`:** the seeding API documents that
  the system prompt is configured via `WithSystemPrompt` (agentcore prepends it
  each call) and must not be duplicated in seeded history (addition 2). Whether
  talon's `ChatStore` persists system rows, and the exact mapping, is settled in
  sub-project 2.
