// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setForTest sets *p to v until t ends: t.Cleanup puts back what was there,
// whether the test passes, fails or stops at a require. A package variable
// (globalOutput, errOut, buildClient, ...) a test changes and resets on a
// plain line stays changed when the test stops before that line, and every
// later test that reads it fails.
func setForTest[T any](t testing.TB, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// keepStyle puts back, when t ends, what running the root command sets for
// the whole package: sty and errOut (useStyle, before every command). A test
// that runs the root command (Execute) calls it, so a later test that calls
// a function directly does not write to this test's buffer.
func keepStyle(t testing.TB) {
	t.Helper()
	oldSty, oldErr := sty, errOut
	t.Cleanup(func() { sty, errOut = oldSty, oldErr })
}

// TestPackageVariablesAreRestored: every Test, Benchmark and Fuzz function
// that changes a package variable of this package puts it back when it
// ends, so one failing test cannot fail the ones after it. A change is an
// assignment to the variable (or to a field, element or pointee of it), ++
// or --, or running the root command (Execute sets errOut and sty). It is
// put back by setForTest or keepStyle, or by a cleanup (the Cleanup of a
// *testing.T, B, F or testing.TB, or a defer) that assigns it the copy
// saved from it (prev := v, while prev still holds it), its initializer, or
// the zero value when it has none; any other value is a change. A cleanup
// may be a function literal, a local bound to one, a function by name, or
// what a helper returns (t.Cleanup(stub())), whose other changes count. Names
// are resolved by type: a local of the same name is not the package's, and
// the functions followed are every function and method of the package, test
// files or not, called directly, through a local (f := stub; f()) or passed
// by name (t.Run("s", sub), taken to be called). TestMain is exempt: it sets
// up the whole run. Not seen: writes through a pointer or a method of
// another package (other than Execute), and a function value passed through
// a struct field or a parameter.
func TestPackageVariablesAreRestored(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	srcs := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		srcs[f] = string(b)
	}
	leaks, err := restoreLeaks(srcs)
	require.NoError(t, err)
	assert.Empty(t, leaks, "put each back with setForTest(t, &v, value), keepStyle(t) (for Execute), or save it and restore it in t.Cleanup")
}

// TestRestoreLeaks: the check on small packages, a leak and a safe form of
// each kind.
func TestRestoreLeaks(t *testing.T) {
	const pkg = "package cmd\n\nvar x, y string\nvar out *int\n"
	cases := []struct {
		name, test string
		leaks      []string
		src        string // more of x.go
	}{
		{"reset on a plain line", `func TestA(t *testing.T) { x = "json"; check(); x = "" }`, []string{"TestA changes x"}, ""},
		{"a Cleanup", `func TestA(t *testing.T) { prev := x; t.Cleanup(func() { x = prev }); x = "json" }`, nil, ""},
		{"a defer", `func TestA(t *testing.T) { prev := x; defer func() { x = prev }(); x = "json" }`, nil, ""},
		{"a restore func", `func TestA(t *testing.T) { prev := x; restore := func() { x = prev }; t.Cleanup(restore); x = "a" }`, nil, ""},
		{"setForTest", `func TestA(t *testing.T) { setForTest(t, &x, "a") }`, nil, ""},
		{"a local value is not a saved one", `func TestA(t *testing.T) { v := "json"; x = v }`, []string{"TestA changes x"}, ""},
		{"saved from another variable", `func TestA(t *testing.T) { prev := y; x = "a"; x = prev }`, []string{"TestA changes x"}, ""},
		{"a Cleanup that only reads it", `func TestA(t *testing.T) { t.Cleanup(func() { t.Log(x) }); x = "a" }`, []string{"TestA changes x"}, ""},
		{"a local of the same name", `func TestA(t *testing.T) { x := "a"; x = "b"; _ = x }`, nil, ""},
		{"a parameter of the same name", `func TestA(t *testing.T) { f := func(x string) { x = "b"; _ = x }; f("") }`, nil, ""},
		{"a shadow does not hide the package's", `func TestA(t *testing.T) { x = "a"; { x := "b"; _ = x } }`, []string{"TestA changes x"}, ""},
		{"++, a pointee", `func TestA(t *testing.T) { *out = 1; y += "a" }`, []string{"TestA changes out", "TestA changes y"}, ""},
		{"a helper that registers its restore", `func stub(t *testing.T) { prev := x; t.Cleanup(func() { x = prev }); x = "a" }
func TestA(t *testing.T) { stub(t); x = "b" }`, nil, ""},
		{"a helper that returns its restore", `func stub() func() { prev := x; x = "a"; return func() { x = prev } }
func TestA(t *testing.T) { t.Cleanup(stub()) }`, nil, ""},
		{"a helper that leaks", `func stub() { x = "a" }
func TestA(t *testing.T) { stub() }`, []string{"TestA changes x"}, ""},
		{"running the root command", `func TestA(t *testing.T) { root := NewRootCmd(); _ = root.Execute() }`,
			[]string{"TestA changes errOut", "TestA changes sty"}, ""},
		{"keepStyle", `func keepStyle(t *testing.T) { a, b := errOut, sty; t.Cleanup(func() { errOut, sty = a, b }) }
func TestA(t *testing.T) { keepStyle(t); root := NewRootCmd(); _ = root.Execute() }`, nil, ""},
		{"TestMain", `func TestMain(m *testing.M) { x = "a"; m.Run() }`, nil, ""},
		// #1778: what the check did not see.
		{"a subtest by name", `func sub(t *testing.T) { x = "a" }
func TestA(t *testing.T) { t.Run("s", sub) }`, []string{"TestA changes x"}, ""},
		{"a subtest by name that restores", `func sub(t *testing.T) { setForTest(t, &x, "a") }
func TestA(t *testing.T) { t.Run("s", sub) }`, nil, ""},
		{"a cleanup by name", `func reset() { x = "" }
func TestA(t *testing.T) { t.Cleanup(reset); x = "a" }`, nil, ""},
		{"a returned restore that misses one", `func stub() func() { px := x; x = "a"; y = "b"; return func() { x = px } }
func TestA(t *testing.T) { t.Cleanup(stub()) }`, []string{"TestA changes y"}, ""},
		{"a deferred returned restore that misses one", `func stub() func() { px := x; x = "a"; y = "b"; return func() { x = px } }
func TestA(t *testing.T) { defer stub()() }`, []string{"TestA changes y"}, ""},
		{"a saved copy changed", `func TestA(t *testing.T) { prev := x; prev = "a"; x = prev }`, []string{"TestA changes x"}, ""},
		{"a saved copy changed, in a cleanup", `func TestA(t *testing.T) { prev := x; prev = "b"; t.Cleanup(func() { x = prev }); x = "a" }`,
			[]string{"TestA changes x"}, ""},
		{"a saved copy saved again", `func TestA(t *testing.T) { prev := y; prev = x; t.Cleanup(func() { x = prev }); x = "a" }`, nil, ""},
		{"a method helper", `type h struct{}
func (h) stub() { x = "a" }
func TestA(t *testing.T) { var v h; v.stub() }`, []string{"TestA changes x"}, ""},
		{"a function value", `func stub() { x = "a" }
func TestA(t *testing.T) { f := stub; f() }`, []string{"TestA changes x"}, ""},
		{"a helper in a non-test file", `func TestA(t *testing.T) { setX() }`, []string{"TestA changes x"},
			"func setX() { x = \"a\" }\n"},
		{"a Cleanup that is not testing's", `type r struct{}
func (r) Cleanup(f func()) {}
func TestA(t *testing.T) { var c r; prev := x; c.Cleanup(func() { x = prev }); x = "a" }`, []string{"TestA changes x"}, ""},
		{"a helper's testing.TB", `func stub(tb testing.TB) { prev := x; tb.Cleanup(func() { x = prev }); x = "a" }
func TestA(t *testing.T) { stub(t) }`, nil, ""},
		{"a cleanup that sets another value", `func TestA(t *testing.T) { t.Cleanup(func() { x = "zzz" }); x = "a" }`,
			[]string{"TestA changes x"}, ""},
		{"a cleanup back to the zero value", `func TestA(t *testing.T) { t.Cleanup(func() { x = "" }); x = "a" }`, nil, ""},
		{"a cleanup back to the initializer", `func TestA(t *testing.T) { t.Cleanup(func() { z = mk(1) }); z = nil }`, nil,
			"var z = mk(1)\nfunc mk(int) *int { return nil }\n"},
		{"a cleanup to another call", `func TestA(t *testing.T) { t.Cleanup(func() { z = mk(2) }); z = nil }`,
			[]string{"TestA changes z"}, "var z = mk(1)\nfunc mk(int) *int { return nil }\n"},
		{"a Benchmark", `func BenchmarkA(b *testing.B) { x = "a" }`, []string{"BenchmarkA changes x"}, ""},
		{"a Fuzz test that restores", `func FuzzA(f *testing.F) { prev := x; f.Cleanup(func() { x = prev }); x = "a" }`, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaks, err := restoreLeaks(map[string]string{
				"x.go":      pkg + "var errOut, sty any\n" + tc.src,
				"x_test.go": "package cmd\n\nimport \"testing\"\n\n" + tc.test + "\n",
			})
			require.NoError(t, err)
			var got []string
			for _, l := range leaks {
				got = append(got, l[strings.Index(l, ": ")+2:])
			}
			assert.Equal(t, tc.leaks, got)
		})
	}
}

// restoreLeaks are the changes to package variables that the Test, Benchmark
// and Fuzz functions of the package in srcs (file name to source) do not put
// back, each as "file:line:col: TestX changes v", sorted.
func restoreLeaks(srcs map[string]string) ([]string, error) {
	fset := token.NewFileSet()
	a := &restoreCheck{info: &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}},
		scopes: map[*types.Scope]bool{}, decls: map[types.Object]*ast.FuncDecl{}, byName: map[string]*ast.FuncDecl{},
		testing: map[types.Object]bool{}, inits: map[string]string{}, done: map[*ast.FuncDecl]*restoreSummary{}}
	var tests []*ast.FuncDecl
	names := make([]string, 0, len(srcs))
	for f := range srcs {
		names = append(names, f)
	}
	slices.Sort(names)
	byPkg := map[string][]*ast.File{}
	var files []*ast.File
	testFile := map[*ast.File]bool{}
	for _, f := range names {
		file, err := parser.ParseFile(fset, f, srcs[f], parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		byPkg[file.Name.Name] = append(byPkg[file.Name.Name], file)
		files = append(files, file)
		testFile[file] = strings.HasSuffix(f, "_test.go")
	}
	// Each package (cmd, and cmd_test) is type-checked for what its names
	// refer to; imports are empty stand-ins, so their errors are ignored.
	for name, files := range byPkg {
		conf := types.Config{Importer: emptyImporter{}, Error: func(error) {}}
		pkg, _ := conf.Check(name, fset, files, a.info)
		a.scopes[pkg.Scope()] = true
	}
	for _, file := range files {
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				if obj := a.info.Defs[d.Name]; obj != nil {
					a.decls[obj] = d
				}
				if d.Recv == nil && file.Name.Name == "cmd" {
					a.byName[d.Name.Name] = d
				}
				if testFile[file] && d.Recv == nil && d.Name.Name != "TestMain" &&
					(strings.HasPrefix(d.Name.Name, "Test") || strings.HasPrefix(d.Name.Name, "Benchmark") || strings.HasPrefix(d.Name.Name, "Fuzz")) {
					tests = append(tests, d)
				}
			case *ast.GenDecl:
				// What a package variable starts as: its initializer, or
				// "" for the zero value.
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok && d.Tok == token.VAR {
						for i, n := range vs.Names {
							init := ""
							if len(vs.Values) == len(vs.Names) {
								init = types.ExprString(vs.Values[i])
							}
							a.inits[n.Name] = init
						}
					}
				}
			}
		}
		// The *testing.T, B, F and testing.TB parameters: their Cleanup is
		// testing's.
		ast.Inspect(file, func(n ast.Node) bool {
			if ft, ok := n.(*ast.FuncType); ok && ft.Params != nil {
				for _, f := range ft.Params.List {
					switch types.ExprString(f.Type) {
					case "*testing.T", "*testing.B", "*testing.F", "testing.TB":
						for _, id := range f.Names {
							if obj := a.info.Defs[id]; obj != nil {
								a.testing[obj] = true
							}
						}
					}
				}
			}
			return true
		})
	}
	var leaks []string
	for _, fn := range tests {
		sum := a.summary(fn)
		for v, pos := range sum.writes {
			if !sum.restored[v] {
				leaks = append(leaks, fmt.Sprintf("%s: %s changes %s", fset.Position(pos), fn.Name.Name, v))
			}
		}
	}
	slices.Sort(leaks)
	return leaks, nil
}

// emptyImporter imports every package as an empty one: the check needs the
// package's own names only.
type emptyImporter struct{}

func (emptyImporter) Import(path string) (*types.Package, error) {
	p := types.NewPackage(path, filepath.Base(path))
	p.MarkComplete()
	return p, nil
}

// restoreCheck is TestPackageVariablesAreRestored's view of the package.
type restoreCheck struct {
	info    *types.Info                    // what each name refers to
	scopes  map[*types.Scope]bool          // the package scopes
	decls   map[types.Object]*ast.FuncDecl // every function and method with a body, test files or not
	byName  map[string]*ast.FuncDecl       // package cmd's functions, for cmd.X from package cmd_test
	testing map[types.Object]bool          // the *testing.T, B, F and testing.TB parameters
	inits   map[string]string              // a package variable's initializer ("" for the zero value)
	done    map[*ast.FuncDecl]*restoreSummary
}

// restoreSummary is what a function changes and puts back.
type restoreSummary struct {
	writes   map[string]token.Pos // changed, where first
	restored map[string]bool      // put back by a cleanup it registers
	returns  map[string]bool      // put back by a function it returns
}

// pkgVar is the package variable id names, or "" (a local, a field, a
// parameter, or anything else).
func (a *restoreCheck) pkgVar(id *ast.Ident) string {
	if v, ok := a.info.ObjectOf(id).(*types.Var); ok && !v.IsField() && a.scopes[v.Parent()] {
		return id.Name
	}
	return ""
}

// object is what id names (nil when unknown).
func (a *restoreCheck) object(id *ast.Ident) types.Object { return a.info.ObjectOf(id) }

// rootVar is the package variable an assignment target changes: v, v.F,
// v[k], *v.
func (a *restoreCheck) rootVar(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return a.pkgVar(x)
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return ""
		}
	}
}

// callee is the function e names: a function or method of the package, a
// local bound to one (aliases: f := stub), or cmd.X from package cmd_test.
func (a *restoreCheck) callee(e ast.Expr, aliases map[types.Object]*ast.FuncDecl) *ast.FuncDecl {
	switch x := e.(type) {
	case *ast.Ident:
		if d := a.decls[a.object(x)]; d != nil {
			return d
		}
		return aliases[a.object(x)]
	case *ast.SelectorExpr:
		if d := a.decls[a.info.Uses[x.Sel]]; d != nil {
			return d
		}
		if id, ok := x.X.(*ast.Ident); ok && id.Name == "cmd" && (a.object(id) == nil || isPkgName(a.object(id))) {
			return a.byName[x.Sel.Name]
		}
	case *ast.ParenExpr:
		return a.callee(x.X, aliases)
	}
	return nil
}

// isPkgName reports whether obj is an imported package's name.
func isPkgName(obj types.Object) bool {
	_, ok := obj.(*types.PkgName)
	return ok
}

// isZero reports whether e is a zero-value literal.
func isZero(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return x.Value == `""` || x.Value == "``" || x.Value == "0"
	case *ast.Ident:
		return x.Name == "nil" || x.Name == "false"
	}
	return false
}

// restores are the package variables the body of a cleanup n puts back: an
// assignment of the copy saved from it (saved), of its initializer, or of
// the zero value when it has none; setForTest. Any other value is a write.
func (a *restoreCheck) restores(n ast.Node, saved map[types.Object]string) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE || len(n.Lhs) != len(n.Rhs) {
				return true
			}
			for i, l := range n.Lhs {
				v := a.rootVar(l)
				if v == "" {
					continue
				}
				r := n.Rhs[i]
				init, known := a.inits[v]
				rid, isID := r.(*ast.Ident)
				switch {
				case isID && a.object(rid) != nil && saved[a.object(rid)] == v:
				case known && init != "" && types.ExprString(r) == init:
				case known && init == "" && isZero(r):
				default:
					continue
				}
				out[v] = true
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "setForTest" && len(n.Args) > 1 {
				if u, ok := n.Args[1].(*ast.UnaryExpr); ok {
					if v := a.rootVar(u.X); v != "" {
						out[v] = true
					}
				}
			}
		}
		return true
	})
	return out
}

// summary is what function fn changes and puts back, the functions it calls
// included.
func (a *restoreCheck) summary(fn *ast.FuncDecl) *restoreSummary {
	if s, ok := a.done[fn]; ok {
		return s
	}
	s := &restoreSummary{writes: map[string]token.Pos{}, restored: map[string]bool{}, returns: map[string]bool{}}
	a.done[fn] = s                              // a recursive call sees what is known so far
	saved := map[types.Object]string{}          // prev := v
	locals := map[types.Object]*ast.FuncLit{}   // restore := func() { ... }
	aliases := map[types.Object]*ast.FuncDecl{} // f := stub
	write := func(v string, pos token.Pos) {
		if _, ok := s.writes[v]; !ok && v != "" {
			s.writes[v] = pos
		}
	}
	merge := func(into, from map[string]bool) {
		for v := range from {
			into[v] = true
		}
	}
	// call is a call of helper h at pos: what it changes and does not put
	// back is changed here; what it registers a cleanup for is put back.
	call := func(h *ast.FuncDecl, pos token.Pos) {
		if h == fn {
			return
		}
		hs := a.summary(h)
		for v := range hs.writes {
			if !hs.restored[v] {
				write(v, pos)
			}
		}
		merge(s.restored, hs.restored)
	}
	// cleanup is what cleanup function e puts back: a function literal, a
	// local bound to one, a function by name, or a helper's call that
	// returns one (whose own changes are changes here).
	cleanup := func(e ast.Expr) map[string]bool {
		switch x := e.(type) {
		case *ast.FuncLit:
			return a.restores(x, saved)
		case *ast.Ident:
			if lit := locals[a.object(x)]; lit != nil {
				return a.restores(lit, saved)
			}
			if d := a.callee(x, aliases); d != nil {
				return a.restores(d.Body, nil)
			}
		case *ast.CallExpr:
			if h := a.callee(x.Fun, aliases); h != nil {
				call(h, x.Pos())
				return a.summary(h).returns
			}
		}
		return nil
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, l := range n.Lhs {
				var r ast.Expr
				if len(n.Rhs) == len(n.Lhs) {
					r = n.Rhs[i]
				}
				rid, _ := r.(*ast.Ident)
				if id, ok := l.(*ast.Ident); ok && a.object(id) != nil && a.pkgVar(id) == "" {
					// A local: what it holds now.
					obj := a.object(id)
					delete(saved, obj)
					delete(locals, obj)
					delete(aliases, obj)
					switch {
					case rid != nil && a.pkgVar(rid) != "":
						saved[obj] = a.pkgVar(rid)
					case rid != nil && a.callee(rid, aliases) != nil:
						aliases[obj] = a.callee(rid, aliases)
					}
					if lit, ok := r.(*ast.FuncLit); ok {
						locals[obj] = lit
					}
					continue
				}
				if n.Tok == token.DEFINE {
					continue
				}
				v := a.rootVar(l)
				if rid != nil && v != "" && a.object(rid) != nil && saved[a.object(rid)] == v {
					continue // putting back what was saved from it
				}
				write(v, l.Pos())
			}
		case *ast.IncDecStmt:
			write(a.rootVar(n.X), n.Pos())
		case *ast.DeferStmt:
			if h := a.callee(n.Call.Fun, aliases); h != nil {
				call(h, n.Pos()) // defer helper(): it runs, at the end
			} else {
				merge(s.restored, cleanup(n.Call.Fun))
			}
			return false
		case *ast.ReturnStmt:
			for _, r := range n.Results {
				switch x := r.(type) {
				case *ast.FuncLit:
					merge(s.returns, a.restores(x, saved))
				case *ast.Ident:
					if lit := locals[a.object(x)]; lit != nil {
						merge(s.returns, a.restores(lit, saved))
					}
				case *ast.CallExpr:
					if h := a.callee(x.Fun, aliases); h != nil {
						merge(s.returns, a.summary(h).returns)
					}
				}
			}
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && sel.Sel.Name == "Cleanup" && a.testing[a.object(x)] && len(n.Args) == 1 {
					merge(s.restored, cleanup(n.Args[0]))
					return false
				}
				switch sel.Sel.Name {
				case "Execute", "ExecuteContext", "ExecuteC", "ExecuteContextC":
					write("errOut", n.Pos()) // useStyle, before every command
					write("sty", n.Pos())
				}
			}
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "setForTest" && len(n.Args) > 1 {
				if u, ok := n.Args[1].(*ast.UnaryExpr); ok {
					s.restored[a.rootVar(u.X)] = true
				}
			} else if h := a.callee(n.Fun, aliases); h != nil {
				call(h, n.Pos())
			}
			// A function passed by name (t.Run("s", sub)) is taken to be
			// called.
			for _, arg := range n.Args {
				if id, ok := arg.(*ast.Ident); ok {
					if h := a.callee(id, aliases); h != nil {
						call(h, arg.Pos())
					}
				}
			}
		}
		return true
	})
	return s
}
