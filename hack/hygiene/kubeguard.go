// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"fmt"
	"go/ast"
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
//     which read the ambient kubeconfig too), or its own files, or any
//     package its test binary imports under any build tag set but e2e, call
//     a kube config loader. The guard must be in a file without build
//     constraints, so every tag set has it. The e2e suites (test/e2e/live,
//     every test file tagged e2e) talk to their own kind clusters on
//     purpose and are not checked.
//   - kubeguard-home <file:line>: clientcmd's RecommendedHomeFile,
//     RecommendedConfigDir or RecommendedHomeDir outside test/e2e. They are
//     computed from $HOME when clientcmd initializes, before any TestMain,
//     and ignore $KUBECONFIG.
//   - kubeguard-init <file:line>: a loader called in a package-level var or
//     an init function, which runs before TestMain.
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
					inTestFile: f.test, inE2E: strings.HasPrefix(f.rel, "test/e2e/"),
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

// guardWrappers are the functions (import path or dir, name) whose body
// calls kubetest.Main or kubetest.Isolate: a TestMain may call one of them
// (scenario.MainWithRenderJobs).
func guardWrappers(files []*goFile, module string) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		for _, d := range f.file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil && callsGuard(fd.Body, f, nil) {
				out[module+"/"+f.dir+"."+fd.Name.Name] = true // pkg.Name from another package
				out[f.dir+"."+fd.Name.Name] = true            // Name from its own package
			}
		}
	}
	return out
}

// callsGuard reports whether body calls kubetest.Main or Isolate, or a
// wrapper (module path + "." + name; unqualified for f's own package).
func callsGuard(body ast.Node, f *goFile, wrappers map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if id, ok := fn.X.(*ast.Ident); ok {
				ip := f.imports[id.Name]
				if ip == kubetestPath && (fn.Sel.Name == "Main" || fn.Sel.Name == "Isolate") || wrappers[ip+"."+fn.Sel.Name] {
					found = true
				}
			}
		case *ast.Ident:
			if wrappers[f.dir+"."+fn.Name] {
				found = true
			}
		}
		return !found
	})
	return found
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
	byDir := map[string][]*goFile{}
	for _, f := range files {
		byDir[f.dir] = append(byDir[f.dir], f)
		for _, u := range loaderUses(f, fset) {
			ownLoader[f.dir] = true
			if !u.inTestFile {
				loaderPkgs[module+"/"+f.dir] = true
			}
			rel := fmt.Sprintf("%s:%d", f.rel, u.pos.Line)
			if u.homeUse && !u.inE2E {
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
				e2eOnly = e2eOnly && strings.Contains(f.tags, "e2e")
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
		default:
			for d := range deps[ip] {
				if loaderPkgs[d] && (reach == "" || d < strings.TrimPrefix(reach, "its tests reach ")) {
					reach = "its tests reach " + d
				}
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
					callsGuard(fd.Body, f, wrappers) {
					guarded = true
				}
			}
		}
		if !guarded {
			out = append(out, fmt.Sprintf("kubeguard %s: %s, and no TestMain calls kubetest.Main or kubetest.Isolate in a "+
				"file without build constraints; add func TestMain(m *testing.M) { kubetest.Main(m) } (test/kubetest)", ip, reach))
		}
	}
	sort.Strings(out)
	return out
}
