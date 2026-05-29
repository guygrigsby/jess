package skill

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeTool is a minimal agentcore.Tool for exercising Set.Tools.
type fakeTool struct{ name string }

func (f fakeTool) Name() string                                                      { return f.name }
func (f fakeTool) Description() string                                               { return "" }
func (f fakeTool) Schema() map[string]any                                            { return nil }
func (f fakeTool) Execute(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }

func mustAddAll(t *testing.T, s *Set, skills ...Skill) {
	t.Helper()
	for _, sk := range skills {
		if err := s.Add(sk); err != nil {
			t.Fatalf("Add %q: %v", sk.Name, err)
		}
	}
}

func TestSet_SystemBlocks(t *testing.T) {
	tests := []struct {
		name       string
		skills     []Skill
		wantBlocks int    // total blocks incl. index
		wantIndex  string // expected full text of the index block (block 0); "" = skip check
	}{
		{
			name:       "empty set returns nil",
			skills:     nil,
			wantBlocks: 0,
		},
		{
			name: "index lists all, body only for skills with a prompt",
			skills: []Skill{
				{Name: "beta", Description: "second", SystemPrompt: "do beta"},
				{Name: "alpha", Description: "first"}, // no SystemPrompt
			},
			// 1 index + 1 body (beta only); alpha has no prompt.
			wantBlocks: 2,
			// Sorted by name: alpha before beta.
			wantIndex: "Available skills:\n- alpha — first\n- beta — second\n",
		},
		{
			name: "all skills have prompts",
			skills: []Skill{
				{Name: "a", Description: "da", SystemPrompt: "pa"},
				{Name: "b", Description: "db", SystemPrompt: "pb"},
			},
			wantBlocks: 3, // index + 2 bodies
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSet()
			mustAddAll(t, s, tt.skills...)
			blocks := s.SystemBlocks()
			if len(blocks) != tt.wantBlocks {
				t.Fatalf("blocks = %d, want %d", len(blocks), tt.wantBlocks)
			}
			if tt.wantIndex != "" && blocks[0].Text != tt.wantIndex {
				t.Errorf("index block = %q, want %q", blocks[0].Text, tt.wantIndex)
			}
		})
	}
}

func TestSet_Tools(t *testing.T) {
	tests := []struct {
		name      string
		skills    []Skill
		wantNames []string // expected tool names in order
	}{
		{
			name:      "no skills",
			skills:    nil,
			wantNames: nil,
		},
		{
			name: "non-Tool entries are skipped",
			skills: []Skill{
				{Name: "x", Tools: []any{fakeTool{"t1"}, "not-a-tool", 42}},
			},
			wantNames: []string{"t1"},
		},
		{
			name: "sorted by skill name, declared order within a skill",
			skills: []Skill{
				{Name: "zeta", Tools: []any{fakeTool{"z1"}}},
				{Name: "alpha", Tools: []any{fakeTool{"a1"}, fakeTool{"a2"}}},
			},
			wantNames: []string{"a1", "a2", "z1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSet()
			mustAddAll(t, s, tt.skills...)
			tools := s.Tools()
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
