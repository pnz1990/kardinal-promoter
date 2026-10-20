// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
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

// assertNoNullApproval: a rendered PolicyGate never has a null
// spec.approval. kro renders an optional with no value as null (its alpha
// CELOmitFunction gate is off), and the CRD refuses it, so such an instance
// is never created (#2271). The renderer here renders it null too, so every
// test that renders a Graph checks it.
func assertNoNullApproval(t *testing.T, node string, obj map[string]interface{}) {
	t.Helper()
	if obj["kind"] != "PolicyGate" {
		return
	}
	spec, _ := obj["spec"].(map[string]interface{})
	if v, ok := spec["approval"]; ok {
		require.NotNil(t, v, "node %s renders PolicyGate %v with spec.approval null, which the API server refuses (#2271)",
			node, obj["metadata"].(map[string]interface{})["name"])
	}
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
				assertNoNullApproval(t, n.ID, obj)
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
	prg := cachedProgram(t, "render", func() []cel.EnvOption {
		return []cel.EnvOption{cel.OptionalTypes(), ext.Lists()}
	}, vars, m[1])
	val, _, err := prg.Eval(vars)
	require.NoError(t, err, "eval %q", m[1])
	return toGo(t, val)
}

// celPrograms caches the CEL environments and programs the test helpers
// build. A Graph expression is evaluated once per collection item and per
// simulation round with the same variable names, and building a cel.Env (with
// optional types and the lists extension) and compiling dominate the
// package's test time under -race. An environment depends only on its library
// options and on the names of the variables (all declared dyn), so the cache
// key is the helper's kind, the sorted variable names and the expression: a
// cached program is the one the helper would build again. cel.Program is safe
// for concurrent use.
var celPrograms = struct {
	sync.Mutex
	envs map[string]*cel.Env
	prgs map[string]cel.Program
}{envs: map[string]*cel.Env{}, prgs: map[string]cel.Program{}}

// cachedProgram returns the program of expr in the environment of the
// options base returns plus vars' names declared dyn. kind names the base
// options; each helper uses its own kind.
func cachedProgram(t *testing.T, kind string, base func() []cel.EnvOption, vars map[string]interface{}, expr string) cel.Program {
	t.Helper()
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	envKey := kind + "\x00" + strings.Join(names, ",")
	prgKey := envKey + "\x00" + expr
	celPrograms.Lock()
	defer celPrograms.Unlock()
	if prg, ok := celPrograms.prgs[prgKey]; ok {
		return prg
	}
	env, ok := celPrograms.envs[envKey]
	if !ok {
		opts := base()
		for _, k := range names {
			opts = append(opts, cel.Variable(k, cel.DynType))
		}
		var err error
		env, err = cel.NewEnv(opts...)
		require.NoError(t, err)
		celPrograms.envs[envKey] = env
	}
	ast, iss := env.Compile(expr)
	require.NoError(t, iss.Err(), "compile %q", expr)
	prg, err := env.Program(ast)
	require.NoError(t, err)
	celPrograms.prgs[prgKey] = prg
	return prg
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
	name := fmt.Sprint(md["name"])
	// A gated name (resolvableWhen, ledger G1) renders the quoted literal
	// once its condition holds.
	if m := reGatedName.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return name
}

// reGatedName matches a resolvableWhen name: ${["<name>"].filter(...)[0]}.
var reGatedName = regexp.MustCompile(`^\$\{\["([^"]+)"\]\.filter\(.*\)\[0\]\}$`)

// objLabels is metadata.labels of a rendered object.
func objLabels(o map[string]interface{}) map[string]interface{} {
	md, _ := o["metadata"].(map[string]interface{})
	l, _ := md["labels"].(map[string]interface{})
	return l
}
