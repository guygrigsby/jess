# jess facade additions for the talon port - Pre-Implementation Review

- Reviewed: 2026-05-29
- Target: `docs/superpowers/specs/2026-05-29-jess-facade-additions-for-talon-design.md`
- Result: Changes requested before implementation

## Findings

### 1. High - Prompt context threading is not implementable as a general contract on the current agentcore API

The design says the ACL tool wrapper should build each tool `Execute` context from
the active `Session.Prompt` / `Continue` context, so any value on the prompt ctx
reaches tools (`...design.md:107-128`). With agentcore v1.6.9, `Agent.Prompt`
does not accept a context; it starts the loop with `context.Background()` inside
`startPromptRunLocked` / `startContinueRunLocked`
(`github.com/voocel/agentcore@v1.6.9/agent.go:287` and
`github.com/voocel/agentcore@v1.6.9/agent.go:308`). Tools then receive
`progressCtx`, derived from that loop context and carrying agentcore
cancellation/progress state
(`github.com/voocel/agentcore@v1.6.9/loop.go:874-934`).

If the wrapper replaces agentcore's incoming ctx with the prompt ctx, it can drop
agentcore cancellation and progress plumbing. In particular, `Session.Abort()`
would no longer necessarily cancel a blocking tool, because the prompt ctx may
not be cancelled when `Abort()` calls through to agentcore. If the wrapper keeps
agentcore's ctx, arbitrary prompt ctx values cannot be copied generically because
Go contexts are not enumerable.

Recommendation: either narrow this addition to the specific talon need by copying
`memory.SourceFromContext(activeRunCtx)` onto the incoming tool ctx via
`memory.WithSource(ctx, src)` before applying `event.ContextWithStream`, or first
add/use an agentcore API that accepts the run ctx for `Prompt` / `Continue`.
Whichever path is chosen, add tests that source reaches the tool and that direct
`Session.Abort()` still cancels a blocking tool.

### 2. High - The LiteLLM max-token implementation path is underspecified and can become a no-op

The design says `acl.NewLiteLLMModel` should apply `MaxTokens` to the litellm
model "via agentcore's max-tokens call option" (`...design.md:147-149`). In
agentcore v1.6.9, `agentcore.WithMaxTokens` is a per-call `CallOption`; it is
only consumed when a `Generate` / `GenerateStream` call receives options
(`github.com/voocel/agentcore@v1.6.9/llm/litellm.go:557-592`).
`llm.NewModel` construction options cover API key, base URL, timeout, stream
idle timeout, and resilience, but not max tokens
(`github.com/voocel/agentcore@v1.6.9/llm/litellm.go:70-124`).
The agent loop builds call options internally and currently appends only thinking
options before calling the model
(`github.com/voocel/agentcore@v1.6.9/loop.go:589-599`).

Simply adding `MaxTokens` to jess's `LiteLLMConfig` has nowhere to take effect
unless jess adds another layer. Recommendation: implement an ACL-local
`ac.ChatModel` wrapper that appends `ac.WithMaxTokens(n)` on every `Generate` and
`GenerateStream` call before returning `nativeModel`, or explicitly clone and set
the adapter generation config if agentcore exposes a safe non-global way to do
that. Add an internal test with a fake `ac.ChatModel` that captures resolved call
options.

### 3. Medium - Run usage needs a concrete per-run source of truth

The design leaves the source open: aggregate "whichever agentcore exposes" from
assistant messages and/or run summary (`...design.md:71-76`). Current agentcore
`RunSummary` has no usage field
(`github.com/voocel/agentcore@v1.6.9/event.go:50-55`),
and `Agent.TotalUsage()` is cumulative across the whole agent/session
(`github.com/voocel/agentcore@v1.6.9/agent.go:408-413`),
so it is not a correct per-run result unless the ACL snapshots and diffs it.

The least ambiguous implementation is to aggregate `ac.Message.Usage` from the
current run's assistant messages before `messagesFromACAgent` drops usage. The
`EventAgentEnd.NewMessages` payload is already what `captureEnd` consumes for the
run result (`internal/acl/runtime.go:159-163`), so the spec should name that path
explicitly. Add a two-run same-session test to catch accidental cumulative usage
leaking into the second result.

### 4. Low - The history seeding plan needs a slice conversion helper

The design says to translate history with `messagesToAC` and call
`agentcore.Agent.SetMessages` (`...design.md:95-96`). `messagesToAC` returns
`[]ac.Message`, while `SetMessages` accepts `[]AgentMessage`
(`github.com/voocel/agentcore@v1.6.9/agent.go:251`).
Go slices are not covariant, so the implementation cannot pass the result
directly. Add an ACL helper that converts each translated `ac.Message` into an
`ac.AgentMessage` slice before calling `SetMessages`.

## Open Questions

- Should `event.Usage` intentionally omit `CacheRead`, `CacheWrite`, and cost
  even though agentcore usage carries them? If the goal is only talon's current
  input/output display, that is fine, but the public API choice should be
  deliberate.
- Does talon's `ChatStore` ever persist system messages? If yes, the
  `NewSessionWithHistory` docs should clarify how seeded system messages interact
  with `WithSystemPrompt`, since agentcore also prepends the configured system
  prompt on every LLM call.

## Summary

The four additions are directionally aligned with the talon port. Before coding,
revise the provenance and max-token sections so they describe mechanisms that can
work against agentcore v1.6.9 without losing cancellation/progress semantics or
silently ignoring `MaxTokens`. Also tighten the usage aggregation source and the
history conversion detail so the implementation plan is mechanically precise.
