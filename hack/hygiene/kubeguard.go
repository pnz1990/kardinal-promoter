// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The kube guard (QA on #2179 and #2216): a unit test must never reach the
// cluster the developer's machine points at (on the shared host, a
// production one). test/kubetest.Main, or Isolate in a TestMain, points
// $KUBECONFIG at nothing and unsets the in-cluster address and
// USE_EXISTING_CLUSTER before any test runs, and every child process the
// tests start inherits that. kubeGuard reports:
//
//   - kubeguard <package>: a package with tests and no guard, when it is
//     under cmd/ or test/ (tests there run the CLI, kubectl, helm and kind,
//     which read the ambient kubeconfig too), or its own files or a package
//     its test binary imports start processes (os/exec) or call a kube
//     config loader, under any build tag set but e2e. The guard must be in a
//     file without build constraints, so every tag set has it, and run
//     before the tests: kubetest.Main or kubetest.Run, or kubetest.Isolate
//     (or a wrapper that calls one) before m.Run. The e2e suites
//     (test/e2e/live, every file only an e2e build has) talk to their own
//     kind clusters on purpose and are not checked.
//   - kubeguard-home <file:line>: clientcmd's RecommendedHomeFile,
//     RecommendedConfigDir or RecommendedHomeDir outside the e2e builds and
//     test/kubetest. They are computed from $HOME when clientcmd
//     initializes, before any TestMain, and ignore $KUBECONFIG.
//   - kubeguard-init <file:line>: a loader called in a package-level var
//     (a function literal called there too) or an init function, which runs
//     before TestMain.
//   - kubeguard-env <file:line>: a test that sets KUBECONFIG to "" or unsets
//     it, or does either to HOME: client-go then falls back to
//     ~/.kube/config, and controller-runtime to the password database's
//     home (QA-3 on #2216). kubetest.Isolate points both at nothing; a test
//     that wants no kubeconfig sets KUBECONFIG to a path that does not exist.
//     Also a child process a test (or test/ code) starts with an environment
//     built from scratch and no HOME, which a controller-runtime child turns
//     into the password database's home (#2321).
//   - kubeguard-init <file:line> also covers the home directory read at
//     package initialization (os.Getenv("HOME"), os.UserHomeDir, ...): it is
//     the developer's, read before the guard replaces it (#2321).
//
// Calls are resolved by import path, so an aliased or dot import is seen.

// kubeLoaders are, by import path, the names that load the ambient
// kubeconfig or reach a cluster without an explicit config.
var kubeLoaders = map[string]map[string]bool{
	"k8s.io/client-go/tools/clientcmd": {"NewDefaultClientConfigLoadingRules": true, "NewDefaultPathOptions": true,
		"BuildConfigFromFlags": true, "RecommendedHomeFile": true, "RecommendedConfigDir": true, "RecommendedHomeDir": true},
	"sigs.k8s.io/controller-runtime/pkg/client/config": {"GetConfig": true, "GetConfigOrDie": true, "GetConfigWithContext": true},
	"sigs.k8s.io/controller-runtime":                   {"GetConfig": true, "GetConfigOrDie": true},
	"k8s.io/client-go/rest":                            {"InClusterConfig": true},
	// envtest.Environment with USE_EXISTING_CLUSTER=true takes the ambient
	// config (controller-runtime pkg/envtest/server.go) and installs CRDs
	// into that cluster.
	"sigs.k8s.io/controller-runtime/pkg/envtest": {"Environment": true},
}

// homeNames are the clientcmd names read from $HOME at package init.
var homeNames = map[string]bool{"RecommendedHomeFile": true, "RecommendedConfigDir": true, "RecommendedHomeDir": true}

// pkgNames are the package names of import paths whose last element is not
// the name.
var pkgNames = map[string]string{"sigs.k8s.io/controller-runtime": "controllerruntime"}

const kubetestPath = "github.com/kardinal-promoter/kardinal-promoter/test/kubetest"

// goFile is one parsed Go file of the module.
type goFile struct {
	rel     string // repo-relative path
	dir     string // repo-relative directory: the package
	test    bool
	tags    string // its //go:build expression, "" for none
	file    *ast.File
	imports map[string]string // local name -> import path ("." for a dot import)
}

// e2eOnly reports whether only an e2e build has the file: its build
// constraint holds for no set of the other tags without e2e. (A file
// tagged !e2e, or envtest, is in the unit builds.)
func (f *goFile) e2eOnly() bool {
	if f.tags == "" {
		return false
	}
	expr, err := constraint.Parse("//go:build " + f.tags)
	if err != nil {
		return false
	}
	var tags []string
	seen := map[string]bool{"e2e": true}
	var collect func(constraint.Expr)
	collect = func(e constraint.Expr) {
		switch e := e.(type) {
		case *constraint.AndExpr:
			collect(e.X)
			collect(e.Y)
		case *constraint.OrExpr:
			collect(e.X)
			collect(e.Y)
		case *constraint.NotExpr:
			collect(e.X)
		case *constraint.TagExpr:
			if !seen[e.Tag] {
				seen[e.Tag] = true
				tags = append(tags, e.Tag)
			}
		}
	}
	collect(expr)
	if len(tags) > 12 {
		return false // too many to try: not e2e-only, so it is checked
	}
	for set := 0; set < 1<<len(tags); set++ {
		on := map[string]bool{}
		for i, tag := range tags {
			on[tag] = set&(1<<i) != 0
		}
		if expr.Eval(func(tag string) bool { return on[tag] }) {
			return false
		}
	}
	return true
}

// loaderUse is a reference to a kube loader.
type loaderUse struct {
	pos            token.Position
	path, name     string
	packageLevel   bool
	inTestFile     bool
	inE2E, homeUse bool
}

// parseModule parses every .go file under root (not vendor, node_modules
// or testdata), whatever its build tags.
func parseModule(root string, fset *token.FileSet) ([]*goFile, error) {
	var files []*goFile
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "node_modules", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		rel, _ := filepath.Rel(root, p)
		g := &goFile{rel: filepath.ToSlash(rel), dir: filepath.ToSlash(filepath.Dir(rel)), test: strings.HasSuffix(p, "_test.go"),
			file: f, imports: map[string]string{}}
		for _, c := range f.Comments {
			if c.Pos() < f.Package && strings.HasPrefix(c.List[0].Text, "//go:build ") {
				g.tags = strings.TrimPrefix(c.List[0].Text, "//go:build ")
			}
		}
		for _, im := range f.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			name := path.Base(ip)
			if n, ok := pkgNames[ip]; ok {
				name = n
			}
			if im.Name != nil {
				name = im.Name.Name
			}
			g.imports[name] = ip
		}
		files = append(files, g)
		return nil
	})
	return files, err
}

// loaderUses are f's references to kube loaders.
func loaderUses(f *goFile, fset *token.FileSet) []loaderUse {
	var out []loaderUse
	dots := map[string]bool{}
	for name, ip := range f.imports {
		if name == "." && kubeLoaders[ip] != nil {
			dots[ip] = true
		}
	}
	var visit func(n ast.Node, packageLevel bool)
	visit = func(n ast.Node, packageLevel bool) {
		ast.Inspect(n, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && packageLevel {
				if lit, ok := call.Fun.(*ast.FuncLit); ok {
					// A function literal called in a package var runs at
					// initialization (QA-3 on #2216).
					visit(lit.Body, true)
					for _, a := range call.Args {
						visit(a, true)
					}
					return false
				}
			}
			if lit, ok := n.(*ast.FuncLit); ok && packageLevel {
				// A function stored in a package var runs when called, not
				// at initialization.
				visit(lit.Body, false)
				return false
			}
			var ip, name string
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok {
					ip, name = f.imports[id.Name], x.Sel.Name
				}
			case *ast.Ident:
				for d := range dots {
					if kubeLoaders[d][x.Name] {
						ip, name = d, x.Name
					}
				}
			}
			if ip != "" && kubeLoaders[ip][name] {
				out = append(out, loaderUse{pos: fset.Position(n.Pos()), path: ip, name: name, packageLevel: packageLevel,
					inTestFile: f.test, inE2E: f.e2eOnly(),
					homeUse: ip == "k8s.io/client-go/tools/clientcmd" && homeNames[name]})
			}
			return true
		})
	}
	for _, d := range f.file.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			if d.Tok == token.VAR {
				visit(d, true)
			}
		case *ast.FuncDecl:
			if d.Body != nil {
				visit(d.Body, d.Recv == nil && d.Name.Name == "init")
			}
		}
	}
	return out
}

// guardWrappers are the functions (import path or dir, name) that run the
// kubetest guard before any m.Run: a TestMain may call one of them
// (scenario.MainWithRenderJobs).
func guardWrappers(files []*goFile, module string) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		for _, d := range f.file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil && guardsFirst(fd, f, nil) {
				out[module+"/"+f.dir+"."+fd.Name.Name] = true // pkg.Name from another package
				out[f.dir+"."+fd.Name.Name] = true            // Name from its own package
			}
		}
	}
	return out
}

// guardNames are test/kubetest's guards.
var guardNames = map[string]bool{"Main": true, "Run": true, "Isolate": true}

// guardsFirst reports whether fd runs the kubetest guard before anything
// can start its tests: kubetest.Main(m), os.Exit(kubetest.Run(m)), code :=
// kubetest.Run(m), or kubetest.Isolate() or a wrapper (module path + "." +
// name; unqualified for f's own package), as one of fd's own statements,
// not deferred and not inside an if, a loop or a function, before any
// statement that mentions its *testing.M parameter. A guard after the
// tests, or one that may not run, guards nothing: a helper that runs the
// tests, a defer and a condition all slipped past a check that only looked
// for a direct m.Run (QA-3 on #2216).
func guardsFirst(fd *ast.FuncDecl, f *goFile, wrappers map[string]bool) bool {
	m := ""
	for _, p := range fd.Type.Params.List {
		if star, ok := p.Type.(*ast.StarExpr); ok && len(p.Names) > 0 {
			if sel, ok := star.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "M" {
				m = p.Names[0].Name
			}
		}
	}
	for _, st := range fd.Body.List {
		if guardStmt(st, f, wrappers) {
			return true
		}
		if m != "" && mentions(st, m) {
			return false
		}
	}
	return false
}

// guardStmt reports whether st is a guard on its own: a guard call as a
// statement, as os.Exit's argument or as an assignment's value.
func guardStmt(st ast.Stmt, f *goFile, wrappers map[string]bool) bool {
	var e ast.Expr
	switch st := st.(type) {
	case *ast.ExprStmt:
		e = st.X
		if call, ok := e.(*ast.CallExpr); ok && len(call.Args) == 1 {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Exit" {
				if id, ok := sel.X.(*ast.Ident); ok && f.imports[id.Name] == "os" {
					e = call.Args[0]
				}
			}
		}
	case *ast.AssignStmt:
		if len(st.Rhs) == 1 {
			e = st.Rhs[0]
		}
	}
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		if id, ok := fn.X.(*ast.Ident); ok {
			ip := f.imports[id.Name]
			return ip == kubetestPath && guardNames[fn.Sel.Name] || wrappers[ip+"."+fn.Sel.Name]
		}
	case *ast.Ident:
		return wrappers[f.dir+"."+fn.Name]
	}
	return false
}

// mentions reports whether n refers to the identifier name.
func mentions(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// startsProcesses reports whether f imports os/exec.
func startsProcesses(f *goFile) bool {
	for _, ip := range f.imports {
		if ip == "os/exec" {
			return true
		}
	}
	return false
}

// envFindings are f's settings of KUBECONFIG or HOME to "", and unsettings
// of either: os.Setenv, t.Setenv and os.Unsetenv with those literals.
func envFindings(f *goFile, fset *token.FileSet) []string {
	var out []string
	ast.Inspect(f.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Setenv" && sel.Sel.Name != "Unsetenv") {
			return true
		}
		name, ok := stringLit(call.Args[0])
		if !ok || (name != "KUBECONFIG" && name != "HOME") {
			return true
		}
		if sel.Sel.Name == "Setenv" {
			if len(call.Args) < 2 {
				return true
			}
			if v, ok := stringLit(call.Args[1]); !ok || v != "" {
				return true
			}
		}
		out = append(out, fmt.Sprintf("kubeguard-env %s:%d: %s(%q) makes client-go fall back to ~/.kube/config "+
			"(controller-runtime to the password database's home); give KUBECONFIG a path that does not exist, "+
			"such as kubetest.NoKubeconfig", f.rel, fset.Position(call.Pos()).Line, sel.Sel.Name, name))
		return true
	})
	return out
}

// childEnvFindings are f's child processes (exec.Command, exec.CommandContext
// or an exec.Cmd literal) given an environment built from scratch, a
// []string literal or an append to one, with no HOME in it. A child that
// uses controller-runtime's GetConfig (the CLI, the controller) then adds
// the login account's ~/.kube/config, from the password database, to
// whatever KUBECONFIG names, and loads it (QA-3 on #2216). An environment
// that starts from os.Environ() keeps the guard's scratch $HOME.
func childEnvFindings(f *goFile, fset *token.FileSet) []string {
	execName := ""
	for name, ip := range f.imports {
		if ip == "os/exec" {
			execName = name
		}
	}
	if execName == "" {
		return nil
	}
	var out []string
	report := func(n ast.Node) {
		out = append(out, fmt.Sprintf("kubeguard-env %s:%d: a child process with an environment built from scratch and no HOME: "+
			"a child that uses controller-runtime (the CLI, the controller) then loads the login account's ~/.kube/config, "+
			"whatever KUBECONFIG says; add HOME (t.TempDir()), or start from os.Environ()", f.rel, fset.Position(n.Pos()).Line))
	}
	isCommand := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == execName && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext")
	}
	inspectBodies(f.file, func(body *ast.BlockStmt) {
		if hasHomeLiteral(body) {
			return
		}
		cmds := map[string]bool{}
		ast.Inspect(body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncLit:
				return false // its own body
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					if i < len(n.Rhs) && isCommand(n.Rhs[i]) {
						if id, ok := lhs.(*ast.Ident); ok {
							cmds[id.Name] = true
						}
					}
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Env" && i < len(n.Rhs) && fromScratch(n.Rhs[i]) {
						if id, ok := sel.X.(*ast.Ident); ok && cmds[id.Name] {
							report(n)
						}
					}
				}
			case *ast.CompositeLit:
				if sel, ok := n.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Cmd" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == execName {
						for _, el := range n.Elts {
							if kv, ok := el.(*ast.KeyValueExpr); ok && fromScratch(kv.Value) {
								if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Env" {
									report(n)
								}
							}
						}
					}
				}
			}
			return true
		})
	})
	return out
}

// inspectBodies calls fn with every function body of file, function
// literals included.
func inspectBodies(file *ast.File, fn func(*ast.BlockStmt)) {
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Body != nil {
				fn(n.Body)
			}
		case *ast.FuncLit:
			fn(n.Body)
		}
		return true
	})
}

// fromScratch reports whether e is an environment that does not start from
// this process's: a []string literal, or an append to one, with no
// os.Environ() in it.
func fromScratch(e ast.Expr) bool {
	lit := e
	if call, ok := e.(*ast.CallExpr); ok {
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "append" && len(call.Args) > 0 {
			lit = call.Args[0]
		}
	}
	if _, ok := lit.(*ast.CompositeLit); !ok {
		return false
	}
	environ := false
	ast.Inspect(e, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Environ" {
			environ = true
		}
		return !environ
	})
	return !environ
}

// hasHomeLiteral reports whether body has a string literal that starts
// with HOME=: the environment it builds sets HOME.
func hasHomeLiteral(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if v, err := strconv.Unquote(lit.Value); err == nil && strings.HasPrefix(v, "HOME=") {
				found = true
			}
		}
		return !found
	})
	return found
}

// homeReaders are, by import path, the calls that read the home directory.
var homeReaders = map[string]map[string]bool{
	"os":                            {"UserHomeDir": true},
	"os/user":                       {"Current": true},
	"k8s.io/client-go/util/homedir": {"HomeDir": true},
}

// homeAtInit are f's reads of the home directory at package initialization
// (a package var, a function literal called there, an init function):
// os.Getenv or os.LookupEnv of HOME, os.UserHomeDir, user.Current or
// homedir.HomeDir. They run before TestMain, so they read the developer's
// $HOME, not the guard's scratch one, and a kubeconfig or credentials found
// from it later reach the real ones (QA-3's probe E on #2216).
func homeAtInit(f *goFile, fset *token.FileSet) []string {
	var out []string
	check := func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return
		}
		ip, name := f.imports[id.Name], sel.Sel.Name
		home := homeReaders[ip][name]
		if ip == "os" && (name == "Getenv" || name == "LookupEnv") && len(call.Args) == 1 {
			v, ok := stringLit(call.Args[0])
			home = ok && v == "HOME"
		}
		if home {
			out = append(out, fmt.Sprintf("kubeguard-init %s:%d: %s.%s reads the home directory at package initialization (a package var "+
				"or init), before the kubetest guard replaces $HOME: it gets the developer's; read it in a function",
				f.rel, fset.Position(call.Pos()).Line, path.Base(ip), name))
		}
	}
	var visit func(n ast.Node, packageLevel bool)
	visit = func(n ast.Node, packageLevel bool) {
		ast.Inspect(n, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				if lit, ok := n.(*ast.FuncLit); ok {
					visit(lit.Body, false) // stored, not called at initialization
					return false
				}
				return true
			}
			if lit, ok := call.Fun.(*ast.FuncLit); ok && packageLevel {
				visit(lit.Body, true) // called at initialization
				for _, a := range call.Args {
					visit(a, true)
				}
				return false
			}
			if packageLevel {
				check(call)
			}
			return true
		})
	}
	for _, d := range f.file.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			if d.Tok == token.VAR {
				visit(d, true)
			}
		case *ast.FuncDecl:
			if d.Body != nil && d.Recv == nil && d.Name.Name == "init" {
				visit(d.Body, true)
			}
		}
	}
	return out
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil
}

// testDeps are, per package import path, the transitive imports of its
// test binaries under the tag sets the unit tests use (none, envtest,
// integration), from go list.
func testDeps(root string) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	for _, tags := range []string{"", "envtest", "integration"} {
		args := []string{"list", "-e", "-test"}
		if tags != "" {
			args = append(args, "-tags", tags)
		}
		args = append(args, "-f", "{{.ImportPath}}\t{{join .Deps \" \"}}", "./...")
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		b, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err, stderr.String())
		}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			name, deps, _ := strings.Cut(l, "\t")
			base, variant, ok := strings.Cut(name, " [")
			if !ok || !strings.HasSuffix(variant, ".test]") {
				continue
			}
			p := strings.TrimSuffix(base, "_test")
			if out[p] == nil {
				out[p] = map[string]bool{}
			}
			for _, d := range strings.Fields(deps) {
				out[p][d] = true
			}
		}
	}
	return out, nil
}

// kubeGuard runs the kube guard checks over the module at root.
func kubeGuard(root string) ([]string, error) {
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("read go.mod: %w", err)
	}
	module := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(string(mod), "\n", 2)[0], "module"))
	fset := token.NewFileSet()
	files, err := parseModule(root, fset)
	if err != nil {
		return nil, err
	}
	deps, err := testDeps(root)
	if err != nil {
		return nil, err
	}
	return kubeFindings(files, fset, module, deps), nil
}

// kubeFindings is kubeGuard over parsed files and test dependencies.
func kubeFindings(files []*goFile, fset *token.FileSet, module string, deps map[string]map[string]bool) []string {
	var out []string
	loaderPkgs := map[string]bool{} // import paths whose own code calls a loader
	ownLoader := map[string]bool{}  // dirs whose files (tests too) call a loader
	execPkgs := map[string]bool{}   // import paths whose own code starts processes
	ownExec := map[string]bool{}    // dirs whose files (tests too) start processes
	byDir := map[string][]*goFile{}
	for _, f := range files {
		byDir[f.dir] = append(byDir[f.dir], f)
		if startsProcesses(f) && !f.e2eOnly() {
			ownExec[f.dir] = true
			if !f.test {
				execPkgs[module+"/"+f.dir] = true
			}
		}
		if f.test && f.dir != "test/kubetest" && !f.e2eOnly() {
			out = append(out, envFindings(f, fset)...)
		}
		if (f.test || strings.HasPrefix(f.dir, "test/")) && !f.e2eOnly() {
			out = append(out, childEnvFindings(f, fset)...)
		}
		if !f.e2eOnly() {
			out = append(out, homeAtInit(f, fset)...)
		}
		for _, u := range loaderUses(f, fset) {
			ownLoader[f.dir] = true
			if !u.inTestFile {
				loaderPkgs[module+"/"+f.dir] = true
			}
			rel := fmt.Sprintf("%s:%d", f.rel, u.pos.Line)
			if u.homeUse && !u.inE2E && f.dir != "test/kubetest" {
				out = append(out, fmt.Sprintf("kubeguard-home %s: clientcmd.%s is $HOME's kubeconfig, read when clientcmd "+
					"initializes, before any TestMain, whatever $KUBECONFIG says; use the default loading rules", rel, u.name))
			}
			if u.packageLevel {
				out = append(out, fmt.Sprintf("kubeguard-init %s: %s.%s runs at package initialization (a package var or init), "+
					"before the kubetest guard; call it from a function", rel, path.Base(u.path), u.name))
			}
		}
	}
	wrappers := guardWrappers(files, module)
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		var tests []*goFile
		e2eOnly := true
		for _, f := range byDir[dir] {
			if f.test {
				tests = append(tests, f)
				e2eOnly = e2eOnly && f.e2eOnly()
			}
		}
		if len(tests) == 0 || e2eOnly {
			continue
		}
		ip := module + "/" + dir
		reach := ""
		switch {
		case strings.HasPrefix(dir, "cmd/") || strings.HasPrefix(dir, "test/"):
			reach = "it is under " + strings.SplitN(dir, "/", 2)[0] + "/, whose tests run the CLI, kubectl, helm and kind"
		case ownLoader[dir]:
			reach = "its own code calls a kube config loader"
		case ownExec[dir]:
			reach = "its code starts processes (os/exec): a kubectl, helm, kind or kardinal reads the ambient kubeconfig too"
		default:
			for d := range deps[ip] {
				if (loaderPkgs[d] || execPkgs[d]) && (reach == "" || d < strings.TrimPrefix(reach, "its tests reach ")) {
					reach = "its tests reach " + d
				}
			}
			if reach != "" && execPkgs[strings.TrimPrefix(reach, "its tests reach ")] {
				reach += ", which starts processes (os/exec)"
			}
		}
		if reach == "" {
			continue
		}
		guarded := false
		for _, f := range tests {
			if f.tags != "" {
				continue // the guard must be in every tag set's build
			}
			for _, d := range f.file.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "TestMain" && fd.Body != nil &&
					guardsFirst(fd, f, wrappers) {
					guarded = true
				}
			}
		}
		if !guarded {
			out = append(out, fmt.Sprintf("kubeguard %s: %s, and no TestMain runs kubetest.Main, Run or Isolate before "+
				"its tests, in a file without build constraints; add func TestMain(m *testing.M) { kubetest.Main(m) } (test/kubetest)", ip, reach))
		}
	}
	sort.Strings(out)
	return out
}
