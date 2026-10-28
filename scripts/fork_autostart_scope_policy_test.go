package scripts_test

// FORK (Voxist/beads): auto-start decisions are workspace-scoped.
//
// The fork reads dolt.auto-start from the workspace's own config as well as
// BEADS_DOLT_AUTO_START and the global config (fork #56/#57), through
// doltserver.IsAutoStartDisabledFor(beadsDir). Upstream's global
// IsAutoStartDisabled() misses a workspace's own `dolt.auto-start: false`.
// The 2026-10-27 resync brought four new global calls in through a clean,
// conflict-free merge; this scan fails on the next one.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// scanGlobalAutoStartCalls returns "file:line" for every reference to
// IsAutoStartDisabled (package-qualified or not) in src: a call, a function
// value, or a struct field initialised with it. Only its own declaration is
// skipped.
func scanGlobalAutoStartCalls(rel string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, err
	}
	const name = "IsAutoStartDisabled"
	declared := map[*ast.Ident]bool{}
	selected := map[*ast.Ident]bool{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			declared[fn.Name] = true
		}
	}
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if x.Sel.Name == name {
				selected[x.Sel] = true
				found = append(found, rel+":"+strconv.Itoa(fset.Position(x.Pos()).Line))
			}
		case *ast.Ident:
			if x.Name == name && !declared[x] && !selected[x] {
				found = append(found, rel+":"+strconv.Itoa(fset.Position(x.Pos()).Line))
			}
		}
		return true
	})
	return found, nil
}

// TestGlobalAutoStartScanCatchesBothShapes keeps the scan from passing by
// matching nothing: a qualified call, a bare call, a function value and a
// struct field are all found; the declaration itself and the workspace-scoped
// IsAutoStartDisabledFor are not.
func TestGlobalAutoStartScanCatchesBothShapes(t *testing.T) {
	src := []byte("package p\n\nfunc a() bool { return doltserver.IsAutoStartDisabled() }\n\nfunc b() bool { return IsAutoStartDisabled() || IsAutoStartDisabledFor(\"d\") }\n\nvar f = doltserver.IsAutoStartDisabled\n\nvar s = struct{ off func() bool }{off: IsAutoStartDisabled}\n\nfunc IsAutoStartDisabled() bool { return false }\n")
	got, err := scanGlobalAutoStartCalls("p/p.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "p/p.go:3,p/p.go:5,p/p.go:7,p/p.go:9" {
		t.Fatalf("scan found %v, want the qualified call (3), the bare call (5), the function value (7) and the struct field (9), not the declaration (11)", got)
	}
}

func TestNoGlobalAutoStartCheckInProductionCode(t *testing.T) {
	root := sourceRepoRoot(t)
	var offenders []string
	scanned := 0
	for _, rel := range repoFiles(t, root, 100) {
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") ||
			strings.Contains(rel, "/testdata/") || strings.HasPrefix(rel, "testdata/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		scanned++
		found, err := scanGlobalAutoStartCalls(rel, src)
		if err != nil {
			continue // not this test's concern; the build reports it
		}
		offenders = append(offenders, found...)
	}
	if scanned < 500 {
		t.Fatalf("scanned only %d production Go files under %s; the listing is broken", scanned, root)
	}
	for _, o := range offenders {
		t.Errorf("%s calls the global doltserver.IsAutoStartDisabled(); use IsAutoStartDisabledFor(beadsDir) so the workspace's own dolt.auto-start counts (fork #56/#57)", o)
	}
}
