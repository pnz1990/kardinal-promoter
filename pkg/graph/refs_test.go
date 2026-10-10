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
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/assert"
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
	var free []string
	seen := map[string]bool{}
	// walk visits e with the loop variables in scope. A comprehension's
	// variables are bound only inside it: the iteration and accumulator
	// variables in its condition and step, the accumulator in its result,
	// neither in its range or initial value, and nothing after it. So
	// "l.map(x, x) + [x]" reads a free x.
	var walk func(e celast.Expr, scope map[string]bool)
	walk = func(e celast.Expr, scope map[string]bool) {
		if e == nil {
			return
		}
		with := func(names ...string) map[string]bool {
			inner := make(map[string]bool, len(scope)+len(names))
			for k := range scope {
				inner[k] = true
			}
			for _, n := range names {
				if n != "" {
					inner[n] = true
				}
			}
			return inner
		}
		switch e.Kind() {
		case celast.IdentKind:
			if id := e.AsIdent(); !scope[id] && !seen[id] {
				seen[id] = true
				free = append(free, id)
			}
		case celast.SelectKind:
			walk(e.AsSelect().Operand(), scope)
		case celast.CallKind:
			c := e.AsCall()
			walk(c.Target(), scope)
			for _, a := range c.Args() {
				walk(a, scope)
			}
		case celast.ListKind:
			for _, el := range e.AsList().Elements() {
				walk(el, scope)
			}
		case celast.MapKind:
			for _, en := range e.AsMap().Entries() {
				m := en.AsMapEntry()
				walk(m.Key(), scope)
				walk(m.Value(), scope)
			}
		case celast.StructKind:
			for _, f := range e.AsStruct().Fields() {
				walk(f.AsStructField().Value(), scope)
			}
		case celast.ComprehensionKind:
			c := e.AsComprehension()
			walk(c.IterRange(), scope)
			walk(c.AccuInit(), scope)
			loop := with(c.IterVar(), c.IterVar2(), c.AccuVar())
			walk(c.LoopCondition(), loop)
			walk(c.LoopStep(), loop)
			walk(c.Result(), with(c.AccuVar()))
		}
	}
	walk(ast.NativeRep().Expr(), map[string]bool{})
	return free
}

// TestFreeIdentifiers: a loop variable is bound only inside its own
// comprehension (QA #1543), so a node ID that shares a loop variable's name
// and is read outside the loop is still reported.
func TestFreeIdentifiers(t *testing.T) {
	env, err := cel.NewEnv(cel.OptionalTypes(), ext.Lists()) // ext.Lists: sortBy is a macro (kro has it)
	require.NoError(t, err)
	for expr, want := range map[string][]string{
		"steps.map(s, s.name)":                                          {"steps"},
		"steps.map(s, s.name) + [s]":                                    {"steps", "s"},
		"a.all(x, x > 0) && b.exists(y, y == x)":                        {"a", "b", "x"},
		"a.map(x, b.filter(y, y == x))":                                 {"a", "b"},
		"a.map(x, x).size() > 0 ? x : y":                                {"a", "x", "y"},
		"{'k': v}.exists(k, k == w)":                                    {"v", "w"},
		`has(bundle.status.phase) && bundle.status.phase == "Verified"`: {"bundle"},
	} {
		got := freeIdentifiers(t, env, expr)
		assert.ElementsMatch(t, want, got, expr)
	}
}

// assertRefsResolve checks that every CEL expression of g (templates, defs,
// refs, patches, readyWhen, includeWhen, forEach) only reads node IDs of g, the forEach
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
	env, err := cel.NewEnv(cel.OptionalTypes(), ext.Lists()) // ext.Lists: sortBy is a macro (kro has it)
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
		eachString(n.Patch, func(s string) { check("patch", s, nil) })
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
	env, err := cel.NewEnv(cel.OptionalTypes(), ext.Lists()) // ext.Lists: sortBy is a macro (kro has it)
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
