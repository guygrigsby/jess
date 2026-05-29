// Package jess is a memory- and skill-augmented agent facade over
// github.com/voocel/agentcore. The host calls jess; jess owns the agent run.
//
// Construct an Agent once with jess.New and functional options, then drive a
// conversation:
//
//	agent, _ := jess.New(
//		jess.WithModel(m),                       // any model.Model (cloud via jess.LiteLLM, or local)
//		jess.WithMemory(store, recaller),        // durable recall, injected each turn
//		jess.WithSkills(set),                    // capability bundles
//	)
//	run, _ := agent.Prompt(ctx, "hello")
//	for ev := range run.Events() { /* observe */ }
//	res, _ := run.Wait()
//
// The surface lives in subpackages:
//   - jess/message — Message, ContentBlock, Role
//   - jess/event   — Event, EventKind, Stream (the observable run stream)
//   - jess/tool    — the Tool interface the model invokes
//   - jess/model   — the vendor-free streaming Model interface
//   - jess/memory  — Store/Recaller/Entry/Kind, the remember & recall tools
//   - jess/skill   — Skill, Set, Loader (capability bundles)
//   - jess/subagent — bounded Pool for fast, abundant subagents
//
// agentcore (the loop, providers, tool dispatch, permission engine, context
// compaction) is an internal implementation detail: it is imported only under
// internal/acl, enforced by a boundary test. No agentcore type appears in
// jess's public API, so the harness is swappable.
//
// Pre-1.0 — API may change before v1. See CHANGELOG.md and the examples/
// directory for runnable wiring.
package jess
