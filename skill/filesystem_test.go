package skill

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

func TestParseSkillMd_BasicFrontmatter(t *testing.T) {
	raw := []byte(`---
name: research
description: Web research with citation
---

When asked to research a topic, follow up with a web_search and
summarize three sources.`)
	sk, err := parseSkillMd(raw)
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "research" {
		t.Errorf("Name = %q, want research", sk.Name)
	}
	if !strings.Contains(sk.Description, "citation") {
		t.Errorf("Description not parsed: %q", sk.Description)
	}
	if !strings.HasPrefix(sk.SystemPrompt, "When asked to research") {
		t.Errorf("SystemPrompt body wrong: %q", sk.SystemPrompt)
	}
}

func TestParseSkillMd_NoFrontmatter_TreatsWholeFileAsBody(t *testing.T) {
	raw := []byte("Just a prompt, no frontmatter.")
	sk, err := parseSkillMd(raw)
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "" {
		t.Errorf("missing frontmatter should leave Name empty: %q", sk.Name)
	}
	if sk.SystemPrompt != "Just a prompt, no frontmatter." {
		t.Errorf("SystemPrompt = %q", sk.SystemPrompt)
	}
}

func TestParseSkillMd_QuotedValuesStripped(t *testing.T) {
	raw := []byte(`---
name: "with-spaces"
description: 'single-quoted'
---
body
`)
	sk, _ := parseSkillMd(raw)
	if sk.Name != "with-spaces" {
		t.Errorf("Name = %q, want with-spaces (no quotes)", sk.Name)
	}
	if sk.Description != "single-quoted" {
		t.Errorf("Description = %q", sk.Description)
	}
}

func TestParseSkillMd_UnknownKeysIgnored(t *testing.T) {
	raw := []byte(`---
name: x
mystery_key: irrelevant
tools: foo, bar
---
body
`)
	sk, err := parseSkillMd(raw)
	if err != nil {
		t.Fatalf("unknown keys should NOT error: %v", err)
	}
	if sk.Name != "x" {
		t.Errorf("Name = %q", sk.Name)
	}
}

func TestFilesystemLoader_WalksSkillMd(t *testing.T) {
	root := fstest.MapFS{
		"cooking/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: cooking\ndescription: Recipes\n---\nUse the kitchen tools to make food."),
		},
		"research/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: research\ndescription: Web lookup\n---\nLook things up."),
		},
		"research/helper.md": &fstest.MapFile{
			Data: []byte("# not a skill — filename isn't SKILL.md"),
		},
	}
	loader, err := NewFilesystemLoader("does-not-matter-when-fs-set")
	if err != nil {
		t.Fatal(err)
	}
	loader.SetFS(root)
	set, err := loader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := set.Names()
	if len(names) != 2 {
		t.Fatalf("loaded %d skills, want 2: %v", len(names), names)
	}
	if _, ok := set.Get("cooking"); !ok {
		t.Error("cooking skill missing")
	}
	if _, ok := set.Get("research"); !ok {
		t.Error("research skill missing")
	}
}

func TestFilesystemLoader_DerivesNameFromDirWhenMissing(t *testing.T) {
	root := fstest.MapFS{
		"unnamed/SKILL.md": &fstest.MapFile{
			Data: []byte("---\ndescription: no name in frontmatter\n---\nbody"),
		},
	}
	loader, _ := NewFilesystemLoader("ignored")
	loader.SetFS(root)
	set, _ := loader.Load(context.Background())
	if _, ok := set.Get("unnamed"); !ok {
		t.Errorf("missing-name skill should be named after its directory; got names %v", set.Names())
	}
}

func TestFilesystemLoader_EmptyRootRejected(t *testing.T) {
	if _, err := NewFilesystemLoader(""); err == nil {
		t.Fatal("empty root should be rejected to prevent accidental cwd walks")
	}
}

// Bad-frontmatter skill: malformed line should produce a stderr
// warning and skip the skill, not abort the whole Load.
func TestFilesystemLoader_BadFrontmatter_SkipsButContinues(t *testing.T) {
	root := fstest.MapFS{
		"bad/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nthis-line-has-no-colon\n---\nbody"),
		},
		"good/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: good\n---\nbody"),
		},
	}
	loader, _ := NewFilesystemLoader("ignored")
	loader.SetFS(root)
	set, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load should not abort on single-file parse error: %v", err)
	}
	if _, ok := set.Get("good"); !ok {
		t.Error("good skill should still load when sibling has bad frontmatter")
	}
}
