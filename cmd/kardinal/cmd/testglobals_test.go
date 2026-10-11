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
	"go/ast"
	"go/parser"
	"go/token"
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

// TestPackageVariablesAreRestored: every test that assigns a package
// variable of this package puts it back in a t.Cleanup or a defer that names
// it (or uses setForTest), so one failing test cannot fail the ones after it.
func TestPackageVariablesAreRestored(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	pkgVars := map[string]bool{}
	parsed := map[string]*ast.File{}
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, err, f)
		parsed[f] = file
		for _, d := range file.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.VAR {
				for _, s := range g.Specs {
					for _, n := range s.(*ast.ValueSpec).Names {
						pkgVars[n.Name] = true
					}
				}
			}
		}
	}
	var leaks []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			continue
		}
		for _, d := range parsed[f].Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv != nil || fn.Name.Name == "init" || fn.Name.Name == "setForTest" {
				continue
			}
			local := map[string]bool{}
			for _, p := range fn.Type.Params.List {
				for _, n := range p.Names {
					local[n.Name] = true
				}
			}
			restored := map[string]bool{}
			funcs := map[string]*ast.FuncLit{} // restore := func() { ... }
			var assigned []*ast.Ident
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.AssignStmt:
					for i, lhs := range n.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						var rhs ast.Expr
						if len(n.Rhs) == len(n.Lhs) {
							rhs = n.Rhs[i]
						}
						if lit, ok := rhs.(*ast.FuncLit); ok {
							funcs[id.Name] = lit
						}
						// Putting back a saved value (v = prev) is the restore.
						if r, ok := rhs.(*ast.Ident); ok && local[r.Name] {
							continue
						}
						if n.Tok == token.DEFINE {
							local[id.Name] = true
						} else if n.Tok == token.ASSIGN && pkgVars[id.Name] {
							assigned = append(assigned, id)
						}
					}
				case *ast.ValueSpec:
					for _, id := range n.Names {
						local[id.Name] = true
					}
				case *ast.FuncLit:
					for _, p := range n.Type.Params.List {
						for _, id := range p.Names {
							local[id.Name] = true
						}
					}
				case *ast.DeferStmt:
					markIdents(n.Call, funcs, restored)
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Cleanup" {
						markIdents(n, funcs, restored)
					}
				}
				return true
			})
			for _, id := range assigned {
				if !local[id.Name] && !restored[id.Name] {
					leaks = append(leaks, fset.Position(id.Pos()).String()+": "+fn.Name.Name+" sets "+id.Name)
				}
			}
		}
	}
	slices.Sort(leaks)
	assert.Empty(t, leaks, "put each back with setForTest(t, &v, value), or save it and restore it in t.Cleanup")
}

// markIdents marks every identifier under n, and under the function a
// local name of n is bound to (t.Cleanup(restore)).
func markIdents(n ast.Node, funcs map[string]*ast.FuncLit, into map[string]bool) {
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			into[id.Name] = true
			if lit := funcs[id.Name]; lit != nil {
				delete(funcs, id.Name) // once: a recursive closure ends here
				markIdents(lit, funcs, into)
			}
		}
		return true
	})
}
