package core

import (
	"sort"
	"strings"

	ac "github.com/voocel/agentcore"

	"github.com/guygrigsby/jess/skill"
)

// SkillBlocks builds the agentcore system-prompt contributions for a skill Set:
// an index header listing every skill name + one-line description, then one
// block per skill that has a SystemPrompt. Sorted by name for stable output
// (matters for prompt caching). Returns nil for an empty/nil Set.
//
// It reads the Set through its exported API (Names + Get), so the skill package
// stays agentcore-free; the agentcore-typed blocks are produced here.
func SkillBlocks(s *skill.Set) []ac.SystemBlock {
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

// SkillTools collects the agentcore tools contributed by every skill in the Set,
// in the same order as SkillBlocks (sorted by skill name, tools in declared
// order within a skill). Entries in a Skill's Tools slice that do not implement
// agentcore.Tool are silently skipped: the field is typed any to keep the skill
// package decoupled, but only ac.Tool values reach the agent.
func SkillTools(s *skill.Set) []ac.Tool {
	if s == nil {
		return nil
	}
	names := s.Names()
	sort.Strings(names)

	var out []ac.Tool
	for _, name := range names {
		sk, ok := s.Get(name)
		if !ok {
			continue
		}
		for _, t := range sk.Tools {
			if at, ok := t.(ac.Tool); ok {
				out = append(out, at)
			}
		}
	}
	return out
}
