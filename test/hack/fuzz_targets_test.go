// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// repoFuzzTargets returns every fuzz target of the repo, "./<dir>:<Name>":
// each func Fuzz<X>(*testing.F) of a _test.go file, found by parsing, not by
// the release gate's own pattern.
func repoFuzzTargets(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "results", "bin", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Fuzz") || len(fn.Type.Params.List) != 1 {
				continue
			}
			star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if sel, ok := star.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "F" {
				rel, err := filepath.Rel(root, filepath.Dir(p))
				if err != nil {
					return err
				}
				out = append(out, "./"+filepath.ToSlash(rel)+":"+fn.Name.Name)
			}
		}
		return nil
	}))
	sort.Strings(out)
	return out
}

var ciFuzzTarget = regexp.MustCompile(`(\./[\w./-]+):(Fuzz\w+)`)

// TestFuzzTargetsAllRun: every fuzz target of the repo runs in CI's per-PR
// fuzz job and in the release gate (one fuzz-<Name> step each); a target
// added to one place only, or neither, fails here (#2078 follow-up: CI's job
// listed four of six).
func TestFuzzTargetsAllRun(t *testing.T) {
	t.Parallel() // reads the repo, writes only its temp dirs: beside the package's others (#2292)
	targets := repoFuzzTargets(t)
	require.NotEmpty(t, targets)

	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	require.NoError(t, err)
	var wf workflowFile
	require.NoError(t, yaml.Unmarshal(data, &wf))
	job, ok := wf.Jobs["fuzz"]
	require.True(t, ok, "ci.yml has a fuzz job")
	inCI := map[string]bool{}
	for _, s := range job.Steps {
		for _, m := range ciFuzzTarget.FindAllStringSubmatch(s.Run, -1) {
			inCI[m[1]+":"+m[2]] = true
		}
	}

	cmd := exec.Command("go", "run", "./hack/release-gate", "-list", "-no-lock", "-no-slots")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "KUBECONFIG=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	inGate := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasPrefix(f[0], "fuzz-") {
			inGate[strings.TrimPrefix(f[0], "fuzz-")] = true
		}
	}

	names := map[string]string{}
	for _, tg := range targets {
		name := tg[strings.LastIndex(tg, ":")+1:]
		if other, dup := names[name]; dup {
			t.Errorf("%s and %s share a name: the release gate's step IDs (fuzz-<Name>) would collide", tg, other)
		}
		names[name] = tg
		assert.True(t, inCI[tg], "%s is not in ci.yml's fuzz job", tg)
		assert.True(t, inGate[name], "%s is not a release gate step (fuzz-%s)", tg, name)
	}
	for tg := range inCI {
		assert.Contains(t, targets, tg, "ci.yml's fuzz job lists %s, which is not a fuzz target", tg)
	}
}
