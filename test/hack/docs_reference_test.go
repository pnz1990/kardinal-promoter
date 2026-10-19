// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metricNames parses every non-test Go file under pkg/ and cmd/ and returns
// the kardinal_* metric names its prometheus *Opts literals register (the
// Name field), each with the file that defines it.
func metricNames(t *testing.T, root string) map[string]string {
	t.Helper()
	names := map[string]string{}
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
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || !strings.HasSuffix(sel.Sel.Name, "Opts") {
					return true
				}
				for _, e := range lit.Elts {
					kv, ok := e.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					b, isLit := kv.Value.(*ast.BasicLit)
					if !ok || key.Name != "Name" || !isLit || b.Kind != token.STRING {
						continue
					}
					name, _ := strconv.Unquote(b.Value)
					if strings.HasPrefix(name, "kardinal_") {
						rel, _ := filepath.Rel(root, path)
						names[name] = rel
					}
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	return names
}

// TestMetricsReferenceListsEveryMetric: docs/reference/metrics.md has a
// table row for every kardinal_* metric the controller registers, and no row
// for a metric it does not (V013 docs audit A8: two metrics shipped without
// one).
func TestMetricsReferenceListsEveryMetric(t *testing.T) {
	root := repoRoot(t)
	names := metricNames(t, root)
	require.Greater(t, len(names), 25, "the scan finds the registered metrics")

	doc, err := os.ReadFile(filepath.Join(root, "docs", "reference", "metrics.md"))
	require.NoError(t, err)
	rows := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\| `(kardinal_[a-z0-9_]+)` \\|").FindAllStringSubmatch(string(doc), -1) {
		rows[m[1]] = true
	}
	for name, file := range names {
		assert.True(t, rows[name], "%s registers %s, which docs/reference/metrics.md has no row for", file, name)
	}
	for name := range rows {
		_, ok := names[name]
		assert.True(t, ok, "docs/reference/metrics.md has a row for %s, which no package registers", name)
	}
}

// auditActions parses api/v1alpha1/auditevent_types.go and returns the
// values of v1alpha1.AuditActions, the actions an AuditEvent may have.
func auditActions(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "api", "v1alpha1", "auditevent_types.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err)
	var actions []string
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "AuditActions" || len(spec.Values) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.CompositeLit)
		require.True(t, ok, "AuditActions is a slice literal")
		for _, e := range lit.Elts {
			b, ok := e.(*ast.BasicLit)
			require.True(t, ok && b.Kind == token.STRING, "AuditActions holds string literals")
			v, _ := strconv.Unquote(b.Value)
			actions = append(actions, v)
		}
		return false
	})
	return actions
}

// TestAuditActionsDocumented: the "Events written automatically" section of
// docs/guides/security.md names every value of v1alpha1.AuditActions, in the
// first cell of a table row or in its sentence on the actions kardinal never
// writes, and names no other action (V013 docs audit A9: seven actions had no
// row).
func TestAuditActionsDocumented(t *testing.T) {
	root := repoRoot(t)
	actions := auditActions(t, root)
	require.Greater(t, len(actions), 30, "the parse finds AuditActions")

	doc, err := os.ReadFile(filepath.Join(root, "docs", "guides", "security.md"))
	require.NoError(t, err)
	const heading = "### Events written automatically\n"
	s := string(doc)
	i := strings.Index(s, heading)
	require.GreaterOrEqual(t, i, 0, "docs/guides/security.md has %q", strings.TrimSpace(heading))
	section := s[i+len(heading):]
	if j := strings.Index(section, "\n### "); j >= 0 {
		section = section[:j]
	}

	name := regexp.MustCompile("`([A-Z][A-Za-z]+)`")
	documented := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\| ([^|]+) \|`).FindAllStringSubmatch(section, -1) {
		for _, n := range name.FindAllStringSubmatch(m[1], -1) {
			documented[n[1]] = "a table row"
		}
	}
	never := regexp.MustCompile(`(?s)The CRD also accepts the actions (.*?) but kardinal never writes`).FindStringSubmatch(section)
	require.NotNil(t, never, "the section says which actions the CRD accepts but kardinal never writes")
	for _, n := range name.FindAllStringSubmatch(never[1], -1) {
		documented[n[1]] = "the never-written sentence"
	}

	valid := map[string]bool{}
	for _, a := range actions {
		valid[a] = true
		_, ok := documented[a]
		assert.True(t, ok, "AuditActions has %s, which \"Events written automatically\" does not name", a)
	}
	for n, where := range documented {
		assert.True(t, valid[n], "\"Events written automatically\" names %s in %s, which is not in AuditActions", n, where)
	}
}
