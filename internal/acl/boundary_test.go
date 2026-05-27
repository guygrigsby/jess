package acl

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// preMigrationAgentcoreImporters are the files that still import agentcore
// outside the ACL while the encapsulation (ADR 0001) is in progress. Phase 4
// relocates the memory adapter and skills conversion into internal/acl and
// rewrites doc.go + the quickstart, after which this allowlist shrinks to
// nothing and the boundary becomes "only internal/acl".
//
// TODO(ADR-0001 Phase 4): delete these entries; the only allowed importer is
// internal/acl.
var preMigrationAgentcoreImporters = []string{
	"doc.go",
	"memory/",
	"skills/",
	"examples/",
}

// TestAgentcoreImportBoundary fails if any package outside internal/acl (and
// the temporary pre-migration allowlist) imports github.com/voocel/agentcore.
// This enforces ADR 0001's anti-corruption-layer boundary, protecting the new
// vendor-free domain packages (message, event, tool, subagent, root jess) from
// leaking the harness.
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
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/acl/") {
			return true
		}
		for _, p := range preMigrationAgentcoreImporters {
			if p == rel || (strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p)) {
				return true
			}
		}
		return false
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
