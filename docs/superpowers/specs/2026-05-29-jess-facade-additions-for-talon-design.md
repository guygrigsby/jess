# jess facade additions for the talon port — Design

- Status: Approved (brainstormed 2026-05-29)
- Repo: `github.com/guygrigsby/jess`
- Branch: `feat/facade-additions-for-talon`

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
- The ACL populates it in `summaryFromAC` (or `captureEnd`) from the run's token
  usage. Source of truth: agentcore reports usage on assistant messages
  (`ac.Usage` on `StreamEventDone`, already mapped to `model.Usage` in
  `internal/acl/model.go`) and/or on its run summary. Implementation aggregates
  whichever agentcore exposes for the completed run; the plan picks the exact
  agentcore field after confirming it. Zero-valued when the model reports none.
- Surfaced unchanged through `jess.Result` (`Result.Summary.Usage`) and the
  `event.KindRunEnd` event's `Summary`.

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

- ACL: the runtime translates `history` via the existing `messagesToAC` and calls
  `agentcore.Agent.SetMessages` on the freshly constructed agent before any run.
- `NewSession()` stays the empty case (equivalent to
  `NewSessionWithHistory(nil)`).
- Roles supported by `messagesToAC` already cover user/assistant/system and
  tool-result blocks, which is what talon's `ChatStore` holds.

**Invariant:** seeding is a construction-time operation; it does not start a run
and emits no events.

### 3. Per-run memory provenance (ctx threading)

- The facade threads the context passed to `Session.Prompt`/`Continue` through to
  each tool's `Execute`, composed with the ACL's existing run-stream injection
  (`event.ContextWithStream`). A host stamps per-run values on the Prompt ctx and
  tools observe them:

  ```go
  run, _ := sess.Prompt(memory.WithSource(ctx, memory.Source{
      SessionID: sessionKey, MessageID: runID, Tool: "remember", Reason: "model decided",
  }), userText)
  ```

  The memory `RememberTool`/`RecallTool` read `memory.SourceFromContext(ctx)` as
  they do today, so writes during the run are stamped.

- Implementation: the ACL's tool wrapper builds each Execute ctx from the active
  run's ctx (the one passed to `Prompt`) and then applies the stream inject,
  rather than starting from a background/loop ctx that drops caller values. The
  plan verifies whether agentcore already forwards the run ctx to tool execution;
  if it does, this is a no-op confirmation plus a test; if it does not, the
  runtime records the Prompt ctx and uses it as the base for the tool Execute
  ctx. Either way the observable contract is "values on the Prompt ctx reach
  tools."

**Why ctx threading over a dedicated `WithSource` option:** provenance is
per-run (the `MessageID`/runID changes every turn), so it cannot live on the
long-lived Agent config; threading the Prompt ctx is the least-surface way to
carry any per-run value (not just memory Source) and reuses the existing inject
seam. No memory type enters a new public API surface beyond what `WithMemory`
already implies.

### 4. LiteLLM token cap

- New option:

  ```go
  // WithLLMMaxTokens caps the model's max output tokens per call (0 = provider
  // default). Prevents over-long generations / provider 400s.
  func WithLLMMaxTokens(n int) LiteLLMOption
  ```

- `LiteLLMConfig` gains `MaxTokens int`; `acl.NewLiteLLMModel` applies it to the
  agentcore litellm model via agentcore's max-tokens call option (the behavior
  talon's `cappedChatModel` wraps today).
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
  assert `Result.Summary.Usage` aggregates Input/Output/Total; assert zero when
  the model reports none.
- **History seeding:** `NewSessionWithHistory(msgs)`; drive one Prompt through a
  fake model that echoes the messages it received; assert the seeded history is
  present ahead of the new turn. Cover user/assistant/tool-result roles.
- **Provenance:** a source-probe `tool.Tool` records `memory.SourceFromContext`;
  call `Prompt(memory.WithSource(ctx, src), ...)` with a fake model that emits a
  tool call; assert the probe saw `src`.
- **Max tokens:** a fake/captured litellm path asserts the max-tokens call option
  reaches the model call (table value in / value out).
- All under `-race`; the `TestAgentcoreImportBoundary` test stays green (empty
  allowlist); `go vet`, `make lint`, `make license-audit` clean.

## Consequences

- Positive: talon (and any host) gets token accounting, resumable sessions,
  per-run provenance, and model caps without touching agentcore. The facade
  becomes sufficient to fully replace direct agentcore use for a real app.
- Cost: four small public-API additions to maintain; one more agentcore field
  the ACL must track (run usage).
- Risk: agentcore's run-usage and tool-ctx semantics are the two unknowns; both
  are confined to `internal/acl` and covered by the plan's verification steps.
