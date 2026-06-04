package core

import (
	"sort"
	"strings"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/skill"
	"github.com/guygrigsby/jess/tool"
)

// skillSystemBlocks builds the agentcore system-prompt contributions for a skill
// Set: an index header listing every skill name + one-line description, then one
// block per skill that has a SystemPrompt. Sorted by name for stable output
// (matters for prompt caching). Returns nil for an empty/nil Set.
//
// This is the former skills.Set.SystemBlocks(), relocated into the ACL so the
// skill package stays vendor-free (ADR 0001). It reads the Set through its
// exported API (Names + Get).
func skillSystemBlocks(s *skill.Set) []ac.SystemBlock {
	if s == nil {
		return nil
	}
	names := s.Names()
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	var index strings.Builder
	index.WriteString("Available skills:\n")
	for _, name := range names {
		sk, ok := s.Get(name)
		if !ok {
			continue
		}
		index.WriteString("- ")
		index.WriteString(sk.Name)
		if sk.Description != "" {
			index.WriteString(" — ")
			index.WriteString(sk.Description)
		}
		index.WriteByte('\n')
	}

	blocks := []ac.SystemBlock{{Text: index.String()}}
	for _, name := range names {
		sk, ok := s.Get(name)
		if !ok {
			continue
		}
		body := strings.TrimSpace(sk.SystemPrompt)
		if body == "" {
			continue
		}
		blocks = append(blocks, ac.SystemBlock{
			Text: "## Skill: " + sk.Name + "\n\n" + body,
		})
	}
	return blocks
}

// skillTools collects the jess tools contributed by every skill in the Set, in
// the same order as skillSystemBlocks (sorted by skill name, tools in declared
// order within a skill). Entries in a Skill's Tools slice that do not implement
// tool.Tool are silently skipped — the field is typed `any` to keep the skill
// package decoupled, but only tool.Tool values reach the agent.
//
// Returning tool.Tool (not ac.Tool) lets the runtime wrap skill tools through
// the same wrapToolsInject path as standalone tools, so a skill-shipped
// stream-aware tool now receives the active run's event stream.
func skillTools(s *skill.Set) []tool.Tool {
	if s == nil {
		return nil
	}
	names := s.Names()
	sort.Strings(names)

	var out []tool.Tool
	for _, name := range names {
		sk, ok := s.Get(name)
		if !ok {
			continue
		}
		for _, t := range sk.Tools {
			if jt, ok := t.(tool.Tool); ok {
				out = append(out, jt)
			}
		}
	}
	return out
}
