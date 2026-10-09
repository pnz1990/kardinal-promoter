// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// celNamespaces are the identifiers a kro expression may use that are not
// node IDs: kro's library namespaces (json.marshal, maps.merge, lists.*,
// random.*) and cel-go's extensions.
var celNamespaces = map[string]bool{
	"json": true, "maps": true, "lists": true, "random": true, "math": true,
	"strings": true, "base64": true, "sets": true, "optional": true,
}

// kroExpressions returns every ${...} expression in s, in order. It matches
// braces outside string literals, so ${"a}b"} and ${m.filter(x, {"a": 1}...)}
// are one expression each.
func kroExpressions(t *testing.T, s string) []string {
	t.Helper()
	var out []string
	for i := 0; i < len(s); i++ {
		if !strings.HasPrefix(s[i:], "${") {
			continue
		}
		depth, quote, j := 1, byte(0), i+2
		for ; j < len(s) && depth > 0; j++ {
			c := s[j]
			switch {
			case quote != 0 && c == '\\':
				j++
			case quote != 0 && c == quote:
				quote = 0
			case quote != 0:
			case c == '"' || c == '\'':
				quote = c
			case c == '{':
				depth++
			case c == '}':
				depth--
			}
		}
		require.Zero(t, depth, "unterminated expression in %q", s)
		out = append(out, s[i+2:j-1])
		i = j - 1
	}
	return out
}

// strings walks v (a template, def or ref value) and calls f with each string.
func eachString(v interface{}, f func(string)) {
	switch x := v.(type) {
	case string:
		f(x)
	case map[string]interface{}:
		for _, e := range x {
			eachString(e, f)
		}
	case []interface{}:
		for _, e := range x {
			eachString(e, f)
		}
	}
}

// freeIdentifiers returns the identifiers expr reads that it does not bind:
// every identifier except comprehension variables (filter, map, all,
// exists...).
func freeIdentifiers(t *testing.T, env *cel.Env, expr string) []string {
	t.Helper()
	ast, iss := env.Parse(expr)
	require.NoError(t, iss.Err(), "parse %q", expr)
	bound := map[string]bool{}
	var idents []string
	celast.PreOrderVisit(ast.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		switch e.Kind() {
		case celast.ComprehensionKind:
			c := e.AsComprehension()
			bound[c.IterVar()] = true
			bound[c.AccuVar()] = true
			if v := c.IterVar2(); v != "" {
				bound[v] = true
			}
		case celast.IdentKind:
			idents = append(idents, e.AsIdent())
		}
	}))
	var free []string
	seen := map[string]bool{}
	for _, id := range idents {
		if !bound[id] && !seen[id] {
			seen[id] = true
			free = append(free, id)
		}
	}
	return free
}

// assertRefsResolve checks that every CEL expression of g (templates, defs,
// refs, readyWhen, includeWhen, forEach) only reads node IDs of g, the forEach
// iterators of its own node, "each" in readyWhen, and celNamespaces. A Graph
// whose expression names a node it does not have (a MetricCheck held on a
// step node the compact shape folds into a collection) fails here, though
// its object kinds match.
func assertRefsResolve(t *testing.T, g *graph.Graph) {
	t.Helper()
	require.Empty(t, unresolvedRefs(t, g), "expressions that read identifiers the Graph does not define")
}

// unresolvedRefs is assertRefsResolve's list of offending expressions.
func unresolvedRefs(t *testing.T, g *graph.Graph) []string {
	t.Helper()
	env, err := cel.NewEnv(cel.OptionalTypes())
	require.NoError(t, err)
	nodes := map[string]bool{}
	for _, n := range g.Spec.Nodes {
		nodes[n.ID] = true
	}
	var bad []string
	for _, n := range g.Spec.Nodes {
		local := map[string]bool{}
		for _, dim := range n.ForEach {
			for iter := range dim {
				local[iter] = true
			}
		}
		check := func(where, s string, extra map[string]bool) {
			for _, expr := range kroExpressions(t, s) {
				for _, id := range freeIdentifiers(t, env, expr) {
					if !nodes[id] && !local[id] && !extra[id] && !celNamespaces[id] {
						bad = append(bad, fmt.Sprintf("node %s %s: %q reads %q", n.ID, where, expr, id))
					}
				}
			}
		}
		eachString(n.Template, func(s string) { check("template", s, nil) })
		eachString(n.Def, func(s string) { check("def", s, nil) })
		eachString(n.Ref, func(s string) { check("ref", s, nil) })
		for _, s := range n.ReadyWhen {
			check("readyWhen", s, map[string]bool{"each": true})
		}
		for _, s := range n.IncludeWhen {
			check("includeWhen", s, nil)
		}
		for _, dim := range n.ForEach {
			for _, s := range dim {
				// A forEach expression cannot read its own iterators.
				for _, expr := range kroExpressions(t, s) {
					for _, id := range freeIdentifiers(t, env, expr) {
						if !nodes[id] && !celNamespaces[id] {
							bad = append(bad, fmt.Sprintf("node %s forEach: %q reads %q", n.ID, expr, id))
						}
					}
				}
			}
		}
	}
	sort.Strings(bad)
	return bad
}

// TestKroExpressions checks the expression scanner on quoting and nesting.
func TestKroExpressions(t *testing.T) {
	got := kroExpressions(t, `a ${x.y} b ${"}"} c ${m.filter(e, e.name == '}')} ${{"k": 1}.k}`)
	require.Equal(t, []string{`x.y`, `"}"`, `m.filter(e, e.name == '}')`, `{"k": 1}.k`}, got)
}

// TestAssertRefsResolve checks that the reference check catches a Graph that
// names a node it does not have.
func TestAssertRefsResolve(t *testing.T) {
	env, err := cel.NewEnv(cel.OptionalTypes())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"uat", "PromotionState"},
		freeIdentifiers(t, env, `uat.status.state == "Verified" && PromotionState.steps.all(s, s in uat.list)`))
}

// TestUnresolvedRefs checks the reference check on a Graph like the one
// #1516's steward caught: a compact Graph whose MetricCheck query is held on
// a step node ("uat") the compact shape does not have.
func TestUnresolvedRefs(t *testing.T) {
	g := &graph.Graph{}
	g.Spec.Nodes = []graph.GraphNode{
		{ID: "bundle", Ref: map[string]interface{}{"kind": "Bundle"}},
		{ID: "PromotionSteps", ForEach: []map[string]string{{"Step": "${bundle.spec.envs}"}},
			Template: map[string]interface{}{"metadata": map[string]interface{}{"name": "${Step.name}"}}},
		{ID: "metric0x", Template: map[string]interface{}{"spec": map[string]interface{}{
			"query":   `${[".."].filter(x_, uat.status.state == "Verified")[0]}`,
			"suspend": `${!(bundle.status.phase in ["Available"])}`,
		}}},
	}
	bad := unresolvedRefs(t, g)
	require.Len(t, bad, 1)
	require.Contains(t, bad[0], `reads "uat"`)
}
