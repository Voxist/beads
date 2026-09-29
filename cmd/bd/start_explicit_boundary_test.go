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

		// internal/doltserver ITSELF can never import itself, so its own
		// files can never match the alias resolution below -- they would
		// only ever reference StartExplicit as a bare same-package
		// identifier, which the sibling tests named in this function's doc
		// police instead. Scoped to files DIRECTLY in that directory, not
		// the whole "internal/doltserver/" prefix: a SUBPACKAGE (e.g. a
		// hypothetical internal/doltserver/foo) is a DIFFERENT package that
		// could legitimately import and reference doltserver.StartExplicit
		// from the outside, exactly like any other caller in the module --
		// excluding the whole prefix would have silently exempted it too,
		// which would have been a real safety gap, not an optimization.
		if filepath.Dir(rel) == "internal/doltserver" {
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

// funcDeclLabel identifies fd for the "am I inside the allowed declaration"
// comparisons below. A RECEIVER-LESS function named "StartExplicit" labels
// as exactly "StartExplicit". Anything else -- including a METHOD also
// named StartExplicit, e.g. `func (Recovery) StartExplicit(dir string)
// (*State, error)` -- gets a label that can never equal "StartExplicit",
// because a same-named method is a completely different declaration and
// must never be mistaken for the one funnel exception. Comparing only
// fd.Name.Name (ignoring fd.Recv) was exactly this gap: it let a method
// literally named StartExplicit satisfy an `enclosingFuncDecl ==
// "StartExplicit"` check meant for the package-level function of that name
// (ga-dpbbw L1).
func funcDeclLabel(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	return "(method, not the funnel exception) " + fd.Name.Name
}

// funcDeclScopedVisitor walks an *ast.File (or subtree) tracking which
// top-level FuncDecl, if any, lexically encloses the current node -- NOT
// which function LITERAL encloses it. A func literal nested inside a
// FuncDecl's body is still considered "inside" that FuncDecl for this
// purpose; only crossing into a NEW top-level FuncDecl changes the tracked
// label (see funcDeclLabel for how a method is distinguished from a
// receiver-less function of the same name), and code that is not inside any
// FuncDecl at all (a package-level var initializer, including one whose
// value is itself a func literal) is tracked as enclosingFuncDecl == "" and
// is NEVER treated as being "inside" any named function.
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
		child := &funcDeclScopedVisitor{enclosingFuncDecl: funcDeclLabel(fd), visit: v.visit}
		ast.Walk(child, fd.Body)
		return nil // already walked the body manually above; don't double-walk it
	}
	return v
}

// parseInternalDoltserver parses internal/doltserver's production (non-test)
// source, shared by the two same-package guard tests below.
func parseInternalDoltserver(t *testing.T, fset *token.FileSet) map[string]*ast.Package {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	dir := filepath.Join(repoRoot, "internal", "doltserver")
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	return pkgs
}

// receiverlessStartExplicitDecls returns the *ast.Ident.Pos() of every
// RECEIVER-LESS `func StartExplicit(...)` declaration across pkgs. Excluding
// methods here (fd.Recv != nil) is exactly the fix for ga-dpbbw L1: a method
// also named StartExplicit, e.g. `func (Recovery) StartExplicit(dir string)
// (*State, error)`, is a distinct declaration and must never be treated as
// THE funnel exception just because its name string matches.
//
// Callers should assert this returns exactly one position: internal/doltserver
// is expected to declare precisely one receiver-less StartExplicit, and a
// count of zero or more than one means the assumption these guard tests are
// built on (a single, unambiguous, well-known declaration to exempt) no
// longer holds -- which the tests must fail loudly on rather than silently
// picking one arbitrarily.
func receiverlessStartExplicitDecls(pkgs map[string]*ast.Package) []token.Pos {
	var positions []token.Pos
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Name.Name != "StartExplicit" {
					continue
				}
				if fd.Recv != nil && len(fd.Recv.List) > 0 {
					continue // a method, not the funnel exception -- see funcDeclLabel
				}
				positions = append(positions, fd.Name.Pos())
			}
		}
	}
	return positions
}

// TestNoInternalDoltserverBypassOfStartInternal is a sibling check to
// TestOnlyDoltStartCallsStartExplicit, scoped to the doltserver package
// itself: the only function anywhere in internal/doltserver's production
// (non-test) source allowed to call startInternal(dir, true) -- the literal
// forced=true bypass -- is the RECEIVER-LESS StartExplicit itself. A new
// unexported wrapper added inside the package (e.g. `func startForRecovery(
// dir string) (*State, error) { return startInternal(dir, true) }`) would
// never write the string "StartExplicit" and so would be invisible to the
// whole-module scan above; this catches that shape instead.
//
// This walks the WHOLE file via funcDeclScopedVisitor, not just each
// top-level FuncDecl's own Body in isolation -- a call reached only through
// a package-level function LITERAL (`var sneaky = func(dir string) (*State,
// error) { return startInternal(dir, true) }`) is not inside any FuncDecl at
// all and must still be caught; walking only `range file.Decls` filtered to
// *ast.FuncDecl would miss it entirely, since that GenDecl/ValueSpec/FuncLit
// shape is never a FuncDecl. funcDeclLabel additionally makes sure a METHOD
// named StartExplicit (`func (Recovery) StartExplicit(dir string) (*State,
// error) { return startInternal(dir, true) }`, called elsewhere as
// `r.StartExplicit(dir)`) is never mistaken for the real, receiver-less
// exception just because the name string matches (ga-dpbbw L1) -- proven
// caught by reproducing exactly that shape and confirming this test fails,
// before reverting the probe.
//
// Known limits, not fixed by construction, documented rather than chased
// further: a function-VALUE alias (`var f = startInternal; f(dir, true)`),
// a parenthesized call written as `(startInternal)(dir, true)`, and a
// launder through a variable (`forced := true; startInternal(dir, forced)`)
// are all invisible to this literal-shape AST match. Each needs type-aware
// dataflow analysis to catch, not a syntactic walk -- the same class of gap
// as gascity's ga-vsew4 (a run-time-assembled string invisible to its own
// AST funnel test), not an oversight specific to this test.
func TestNoInternalDoltserverBypassOfStartInternal(t *testing.T) {
	fset := token.NewFileSet()
	pkgs := parseInternalDoltserver(t, fset)

	if decls := receiverlessStartExplicitDecls(pkgs); len(decls) != 1 {
		t.Fatalf("found %d receiver-less `func StartExplicit(...)` declarations in internal/doltserver, want exactly 1 -- "+
			"this test's exemption logic assumes there is exactly one to exempt", len(decls))
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
		t.Errorf("startInternal(dir, true) called outside the receiver-less StartExplicit: %v\n"+
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
// internal/doltserver's production source, OTHER than the RECEIVER-LESS
// `func StartExplicit(...)` declaration itself, is an offender -- whether or
// not it is actually called (a same-package method value, `var f =
// StartExplicit`, is caught too, matching the cross-package scan's own
// standard).
//
// Uses receiverlessStartExplicitDecls, and asserts it finds exactly one, for
// the same ga-dpbbw L1 reason as its sibling test: a METHOD also named
// StartExplicit (`func (Recovery) StartExplicit(dir string) (*State,
// error)`) is a different declaration and must never be excluded here as if
// it were the one real exception -- if it were, a reference to THAT method
// from elsewhere in the package would be silently allowed.
func TestNoInternalDoltserverReferencesStartExplicitOutsideOwnDeclaration(t *testing.T) {
	fset := token.NewFileSet()
	pkgs := parseInternalDoltserver(t, fset)

	decls := receiverlessStartExplicitDecls(pkgs)
	if len(decls) != 1 {
		t.Fatalf("found %d receiver-less `func StartExplicit(...)` declarations in internal/doltserver, want exactly 1 -- "+
			"this test's exemption logic assumes there is exactly one to exempt", len(decls))
	}
	declPos := decls[0]

	var offenders []string

	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			base := filepath.Base(filename)
			ast.Inspect(file, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if !ok || ident.Name != "StartExplicit" {
					return true
				}
				if ident.Pos() == declPos {
					return true // the one receiver-less declaration itself
				}
				pos := fset.Position(ident.Pos())
				offenders = append(offenders, fmt.Sprintf("%s:%d", base, pos.Line))
				return true
			})
		}
	}

	if len(offenders) > 0 {
		t.Errorf("StartExplicit referenced from within internal/doltserver itself, outside its own receiver-less declaration: %v\n"+
			"a same-package wrapper (function OR method) around StartExplicit is exactly as much a funnel bypass as one calling"+
			" startInternal(dir, true) directly", offenders)
	}
}
