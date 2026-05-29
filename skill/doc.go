// Package skills adds registerable capability bundles on top of
// agentcore.
//
// A Skill is a unit of behavior an agent can opt into: a name and
// description (the agent sees both in its system prompt), a
// system-prompt contribution (instructions about how/when to use
// the skill), and zero-or-more tool implementations
// (the actual callable surface).
//
// Skills are not a replacement for tools — they're a way to bundle
// a tool or set of tools with the instructions the model needs to
// use them well. A "web research" skill might contribute a system
// prompt block ("When asked to research something, follow up with
// a web_search then summarize three sources") plus a web_search
// tool. The model sees both together; the host hasn't had to
// custom-prompt for each tool.
//
// Loading model:
//
//   - Direct registration: a Set is a collection of Skills; Set.Add
//     appends. Hosts build Sets programmatically when skills come
//     from in-process Go code.
//   - Filesystem loading: NewFilesystemLoader walks a directory
//     looking for SKILL.md files (layout mirrors Claude Code's
//     skill plugins — markdown frontmatter declares name +
//     description + tools, body becomes the system-prompt
//     contribution). A loader returns a Set the host hands to jess.
//
// Integration:
//
//   - Hand a Set to an agent via jess.WithSkills(set). jess converts
//     the Set's system prompts and tools into the harness inside its
//     anti-corruption layer; this package itself stays vendor-free
//     (no agentcore types in its API).
//
// Hosts that want runtime add/remove (a /skill add command, say)
// keep their own Set and rebuild the Agent on change. Hot-loading
// without rebuild is out of scope for v0 — agentcore doesn't yet
// expose runtime tool re-registration.
//
// Status: skeleton — interfaces shipped, real loaders land in
// follow-up commits.
package skill
