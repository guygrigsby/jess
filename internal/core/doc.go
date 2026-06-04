// Package core is jess's agentcore-touching implementation: the option-assembler
// (Config + Agent) that wires a model, system blocks, tools, the memory
// ContextManager, the tool gate, and the audit middleware into a ready
// *agentcore.Agent. It is shared by the root jess package and jess/subagent, so
// both build agents the same way without an import cycle.
//
// Unlike the former anti-corruption layer (ADR 0001), core exposes agentcore
// types directly (ADR 0002). It is internal only to keep the assembly helpers
// out of jess's public surface, not to hide agentcore. Portability insurance is
// keeping jess/memory and jess/skill agentcore-free, not wrapping the harness.
package core
