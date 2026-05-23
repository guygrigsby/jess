// Package jess is a thin meta-package; the real surface lives in its
// subpackages:
//
//   - jess/memory — durable agent memory: a Memory interface, a Store
//     interface for persistence, and adapters that plug into
//     agentcore's ContextManager and OnMessage hooks.
//   - jess/skills — registerable capability bundles: a Skill type
//     combining a system-prompt contribution with zero-or-more
//     agentcore.Tool implementations, plus loaders that discover
//     skills from disk.
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
// Status: pre-1.0. API will change before v1. See subpackage docs.
package jess
