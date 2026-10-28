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
	"strings"
	"testing"
)

// scanGlobalAutoStartCalls returns "file:line" for every call of
// IsAutoStartDisabled() (package-qualified or not) in src.
func scanGlobalAutoStartCalls(rel string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, err
	}
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		}
		if name == "IsAutoStartDisabled" {
			found = append(found, rel+":"+itoa(fset.Position(call.Pos()).Line))
		}
		return true
	})
	return found, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// TestGlobalAutoStartScanCatchesBothShapes keeps the scan from passing by
// matching nothing.
func TestGlobalAutoStartScanCatchesBothShapes(t *testing.T) {
	src := []byte("package p\n\nfunc a() bool { return doltserver.IsAutoStartDisabled() }\n\nfunc b() bool { return IsAutoStartDisabled() || IsAutoStartDisabledFor(\"d\") }\n")
	got, err := scanGlobalAutoStartCalls("p/p.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "p/p.go:3,p/p.go:5" {
		t.Fatalf("scan found %v, want the qualified call on line 3 and the bare one on line 5", got)
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
