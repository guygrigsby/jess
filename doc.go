// Package jess is an easy agent harness over github.com/voocel/agentcore. It
// adds durable memory, registerable skills, subagents, and two baked-in safety
// controls: an append-only audit log and a fail-closed tool gate.
//
// jess.New is an option-assembler: pass functional options (WithModel,
// WithMemory, WithSkills, WithTools, WithApprover, ...) and it returns a real
// *agentcore.Agent. Drive it with agentcore's own API, or with jess.Stream,
// which exposes the event channel and a Wait for the run summary and aborts the
// run when its context is cancelled (the kill switch):
//
//	agent := jess.New(
//		jess.WithModel(m),                // any agentcore.ChatModel, or jess.Once for a local one
//		jess.WithMemory(store, recaller), // durable recall, injected each turn
//		jess.WithSkills(set),             // capability bundles
//	)
//	ch, wait := jess.Stream(ctx, agent, "hello")
//	for ev := range ch { /* observe */ }
//	summary := wait()
//
// agentcore types are exposed directly (ADR 0002); jess does not wrap the
// harness. Portability insurance is keeping jess/memory and jess/skill
// agentcore-free, so those stores and skills travel to any harness.
//
// Pre-1.0 — API may change before v1. See CHANGELOG.md and the examples/
// directory for runnable wiring.
package jess
