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
// (globalOutput, errOut, newClient, ...) a test changes and resets on a
// plain line stays changed when the test stops before that line, and every
// later test that reads it fails.
func setForTest[T any](t testing.TB, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// keepStyle puts back, when t ends, what running the root command sets for
// the whole package: sty, errOut and skewCheck (useStyle, before every command). A test
// that runs the root command (Execute) calls it, so a later test that calls
// a function directly does not write to this test's buffer.
func keepStyle(t testing.TB) {
	t.Helper()
	oldSty, oldErr, oldSkew := sty, errOut, skewCheck
	t.Cleanup(func() { sty, errOut, skewCheck = oldSty, oldErr, oldSkew })
}

// TestPackageVariablesAreRestored: every test that changes a package
// variable of this package puts it back when it ends, so one failing test
// cannot fail the ones after it. A change is an assignment to the variable
// (or to a field, element or pointee of it), ++ or --, or running the root
// command (Execute sets errOut, sty and skewCheck). It is put back by an assignment to
// it in a t.Cleanup or defer function, by setForTest or keepStyle, or by a
// helper that registers such a cleanup (noCluster) or returns one that the
// test registers (t.Cleanup(stub())). Names are resolved by scope: a local
// variable of the same name is not the package's. "v = prev" is a restore
// only when prev was saved from v. Helpers are followed one call deep at a
// time (their own calls included). TestMain is exempt: it sets up the whole
// run. Not seen (#1778): writes through a pointer or a method of another
// package (other than Execute); a function passed by name (t.Run("s", sub));
// what a helper whose restore is registered (t.Cleanup(stub())) changes and
// does not restore; helpers that are methods, function values or in non-test
// files.
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
	}{
		{"reset on a plain line", `func TestA(t *testing.T) { x = "json"; check(); x = "" }`, []string{"TestA changes x"}},
		{"a Cleanup", `func TestA(t *testing.T) { prev := x; t.Cleanup(func() { x = prev }); x = "json" }`, nil},
		{"a defer", `func TestA(t *testing.T) { prev := x; defer func() { x = prev }(); x = "json" }`, nil},
		{"a restore func", `func TestA(t *testing.T) { prev := x; restore := func() { x = prev }; t.Cleanup(restore); x = "a" }`, nil},
		{"setForTest", `func TestA(t *testing.T) { setForTest(t, &x, "a") }`, nil},
		{"a local value is not a saved one", `func TestA(t *testing.T) { v := "json"; x = v }`, []string{"TestA changes x"}},
		{"saved from another variable", `func TestA(t *testing.T) { prev := y; x = "a"; x = prev }`, []string{"TestA changes x"}},
		{"a Cleanup that only reads it", `func TestA(t *testing.T) { t.Cleanup(func() { t.Log(x) }); x = "a" }`, []string{"TestA changes x"}},
		{"a local of the same name", `func TestA(t *testing.T) { x := "a"; x = "b"; _ = x }`, nil},
		{"a parameter of the same name", `func TestA(t *testing.T) { f := func(x string) { x = "b"; _ = x }; f("") }`, nil},
		{"a shadow does not hide the package's", `func TestA(t *testing.T) { x = "a"; { x := "b"; _ = x } }`, []string{"TestA changes x"}},
		{"++, a pointee", `func TestA(t *testing.T) { *out = 1; y += "a" }`, []string{"TestA changes out", "TestA changes y"}},
		{"a helper that registers its restore", `func stub(t *testing.T) { prev := x; t.Cleanup(func() { x = prev }); x = "a" }
func TestA(t *testing.T) { stub(t); x = "b" }`, nil},
		{"a helper that returns its restore", `func stub() func() { prev := x; x = "a"; return func() { x = prev } }
func TestA(t *testing.T) { t.Cleanup(stub()) }`, nil},
		{"a helper that leaks", `func stub() { x = "a" }
func TestA(t *testing.T) { stub() }`, []string{"TestA changes x"}},
		{"running the root command", `func TestA(t *testing.T) { root := NewRootCmd(); _ = root.Execute() }`,
			[]string{"TestA changes errOut", "TestA changes skewCheck", "TestA changes sty"}},
		{"keepStyle", `func keepStyle(t *testing.T) { a, b, c := errOut, sty, skewCheck; t.Cleanup(func() { errOut, sty, skewCheck = a, b, c }) }
func TestA(t *testing.T) { keepStyle(t); root := NewRootCmd(); _ = root.Execute() }`, nil},
		{"TestMain", `func TestMain(m *testing.M) { x = "a"; m.Run() }`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaks, err := restoreLeaks(map[string]string{
				"x.go":      pkg + "var errOut, sty, skewCheck any\n",
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

// restoreLeaks are the changes to package variables that the Test
// functions of the package in srcs (file name to source) do not put back,
// each as "file:line:col: TestX changes v", sorted.
func restoreLeaks(srcs map[string]string) ([]string, error) {
	fset := token.NewFileSet()
	a := &restoreCheck{info: &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}},
		scopes: map[*types.Scope]bool{}, funcs: map[string]*ast.FuncDecl{}, done: map[string]*restoreSummary{}}
	var tests []*ast.FuncDecl
	names := make([]string, 0, len(srcs))
	for f := range srcs {
		names = append(names, f)
	}
	slices.Sort(names)
	byPkg := map[string][]*ast.File{}
	for _, f := range names {
		file, err := parser.ParseFile(fset, f, srcs[f], parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		byPkg[file.Name.Name] = append(byPkg[file.Name.Name], file)
		for _, d := range file.Decls {
			if d, ok := d.(*ast.FuncDecl); ok && d.Recv == nil && d.Body != nil && strings.HasSuffix(f, "_test.go") {
				a.funcs[d.Name.Name] = d
				if strings.HasPrefix(d.Name.Name, "Test") && d.Name.Name != "TestMain" {
					tests = append(tests, d)
				}
			}
		}
	}
	// Each package (cmd, and cmd_test) is type-checked for what its names
	// refer to; imports are empty stand-ins, so their errors are ignored.
	for name, files := range byPkg {
		conf := types.Config{Importer: emptyImporter{}, Error: func(error) {}}
		pkg, _ := conf.Check(name, fset, files, a.info)
		a.scopes[pkg.Scope()] = true
	}
	var leaks []string
	for _, fn := range tests {
		sum := a.summary(fn.Name.Name)
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
	info   *types.Info              // what each name refers to
	scopes map[*types.Scope]bool    // the package scopes
	funcs  map[string]*ast.FuncDecl // the functions of the test files
	done   map[string]*restoreSummary
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

// assigned are the package variables n assigns.
func (a *restoreCheck) assigned(n ast.Node) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok != token.DEFINE {
				for _, l := range n.Lhs {
					if v := a.rootVar(l); v != "" {
						out[v] = true
					}
				}
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

// restoresOf are the package variables function e puts back: e is a
// function literal, a local bound to one (locals), or a call of a helper of
// the test files, which returns such a function.
func (a *restoreCheck) restoresOf(e ast.Expr, locals map[types.Object]*ast.FuncLit) map[string]bool {
	switch x := e.(type) {
	case *ast.FuncLit:
		return a.assigned(x)
	case *ast.Ident:
		if lit := locals[a.object(x)]; lit != nil {
			return a.assigned(lit)
		}
	case *ast.CallExpr:
		if id, ok := x.Fun.(*ast.Ident); ok && a.funcs[id.Name] != nil {
			return a.summary(id.Name).returns
		}
	}
	return nil
}

// summary is what function name changes and puts back, its helpers
// included.
func (a *restoreCheck) summary(name string) *restoreSummary {
	if s, ok := a.done[name]; ok {
		return s
	}
	s := &restoreSummary{writes: map[string]token.Pos{}, restored: map[string]bool{}, returns: map[string]bool{}}
	a.done[name] = s // a recursive call sees what is known so far
	fn := a.funcs[name]
	saved := map[types.Object]string{}        // prev := v
	locals := map[types.Object]*ast.FuncLit{} // restore := func() { ... }
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
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, l := range n.Lhs {
				var r ast.Expr
				if len(n.Rhs) == len(n.Lhs) {
					r = n.Rhs[i]
				}
				if id, ok := l.(*ast.Ident); ok && a.object(id) != nil && a.pkgVar(id) == "" {
					if lit, ok := r.(*ast.FuncLit); ok {
						locals[a.object(id)] = lit
					}
				}
				if n.Tok == token.DEFINE {
					if id, ok := l.(*ast.Ident); ok && a.object(id) != nil {
						if rid, ok := r.(*ast.Ident); ok && a.pkgVar(rid) != "" {
							saved[a.object(id)] = a.pkgVar(rid)
						}
					}
					continue
				}
				v := a.rootVar(l)
				if rid, ok := r.(*ast.Ident); ok && v != "" && a.object(rid) != nil && saved[a.object(rid)] == v {
					continue // putting back what was saved from it
				}
				write(v, l.Pos())
			}
		case *ast.IncDecStmt:
			write(a.rootVar(n.X), n.Pos())
		case *ast.DeferStmt:
			if lit, ok := n.Call.Fun.(*ast.FuncLit); ok {
				merge(s.restored, a.assigned(lit))
			} else {
				merge(s.restored, a.restoresOf(n.Call.Fun, locals))
			}
			return false
		case *ast.ReturnStmt:
			for _, r := range n.Results {
				merge(s.returns, a.restoresOf(r, locals))
			}
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "Cleanup":
					if len(n.Args) == 1 {
						merge(s.restored, a.restoresOf(n.Args[0], locals))
					}
					return false
				case "Execute", "ExecuteContext", "ExecuteC", "ExecuteContextC":
					write("errOut", n.Pos()) // useStyle, before every command
					write("sty", n.Pos())
					write("skewCheck", n.Pos())
				default:
					// An exported helper, from the cmd_test package (cmd.KeepStyle).
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "cmd" && a.funcs[sel.Sel.Name] != nil {
						merge(s.restored, a.summary(sel.Sel.Name).restored)
					}
				}
			}
			if id, ok := n.Fun.(*ast.Ident); ok {
				switch {
				case id.Name == "setForTest" && len(n.Args) > 1:
					if u, ok := n.Args[1].(*ast.UnaryExpr); ok {
						s.restored[a.rootVar(u.X)] = true
					}
				case a.funcs[id.Name] != nil && id.Name != name:
					h := a.summary(id.Name)
					for v := range h.writes {
						if !h.restored[v] {
							write(v, n.Pos())
						}
					}
					merge(s.restored, h.restored)
				}
			}
		}
		return true
	})
	return s
}
