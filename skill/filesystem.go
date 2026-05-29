package skill

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FilesystemLoader walks a directory tree for SKILL.md files,
// parses each one into a Skill, and aggregates them into a Set.
//
// Layout (mirrors Claude Code's skill plugins for portability):
//
//	root/
//	  cooking/
//	    SKILL.md            <- frontmatter + body
//	  research/
//	    SKILL.md
//	    helper.md           <- ignored (only files named SKILL.md count)
//
// Frontmatter is a leading YAML-ish block delimited by lines
// consisting only of "---". A minimal parser handles the subset
// real skills use: top-level scalars (name, description) and
// nothing nested. Skills needing richer frontmatter (lists, maps)
// would parse failures into a logged warning + skip the skill;
// this v0 doesn't pull in a YAML dep.
//
// FilesystemLoader doesn't load tools — the SKILL.md frontmatter
// names tools, but registering implementations is the host's job
// (tools are Go code, not data files). Hosts that want to wire
// named tools to implementations can map after Load.
type FilesystemLoader struct {
	root string
	fsys fs.FS // optional override for tests
}

// NewFilesystemLoader returns a loader rooted at the given path.
// Empty root is rejected — most accidental misuses pass "" and
// would otherwise walk the working directory.
func NewFilesystemLoader(root string) (*FilesystemLoader, error) {
	if root == "" {
		return nil, fmt.Errorf("skills: NewFilesystemLoader requires a non-empty root")
	}
	return &FilesystemLoader{root: root}, nil
}

// SetFS swaps the filesystem the loader walks. Test-only — production
// callers use the path-based constructor.
func (l *FilesystemLoader) SetFS(fsys fs.FS) {
	l.fsys = fsys
}

// Load walks the filesystem and returns a Set containing every
// successfully-parsed SKILL.md. Parse errors on individual files
// produce a warning written via fmt.Fprintln(os.Stderr, ...) and
// the skill is skipped — a single malformed SKILL.md should not
// block the rest. Aggregate fatal errors (missing root) return as
// the second return value.
func (l *FilesystemLoader) Load(ctx context.Context) (*Set, error) {
	set := NewSet()
	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(path) != "SKILL.md" {
			return nil
		}
		raw, readErr := l.readFile(path)
		if readErr != nil {
			fmt.Fprintln(os.Stderr, "skills: read", path, ":", readErr)
			return nil
		}
		sk, parseErr := parseSkillMd(raw)
		if parseErr != nil {
			fmt.Fprintln(os.Stderr, "skills: parse", path, ":", parseErr)
			return nil
		}
		if sk.Name == "" {
			// Derive the name from the parent directory if the
			// frontmatter omitted it — common-case Claude Code
			// skills name their dir for what the skill does.
			sk.Name = filepath.Base(filepath.Dir(path))
		}
		if err := set.Add(sk); err != nil {
			fmt.Fprintln(os.Stderr, "skills: add", path, ":", err)
		}
		return nil
	}

	if l.fsys != nil {
		if err := fs.WalkDir(l.fsys, ".", walkFn); err != nil {
			return nil, fmt.Errorf("skills: walk %s: %w", l.root, err)
		}
		return set, nil
	}
	if err := filepath.WalkDir(l.root, walkFn); err != nil {
		return nil, fmt.Errorf("skills: walk %s: %w", l.root, err)
	}
	return set, nil
}

func (l *FilesystemLoader) readFile(path string) ([]byte, error) {
	if l.fsys != nil {
		return fs.ReadFile(l.fsys, path)
	}
	return os.ReadFile(path)
}

// parseSkillMd splits a SKILL.md into frontmatter + body. The
// frontmatter is a minimal YAML subset; the body is the
// SystemPrompt for the skill.
//
// Recognized frontmatter keys (case-sensitive):
//
//	name:         <string>
//	description:  <string>
//	tools:        <comma-separated list>
//
// Any other key is ignored without error — forward-compat for
// future extensions.
func parseSkillMd(raw []byte) (Skill, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	var inFront bool
	var sawDelim bool
	var bodyLines []string
	var sk Skill

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if !sawDelim {
			if trimmed == "---" {
				inFront = true
				sawDelim = true
				continue
			}
			// No frontmatter — treat whole file as body.
			bodyLines = append(bodyLines, line)
			continue
		}
		if inFront {
			if trimmed == "---" {
				inFront = false
				continue
			}
			if trimmed == "" {
				continue
			}
			key, val, ok := splitFrontmatterLine(trimmed)
			if !ok {
				return sk, fmt.Errorf("malformed frontmatter line: %q", trimmed)
			}
			switch key {
			case "name":
				sk.Name = val
			case "description":
				sk.Description = val
			case "tools":
				// Just metadata — actual tool wiring is the
				// host's job. We don't populate sk.Tools from
				// this; we just preserve the names by reading
				// them, in case future versions surface them.
				// For now, ignore — silent forward-compat.
				_ = val
			}
			continue
		}
		bodyLines = append(bodyLines, line)
	}
	if err := scanner.Err(); err != nil {
		return sk, fmt.Errorf("scan: %w", err)
	}
	sk.SystemPrompt = strings.TrimSpace(strings.Join(bodyLines, "\n"))
	return sk, nil
}

// splitFrontmatterLine parses "key: value" lines. Quoted values
// (single or double) have their quotes stripped. Returns ok=false
// when the line doesn't contain a colon separator.
func splitFrontmatterLine(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			value = value[1 : len(value)-1]
		}
	}
	return key, value, true
}
