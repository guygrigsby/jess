package skills

import (
	"sort"
	"strings"

	"github.com/voocel/agentcore"
)

// SystemBlocks returns the system-prompt contributions for every
// skill in the set, suitable for passing to
// agentcore.WithSystemBlocks.
//
// Format: an index header listing skill names + one-line
// descriptions, then one block per skill containing its
// SystemPrompt. Skills without a SystemPrompt are still listed in
// the index — the model can see they exist via their description
// even if they ship no extra prose. Sorted by name for stable
// output (matters for prompt caching).
func (s *Set) SystemBlocks() []agentcore.SystemBlock {
	s.mu.RLock()
	skills := make([]Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		skills = append(skills, sk)
	}
	s.mu.RUnlock()
	if len(skills) == 0 {
		return nil
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })

	var index strings.Builder
	index.WriteString("Available skills:\n")
	for _, sk := range skills {
		index.WriteString("- ")
		index.WriteString(sk.Name)
		if sk.Description != "" {
			index.WriteString(" — ")
			index.WriteString(sk.Description)
		}
		index.WriteByte('\n')
	}

	blocks := []agentcore.SystemBlock{{Text: index.String()}}
	for _, sk := range skills {
		body := strings.TrimSpace(sk.SystemPrompt)
		if body == "" {
			continue
		}
		blocks = append(blocks, agentcore.SystemBlock{
			Text: "## Skill: " + sk.Name + "\n\n" + body,
		})
	}
	return blocks
}

// Tools returns the flat list of agentcore.Tool implementations
// contributed by every skill in the set, suitable for passing to
// agentcore.WithTools. Order matches SystemBlocks (sorted by skill
// name, tools within a skill in their declared order).
//
// Non-Tool entries in a Skill's Tools slice are silently skipped —
// the field is typed `any` to keep the Skill struct decoupled at
// the source level, but only values implementing agentcore.Tool
// reach the agent.
func (s *Set) Tools() []agentcore.Tool {
	s.mu.RLock()
	skills := make([]Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		skills = append(skills, sk)
	}
	s.mu.RUnlock()
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })

	var out []agentcore.Tool
	for _, sk := range skills {
		for _, t := range sk.Tools {
			if ac, ok := t.(agentcore.Tool); ok {
				out = append(out, ac)
			}
		}
	}
	return out
}
