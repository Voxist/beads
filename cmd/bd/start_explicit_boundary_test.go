package main

import (
	"fmt"
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
//   - It resolves EVERY import alias of doltserver per file (github.com/.../
//     doltserver imported as `ds`, dot-imported, or even imported twice under
//     different local names in the same file), rather than assuming the
//     identifier is literally "doltserver" or that only one import spec can
//     name it, so `ds.StartExplicit(...)` is still caught even alongside an
//     unrelated blank import of the same path.
//   - It matches the bare SelectorExpr/Ident reference, not just when it is
//     the Fun of a CallExpr, so a method value (`var f = ds.StartExplicit`)
//     is caught even though it is never itself "called" in this file.
//   - The allowance is scoped to the doltStartCmd variable's own ValueSpec
//     span inside cmd/bd/dolt.go -- not the enclosing GenDecl (which could
//     silently widen to cover unrelated declarations sharing one `var (...)`
//     block) and not the whole file -- so a reference from any OTHER
//     declaration in dolt.go is still an offender.
//
// Known limits, not fixed by construction: (1) it does not attempt to prove
// a reference is never actually invoked outside the allowed span (e.g. a
// StartExplicit method value captured inside doltStartCmd's own declaration
// and then stored somewhere that escapes and gets called elsewhere) -- that
// needs type-checking or dataflow analysis, not an AST walk; a legitimate
// wrapper still has to be visible textually as "doltserver.StartExplicit"
// SOMEWHERE, and this test finds that occurrence, but not everywhere its
// value might later be invoked. (2) a `//go:linkname` directive can bind a
// local symbol directly to an unexported function (including startInternal)
// entirely through the linker, bypassing normal identifier references and
// therefore this whole class of source-level AST scan; nothing here detects
// that, the same way go vet's own unsafeptr/linkname checks are a distinct
// mechanism from this one.
//
// See TestNoInternalDoltserverBypassOfStartInternal for calls to the
// unexported startInternal(dir, true) from inside the doltserver package
// itself, and TestNoInternalDoltserverReferencesStartExplicitOutsideOwnDeclaration
// for same-package references to the exported StartExplicit identifier --
// neither is reachable from this test, since a file inside internal/doltserver
// never imports itself and so never matches the alias resolution above.
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

		// internal/doltserver can never IMPORT itself, so it can never match
		// the alias resolution below -- it would only ever reference
		// StartExplicit as a bare same-package identifier, which the sibling
		// tests named in this function's doc police instead. Skipping the
		// directory here is purely an optimization, not a safety exclusion.
		if strings.HasPrefix(rel, "internal/doltserver/") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			// Non-Go-parseable files under a .go extension are not this
			// test's concern; skip rather than fail the whole scan.
			return nil
		}

		localNames, dotImported := resolveDoltserverImportNames(file)
		if len(localNames) == 0 && !dotImported {
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

		for _, pos := range findDoltserverStartExplicitRefs(file, localNames, dotImported) {
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
// `var <varName> = ...` declaration's own ValueSpec inside the ALREADY-
// PARSED file, so callers can test whether some other position from the
// SAME parse falls inside it.
//
// Deliberately the ValueSpec's span, not the enclosing GenDecl's: for a
// lone `var x = y` (no parens) the two happen to cover nearly the same
// range, but GenDecl.End() falls back to Specs[0].End() only when there is
// no Rparen -- if dolt.go were ever refactored to share one `var (...)`
// block between doltStartCmd and something else, the GenDecl span would
// silently widen to cover every spec in that block, exempting unrelated
// declarations from this scan along with it. The ValueSpec's own span
// cannot widen that way regardless of what else shares its GenDecl.
//
// Returns (token.NoPos, token.NoPos) if not found -- deliberately not a
// t.Fatal here, so this stays a pure function callers can assert on
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
					return vs.Pos(), vs.End()
				}
			}
		}
	}
	return token.NoPos, token.NoPos
}

// resolveDoltserverImportNames inspects file's imports and reports EVERY
// local identifier used to refer to doltserverImportPath in THIS file --
// there can legitimately be more than one (e.g. a blank `_` import for a
// side-effecting init alongside a second, named import of the same path
// used to reference its exported symbols) -- plus whether any import is a
// dot import (in which case its exported names are referenced bare, with no
// qualifier at all). Returns (nil, false) if the file does not import
// doltserver at all.
//
// This does NOT return early on the first match: doing so would miss a
// second import spec of the same path under a different local name (or a
// dot import) that happened to appear later in the import block.
func resolveDoltserverImportNames(file *ast.File) (localNames []string, dotImported bool) {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != doltserverImportPath {
			continue
		}
		switch {
		case imp.Name == nil:
			localNames = append(localNames, "doltserver") // default package identifier
		case imp.Name.Name == ".":
			dotImported = true
		case imp.Name.Name == "_":
			// blank import: side-effecting init only, no identifier to
			// reference StartExplicit through from THIS import spec -- but
			// another spec for the same path, earlier or later in this same
			// file, might still provide one, so keep scanning.
		default:
			localNames = append(localNames, imp.Name.Name) // aliased import
		}
	}
	return localNames, dotImported
}

// findDoltserverStartExplicitRefs returns the token.Pos of every reference to
// StartExplicit on the doltserver package within file, whether or not it is
// actually called (a bare `pkg.StartExplicit` method value counts).
//
// localNames/dotImported come from resolveDoltserverImportNames. For a
// normal (possibly aliased) import, a reference is any SelectorExpr
// `X.StartExplicit` where X is an *ast.Ident matching ANY name in
// localNames. For a dot import, a reference is any bare *ast.Ident named
// "StartExplicit" that is NOT itself the .Sel of some SelectorExpr (a
// selector's field name is never a free-standing reference to an
// outer-scope identifier).
func findDoltserverStartExplicitRefs(file *ast.File, localNames []string, dotImported bool) []token.Pos {
	var refs []token.Pos
	names := make(map[string]bool, len(localNames))
	for _, n := range localNames {
		names[n] = true
	}

	// Every position that is a SelectorExpr's .Sel field, across the whole
	// file -- collected first so the dot-import pass below can exclude them.
	selPositions := make(map[token.Pos]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			selPositions[sel.Sel.Pos()] = true
			if len(names) > 0 {
				if x, ok := sel.X.(*ast.Ident); ok && names[x.Name] && sel.Sel.Name == "StartExplicit" {
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

// funcDeclScopedVisitor walks an *ast.File (or subtree) tracking which
// top-level FuncDecl, if any, lexically encloses the current node -- NOT
// which function LITERAL encloses it. A func literal nested inside a
// FuncDecl's body is still considered "inside" that FuncDecl for this
// purpose; only crossing into a NEW top-level FuncDecl changes the tracked
// name, and code that is not inside any FuncDecl at all (a package-level var
// initializer, including one whose value is itself a func literal) is
// tracked as enclosingFuncDecl == "" and is NEVER treated as being "inside"
// any named function.
type funcDeclScopedVisitor struct {
	enclosingFuncDecl string
	visit             func(n ast.Node, enclosingFuncDecl string)
}

func (v *funcDeclScopedVisitor) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		return nil
	}
	v.visit(n, v.enclosingFuncDecl)
	if fd, ok := n.(*ast.FuncDecl); ok {
		if fd.Body == nil {
			return nil
		}
		child := &funcDeclScopedVisitor{enclosingFuncDecl: fd.Name.Name, visit: v.visit}
		ast.Walk(child, fd.Body)
		return nil // already walked the body manually above; don't double-walk it
	}
	return v
}

// TestNoInternalDoltserverBypassOfStartInternal is a sibling check to
// TestOnlyDoltStartCallsStartExplicit, scoped to the doltserver package
// itself: the only function anywhere in internal/doltserver's production
// (non-test) source allowed to call startInternal(dir, true) -- the literal
// forced=true bypass -- is StartExplicit itself. A new unexported wrapper
// added inside the package (e.g. `func startForRecovery(dir string) (*State,
// error) { return startInternal(dir, true) }`) would never write the string
// "StartExplicit" and so would be invisible to the whole-module scan above;
// this catches that shape instead.
//
// This walks the WHOLE file via funcDeclScopedVisitor, not just each
// top-level FuncDecl's own Body in isolation -- a call reached only through
// a package-level function LITERAL (`var sneaky = func(dir string) (*State,
// error) { return startInternal(dir, true) }`) is not inside any FuncDecl at
// all and must still be caught; walking only `range file.Decls` filtered to
// *ast.FuncDecl would miss it entirely, since that GenDecl/ValueSpec/FuncLit
// shape is never a FuncDecl.
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
			base := filepath.Base(filename)
			visitor := &funcDeclScopedVisitor{visit: func(n ast.Node, enclosingFuncDecl string) {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return
				}
				fn, ok := call.Fun.(*ast.Ident)
				if !ok || fn.Name != "startInternal" || len(call.Args) != 2 {
					return
				}
				arg, ok := call.Args[1].(*ast.Ident)
				if !ok || arg.Name != "true" {
					return
				}
				if enclosingFuncDecl != "StartExplicit" {
					label := enclosingFuncDecl
					if label == "" {
						label = "<package level, or inside a func literal not nested in any FuncDecl>"
					}
					pos := fset.Position(call.Pos())
					offenders = append(offenders, fmt.Sprintf("%s:%d (enclosing func %s)", base, pos.Line, label))
				}
			}}
			ast.Walk(visitor, file)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("startInternal(dir, true) called outside StartExplicit: %v\n"+
			"the forced=true bypass must stay reachable only through the one exported function whose"+
			" name says so", offenders)
	}
}

// TestNoInternalDoltserverReferencesStartExplicitOutsideOwnDeclaration is the
// same-package counterpart to TestOnlyDoltStartCallsStartExplicit. That test
// can never see a call like `return StartExplicit(dir)` written INSIDE
// internal/doltserver itself: from within the package, StartExplicit is a
// bare identifier with no `doltserver.` qualifier to match against, so a new
// wrapper such as
//
//	func startForRecovery(dir string) (*State, error) { return StartExplicit(dir) }
//
// would pass both TestOnlyDoltStartCallsStartExplicit (wrong package, wrong
// spelling) AND TestNoInternalDoltserverBypassOfStartInternal (it calls
// StartExplicit, not startInternal(dir, true), directly). This test closes
// that gap: any bare *ast.Ident named "StartExplicit" anywhere in
// internal/doltserver's production source, OTHER than the
// `func StartExplicit(...)` declaration itself, is an offender -- whether or
// not it is actually called (a same-package method value, `var f =
// StartExplicit`, is caught too, matching the cross-package scan's own
// standard).
func TestNoInternalDoltserverReferencesStartExplicitOutsideOwnDeclaration(t *testing.T) {
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
	sawDeclaration := false

	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			base := filepath.Base(filename)

			// The declaration's own *ast.Ident (fd.Name) is excluded by
			// position, not by "skip the first match": collect it first so
			// the generic scan below can recognize and skip exactly that
			// one identifier, however the file is organized.
			var declPos token.Pos
			for _, decl := range file.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "StartExplicit" {
					declPos = fd.Name.Pos()
					sawDeclaration = true
				}
			}

			ast.Inspect(file, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if !ok || ident.Name != "StartExplicit" {
					return true
				}
				if ident.Pos() == declPos {
					return true // the declaration itself
				}
				pos := fset.Position(ident.Pos())
				offenders = append(offenders, fmt.Sprintf("%s:%d", base, pos.Line))
				return true
			})
		}
	}

	if !sawDeclaration {
		t.Fatal("could not find `func StartExplicit(...)` in internal/doltserver -- has it been renamed or moved?")
	}
	if len(offenders) > 0 {
		t.Errorf("StartExplicit referenced from within internal/doltserver itself, outside its own declaration: %v\n"+
			"a same-package wrapper around StartExplicit is exactly as much a funnel bypass as one calling"+
			" startInternal(dir, true) directly", offenders)
	}
}
