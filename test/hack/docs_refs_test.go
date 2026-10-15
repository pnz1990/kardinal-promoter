// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// docsPathLiteral is a string that names the repository's docs.
var docsPathLiteral = regexp.MustCompile(`^(\.\./)*(docs(/.*)?|README\.md|mkdocs\.yml|overrides/.*)$`)

// goSource is a parsed Go file of the module.
type goSource struct {
	dir     string            // relative to the module root, slash-separated
	test    bool              // a _test.go file
	e2e     bool              // behind //go:build e2e
	file    *ast.File         //
	imports map[string]string // local name -> module-relative dir, or "pkg:<import path>" outside it
}

// docsRefs finds, in a module's Go test files, the expressions that name the
// repository's docs (#1947): string literals, constant expressions built from
// pieces ("do"+"cs"), constants of the package or of another package of the
// module (fragments.Dir), filepath.Join and path.Join of such constants, and
// calls of a function of the module whose body names the docs (a helper that
// returns the docs root). It evaluates constants from the source, as go/types
// would for these forms, without type-checking the module.
type docsRefs struct {
	files  []*goSource
	consts map[string]map[string]constRef // dir -> name -> its expression
	funcs  map[string]map[string]string   // dir -> function -> the docs path its body names
}

type constRef struct {
	expr ast.Expr
	src  *goSource
}

// loadDocsRefs parses every Go file under root (module module).
func loadDocsRefs(root, module string) (*docsRefs, error) {
	r := &docsRefs{consts: map[string]map[string]constRef{}, funcs: map[string]map[string]string{}}
	pkgNames := map[string]string{} // dir -> package name (non-test)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "results" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, src, parser.ParseComments)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		s := &goSource{dir: filepath.ToSlash(rel), test: strings.HasSuffix(p, "_test.go"),
			e2e: strings.Contains(string(src), "//go:build e2e"), file: f}
		if !s.test && !strings.HasSuffix(f.Name.Name, "_test") {
			pkgNames[s.dir] = f.Name.Name
		}
		r.files = append(r.files, s)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, s := range r.files {
		s.imports = map[string]string{}
		for _, imp := range s.file.Imports {
			ip, _ := strconv.Unquote(imp.Path.Value)
			target := "pkg:" + ip
			if rel, ok := strings.CutPrefix(ip, module+"/"); ok {
				target = rel
			}
			name := path.Base(ip)
			if n := pkgNames[target]; n != "" {
				name = n // a module package's name, which may differ from its dir
			}
			if imp.Name != nil {
				name = imp.Name.Name
			}
			s.imports[name] = target
		}
		// Constants, and variables initialized from them (a helper's
		// package-level var docsDir = "docs"), anywhere in the file: the
		// package-level ones and function-local ones share the dir's scope.
		ast.Inspect(s.file, func(n ast.Node) bool {
			if vs, ok := n.(*ast.ValueSpec); ok {
				for i, id := range vs.Names {
					if i < len(vs.Values) {
						if r.consts[s.dir] == nil {
							r.consts[s.dir] = map[string]constRef{}
						}
						r.consts[s.dir][id.Name] = constRef{expr: vs.Values[i], src: s}
					}
				}
			}
			return true
		})
	}
	// Functions (not methods) whose body names a docs path.
	for _, s := range r.files {
		if s.test {
			continue
		}
		for _, decl := range s.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Body == nil {
				continue
			}
			if v := r.firstDocsPath(s, fd.Body); v != "" {
				if r.funcs[s.dir] == nil {
					r.funcs[s.dir] = map[string]string{}
				}
				r.funcs[s.dir][fd.Name.Name] = v
			}
		}
	}
	return r, nil
}

// firstDocsPath is the first docs path an expression under n names in s, or "".
func (r *docsRefs) firstDocsPath(s *goSource, n ast.Node) string {
	var found string
	ast.Inspect(n, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		if e, ok := n.(ast.Expr); ok {
			if v, ok := r.eval(s, e, 0); ok && docsPathLiteral.MatchString(v) {
				found = v
				return false
			}
		}
		return true
	})
	return found
}

// eval is the constant string value of e in s.
func (r *docsRefs) eval(s *goSource, e ast.Expr, depth int) (string, bool) {
	if depth > 20 {
		return "", false
	}
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		return v, err == nil
	case *ast.ParenExpr:
		return r.eval(s, e.X, depth+1)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		a, ok := r.eval(s, e.X, depth+1)
		if !ok {
			return "", false
		}
		b, ok := r.eval(s, e.Y, depth+1)
		return a + b, ok
	case *ast.Ident:
		if c, ok := r.consts[s.dir][e.Name]; ok {
			return r.eval(c.src, c.expr, depth+1)
		}
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		if c, ok := r.consts[s.imports[x.Name]][e.Sel.Name]; ok {
			return r.eval(c.src, c.expr, depth+1)
		}
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Join" {
			return "", false
		}
		if x, ok := sel.X.(*ast.Ident); !ok || (s.imports[x.Name] != "pkg:path/filepath" && s.imports[x.Name] != "pkg:path") {
			return "", false
		}
		parts := make([]string, 0, len(e.Args))
		for _, a := range e.Args {
			v, ok := r.eval(s, a, depth+1)
			if !ok {
				return "", false
			}
			parts = append(parts, v)
		}
		return path.Join(parts...), true
	}
	return "", false
}

// testPackages maps each module-relative dir with Go tests (not behind
// //go:build e2e) that name the docs to one way they do: a docs path, or the
// call of a module function whose body names one.
func (r *docsRefs) testPackages() map[string]string {
	found := map[string]string{}
	for _, s := range r.files {
		if !s.test || s.e2e {
			continue
		}
		ast.Inspect(s.file, func(n ast.Node) bool {
			if _, ok := found[s.dir]; ok {
				return false
			}
			if call, ok := n.(*ast.CallExpr); ok {
				switch f := call.Fun.(type) {
				case *ast.Ident:
					if v := r.funcs[s.dir][f.Name]; v != "" {
						found[s.dir] = f.Name + "() names " + v
						return false
					}
				case *ast.SelectorExpr:
					if x, ok := f.X.(*ast.Ident); ok {
						if v := r.funcs[s.imports[x.Name]][f.Sel.Name]; v != "" {
							found[s.dir] = x.Name + "." + f.Sel.Name + "() names " + v
							return false
						}
					}
				}
			}
			if e, ok := n.(ast.Expr); ok {
				if v, ok := r.eval(s, e, 0); ok && docsPathLiteral.MatchString(v) {
					found[s.dir] = v
					return false
				}
			}
			return true
		})
	}
	return found
}

// TestDocsRefs (#1947): the docs-tests check finds a package whose tests
// name the docs by a literal, by a constant built from pieces, by another
// package's constant, by a Join of constants, or by calling a helper whose
// body names them; and not one that only names other paths.
func TestDocsRefs(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                 "module example.com/m\n",
		"paths/paths.go":         "package paths\n\nconst Dir = \"docs/changelog.d\"\nconst Other = \"test/e2e\"\n",
		"helper/helper.go":       "package helper\n\nimport \"path/filepath\"\n\nfunc DocsRoot(root string) string { return filepath.Join(root, \"do\"+\"cs\") }\n",
		"literal/a_test.go":      "package literal\n\nvar _ = \"docs/x.md\"\n",
		"pieces/a_test.go":       "package pieces\n\nimport \"path/filepath\"\n\nvar _ = filepath.Join(\"root\", \"do\"+\"cs\")\n",
		"localconst/a_test.go":   "package localconst\n\nconst base = \"mk\"\n\nvar _ = base + \"docs.yml\"\n",
		"imported/a_test.go":     "package imported\n\nimport p \"example.com/m/paths\"\n\nvar _ = p.Dir\n",
		"joined/a_test.go":       "package joined\n\nimport (\n\t\"path\"\n\n\t\"example.com/m/paths\"\n)\n\nvar _ = path.Join(\"..\", paths.Dir)\n",
		"viahelper/a_test.go":    "package viahelper\n\nimport \"example.com/m/helper\"\n\nvar _ = helper.DocsRoot(\".\")\n",
		"clean/a_test.go":        "package clean\n\nimport \"example.com/m/paths\"\n\nvar _ = paths.Other + \"/live\"\n",
		"e2eonly/a_test.go":      "//go:build e2e\n\npackage e2eonly\n\nvar _ = \"docs\"\n",
		"external/a_test.go":     "package external_test\n\nimport \"example.com/m/paths\"\n\nvar _ = paths.Dir\n",
		"helper/notdocs_test.go": "package helper\n",
		"localfunc/root.go":      "package localfunc\n\nfunc root() string { return \"README.md\" }\n",
		"localfunc/root_test.go": "package localfunc\n\nvar _ = root()\n",
		"literal/a.go":           "package literal\n",
		"pieces/a.go":            "package pieces\n",
		"localconst/a.go":        "package localconst\n",
		"imported/a.go":          "package imported\n",
		"joined/a.go":            "package joined\n",
		"viahelper/a.go":         "package viahelper\n",
		"clean/a.go":             "package clean\n",
		"e2eonly/a.go":           "package e2eonly\n",
		"external/a.go":          "package external\n",
	}
	for p, c := range files {
		require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, p), []byte(c), 0o600))
	}
	r, err := loadDocsRefs(root, "example.com/m")
	require.NoError(t, err)
	found := r.testPackages()
	assert.Equal(t, map[string]string{
		"literal":    "docs/x.md",
		"pieces":     "docs",
		"localconst": "mkdocs.yml",
		"imported":   "docs/changelog.d",
		"joined":     "../docs/changelog.d",
		"viahelper":  "helper.DocsRoot() names docs",
		"external":   "docs/changelog.d",
		"localfunc":  "root() names README.md",
	}, found)
}
