package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// doltserverImportPath is the fully-qualified import path this scan looks
// for. Using the real path (not just the package name "doltserver") means an
// import alias is resolved correctly and a same-named unrelated package
// cannot masquerade as it.
const doltserverImportPath = "github.com/steveyegge/beads/internal/doltserver"

// TestOnlyDoltStartCallsStartExplicit pins the ga-dpbbw funnel property: once
// doltserver.Start gates itself on the target directory's auto-start policy,
// the only legitimate reason to reference doltserver.StartExplicit at all is
// the one command that asks for a server BY NAME -- the doltStartCmd RunE in
// cmd/bd/dolt.go. Every other production call site must go through the
// gated Start and treat doltserver.ErrAutoStartDisabled as a skip signal.
//
// This scan is deliberately stricter than "grep for StartExplicit":
//
//   - It resolves the import alias per file (github.com/.../doltserver
//     imported as `ds` or dot-imported) rather than assuming the identifier
//     is literally "doltserver", so `ds.StartExplicit(...)` is still caught.
//   - It matches the bare SelectorExpr/Ident reference, not just when it is
//     the Fun of a CallExpr, so a method value (`var f = ds.StartExplicit`)
//     is caught even though it is never itself "called" in this file.
//   - The allowance is scoped to the doltStartCmd declaration's own source
//     span inside cmd/bd/dolt.go, not the whole file -- a reference from any
//     OTHER declaration in dolt.go is still an offender.
//
// It does not attempt to prove a reference is never actually invoked (e.g. a
// StartExplicit method value stored in a struct field and called indirectly
// through it several hops away) -- that needs type-checking or dataflow
// analysis, not an AST walk. Catching the reference itself is the enforced
// invariant; a legitimate wrapper still has to be visible textually as
// "doltserver.StartExplicit" somewhere, and this test still finds that.
//
// See TestNoInternalDoltserverBypassOfStartInternal for the sibling check
// inside the doltserver package itself: a new unexported wrapper that calls
// startInternal(dir, true) directly, without ever writing "StartExplicit",
// would not be caught here at all -- it is caught there instead.
func TestOnlyDoltStartCallsStartExplicit(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	const allowedFile = "cmd/bd/dolt.go"
	const allowedVarName = "doltStartCmd"

	fset := token.NewFileSet()

	type occurrence struct {
		file string
		line int
	}
	var offenders []occurrence

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

		// internal/doltserver itself can never spell "doltserver.StartExplicit"
		// (it would just be the bare identifier StartExplicit, since it's the
		// same package) -- the funnel-bypass risk INSIDE that package is
		// policed separately, by TestNoInternalDoltserverBypassOfStartInternal.
		if strings.HasPrefix(rel, "internal/doltserver/") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			// Non-Go-parseable files under a .go extension are not this
			// test's concern; skip rather than fail the whole scan.
			return nil
		}

		localName, dotImported := resolveDoltserverImportName(file)
		if localName == "" && !dotImported {
			return nil // this file doesn't import doltserver at all
		}

		// Computed from THIS SAME parse (not a separate parser.ParseFile
		// call) so its token.Pos values share one token.File with the refs
		// found below -- re-parsing the same path a second time appends a
		// second, disjoint token.File to fset, whose positions never
		// numerically overlap with the first parse's, making any span
		// comparison across the two vacuously false.
		var allowedStart, allowedEnd token.Pos
		if rel == allowedFile {
			allowedStart, allowedEnd = declSpan(file, allowedVarName)
			if allowedStart == token.NoPos {
				t.Fatalf("could not find `var %s = ...` in %s -- has it been renamed or moved?", allowedVarName, rel)
			}
		}

		for _, pos := range findDoltserverStartExplicitRefs(file, localName, dotImported) {
			if rel == allowedFile && pos >= allowedStart && pos < allowedEnd {
				continue // inside doltStartCmd's own declaration: allowed
			}
			position := fset.Position(pos)
			offenders = append(offenders, occurrence{file: rel, line: position.Line})
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo: %v", walkErr)
	}

	if len(offenders) > 0 {
		t.Errorf("doltserver.StartExplicit referenced outside %s's %s declaration: %v\n"+
			"every implicit path must call doltserver.Start and treat ErrAutoStartDisabled as a skip signal",
			allowedFile, allowedVarName, offenders)
	}
}

// declSpan returns the [start, end) token.Pos span of the top-level
// `var <varName> = ...` declaration inside the ALREADY-PARSED file, so
// callers can test whether some other position from the SAME parse falls
// inside it. Returns (token.NoPos, token.NoPos) if not found -- deliberately
// not a t.Fatal here, so this stays a pure function callers can assert on
// themselves (see the caller's own check for why the distinction matters).
func declSpan(file *ast.File, varName string) (token.Pos, token.Pos) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if name.Name == varName {
					return gd.Pos(), gd.End()
				}
			}
		}
	}
	return token.NoPos, token.NoPos
}

// resolveDoltserverImportName inspects file's imports and reports the local
// identifier used to refer to doltserverImportPath in THIS file: either a
// named/default local package identifier (localName, dotImported=false), or
// dotImported=true if it was dot-imported (in which case its exported names
// are referenced bare, with no qualifier at all). Returns ("", false) if the
// file does not import doltserver.
func resolveDoltserverImportName(file *ast.File) (localName string, dotImported bool) {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != doltserverImportPath {
			continue
		}
		switch {
		case imp.Name == nil:
			return "doltserver", false // default package identifier
		case imp.Name.Name == ".":
			return "", true
		case imp.Name.Name == "_":
			return "", false // blank import: package-level init only, no identifier to reference StartExplicit through
		default:
			return imp.Name.Name, false // aliased import
		}
	}
	return "", false
}

// findDoltserverStartExplicitRefs returns the token.Pos of every reference to
// StartExplicit on the doltserver package within file, whether or not it is
// actually called (a bare `pkg.StartExplicit` method value counts).
//
// localName/dotImported come from resolveDoltserverImportName. For a normal
// (possibly aliased) import, a reference is any SelectorExpr `X.StartExplicit`
// where X is an *ast.Ident equal to localName. For a dot import, a reference
// is any bare *ast.Ident named "StartExplicit" that is NOT itself the .Sel of
// some SelectorExpr (a selector's field name is never a free-standing
// reference to an outer-scope identifier).
func findDoltserverStartExplicitRefs(file *ast.File, localName string, dotImported bool) []token.Pos {
	var refs []token.Pos

	// Every position that is a SelectorExpr's .Sel field, across the whole
	// file -- collected first so the dot-import pass below can exclude them.
	selPositions := make(map[token.Pos]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			selPositions[sel.Sel.Pos()] = true
			if !dotImported && localName != "" {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == localName && sel.Sel.Name == "StartExplicit" {
					refs = append(refs, sel.Pos())
				}
			}
		}
		return true
	})

	if dotImported {
		ast.Inspect(file, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok || ident.Name != "StartExplicit" {
				return true
			}
			if selPositions[ident.Pos()] {
				return true // this is some selector's field name, not a free reference
			}
			refs = append(refs, ident.Pos())
			return true
		})
	}

	return refs
}

// TestNoInternalDoltserverBypassOfStartInternal is the sibling check to
// TestOnlyDoltStartCallsStartExplicit, scoped to the doltserver package
// itself: the only function anywhere in internal/doltserver's production
// (non-test) source allowed to call startInternal(dir, true) -- the literal
// forced=true bypass -- is StartExplicit itself. A new unexported wrapper
// added inside the package (e.g. `func startForRecovery(dir string) (*State,
// error) { return startInternal(dir, true) }`) would never write the string
// "StartExplicit" and so would be invisible to the whole-module scan above;
// this catches that shape instead.
//
// Like the sibling test, this matches the literal boolean `true` as the
// second argument. A caller that launders it through a variable (`forced :=
// true; startInternal(dir, forced)`) is not caught -- proving that requires
// type-aware dataflow analysis, not an AST walk. That is a known, accepted
// limitation of this class of test (compare gascity's ga-vsew4, a
// run-time-assembled string invisible to its own AST funnel test), not an
// oversight.
func TestNoInternalDoltserverBypassOfStartInternal(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	dir := filepath.Join(repoRoot, "internal", "doltserver")

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}

	var offenders []string

	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					fn, ok := call.Fun.(*ast.Ident)
					if !ok || fn.Name != "startInternal" || len(call.Args) != 2 {
						return true
					}
					arg, ok := call.Args[1].(*ast.Ident)
					if !ok || arg.Name != "true" {
						return true
					}
					if fd.Name.Name != "StartExplicit" {
						offenders = append(offenders, filepath.Base(filename)+":"+fd.Name.Name)
					}
					return true
				})
			}
		}
	}

	if len(offenders) > 0 {
		t.Errorf("startInternal(dir, true) called outside StartExplicit: %v\n"+
			"the forced=true bypass must stay reachable only through the one exported function whose"+
			" name says so", offenders)
	}
}
