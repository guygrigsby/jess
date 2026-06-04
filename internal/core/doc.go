// Package acl is jess's anti-corruption layer: the single place that imports
// github.com/voocel/agentcore. It translates between jess's vendor-free domain
// types (message, event, tool) and agentcore's types, so no other jess package
// depends on the harness. A boundary test enforces that agentcore is imported
// only from here.
//
// Translation is deliberately lossy: jess's domain types model only what jess
// needs, so the following agentcore fields are intentionally not preserved
// across translation. Revisit if a phase needs them.
//
//   - ac.Message: Timestamp, StopReason, Usage, and Metadata keys other than
//     tool_call_id / is_error.
//   - ac.ToolCall: ArgsInvalid, ArgsRawText, ArgsParseError (jess forwards the
//     args as-is; malformed-args diagnostics are dropped).
//   - ac content blocks: ContentImage and ContentToolRef (no jess equivalent).
//   - ac.RunSummary: ToolErrors (event.RunSummary does not model it).
package core
