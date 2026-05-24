// Package jess is a thin meta-package; the real surface lives in
// its subpackages:
//
//   - jess/memory — durable agent memory: typed Kind with per-Kind
//     retrieval policy, three pluggable Stores (in-memory, JSONL,
//     chromem-go vector), a pure-Go in-process embedder (GoMLX +
//     sentence-transformers, no CGO), Recallers that compose
//     (Simple + Vector via RRF in HybridRecaller), a RememberTool
//     the model calls to save facts, and a ContextManager adapter
//     that injects layered memory into every agentcore LLM call.
//   - jess/skills — registerable capability bundles. A Skill
//     combines a name, description, system-prompt contribution,
//     and zero-or-more agentcore.Tool implementations. Loaders
//     discover skills from disk (Claude Code SKILL.md layout) or
//     programmatically.
//
// jess sits on top of github.com/voocel/agentcore — it does NOT
// re-implement the agent loop, provider abstraction, tool dispatch,
// or permission engine. Hosts wire jess's extensions in via
// agentcore's AgentOption surface.
//
// Why two packages, not one: memory and skills are independent
// concerns with different stable surfaces. A host that wants only
// memory shouldn't pull in skill loaders, and vice versa.
//
// See the package READMEs and the examples/ directory for runnable
// wiring. Pre-1.0 — API may change before v1. See CHANGELOG.md.
package jess
