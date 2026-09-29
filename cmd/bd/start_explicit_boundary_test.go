package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyDoltStartCallsStartExplicit pins the ga-dpbbw funnel property: once
// doltserver.Start gates itself on the target directory's auto-start policy,
// the only legitimate reason to call doltserver.StartExplicit at all is the
// one command that asks for a server BY NAME -- `bd dolt start`
// (doltStartCmd in cmd/bd/dolt.go). Every other production call site must go
// through the gated Start and treat doltserver.ErrAutoStartDisabled as a
// skip signal.
//
// This is an AST scan, not a plain grep, so it survives comments and string
// literals mentioning "StartExplicit" without false-failing, and it walks
// the whole module so a new call site introduced anywhere -- not just in
// cmd/bd -- is caught. Test files are excluded: internal/doltserver's own
// tests call StartExplicit directly to exercise the bypass itself
// (TestStartExplicitBypassesAutoStartGate), which is expected and not a
// funnel violation.
func TestOnlyDoltStartCallsStartExplicit(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	const allowedFile = "cmd/bd/dolt.go"

	var offenders []string
	fset := token.NewFileSet()

	walkErr := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		// Skip the definition site itself and everything under the
		// doltserver package (Start/startInternal share the implementation
		// with StartExplicit by construction, not by a call the funnel
		// needs to police).
		if strings.HasPrefix(rel, "internal/doltserver/") {
			return nil
		}
		if rel == allowedFile {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			// Non-Go-parseable files under a .go extension are not this
			// test's concern; skip rather than fail the whole scan.
			return nil
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "StartExplicit" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "doltserver" {
				return true
			}
			offenders = append(offenders, rel)
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo: %v", walkErr)
	}

	if len(offenders) > 0 {
		t.Errorf("doltserver.StartExplicit called outside %s: %v\n"+
			"every implicit path must call doltserver.Start and treat ErrAutoStartDisabled as a skip signal",
			allowedFile, offenders)
	}
}
