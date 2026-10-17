// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package compliance_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/compliance"
)

// TestFrameworks_NameRealRecords (spec 22 section 8): every kind the table
// names is a kardinal.io kind of the scheme and every action is one
// AuditEvent's spec.action accepts and kardinal writes, so the page and the pack never point
// at a record kardinal does not write. Every control says what the
// customer still has to do, and has a shipped row or is left off the page.
//
// Covers COMPLIANCE-01.
func TestFrameworks_NameRealRecords(t *testing.T) {
	written := writtenActions(t)
	scheme := k8sruntime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	ids := map[string]bool{}
	for _, f := range compliance.Frameworks() {
		assert.False(t, ids[f.ID], "framework id %s is unique", f.ID)
		ids[f.ID] = true
		assert.NotEmpty(t, f.Name, f.ID)
		require.NotEmpty(t, f.Controls, f.ID)
		controls := map[string]bool{}
		for _, c := range f.Controls {
			where := f.ID + " " + c.ID
			assert.False(t, controls[c.ID], "%s: control id is unique", where)
			controls[c.ID] = true
			assert.NotEmpty(t, c.Title, where)
			assert.NotEmpty(t, c.Requirement, where)
			assert.NotEmpty(t, c.Customer, "%s: every control has a Customer line", where)
			require.NotEmpty(t, c.Evidence, where)
			for _, e := range c.Evidence {
				where := where + " / " + e.Element
				assert.NotEmpty(t, e.Element, where)
				assert.NotEmpty(t, e.Record, where)
				assert.Contains(t, []compliance.Status{compliance.Shipped, compliance.Planned}, e.Status, where)
				if e.Status == compliance.Shipped {
					assert.NotEmpty(t, e.Kinds, "%s: a shipped row names the kinds that hold its evidence", where)
				}
				for _, k := range e.Kinds {
					assert.NotEmpty(t, compliance.PackFiles[k], "%s: kind %s has a file in the evidence pack", where, k)
					gvk := schema.GroupVersionKind{Group: v1alpha1.GroupVersion.Group, Version: v1alpha1.GroupVersion.Version, Kind: k}
					assert.True(t, scheme.Recognizes(gvk), "%s: kind %s is in the scheme", where, k)
				}
				for _, a := range e.Actions {
					assert.True(t, slices.Contains(v1alpha1.AuditActions, a), "%s: action %s is an AuditEvent action", where, a)
					assert.True(t, written[a], "%s: no non-test code in %v writes action %s", where, writerDirs, a)
				}
			}
		}
	}
}

// TestMarkdown_ShippedRowsOnly: the user page lists only shipped rows; a
// planned row is left out, not marked "coming", and a control with no
// shipped row is left out with it (spec 22, decision C7).
func TestMarkdown_ShippedRowsOnly(t *testing.T) {
	md := compliance.Markdown([]compliance.Framework{{
		ID: "f", Name: "Framework F", Caveat: "Only for some systems.",
		Controls: []compliance.Control{{
			ID: "C-1", Title: "One", Requirement: "Do one.", Customer: "Your policy.",
			Evidence: []compliance.Evidence{
				{Element: "Shipped part", Record: "`a | b`", Kinds: []string{"Bundle"}, Status: compliance.Shipped},
				{Element: "Planned part", Record: "later", Status: compliance.Planned},
			},
		}, {
			ID: "C-2", Title: "Two", Requirement: "Do two.", Customer: "Yours.",
			Evidence: []compliance.Evidence{{Element: "Only planned", Record: "later", Status: compliance.Planned}},
		}, {
			ID: "Same", Title: "Same", Requirement: "Titled only.", Customer: "Yours.",
			Evidence: []compliance.Evidence{{Element: "E", Record: "R", Kinds: []string{"Bundle"}, Status: compliance.Shipped}},
		}},
	}, {
		ID: "g", Name: "Framework G",
		Controls: []compliance.Control{{ID: "G-1", Title: "Planned", Requirement: "r", Customer: "c",
			Evidence: []compliance.Evidence{{Element: "e", Record: "r", Status: compliance.Planned}}}},
	}})
	assert.Equal(t, "### Framework F\n\nOnly for some systems.\n\n"+
		"#### C-1: One\n\n*Requirement:* Do one.\n\n| Element | kardinal record |\n|---|---|\n"+
		"| Shipped part | `a \\| b` |\n\n**Your part:** Your policy.\n\n"+
		"#### Same\n\n*Requirement:* Titled only.\n\n| Element | kardinal record |\n|---|---|\n"+
		"| E | R |\n\n**Your part:** Yours.\n", md)
	for _, gone := range []string{"Planned part", "C-2", "Framework G", "later"} {
		assert.NotContains(t, md, gone)
	}
}

// compliancePage is the user page whose tables the table generates.
const compliancePage = "docs/compliance.md"

// The generated part of the page sits between these lines.
const (
	beginGenerated = "<!-- BEGIN GENERATED from pkg/compliance: KARDINAL_UPDATE_COMPLIANCE=1 go test ./pkg/compliance -run TestCompliancePage -->"
	endGenerated   = "<!-- END GENERATED -->"
)

// TestCompliancePage: the tables of docs/compliance.md are the ones the
// table generates, so the page cannot drift from it (as the CLI reference
// cannot). Regenerate with KARDINAL_UPDATE_COMPLIANCE=1 go test
// ./pkg/compliance -run TestCompliancePage.
//
// Covers COMPLIANCE-01.
func TestCompliancePage(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", compliancePage)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	page := string(b)
	start := strings.Index(page, beginGenerated+"\n")
	end := strings.Index(page, "\n"+endGenerated)
	require.True(t, start >= 0 && end > start, "%s has the %q and %q lines", compliancePage, beginGenerated, endGenerated)
	head, tail := page[:start+len(beginGenerated)+1], page[end:]
	want := head + "\n" + compliance.Markdown(compliance.Frameworks()) + tail
	if os.Getenv("KARDINAL_UPDATE_COMPLIANCE") == "1" {
		require.NoError(t, os.WriteFile(path, []byte(want), 0o644))
		return
	}
	require.Equal(t, want, page,
		"%s is stale: run KARDINAL_UPDATE_COMPLIANCE=1 go test ./pkg/compliance -run TestCompliancePage", compliancePage)
}

// TestForPack: controls.json keeps every row, planned ones with their
// status, and names the pack files of each row's kinds.
func TestForPack(t *testing.T) {
	got := compliance.ForPack([]compliance.Framework{{ID: "f", Name: "F", Controls: []compliance.Control{{
		ID: "C", Title: "T", Requirement: "R", Customer: "Y",
		Evidence: []compliance.Evidence{
			{Element: "e", Record: "r", Kinds: []string{"AuditEvent", "Bundle"}, Status: compliance.Shipped},
			{Element: "p", Record: "later", Status: compliance.Planned},
		},
	}}}})
	require.Len(t, got, 1)
	require.Len(t, got[0].Controls, 1)
	ev := got[0].Controls[0].Evidence
	require.Len(t, ev, 2)
	assert.Equal(t, []string{"auditevents.ndjson", "bundles.ndjson"}, ev[0].Files)
	assert.Equal(t, compliance.Planned, ev[1].Status)
	assert.Empty(t, ev[1].Files)
}

// writerDirs are where AuditEvents are written: the reconcilers (their
// outboxes) and the controller's API servers (APIWrite). The CLI only reads
// them (kardinal audit), so its names of the actions do not count.
var writerDirs = []string{"pkg/reconciler", "cmd/kardinal-controller"}

// writtenActions returns the AuditEvent actions the non-test Go code of
// writerDirs writes. An action is written where it, as a string literal or
// as a const or var whose value is that literal, is a call argument, a
// value (not a key) of a composite literal (Action: x in an AuditEventSpec,
// a map from a state to an action), or assigned to an Action field. A
// comparison (action == x), a switch case or a declaration is not a write,
// so the mapping cannot cite an action whose only writer was removed
// (#2126: HealthCheckFailed is accepted by the CRD but never written).
func writtenActions(t *testing.T) map[string]bool {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	actions := map[string]bool{}
	for _, a := range v1alpha1.AuditActions {
		actions[strconv.Quote(a)] = true
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, dir := range writerDirs {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err == nil {
				files = append(files, f)
			}
			return err
		}))
	}
	// The consts and vars whose value is an action, by name.
	named := map[string]string{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if spec, ok := n.(*ast.ValueSpec); ok && len(spec.Values) == len(spec.Names) {
				for i, v := range spec.Values {
					if lit, ok := v.(*ast.BasicLit); ok && actions[lit.Value] {
						named[spec.Names[i].Name] = lit.Value[1 : len(lit.Value)-1]
					}
				}
			}
			return true
		})
	}
	// action is the action e names: a literal, a name (a local of the
	// function first: action := "GateEvaluated"), or pkg.Name.
	action := func(e ast.Expr, locals map[string]string) (string, bool) {
		switch e := e.(type) {
		case *ast.BasicLit:
			if actions[e.Value] {
				return e.Value[1 : len(e.Value)-1], true
			}
		case *ast.Ident:
			if a, ok := locals[e.Name]; ok {
				return a, true
			}
			a, ok := named[e.Name]
			return a, ok
		case *ast.SelectorExpr:
			a, ok := named[e.Sel.Name]
			return a, ok
		}
		return "", false
	}
	written := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			// The function's locals defined as an action, then its writes.
			locals := map[string]string{}
			ast.Inspect(decl, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok && as.Tok == token.DEFINE && len(as.Lhs) == len(as.Rhs) {
					for i, lhs := range as.Lhs {
						if id, ok := lhs.(*ast.Ident); ok {
							if lit, ok := as.Rhs[i].(*ast.BasicLit); ok && actions[lit.Value] {
								locals[id.Name] = lit.Value[1 : len(lit.Value)-1]
							}
						}
					}
				}
				return true
			})
			inspectWrites(decl, func(e ast.Expr) {
				if a, ok := action(e, locals); ok {
					written[a] = true
				}
			})
		}
	}
	require.NotEmpty(t, written, "no action found in %v", writerDirs)
	return written
}

// inspectWrites calls write with every expression of n that is written: a
// call argument, a value of a composite literal, or the value assigned to
// an Action field.
func inspectWrites(n ast.Node, write func(ast.Expr)) {
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			for _, arg := range n.Args {
				write(arg)
			}
		case *ast.CompositeLit:
			for _, el := range n.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					write(kv.Value)
				} else {
					write(el)
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Action" && i < len(n.Rhs) {
					write(n.Rhs[i])
				}
			}
		}
		return true
	})
}
