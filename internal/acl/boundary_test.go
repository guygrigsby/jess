package acl

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentcoreImportBoundary fails if any package outside internal/acl imports
// github.com/voocel/agentcore. This enforces ADR 0001's anti-corruption-layer
// boundary, keeping the vendor-free domain packages (message, event, tool,
// model, subagent, memory, skill, root jess) free of the harness.
//
// It parses each file's import declarations (parser.ImportsOnly) rather than
// scanning the raw bytes, so a mere mention of the path in a comment or string
// literal does not trigger a false positive.
func TestAgentcoreImportBoundary(t *testing.T) {
	root, err := filepath.Abs("../..") // module root, two levels above internal/acl
	if err != nil {
		t.Fatal(err)
	}
	const dep = "github.com/voocel/agentcore"

	allowed := func(rel string) bool {
		return strings.HasPrefix(filepath.ToSlash(rel), "internal/acl/")
	}

	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if allowed(rel) {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == dep || strings.HasPrefix(p, dep+"/") {
				t.Errorf("%s imports %s; only internal/acl may (see ADR 0001)", filepath.ToSlash(rel), p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
