// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parsed is the goFile of source src at repo-relative rel.
func parsed(t *testing.T, fset *token.FileSet, rel, src string) *goFile {
	t.Helper()
	f, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	require.NoError(t, err, rel)
	g := &goFile{rel: rel, dir: path.Dir(rel), test: strings.HasSuffix(rel, "_test.go"), file: f, imports: map[string]string{}}
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
	return g
}

const guardMain = "package p\nimport (\n\"testing\"\n\"github.com/kardinal-promoter/kardinal-promoter/test/kubetest\"\n)\nfunc TestMain(m *testing.M) { kubetest.Main(m) }\n"

// TestKubeFindings (QA on #2216): each probe QA planted, and the safe
// forms. Loaders are resolved by import path (an alias, a dot import), in
// test files too and under any build tag; $HOME's kubeconfig is banned
// outside test/e2e; a loader at package initialization is refused; every
// test package under cmd/ and test/ needs the guard in an untagged file,
// directly or through a wrapper; an e2e-only package is left alone.
func TestKubeFindings(t *testing.T) {
	fset := token.NewFileSet()
	src := map[string]string{
		// a. an aliased controller-runtime import, reached by another package's test.
		"pkg/a/a.go":         "package a\nimport crcfg \"sigs.k8s.io/controller-runtime/pkg/client/config\"\nfunc A() { _ = crcfg.GetConfigOrDie() }\n",
		"pkg/user/u_test.go": "package user\nimport \"testing\"\nfunc TestU(t *testing.T) {}\n",
		// b. a loader in a test file only.
		"pkg/b/b_test.go": "package b\nimport (\n\"testing\"\n\"k8s.io/client-go/tools/clientcmd\"\n)\nfunc TestB(t *testing.T) { _ = clientcmd.NewDefaultClientConfigLoadingRules() }\n",
		// c. $HOME's kubeconfig, which ignores $KUBECONFIG.
		"pkg/c/c.go":      "package c\nimport \"k8s.io/client-go/tools/clientcmd\"\nfunc C() { _, _ = clientcmd.BuildConfigFromFlags(\"\", clientcmd.RecommendedHomeFile) }\n",
		"pkg/c/c_test.go": guardMain,
		// d. envtest under its build tag, the guard only in a tagged file.
		"pkg/d/d_test.go": "//go:build envtest\n\npackage d\nimport (\n\"testing\"\n\"sigs.k8s.io/controller-runtime/pkg/envtest\"\n\"github.com/kardinal-promoter/kardinal-promoter/test/kubetest\"\n)\nfunc TestMain(m *testing.M) { kubetest.Main(m) }\nfunc TestD(t *testing.T) { _ = &envtest.Environment{} }\n",
		// e. a dot import.
		"pkg/e/e_test.go": "package e\nimport (\n\"testing\"\n. \"k8s.io/client-go/rest\"\n)\nfunc TestE(t *testing.T) { _, _ = InClusterConfig() }\n",
		// f. package initialization; a function stored in a var is not.
		"pkg/f/f.go": "package f\nimport ctrl \"sigs.k8s.io/controller-runtime\"\nvar cfg = ctrl.GetConfigOrDie()\nvar later = func() { _ = ctrl.GetConfigOrDie() }\nfunc init() { _, _ = ctrl.GetConfig() }\n",
		// g. under test/: the guard is needed with no loader at all, and a
		// wrapper that calls the guard counts.
		"test/plain/p_test.go":      "package plain\nimport \"testing\"\nfunc TestP(t *testing.T) {}\n",
		"test/scen/main.go":         "package scen\nimport (\n\"testing\"\n\"github.com/kardinal-promoter/kardinal-promoter/test/kubetest\"\n)\nfunc Main(m *testing.M) { kubetest.Isolate(); m.Run() }\n",
		"test/scen/part1/x_test.go": "package part1\nimport (\n\"testing\"\n\"github.com/kardinal-promoter/kardinal-promoter/test/scen\"\n)\nfunc TestMain(m *testing.M) { scen.Main(m) }\n",
		"test/scen/s_test.go":       "package scen\nimport \"testing\"\nfunc TestMain(m *testing.M) { Main(m) }\n",
		"cmd/tool/t_test.go":        guardMain,
		// h. the e2e suites talk to their own clusters.
		"test/e2e/live/l_test.go": "//go:build e2e\n\npackage live\nimport (\n\"testing\"\n\"k8s.io/client-go/tools/clientcmd\"\n)\nfunc TestL(t *testing.T) { _ = clientcmd.RecommendedHomeFile }\n",
		// i. a method of the manager is not the ambient config.
		"pkg/i/i.go":      "package i\nfunc I(mgr interface{ GetConfig() any }) { _ = mgr.GetConfig() }\n",
		"pkg/i/i_test.go": "package i\nimport \"testing\"\nfunc TestI(t *testing.T) {}\n",
		// QA-3 on #2216, at c4052b7bb:
		// 1. a guarded test that clears KUBECONFIG, or HOME, falls back to
		// ~/.kube/config; a path that does not exist is the safe form, and
		// test/kubetest's own tests may.
		"test/env/e_test.go": "package env\nimport (\n\"os\"\n\"testing\"\n\"github.com/kardinal-promoter/kardinal-promoter/test/kubetest\"\n)\n" +
			"func TestMain(m *testing.M) { kubetest.Main(m) }\n" +
			"func TestE(t *testing.T) {\nt.Setenv(\"KUBECONFIG\", \"\")\n_ = os.Unsetenv(\"KUBECONFIG\")\n_ = os.Unsetenv(\"HOME\")\nt.Setenv(\"HOME\", \"\")\n" +
			"t.Setenv(\"KUBECONFIG\", \"/dev/null\")\nt.Setenv(\"OTHER\", \"\")\n}\n",
		"test/kubetest/k_test.go": "package kubetest_test\nimport (\n\"testing\"\n\"github.com/kardinal-promoter/kardinal-promoter/test/kubetest\"\n)\n" +
			"func TestMain(m *testing.M) { kubetest.Main(m) }\nfunc TestK(t *testing.T) { t.Setenv(\"KUBECONFIG\", \"\") }\n",
		"test/kubetest/k.go": "package kubetest\nimport \"k8s.io/client-go/tools/clientcmd\"\nfunc Isolate() { clientcmd.RecommendedHomeFile = \"/nonexistent\" }\nfunc Main(m any) {}\n",
		// 2. a pkg/ test that starts processes (kubectl, helm, kind, the
		// CLI), or reaches code that does.
		"pkg/x/x_test.go": "package x\nimport (\n\"os/exec\"\n\"testing\"\n)\nfunc TestX(t *testing.T) { _ = exec.Command(\"kubectl\", \"get\", \"ns\").Run() }\n",
		"pkg/z/z.go":      "package z\nimport \"os/exec\"\nfunc Z() { _ = exec.Command(\"helm\").Run() }\n",
		"pkg/y/y_test.go": "package y\nimport \"testing\"\nfunc TestY(t *testing.T) {}\n",
		// 3. a function literal called in a package var runs at init.
		"pkg/g/g.go": "package g\nimport (\n\"k8s.io/client-go/rest\"\n\"sigs.k8s.io/controller-runtime/pkg/client/config\"\n)\n" +
			"var Cfg = func() *rest.Config { c, _ := config.GetConfig(); return c }()\n",
		// 4. test files all //go:build !e2e are in the unit builds; e2e &&
		// linux is e2e-only.
		"pkg/n/n_test.go": "//go:build !e2e\n\npackage n\nimport (\n\"testing\"\n\"k8s.io/client-go/rest\"\n)\nfunc TestN(t *testing.T) { _, _ = rest.InClusterConfig() }\n",
		"pkg/o/o_test.go": "//go:build e2e && linux\n\npackage o\nimport (\n\"testing\"\n\"k8s.io/client-go/rest\"\n)\nfunc TestO(t *testing.T) { _, _ = rest.InClusterConfig() }\n",
		// 5. a guard after the tests guards nothing; kubetest.Run counts.
		"test/late/l_test.go": "package late\nimport (\n\"os\"\n\"testing\"\n\"github.com/kardinal-promoter/kardinal-promoter/test/kubetest\"\n)\n" +
			"func TestMain(m *testing.M) { code := m.Run(); kubetest.Isolate(); os.Exit(code) }\n",
		"test/run/r_test.go": "package run\nimport (\n\"os\"\n\"testing\"\n\"github.com/kardinal-promoter/kardinal-promoter/test/kubetest\"\n)\n" +
			"func TestMain(m *testing.M) { os.Exit(kubetest.Run(m)) }\n",
		// 6. $HOME's kubeconfig in untagged test/e2e code (unit binaries
		// import it) is refused; in an e2e-only file it is not.
		"test/e2e/framework/h.go":      "package framework\nimport \"k8s.io/client-go/tools/clientcmd\"\nvar _ = func() string { return clientcmd.RecommendedHomeFile }\n",
		"test/e2e/framework/h_test.go": guardMain,
	}
	var files []*goFile
	for rel, s := range src {
		files = append(files, parsed(t, fset, rel, s))
	}
	const mod = "github.com/kardinal-promoter/kardinal-promoter"
	deps := map[string]map[string]bool{mod + "/pkg/user": {mod + "/pkg/a": true, "fmt": true}, mod + "/pkg/y": {mod + "/pkg/z": true, "os/exec": true}}
	got := kubeFindings(files, fset, mod, deps)
	keys := make([]string, len(got))
	for i, g := range got {
		keys[i], _, _ = strings.Cut(g, ": ")
	}
	assert.Equal(t, []string{
		"kubeguard " + mod + "/pkg/b",
		"kubeguard " + mod + "/pkg/d",
		"kubeguard " + mod + "/pkg/e",
		"kubeguard " + mod + "/pkg/n",
		"kubeguard " + mod + "/pkg/user",
		"kubeguard " + mod + "/pkg/x",
		"kubeguard " + mod + "/pkg/y",
		"kubeguard " + mod + "/test/late",
		"kubeguard " + mod + "/test/plain",
		"kubeguard-env test/env/e_test.go:10",
		"kubeguard-env test/env/e_test.go:11",
		"kubeguard-env test/env/e_test.go:12",
		"kubeguard-env test/env/e_test.go:9",
		"kubeguard-home pkg/c/c.go:3",
		"kubeguard-home test/e2e/framework/h.go:3",
		"kubeguard-init pkg/f/f.go:3",
		"kubeguard-init pkg/f/f.go:5",
		"kubeguard-init pkg/g/g.go:6",
	}, keys, strings.Join(got, "\n"))
	for _, g := range got {
		switch {
		case strings.HasPrefix(g, "kubeguard "+mod+"/pkg/user:"):
			assert.Contains(t, g, "its tests reach "+mod+"/pkg/a", "the aliased loader is found")
		case strings.HasPrefix(g, "kubeguard "+mod+"/pkg/y:"):
			assert.Contains(t, g, "its tests reach "+mod+"/pkg/z, which starts processes (os/exec)")
		case strings.HasPrefix(g, "kubeguard "+mod+"/pkg/x:"):
			assert.Contains(t, g, "its code starts processes (os/exec)")
		}
	}
}

// TestE2EOnly (QA-3 on #2216): a file is e2e-only when its build constraint
// holds for no set of the other tags without e2e; !e2e is in every unit build.
func TestE2EOnly(t *testing.T) {
	for tags, want := range map[string]bool{
		"": false, "e2e": true, "!e2e": false, "e2e && linux": true, "linux && e2e": true, "e2e || envtest": false,
		"envtest": false, "!race && e2e": true, "!race": false, "(e2e && race) || (e2e && !race)": true,
	} {
		assert.Equal(t, want, (&goFile{tags: tags}).e2eOnly(), "//go:build %s", tags)
	}
}
