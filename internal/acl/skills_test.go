package acl

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/guygrigsby/jess/skill"
)

// fakeTool is a minimal tool.Tool for exercising skillTools.
type fakeTool struct{ name string }

func (f fakeTool) Name() string                                                      { return f.name }
func (f fakeTool) Description() string                                               { return "" }
func (f fakeTool) Schema() map[string]any                                            { return nil }
func (f fakeTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }

func mustAddAll(t *testing.T, s *skill.Set, skills ...skill.Skill) {
	t.Helper()
	for _, sk := range skills {
		if err := s.Add(sk); err != nil {
			t.Fatalf("Add %q: %v", sk.Name, err)
		}
	}
}

func TestSkillSystemBlocks(t *testing.T) {
	tests := []struct {
		name       string
		skills     []skill.Skill
		wantBlocks int
		wantIndex  string
	}{
		{
			name:       "empty set returns nil",
			skills:     nil,
			wantBlocks: 0,
		},
		{
			name: "index lists all, body only for skills with a prompt",
			skills: []skill.Skill{
				{Name: "beta", Description: "second", SystemPrompt: "do beta"},
				{Name: "alpha", Description: "first"},
			},
			wantBlocks: 2,
			wantIndex:  "Available skills:\n- alpha — first\n- beta — second\n",
		},
		{
			name: "all skills have prompts",
			skills: []skill.Skill{
				{Name: "a", Description: "da", SystemPrompt: "pa"},
				{Name: "b", Description: "db", SystemPrompt: "pb"},
			},
			wantBlocks: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := skill.NewSet()
			mustAddAll(t, s, tt.skills...)
			blocks := skillSystemBlocks(s)
			if len(blocks) != tt.wantBlocks {
				t.Fatalf("blocks = %d, want %d", len(blocks), tt.wantBlocks)
			}
			if tt.wantIndex != "" && blocks[0].Text != tt.wantIndex {
				t.Errorf("index block = %q, want %q", blocks[0].Text, tt.wantIndex)
			}
		})
	}
}

func TestSkillTools(t *testing.T) {
	tests := []struct {
		name      string
		skills    []skill.Skill
		wantNames []string
	}{
		{
			name:      "no skills",
			skills:    nil,
			wantNames: nil,
		},
		{
			name: "non-Tool entries are skipped",
			skills: []skill.Skill{
				{Name: "x", Tools: []any{fakeTool{"t1"}, "not-a-tool", 42}},
			},
			wantNames: []string{"t1"},
		},
		{
			name: "sorted by skill name, declared order within a skill",
			skills: []skill.Skill{
				{Name: "zeta", Tools: []any{fakeTool{"z1"}}},
				{Name: "alpha", Tools: []any{fakeTool{"a1"}, fakeTool{"a2"}}},
			},
			wantNames: []string{"a1", "a2", "z1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := skill.NewSet()
			mustAddAll(t, s, tt.skills...)
			tools := skillTools(s)
			if len(tools) != len(tt.wantNames) {
				t.Fatalf("got %d tools, want %d", len(tools), len(tt.wantNames))
			}
			for i, want := range tt.wantNames {
				if tools[i].Name() != want {
					t.Errorf("tool[%d] = %q, want %q", i, tools[i].Name(), want)
				}
			}
		})
	}
}
