// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// targetLabels are the labels a Prometheus Operator ServiceMonitor (and most
// scrape configs) attach to every scraped series. A metric label with one of
// these names is renamed exported_<name> at scrape time, so the dashboard and
// any query on the documented name see the target's value instead
// (kardinal_git_transfer_bytes_total{service} read "kardinal-promoter", not
// "fetch" or "push", in the acceptance run of 2026-10-09).
var targetLabels = map[string]bool{
	"job": true, "instance": true, "namespace": true, "pod": true, "service": true,
	"container": true, "endpoint": true,
}

// TestMetricLabelsAvoidTargetLabels parses every prometheus.New*Vec call in
// the controller's packages and refuses a label named like a scrape target
// label.
func TestMetricLabelsAvoidTargetLabels(t *testing.T) {
	root := repoRoot(t)
	found := 0
	for _, dir := range []string{"pkg", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !strings.HasPrefix(sel.Sel.Name, "New") || !strings.HasSuffix(sel.Sel.Name, "Vec") || len(call.Args) < 2 {
					return true
				}
				lit, ok := call.Args[len(call.Args)-1].(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, e := range lit.Elts {
					b, ok := e.(*ast.BasicLit)
					if !ok || b.Kind != token.STRING {
						continue
					}
					name, _ := strconv.Unquote(b.Value)
					found++
					rel, _ := filepath.Rel(root, path)
					assert.False(t, targetLabels[name], "%s: metric label %q collides with a scrape target label", rel, name)
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	assert.Greater(t, found, 10, "the scan finds the metric labels")
}
