// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// renderedObject is one object a template node of a Graph creates.
type renderedObject struct {
	NodeID string
	Object map[string]interface{}
}

// reDefField matches a forEach over a def node's field: ${Node.field}.
var reDefField = regexp.MustCompile(`^\$\{([A-Za-z][A-Za-z0-9]*)\.([A-Za-z][A-Za-z0-9]*)\}$`)

// reWholeExpr matches a field whose whole value is one ${...} expression.
var reWholeExpr = regexp.MustCompile(`^\$\{(.*)\}$`)

// renderObjects returns every object g's template nodes create, as kro would
// render them. A collection is expanded over the list its forEach reads from
// a def node, and each field whose whole value is a ${...} expression over
// the iterator and the def nodes is evaluated with cel-go. Fields of a
// scalar template are returned as they are, with any ${...} left in place.
func renderObjects(t *testing.T, g *graph.Graph) []renderedObject {
	t.Helper()
	return renderObjectsWith(t, g, nil)
}

// renderObjectsWith is renderObjects with the values of ref nodes (by node
// ID) in scope, as kro reads them from the cluster.
func renderObjectsWith(t *testing.T, g *graph.Graph, refs map[string]interface{}) []renderedObject {
	t.Helper()
	defs := map[string]interface{}{}
	for k, v := range refs {
		defs[k] = v
	}
	for _, n := range g.Spec.Nodes {
		if n.Def != nil {
			defs[n.ID] = n.Def
		}
		// A selector ref not given is an empty collection; a named ref not
		// given is the object with its name and a made-up UID.
		if md, _ := n.Ref["metadata"].(map[string]interface{}); md != nil {
			if _, given := defs[n.ID]; !given {
				if md["selector"] != nil {
					defs[n.ID] = []interface{}{}
				} else {
					defs[n.ID] = map[string]interface{}{"metadata": map[string]interface{}{
						"name": md["name"], "namespace": md["namespace"], "uid": fmt.Sprintf("uid-%v", md["name"])}}
				}
			}
		}
	}
	var out []renderedObject
	for _, n := range g.Spec.Nodes {
		if n.Template == nil {
			continue
		}
		if len(n.ForEach) == 0 {
			out = append(out, renderedObject{NodeID: n.ID, Object: n.Template})
			continue
		}
		require.Len(t, n.ForEach, 1, "node %s: one forEach dimension", n.ID)
		for iter, expr := range n.ForEach[0] {
			// A collection over a computed def field (the compact shape's
			// PromotionWave) depends on cluster state: compactSim renders it.
			if m := reDefField.FindStringSubmatch(expr); m != nil {
				if def, ok := defs[m[1]].(map[string]interface{}); ok {
					if _, computed := def[m[2]].(string); computed {
						continue
					}
				}
			}
			vars := map[string]interface{}{}
			for k, v := range defs {
				vars[k] = v
			}
			list, ok := evalCEL(t, expr, vars).([]interface{})
			require.True(t, ok, "node %s: forEach %q is a list", n.ID, expr)
			for _, item := range list {
				vars[iter] = item
				obj, ok := renderValue(t, n.Template, vars).(map[string]interface{})
				require.True(t, ok)
				out = append(out, renderedObject{NodeID: n.ID, Object: obj})
			}
		}
	}
	return out
}

func renderValue(t *testing.T, v interface{}, vars map[string]interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, c := range x {
			out[k] = renderValue(t, c, vars)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, c := range x {
			out[i] = renderValue(t, c, vars)
		}
		return out
	case string:
		if strings.Contains(x, "${") {
			require.Regexp(t, reWholeExpr, x, "a collection field is one whole expression")
			return evalCEL(t, x, vars)
		}
	}
	return v
}

// evalCEL evaluates the ${...} expression expr with vars declared dyn, the
// way kro evaluates a Graph expression (with optional types), and returns
// the result as plain Go values.
func evalCEL(t *testing.T, expr string, vars map[string]interface{}) interface{} {
	t.Helper()
	m := reWholeExpr.FindStringSubmatch(expr)
	require.NotNil(t, m, "expression %q", expr)
	opts := []cel.EnvOption{cel.OptionalTypes(), ext.Lists()}
	for k := range vars {
		opts = append(opts, cel.Variable(k, cel.DynType))
	}
	env, err := cel.NewEnv(opts...)
	require.NoError(t, err)
	ast, iss := env.Compile(m[1])
	require.NoError(t, iss.Err(), "compile %q", m[1])
	prg, err := env.Program(ast)
	require.NoError(t, err)
	val, _, err := prg.Eval(vars)
	require.NoError(t, err, "eval %q", m[1])
	return toGo(t, val)
}

func toGo(t *testing.T, v ref.Val) interface{} {
	switch x := v.(type) {
	case *types.Optional:
		// A whole-field optional renders as null when it has no value.
		if !x.HasValue() {
			return nil
		}
		return toGo(t, x.GetValue())
	case traits.Mapper:
		out := map[string]interface{}{}
		it := x.Iterator()
		for it.HasNext() == types.True {
			k := it.Next()
			out[fmt.Sprint(k.Value())] = toGo(t, x.Get(k))
		}
		return out
	case traits.Lister:
		var out []interface{}
		it := x.Iterator()
		for it.HasNext() == types.True {
			out = append(out, toGo(t, it.Next()))
		}
		return out
	}
	switch x := v.Value().(type) {
	case int64:
		return int(x)
	default:
		return x
	}
}

// renderedOf returns the rendered objects of kind.
func renderedOf(t *testing.T, g *graph.Graph, kind string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, o := range renderObjects(t, g) {
		if o.Object["kind"] == kind {
			out = append(out, o.Object)
		}
	}
	return out
}

// objName is metadata.name of a rendered object.
func objName(o map[string]interface{}) string {
	md, _ := o["metadata"].(map[string]interface{})
	return fmt.Sprint(md["name"])
}

// objLabels is metadata.labels of a rendered object.
func objLabels(o map[string]interface{}) map[string]interface{} {
	md, _ := o["metadata"].(map[string]interface{})
	l, _ := md["labels"].(map[string]interface{})
	return l
}
