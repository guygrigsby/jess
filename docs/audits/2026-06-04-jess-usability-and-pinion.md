# jess usability audit + facade-reduction plan (2026-06-04)

## Bottom line

Original intent, confirmed: jess is meant to be an easy-to-use agent harness with subagents. Fire up an agent and it handles the rest. Easier than Google ADK. That settles the fork below: jess is the harness (Path 1), the facade is the product, and memory + skill are features of it, not the point. The talon-shaped framing in the rest of this doc still holds as history, but the decision is made.

Judged against that bar, the verdict changes from "is the facade justified" (it is, it's the product) to "is the fire-up-and-go experience actually easy." Mostly yes, with one real gap: subagents, the headline feature, are second-class. See the ergonomics punch-list.

The docs are the other problem. CLAUDE.md still says jess "is two extension packages... deliberately does NOT reimplement the agentcore harness." For a harness-first product that's actively wrong and buries the lede.

Verified before writing any of this: `go build ./...`, `go vet ./...`, `go test -race ./...` all green, and `examples/quickstart` runs offline and proves memory injection through the real path (the echo model's reply contains the injected core memory). The code works. The question is shape, not correctness.

## The value split (real LOC, non-test)

- memory/ — 2722 (49%). The reason jess exists. Store/Recaller/Kind, three backends, three recallers, pure-Go gomlx embedder, remember/recall tools.
- internal/acl + facade root — 1213 + 303 = 1516 (28%). Pure encapsulation of agentcore. Plus 1410 LOC of acl tests.
- skill/ — 350 (6%). Set + SKILL.md loader. Genuine.
- message/model/event/tool/subagent — the rest. Vendor-free vocabulary + the subagent pool. Clean.

memory and skill stand on their own as agentcore extensions. That's the original thesis and it's intact. The 28% facade is the part whose justification left with talon.

## What the facade actually buys, and what it costs

Buys: no agentcore type appears in jess's public API (enforced by `internal/acl/boundary_test.go`), so the harness is swappable, and a host drives jess instead of agentcore.

Costs:
- Two layers for every call. jess.Session wraps acl.Runtime wraps ac.Agent. jess.Run wraps acl.Run. jess.LiteLLM wraps acl.NewLiteLLMModel. Every public type has a private twin in acl, and the model layer translates both directions (nativeModel, streamAdapter, cappedChatModel). A change touches two files; a reader bounces between root and internal/acl.
- The swappability is theoretical. There's no second harness. Swapping agentcore means rewriting all of internal/acl anyway, so the boundary buys insulation against a change nobody is planning, for one maintainer.
- The docs don't match the code. CLAUDE.md still says jess "is two extension packages on top of agentcore (memory/ + skills/)" and "deliberately does NOT reimplement the agentcore harness, providers, tool dispatch... or compaction." The facade re-exposes run/session/model/event, which is exactly that surface. A newcomer reads the description, then finds a different project.

None of this is wrong code. It's surface built ahead of a consumer that then disappeared.

## Facade-reduction plan

Two coherent directions. Pick one. "Keep all of it as-is" is the option that's no longer coherent, because the docs claim A while the code is A+B.

### Path 1: commit to jess as a harness (keep the facade, make it earn its keep)

Your note "this harness will be useful for me in the future" points here. If jess is the harness you'll reach for, then:

- Fix the description first. Rewrite CLAUDE.md and README so jess is "a memory- and skill-augmented agent facade over agentcore," not "two extension packages." Drop the "does NOT reimplement the harness" line. It re-exposes the harness on purpose.
- Earn the boundary. Treat harness-swappability as a real goal or drop it as a justification. If real, the next consumer (or a second toy backend behind internal/acl) is what proves it. Until something exercises the seam, call it what it is: encapsulation for a clean public API, not portability.
- Close the example gap. One offline echo example is thin for a harness meant to be adopted. Add the cloud path (LiteLLM), a subagent example, and a vector-recall example (gomlx + ChromemStore). Right now the whole facade has a single runnable wiring.
- Smaller trims that don't touch the boundary: collapse `run.go` (29 LOC, pure pass-through over acl.Run) into session.go, and drop the `Agent.defaultSession` mutex-cached singleton in favor of explicit `NewSession`. Both reduce indirection without changing the encapsulation story.

Cost: you keep maintaining ~1500 LOC of wrapper + 1400 LOC of its tests for a future you're betting on.

### Path 2: collapse back to the extension thesis (retire most of the facade)

If the honest answer is "I want the memory and skills, not a full harness wrapper," then return jess to what the docs already claim:

- Keep memory/, skill/, and the vendor-free vocabulary (message/model/event/tool) and the subagent pool. These are the value.
- Demote the facade. agentcore's own ContextManager/Tool hooks already take jess's memory ContextManager and tools directly (that's how internal/acl wires them). A host can use agentcore and bolt on jess memory/skills without the jess.Agent/Session/Run layer. Move the run/session driver to an optional `jess/harness` subpackage (or a separate repo) so the core module is the extension layer again, agentcore visible, no ACL tax.
- internal/acl shrinks to just the memory ContextManager adapter and the tool adapter, which is the part that has to touch agentcore regardless.

Cost: a host now sees agentcore types when driving a run. That's the swappability you give up. Given there's no second harness, that cost is currently zero in practice.

### Recommendation

Path 1, decided. jess is the harness. The facade earns its keep because it is the product. The work is not trimming the facade, it's making the fire-up-and-go experience match the pitch. See the punch-list.

## Ergonomics punch-list (fire up an agent with subagents, easier than ADK)

What already hits the goal: `jess.New(WithModel(m)).Prompt(ctx, "...")` then range over `run.Events()` and `run.Wait()` is the clean core loop. The unified event stream across the whole agent tree (subagent events tagged by AgentPath, forwarded into the parent run automatically) is a genuine ADK-beating feature. `model.Once` runs an agent with no provider. Pure Go, no CGO.

The gap, in priority order:

1. Subagents are second-class. This is the headline feature and the clunkiest path. Today: build a `subagent.Pool`, `Register` each `Spec` (re-declaring Model every time, specs inherit nothing from the parent), `subagent.NewTool(pool)`, pass it via `WithTools`. Four concepts to delegate one task. Add `jess.WithSubagents(specs...)`: New builds the pool with default bounds, fills each spec's empty Model/Store/Recaller/AgentID from the parent options, registers them, wires the subagent tool, and owns the pool lifecycle (Close with the agent). Target:

   ```go
   agent, _ := jess.New(
       jess.WithModel(m),
       jess.WithSubagents(
           subagent.Spec{Name: "researcher", SystemPrompt: "..."},
           subagent.Spec{Name: "writer", SystemPrompt: "..."},
       ),
   )
   ```

   Open design question: should subagents share the parent's memory AgentID, get their own scope per name, or stay memoryless by default. Pick one and document it.

2. A real cloud example. The only runnable wiring is the offline echo. First impression for "fire up an agent" should be ~15 lines against a real model via `jess.LiteLLM`. Then a subagent example and a vector-recall example (gomlx + ChromemStore).

3. More batteries. `WithMemory` requires both store and recaller before memory engages. Add `WithMemory(store)` defaulting `NewSimpleRecaller()`, and a `jess.Quick(model)` that wires an in-memory store + simple recaller for the zero-config path.

4. Cheap correctness on the docs/naming, regardless: rewrite CLAUDE.md/README so jess is "an easy agent harness with subagents over agentcore," drop the "does NOT reimplement the harness" line, rename `skills/` references to `skill/` (package is singular), and move `docs/superpowers/` to a neutral `docs/plans/` (tool-named folder, several specs named "...-for-talon..." after a dead consumer).

5. Small indirection trims that don't touch the boundary: collapse `run.go` (pure pass-through over acl.Run) into session.go, and drop the `Agent.defaultSession` mutex singleton in favor of explicit `NewSession`.

## pinion disposition

Agree with your verdict, and the reason is sharper than "can't label every tool."

The architecture is sound in isolation (effect vocabulary, DAG composition, conservative taint analysis, fail-closed CEL). The unlabeled-defaults-to-max-danger design is correct. But that correctness is what kills it in practice: in a real agent with dozens of MCP tools where you've labeled five, every composition that touches an unlabeled node expands to maximum danger and trips the policy. So pinion either screams on nearly every composition (noise, ignored) or you've already labeled densely enough that you knew the risk without it. The value lives only in the regime of dense labeling, and dense labeling is the thing that isn't feasible. A risk assessor that flags everything is the boy who cried wolf. That's why it "isn't it," not soundness.

Salvage: one inert piece has value divorced from the dead thesis. `effect/` (Effect/Kind/Scope + doublestar Grant matching, dependency-free, pure) is a usable capability-grant vocabulary for tool authorization even without composition-risk analysis. compose/ and analyze/ only mean something with the effect labels, so they share the thesis's fate. The CEL seam is generic and not jess's problem.

Recommendation: archive pinion as-is, don't port anything into jess yet. The effect/ kernel stays in the archived history, liftable in an afternoon if a concrete tool-authorization need lands in jess. Porting it preemptively would repeat the exact mistake this whole exercise is about: building surface ahead of a consumer. talon got a facade it then abandoned. pinion got a risk stack ahead of need. The discipline both cases point at is the same one in the memory note "jess provides primitives, not architecture," extended: don't add primitives speculatively either.

Concretely: `gh repo archive guygrigsby/pinion` plus a one-line README note that the effect/ vocabulary is the part worth lifting. Reversible. Not done yet, pending your call.

## Open question

Path 1 or Path 2 for the facade? It comes down to one thing: are you going to drive your next agent project through jess, or do you want jess to hand you memory + skills and stay out of the run loop?
