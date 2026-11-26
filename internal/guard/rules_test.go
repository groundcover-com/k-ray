package guard

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docTestNameRE is a test name written in docs/rules.md, in backticks.
var docTestNameRE = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")

// testFuncRE is the declaration of a test function in a _test.go file.
var testFuncRE = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)

// TestRulesTableNamesRealTests keeps docs/rules.md and the tests together:
// every test that the rule table names must exist. A test that is renamed or
// removed fails this guard until the table is changed too.
func TestRulesTableNamesRealTests(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(root, "docs", "rules.md"))
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, m := range docTestNameRE.FindAllStringSubmatch(string(doc), -1) {
		named[m[1]] = true
	}
	if len(named) < 50 {
		t.Fatalf("docs/rules.md names only %d tests; the guard is not reading the table", len(named))
	}

	exist := map[string]bool{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Hidden directories (.git, other worktrees) and research notes hold
			// no tests of this module.
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "research" || d.Name() == "vendor" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range testFuncRE.FindAllStringSubmatch(string(b), -1) {
			exist[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name := range named {
		if !exist[name] {
			t.Errorf("docs/rules.md names %s, but no test function has that name", name)
		}
	}
}
