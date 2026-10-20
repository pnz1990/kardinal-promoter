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
	}
	var files []*goFile
	for rel, s := range src {
		files = append(files, parsed(t, fset, rel, s))
	}
	const mod = "github.com/kardinal-promoter/kardinal-promoter"
	deps := map[string]map[string]bool{mod + "/pkg/user": {mod + "/pkg/a": true, "fmt": true}}
	got := kubeFindings(files, fset, mod, deps)
	keys := make([]string, len(got))
	for i, g := range got {
		keys[i], _, _ = strings.Cut(g, ": ")
	}
	assert.Equal(t, []string{
		"kubeguard " + mod + "/pkg/b",
		"kubeguard " + mod + "/pkg/d",
		"kubeguard " + mod + "/pkg/e",
		"kubeguard " + mod + "/pkg/user",
		"kubeguard " + mod + "/test/plain",
		"kubeguard-home pkg/c/c.go:3",
		"kubeguard-init pkg/f/f.go:3",
		"kubeguard-init pkg/f/f.go:5",
	}, keys, strings.Join(got, "\n"))
	for _, g := range got {
		if strings.HasPrefix(g, "kubeguard "+mod+"/pkg/user:") {
			assert.Contains(t, g, "its tests reach "+mod+"/pkg/a", "the aliased loader is found")
		}
	}
}
