// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/ext"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8scellib "k8s.io/apiserver/pkg/cel/library"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/library"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// Kro executes kardinal's Graphs on the fake client the way kro's standalone
// Graph executor does (kubernetes-sigs/kro v0.10.0-rc.0,
// pkg/graphengine/executor/simple.go Apply, pkg/graphengine/runtime/node.go):
//
//   - nodes run in dependency order (the node IDs their ${...} expressions
//     read);
//   - a CEL error kro classifies as data pending ("no such key", "no such
//     field", "no such attribute", "index out of bounds",
//     runtime/errors.go celDataPendingPatterns) leaves the node Unresolved:
//     nothing is applied or pruned for it and its dependents, which cannot
//     read it, stay Unresolved too; any other CEL error is a hard error,
//     which kro reports as a degraded Graph (Kro.Errors);
//   - includeWhen false ignores a node and, contagiously, its dependents,
//     and prunes what they applied;
//   - readyWhen does not hold dependents back (GateReadiness is off for a
//     standalone Graph); it only feeds the Graph's Ready condition;
//   - template nodes are applied as a server-side apply would leave them
//     (the template's fields merged over the live object), stamped with
//     kro.run/node-id and, for collection items, kro.run/instance-id,
//     collection-index and collection-size (executor/simple.go stampKROMeta);
//   - ref nodes read one object (metadata.name) or a collection
//     (metadata.selector); a missing object is Unresolved;
//   - patch nodes merge their fields into an existing object and wait while
//     it does not exist;
//   - objects a node applied and no longer renders are deleted, and every
//     object of a deleted Graph is deleted.
//
// Kro stands in for kro in the scenario World (World lists the other
// stand-ins); every kardinal reconciler and the translator run as in the
// controller.
type Kro struct {
	c client.Client

	mu sync.Mutex
	// applied holds, per Graph (namespace/name), the objects each node
	// applied: node ID -> object key.
	applied map[string]map[string]map[objKey]bool
	// programs caches compiled expressions per Graph spec.
	programs map[string]*graphPrograms
	// errs are the hard errors seen, deduplicated; expected, the parts of
	// the ones a scenario causes on purpose.
	errs     map[string]bool
	expected []string
	// Version, when set, counts the cluster's writes: a Sync that finds no
	// write since the last Sync that changed nothing does nothing, as kro
	// only reconciles a Graph when something it watches changed.
	Version func() int64
	clean   int64
	isClean bool
	// Skip, when set, reports a Graph that needs no pass.
	Skip func(g *graph.Graph) bool
	// GraphVersion, when set, counts the writes of Graphs: a Sync after none
	// reuses the Graphs it listed and decoded last (the fake client's List
	// is a JSON round trip of every Graph).
	GraphVersion func() int64
	listed       *listedGraphs
	// failing are the Graphs (namespace/name) whose last pass hit a hard
	// error: kro requeues a Graph whose reconcile failed, so they get a pass
	// every Sync, whatever Version and Skip say, until one succeeds.
	failing map[string]bool
}

type objKey struct {
	apiVersion, kind, namespace, name string
}

type graphPrograms struct {
	env  *cel.Env
	prog map[string]cel.Program
	deps map[string][]string
}

// NewKro returns a Graph executor over c.
func NewKro(c client.Client) *Kro {
	return &Kro{c: c, applied: map[string]map[string]map[objKey]bool{}, programs: map[string]*graphPrograms{}, errs: map[string]bool{},
		failing: map[string]bool{}}
}

// Errors returns the hard errors kro would have reported, sorted, but the
// expected ones (Expect).
func (k *Kro) Errors() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, 0, len(k.errs))
	for e := range k.errs {
		if !slices.ContainsFunc(k.expected, func(x string) bool { return strings.Contains(e, x) }) {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

// Expect makes the errors that contain msg expected: a scenario that
// refuses kro's writes on purpose.
func (k *Kro) Expect(msg string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.expected = append(k.expected, msg)
}

func (k *Kro) hardError(g *graph.Graph, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.errs[fmt.Sprintf("graph %s/%s: %v", g.Namespace, g.Name, err)] = true
}

// libraryNamespaces are identifiers expressions may use that are not node
// IDs (cel-go extensions and the kro library).
var libraryNamespaces = map[string]bool{
	"json": true, "maps": true, "lists": true, "random": true, "math": true,
	"strings": true, "base64": true, "sets": true, "optional": true,
}

// baseEnvOptions mirror kro's coreDeclarations (pkg/cel/environment.go),
// minus the libraries kardinal's Graphs do not use (hash, omit, runtime).
func baseEnvOptions() []cel.EnvOption {
	return []cel.EnvOption{
		ext.TwoVarComprehensions(),
		ext.Lists(),
		ext.Strings(),
		ext.Bindings(),
		cel.OptionalTypes(),
		ext.Encoders(),
		k8scellib.Lists(),
		k8scellib.URLs(),
		k8scellib.Regex(),
		library.Random(),
		library.Maps(),
		library.JSON(),
		library.Lists(),
	}
}

var (
	baseEnvOnce sync.Once
	baseEnv     *cel.Env
	baseEnvErr  error
)

func kroBaseEnv() (*cel.Env, error) {
	baseEnvOnce.Do(func() { baseEnv, baseEnvErr = cel.NewEnv(baseEnvOptions()...) })
	return baseEnv, baseEnvErr
}

// ExpressionsIn returns every ${...} expression in s, in order. Braces
// inside string literals do not count.
func ExpressionsIn(s string) ([]string, error) {
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
		if depth != 0 {
			return nil, fmt.Errorf("unterminated expression in %q", s)
		}
		out = append(out, s[i+2:j-1])
		i = j - 1
	}
	return out, nil
}

// FreeIdentifiers returns the identifiers expr reads that it does not bind
// in a comprehension.
func FreeIdentifiers(env *cel.Env, expr string) ([]string, error) {
	a, iss := env.Parse(expr)
	if iss.Err() != nil {
		return nil, fmt.Errorf("parse %q: %w", expr, iss.Err())
	}
	var free []string
	seen := map[string]bool{}
	var walk func(e celast.Expr, scope map[string]bool)
	with := func(scope map[string]bool, names ...string) map[string]bool {
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
	walk = func(e celast.Expr, scope map[string]bool) {
		if e == nil {
			return
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
			for _, arg := range c.Args() {
				walk(arg, scope)
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
			loop := with(scope, c.IterVar(), c.IterVar2(), c.AccuVar())
			walk(c.LoopCondition(), loop)
			walk(c.LoopStep(), loop)
			walk(c.Result(), with(scope, c.AccuVar()))
		}
	}
	walk(a.NativeRep().Expr(), map[string]bool{})
	return free, nil
}

// eachString calls f with every string in v.
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

// nodeBody is the template, ref, def or patch of n.
func nodeBody(n graph.GraphNode) map[string]interface{} {
	switch {
	case n.Template != nil:
		return n.Template
	case n.Ref != nil:
		return n.Ref
	case n.Def != nil:
		return n.Def
	default:
		return n.Patch
	}
}

var nodeIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// compile validates g's structure as kro's compiler does
// (pkg/graphengine/compiler/validation.go) and compiles its expressions.
func (k *Kro) compile(g *graph.Graph) (*graphPrograms, error) {
	key := fmt.Sprintf("%s/%s@%s", g.Namespace, g.Name, specKey(g))
	k.mu.Lock()
	gp, ok := k.programs[key]
	k.mu.Unlock()
	if ok {
		return gp, nil
	}
	base, err := kroBaseEnv()
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	var decls []cel.EnvOption
	declared := map[string]bool{}
	declare := func(name string) {
		if !declared[name] {
			declared[name] = true
			decls = append(decls, cel.Variable(name, cel.DynType))
		}
	}
	for _, n := range g.Spec.Nodes {
		if !nodeIDPattern.MatchString(n.ID) {
			return nil, fmt.Errorf("node id %q must match %s", n.ID, nodeIDPattern)
		}
		if ids[n.ID] {
			return nil, fmt.Errorf("duplicate node id %q", n.ID)
		}
		ids[n.ID] = true
		set := 0
		for _, b := range []bool{n.Template != nil, n.Ref != nil, n.Def != nil, n.Patch != nil} {
			if b {
				set++
			}
		}
		if set != 1 {
			return nil, fmt.Errorf("node %q: exactly one of template/ref/def/patch must be set (got %d)", n.ID, set)
		}
		if n.Ref != nil && len(n.ForEach) > 0 {
			return nil, fmt.Errorf("node %q: forEach is not supported on ref nodes", n.ID)
		}
		if n.Patch != nil && len(n.ReadyWhen) > 0 {
			return nil, fmt.Errorf("node %q: readyWhen is not supported on patch nodes", n.ID)
		}
		declare(n.ID)
		for _, dim := range n.ForEach {
			if len(dim) != 1 {
				return nil, fmt.Errorf("node %q: forEach dimension must have exactly one entry", n.ID)
			}
			for name := range dim {
				declare(name)
			}
		}
	}
	declare("each")
	env, err := base.Extend(decls...)
	if err != nil {
		return nil, fmt.Errorf("extend env: %w", err)
	}
	gp = &graphPrograms{env: env, prog: map[string]cel.Program{}, deps: map[string][]string{}}
	for _, n := range g.Spec.Nodes {
		iter := map[string]bool{}
		for _, dim := range n.ForEach {
			for name := range dim {
				iter[name] = true
			}
		}
		var exprs []string
		collect := func(s string) {
			es, err2 := ExpressionsIn(s)
			if err2 != nil && err == nil {
				err = err2
			}
			exprs = append(exprs, es...)
		}
		eachString(nodeBody(n), collect)
		for _, s := range n.IncludeWhen {
			collect(s)
		}
		for _, dim := range n.ForEach {
			for _, s := range dim {
				collect(s)
			}
		}
		readyExprs := map[string]bool{}
		for _, s := range n.ReadyWhen {
			es, err2 := ExpressionsIn(s)
			if err2 != nil {
				return nil, err2
			}
			for _, e := range es {
				readyExprs[e] = true
			}
			exprs = append(exprs, es...)
		}
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", n.ID, err)
		}
		depSet := map[string]bool{}
		for _, e := range exprs {
			if _, done := gp.prog[e]; !done {
				ast, iss := env.Compile(e)
				if iss.Err() != nil {
					return nil, fmt.Errorf("node %q: compile %q: %w", n.ID, e, iss.Err())
				}
				prg, perr := env.Program(ast)
				if perr != nil {
					return nil, fmt.Errorf("node %q: program %q: %w", n.ID, e, perr)
				}
				gp.prog[e] = prg
			}
			free, ferr := FreeIdentifiers(env, e)
			if ferr != nil {
				return nil, ferr
			}
			for _, id := range free {
				switch {
				case iter[id], libraryNamespaces[id]:
				case id == "each" && readyExprs[e]:
				case id == n.ID && readyExprs[e]:
					// readyWhen reads the node itself.
				case ids[id]:
					depSet[id] = true
				default:
					return nil, fmt.Errorf("node %q: expression %q reads %q, which is no node, iterator or library", n.ID, e, id)
				}
			}
		}
		for d := range depSet {
			gp.deps[n.ID] = append(gp.deps[n.ID], d)
		}
		sort.Strings(gp.deps[n.ID])
	}
	if _, err := topoOrder(g, gp.deps); err != nil {
		return nil, err
	}
	k.mu.Lock()
	k.programs[key] = gp
	k.mu.Unlock()
	return gp, nil
}

// specKey identifies a Graph spec revision: the generation, or the
// resourceVersion when the fake client keeps no generation.
func specKey(g *graph.Graph) string {
	if g.Generation != 0 {
		return strconv.FormatInt(g.Generation, 10)
	}
	return g.ResourceVersion
}

// topoOrder orders g's nodes so each comes after the nodes it reads; it
// fails on a cycle (kro's compiler rejects one).
func topoOrder(g *graph.Graph, deps map[string][]string) ([]graph.GraphNode, error) {
	byID := map[string]graph.GraphNode{}
	for _, n := range g.Spec.Nodes {
		byID[n.ID] = n
	}
	state := map[string]int{}
	var out []graph.GraphNode
	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("graph has a cycle: %s", strings.Join(append(path, id), " -> "))
		case 2:
			return nil
		}
		state[id] = 1
		for _, d := range deps[id] {
			if err := visit(d, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		out = append(out, byID[id])
		return nil
	}
	for _, n := range g.Spec.Nodes {
		if err := visit(n.ID, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// errPending is a data-pending evaluation: the node stays Unresolved.
var errPending = errors.New("data pending")

// dataPending reports whether a CEL error is one kro retries
// (pkg/graphengine/runtime/errors.go celDataPendingPatterns).
func dataPending(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errPending) {
		return true
	}
	msg := err.Error()
	for _, p := range []string{"no such key", "no such field", "no such attribute", "index out of bounds"} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func (gp *graphPrograms) eval(expr string, scope map[string]interface{}) (interface{}, error) {
	prg, ok := gp.prog[expr]
	if !ok {
		return nil, fmt.Errorf("expression %q not compiled", expr)
	}
	out, _, err := prg.Eval(scope)
	if err != nil {
		return nil, err
	}
	return toNative(out)
}

// toNative converts a CEL value to the JSON-shaped Go value kro writes.
func toNative(v ref.Val) (interface{}, error) {
	switch t := v.(type) {
	case types.String:
		return string(t), nil
	case types.Int:
		return int64(t), nil
	case types.Uint:
		return int64(t), nil
	case types.Double:
		return float64(t), nil
	case types.Bool:
		return bool(t), nil
	case types.Null:
		return nil, nil
	case *types.Optional:
		if !t.HasValue() {
			return nil, nil
		}
		return toNative(t.GetValue())
	case traits.Mapper:
		out := map[string]interface{}{}
		it := t.Iterator()
		for it.HasNext() == types.True {
			key := it.Next()
			ks, ok := key.(types.String)
			if !ok {
				return nil, fmt.Errorf("map key %v is not a string", key)
			}
			val, err := toNative(t.Get(key))
			if err != nil {
				return nil, err
			}
			out[string(ks)] = val
		}
		return out, nil
	case traits.Lister:
		out := []interface{}{}
		it := t.Iterator()
		for it.HasNext() == types.True {
			val, err := toNative(it.Next())
			if err != nil {
				return nil, err
			}
			out = append(out, val)
		}
		return out, nil
	}
	nv, err := v.ConvertToNative(reflect.TypeOf((*interface{})(nil)).Elem())
	if err != nil {
		return nil, fmt.Errorf("convert %T: %w", v, err)
	}
	return nv, nil
}

// render resolves every ${...} in v against scope. A string that is one
// expression becomes its value; a string with text around expressions
// becomes the concatenation of their string values.
func (gp *graphPrograms) render(v interface{}, scope map[string]interface{}) (interface{}, error) {
	switch x := v.(type) {
	case string:
		exprs, err := ExpressionsIn(x)
		if err != nil {
			return nil, err
		}
		if len(exprs) == 0 {
			return x, nil
		}
		if len(exprs) == 1 && x == "${"+exprs[0]+"}" {
			return gp.eval(exprs[0], scope)
		}
		out := x
		for _, e := range exprs {
			val, err := gp.eval(e, scope)
			if err != nil {
				return nil, err
			}
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("expression %q in %q is %T, want string", e, x, val)
			}
			out = strings.Replace(out, "${"+e+"}", s, 1)
		}
		return out, nil
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for key, e := range x {
			r, err := gp.render(e, scope)
			if err != nil {
				return nil, err
			}
			out[key] = r
		}
		return out, nil
	case []interface{}:
		out := make([]interface{}, 0, len(x))
		for _, e := range x {
			r, err := gp.render(e, scope)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	default:
		return v, nil
	}
}

// evalBool evaluates an includeWhen or readyWhen string.
func (gp *graphPrograms) evalBool(s string, scope map[string]interface{}) (bool, error) {
	v, err := gp.render(s, scope)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%q is %T, want bool", s, v)
	}
	return b, nil
}

// rows expands n's forEach dimensions into iterator bindings.
func (gp *graphPrograms) rows(n graph.GraphNode, scope map[string]interface{}) ([]map[string]interface{}, error) {
	rows := []map[string]interface{}{{}}
	for _, dim := range n.ForEach {
		for name, s := range dim {
			v, err := gp.render(s, scope)
			if err != nil {
				return nil, err
			}
			list, ok := v.([]interface{})
			if !ok {
				return nil, fmt.Errorf("forEach %q is %T, want list", name, v)
			}
			var next []map[string]interface{}
			for _, r := range rows {
				for _, item := range list {
					nr := make(map[string]interface{}, len(r)+1)
					for k2, v2 := range r {
						nr[k2] = v2
					}
					nr[name] = item
					next = append(next, nr)
				}
			}
			rows = next
		}
	}
	return rows, nil
}

func withBindings(scope, bindings map[string]interface{}) map[string]interface{} {
	if len(bindings) == 0 {
		return scope
	}
	out := make(map[string]interface{}, len(scope)+len(bindings))
	for k, v := range scope {
		out[k] = v
	}
	for k, v := range bindings {
		out[k] = v
	}
	return out
}

// Sync applies every Graph in the cluster once and deletes the objects of
// Graphs that are gone. It returns how many objects it created, changed or
// deleted.
func (k *Kro) Sync(ctx context.Context) (int, error) {
	if k.Version != nil && k.isClean && k.Version() == k.clean {
		return 0, nil
	}
	n, err := k.sync(ctx)
	k.mu.Lock()
	failing := len(k.failing) > 0
	k.mu.Unlock()
	if k.Version != nil && err == nil && n == 0 && !failing {
		k.clean, k.isClean = k.Version(), true
	} else {
		k.isClean = false
	}
	return n, err
}

// listedGraphs are the Graphs a Sync listed, decoded, at a GraphVersion.
type listedGraphs struct {
	version int64
	list    *unstructured.UnstructuredList
	graphs  []*graph.Graph
}

// graphs lists and decodes the Graphs, or reuses the last list when no Graph
// was written since.
func (k *Kro) graphs(ctx context.Context) (*unstructured.UnstructuredList, []*graph.Graph, error) {
	var version int64
	if k.GraphVersion != nil {
		version = k.GraphVersion()
		if l := k.listed; l != nil && l.version == version {
			return l.list, l.graphs, nil
		}
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: graph.GraphGVR.Group, Version: graph.GraphGVR.Version, Kind: "GraphList"})
	if err := k.c.List(ctx, list); err != nil {
		return nil, nil, fmt.Errorf("list graphs: %w", err)
	}
	graphs := make([]*graph.Graph, len(list.Items))
	for i := range list.Items {
		// Decoded from the unstructured object, not through JSON: kro
		// reads integers in the spec as integers (graph.FromUnstructured's
		// JSON round trip makes them float64, which CEL compares with ints
		// as "no such overload").
		g := &graph.Graph{}
		if err := k8sruntime.DefaultUnstructuredConverter.FromUnstructured(list.Items[i].Object, g); err != nil {
			return nil, nil, fmt.Errorf("decode graph: %w", err)
		}
		graphs[i] = g
	}
	if k.GraphVersion != nil {
		k.listed = &listedGraphs{version: version, list: list, graphs: graphs}
	}
	return list, graphs, nil
}

func (k *Kro) sync(ctx context.Context) (int, error) {
	list, graphs, err := k.graphs(ctx)
	if err != nil {
		return 0, err
	}
	live := map[string]bool{}
	changes := 0
	for i := range list.Items {
		g := graphs[i]
		gk := g.Namespace + "/" + g.Name
		live[gk] = true
		k.mu.Lock()
		retry := k.failing[gk]
		k.mu.Unlock()
		if g.DeletionTimestamp != nil || (!retry && k.Skip != nil && k.Skip(g)) {
			continue
		}
		n, err := k.apply(ctx, g, &list.Items[i])
		changes += n
		if err != nil {
			return changes, err
		}
	}
	k.mu.Lock()
	var gone []string
	for gk := range k.applied {
		if !live[gk] {
			gone = append(gone, gk)
		}
	}
	k.mu.Unlock()
	sort.Strings(gone)
	for _, gk := range gone {
		k.mu.Lock()
		nodes := k.applied[gk]
		delete(k.applied, gk)
		delete(k.failing, gk)
		k.mu.Unlock()
		for _, objs := range nodes {
			for key := range objs {
				deleted, err := k.delete(ctx, key)
				if err != nil {
					return changes, err
				}
				if deleted {
					changes++
				}
			}
		}
	}
	return changes, nil
}

func (k *Kro) delete(ctx context.Context, key objKey) (bool, error) {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(key.apiVersion)
	u.SetKind(key.kind)
	u.SetNamespace(key.namespace)
	u.SetName(key.name)
	if err := k.c.Delete(ctx, u); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("prune %s %s/%s: %w", key.kind, key.namespace, key.name, err)
	}
	return true, nil
}

// apply runs one pass of g. Hard errors are recorded (Errors) and leave the
// Graph degraded; API errors are returned.
func (k *Kro) apply(ctx context.Context, g *graph.Graph, raw *unstructured.Unstructured) (int, error) {
	// kro applies a Graph's objects as the Graph's ServiceAccount.
	ctx = AsInfo(ctx, GraphInfo(raw.GetNamespace()))
	gp, err := k.compile(g)
	if err != nil {
		k.hardError(g, err)
		return 0, k.setStatus(ctx, raw, false, "InvalidGraph", err.Error(), false)
	}
	order, _ := topoOrder(g, gp.deps)
	gk := g.Namespace + "/" + g.Name
	scope := map[string]interface{}{}
	unresolved := map[string]bool{}
	ignored := map[string]bool{}
	notReady := ""
	hard := ""
	applied := map[string]map[objKey]bool{}
	changes := 0
	for _, n := range order {
		blocked := false
		for _, d := range gp.deps[n.ID] {
			if ignored[d] {
				ignored[n.ID] = true
			}
			if unresolved[d] {
				blocked = true
			}
		}
		if ignored[n.ID] {
			continue
		}
		if blocked {
			// A dependency is not in scope: kro's evaluation fails with
			// "no such attribute", data pending.
			unresolved[n.ID] = true
			continue
		}
		include := true
		var ierr error
		for _, s := range n.IncludeWhen {
			b, err := gp.evalBool(s, scope)
			if err != nil {
				ierr = err
				break
			}
			if !b {
				include = false
				break
			}
		}
		if ierr != nil {
			if dataPending(ierr) {
				unresolved[n.ID] = true
				continue
			}
			hard = fmt.Sprintf("node %q: includeWhen: %v", n.ID, ierr)
			k.hardError(g, errors.New(hard))
			unresolved[n.ID] = true
			continue
		}
		if !include {
			ignored[n.ID] = true
			continue
		}
		n2, objs, value, err := k.applyNode(ctx, g, gp, n, scope)
		changes += n2
		if err != nil {
			if dataPending(err) {
				unresolved[n.ID] = true
				continue
			}
			var apiErr apiError
			if errors.As(err, &apiErr) {
				return changes, apiErr.err
			}
			hard = fmt.Sprintf("node %q: %v", n.ID, err)
			k.hardError(g, errors.New(hard))
			unresolved[n.ID] = true
			continue
		}
		if objs != nil {
			applied[n.ID] = objs
		}
		if n.Patch == nil {
			scope[n.ID] = value
		}
		if notReady == "" {
			if r := gp.ready(n, scope, value); r != "" {
				notReady = r
			}
		}
	}
	// Prune what resolved nodes no longer render and what ignored nodes
	// applied; Unresolved nodes keep theirs.
	k.mu.Lock()
	prev := k.applied[gk]
	next := map[string]map[objKey]bool{}
	for id, objs := range prev {
		if unresolved[id] {
			next[id] = objs
		}
	}
	for id, objs := range applied {
		next[id] = objs
	}
	k.applied[gk] = next
	k.mu.Unlock()
	for id, objs := range prev {
		if unresolved[id] {
			continue
		}
		for key := range objs {
			if applied[id][key] {
				continue
			}
			deleted, err := k.delete(ctx, key)
			if err != nil {
				return changes, err
			}
			if deleted {
				changes++
			}
		}
	}
	if len(unresolved) > 0 && notReady == "" {
		ids := make([]string, 0, len(unresolved))
		for id := range unresolved {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		notReady = "data pending: " + strings.Join(ids, ", ")
	}
	k.mu.Lock()
	if hard != "" {
		k.failing[gk] = true
	} else {
		delete(k.failing, gk)
	}
	k.mu.Unlock()
	if hard != "" {
		return changes, k.setStatus(ctx, raw, true, "Compiled", hard, false)
	}
	return changes, k.setStatus(ctx, raw, true, "Compiled", notReady, notReady == "")
}

// ready evaluates n's readyWhen for the Graph's Ready condition: "" when
// ready, else why not.
func (gp *graphPrograms) ready(n graph.GraphNode, scope map[string]interface{}, value interface{}) string {
	if len(n.ReadyWhen) == 0 {
		return ""
	}
	items := []interface{}{value}
	if list, ok := value.([]interface{}); ok && (len(n.ForEach) > 0 || isSelectorRef(n)) {
		items = list
	}
	for _, item := range items {
		sc := scope
		if len(n.ForEach) > 0 || isSelectorRef(n) {
			sc = withBindings(scope, map[string]interface{}{"each": item})
		}
		for _, s := range n.ReadyWhen {
			b, err := gp.evalBool(s, sc)
			if err != nil || !b {
				return fmt.Sprintf("node %q: readyWhen %s not satisfied", n.ID, s)
			}
		}
	}
	return ""
}

func isSelectorRef(n graph.GraphNode) bool {
	if n.Ref == nil {
		return false
	}
	md, _ := n.Ref["metadata"].(map[string]interface{})
	_, ok := md["selector"]
	return ok
}

// apiError wraps an error from the API server, which aborts the pass.
type apiError struct{ err error }

func (e apiError) Error() string { return e.err.Error() }

// applyNode runs one node and returns the objects it applied and its value
// in scope.
func (k *Kro) applyNode(ctx context.Context, g *graph.Graph, gp *graphPrograms, n graph.GraphNode,
	scope map[string]interface{}) (int, map[objKey]bool, interface{}, error) {
	switch {
	case n.Def != nil:
		v, err := gp.render(n.Def, scope)
		return 0, nil, v, err
	case n.Ref != nil:
		v, err := k.readRef(ctx, g, gp, n, scope)
		return 0, nil, v, err
	}
	rows, err := gp.rows(n, scope)
	if err != nil {
		return 0, nil, nil, err
	}
	// Render every row before applying any: kro resolves the whole node
	// first (runtime/node.go Resolve).
	body := n.Template
	if body == nil {
		body = n.Patch
	}
	rendered := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		v, err := gp.render(body, withBindings(scope, r))
		if err != nil {
			return 0, nil, nil, err
		}
		m, ok := v.(map[string]interface{})
		if !ok {
			return 0, nil, nil, fmt.Errorf("rendered %T, want object", v)
		}
		rendered = append(rendered, m)
	}
	changes := 0
	objs := map[objKey]bool{}
	var values []interface{}
	for i, m := range rendered {
		obj := &unstructured.Unstructured{Object: m}
		if obj.GetNamespace() == "" {
			obj.SetNamespace(g.Namespace)
		}
		if n.Patch != nil {
			c, err := k.patch(ctx, obj)
			changes += c
			if err != nil {
				return changes, nil, nil, err
			}
			continue
		}
		lbls := obj.GetLabels()
		if lbls == nil {
			lbls = map[string]string{}
		}
		lbls[graph.LabelKRONodeID] = n.ID
		if len(n.ForEach) > 0 {
			lbls["kro.run/collection-index"] = strconv.Itoa(i)
			lbls["kro.run/collection-size"] = strconv.Itoa(len(rendered))
			if _, ok := lbls["kro.run/instance-id"]; !ok && g.UID != "" {
				lbls["kro.run/instance-id"] = string(g.UID)
			}
		}
		obj.SetLabels(lbls)
		ann := obj.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann["internal.kro.run/node-path"] = n.ID
		obj.SetAnnotations(ann)
		live, c, err := k.applyObject(ctx, obj)
		changes += c
		if err != nil {
			return changes, nil, nil, err
		}
		objs[objKey{obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace(), obj.GetName()}] = true
		values = append(values, live.Object)
	}
	if n.Patch != nil {
		return changes, nil, nil, nil
	}
	if len(n.ForEach) > 0 {
		if values == nil {
			values = []interface{}{}
		}
		return changes, objs, values, nil
	}
	if len(values) != 1 {
		return changes, objs, nil, fmt.Errorf("template rendered %d objects", len(values))
	}
	return changes, objs, values[0], nil
}

// readRef reads a ref node's object or collection.
func (k *Kro) readRef(ctx context.Context, g *graph.Graph, gp *graphPrograms, n graph.GraphNode,
	scope map[string]interface{}) (interface{}, error) {
	v, err := gp.render(n.Ref, scope)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]interface{})
	apiVersion, _ := m["apiVersion"].(string)
	kind, _ := m["kind"].(string)
	md, _ := m["metadata"].(map[string]interface{})
	ns, _ := md["namespace"].(string)
	if sel, ok := md["selector"].(map[string]interface{}); ok {
		// A selector ref without a namespace lists every namespace
		// (kro pkg/graphengine/executor/simple.go:1259-1263, applyRefCollection).
		ls := &metav1.LabelSelector{}
		if ml, ok := sel["matchLabels"].(map[string]interface{}); ok {
			ls.MatchLabels = map[string]string{}
			for k2, v2 := range ml {
				ls.MatchLabels[k2] = fmt.Sprint(v2)
			}
		}
		selector, err := metav1.LabelSelectorAsSelector(ls)
		if err != nil {
			return nil, fmt.Errorf("selector: %w", err)
		}
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion(apiVersion)
		list.SetKind(kind + "List")
		if err := k.c.List(ctx, list, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: selector}); err != nil {
			if meta.IsNoMatchError(err) || runtimeNotRegistered(err) {
				return nil, fmt.Errorf("ref %s: kind not served: %w", kind, errPending)
			}
			return nil, apiError{fmt.Errorf("list %s: %w", kind, err)}
		}
		out := make([]interface{}, 0, len(list.Items))
		sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].GetName() < list.Items[j].GetName() })
		for _, it := range list.Items {
			out = append(out, it.Object)
		}
		return out, nil
	}
	if ns == "" {
		ns = g.Namespace
	}
	name, _ := md["name"].(string)
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	if err := k.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || runtimeNotRegistered(err) {
			return nil, fmt.Errorf("ref %s %s/%s not found: %w", kind, ns, name, errPending)
		}
		return nil, apiError{fmt.Errorf("get %s %s/%s: %w", kind, ns, name, err)}
	}
	return obj.Object, nil
}

func runtimeNotRegistered(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no kind is registered")
}

// applyObject creates obj or merges its fields over the live object, as a
// server-side apply by kro's field manager would.
func (k *Kro) applyObject(ctx context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, int, error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(obj.GroupVersionKind())
	err := k.c.Get(ctx, client.ObjectKeyFromObject(obj), live)
	if apierrors.IsNotFound(err) {
		if err := k.c.Create(ctx, obj.DeepCopy()); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return nil, 0, apiError{err}
			}
			return nil, 0, fmt.Errorf("create %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
		}
		if err := k.c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
			return nil, 1, apiError{err}
		}
		return live, 1, nil
	}
	if err != nil {
		return nil, 0, apiError{fmt.Errorf("get %s: %w", obj.GetName(), err)}
	}
	if live.GetDeletionTimestamp() != nil {
		// kro waits for a terminating object (ResourceDeletingError).
		return nil, 0, fmt.Errorf("%s %s is terminating: %w", obj.GetKind(), obj.GetName(), errPending)
	}
	merged := live.DeepCopy()
	for key, v := range obj.Object {
		if key == "status" || key == "apiVersion" || key == "kind" {
			continue
		}
		if key == "metadata" {
			md, _ := v.(map[string]interface{})
			mergeMetadata(merged, md)
			continue
		}
		cur, _ := merged.Object[key].(map[string]interface{})
		nv, isMap := v.(map[string]interface{})
		if isMap && cur != nil {
			merged.Object[key] = deepMerge(cur, nv)
		} else {
			merged.Object[key] = v
		}
	}
	if reflect.DeepEqual(k.stored(merged), k.stored(live)) {
		return live, 0, nil
	}
	if err := k.c.Update(ctx, merged); err != nil {
		if apierrors.IsConflict(err) {
			return nil, 0, fmt.Errorf("conflict, retried next pass: %w", errPending)
		}
		return nil, 0, fmt.Errorf("update %s %s/%s: %w", obj.GetKind(), obj.GetNamespace(), obj.GetName(), err)
	}
	return merged, 1, nil
}

func mergeMetadata(u *unstructured.Unstructured, md map[string]interface{}) {
	if l, ok := md["labels"].(map[string]interface{}); ok {
		lbls := u.GetLabels()
		if lbls == nil {
			lbls = map[string]string{}
		}
		for k2, v2 := range l {
			lbls[k2] = fmt.Sprint(v2)
		}
		u.SetLabels(lbls)
	}
	if a, ok := md["annotations"].(map[string]interface{}); ok {
		ann := u.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		for k2, v2 := range a {
			ann[k2] = fmt.Sprint(v2)
		}
		u.SetAnnotations(ann)
	}
}

// deepMerge merges src over dst: maps merge, everything else is replaced.
func deepMerge(dst, src map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(dst)+len(src))
	for k2, v := range dst {
		out[k2] = v
	}
	for k2, v := range src {
		sm, sok := v.(map[string]interface{})
		dm, dok := out[k2].(map[string]interface{})
		if sok && dok {
			out[k2] = deepMerge(dm, sm)
			continue
		}
		out[k2] = v
	}
	return out
}

// patch merges a patch node's fields into its existing target.
func (k *Kro) patch(ctx context.Context, obj *unstructured.Unstructured) (int, error) {
	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(obj.GroupVersionKind())
	if err := k.c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil {
		if apierrors.IsNotFound(err) {
			// kro waits while a patch target does not exist.
			return 0, nil
		}
		return 0, apiError{err}
	}
	merged := live.DeepCopy()
	for key, v := range obj.Object {
		if key == "apiVersion" || key == "kind" || key == "metadata" {
			continue
		}
		cur, _ := merged.Object[key].(map[string]interface{})
		nv, isMap := v.(map[string]interface{})
		if isMap && cur != nil {
			merged.Object[key] = deepMerge(cur, nv)
		} else {
			merged.Object[key] = v
		}
	}
	if reflect.DeepEqual(merged.Object, live.Object) {
		return 0, nil
	}
	var err error
	if _, ok := obj.Object["status"]; ok {
		err = k.c.Status().Update(ctx, merged)
	} else {
		err = k.c.Update(ctx, merged)
	}
	if err != nil {
		if apierrors.IsConflict(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("patch %s %s: %w", obj.GetKind(), obj.GetName(), err)
	}
	return 1, nil
}

// setStatus writes the Graph conditions kro writes: Accepted, then
// ResourcesConverged and Ready.
func (k *Kro) setStatus(ctx context.Context, raw *unstructured.Unstructured, accepted bool, reason, msg string, ready bool) error {
	cond := func(t string, ok bool, r, m string) map[string]interface{} {
		s := "False"
		if ok {
			s = "True"
		}
		return map[string]interface{}{"type": t, "status": s, "reason": r, "message": m,
			"lastTransitionTime": "2026-01-01T00:00:00Z", "observedGeneration": raw.GetGeneration()}
	}
	var conds []interface{}
	if !accepted {
		conds = []interface{}{cond("Accepted", false, reason, msg), cond("Ready", false, reason, msg)}
	} else {
		convReason := "AllResourcesReady"
		if !ready {
			convReason = "DataPending"
		}
		conds = []interface{}{
			cond("Accepted", true, reason, ""),
			cond("ResourcesConverged", ready, convReason, msg),
			cond("Ready", ready, convReason, msg),
		}
	}
	old, _, _ := unstructured.NestedSlice(raw.Object, "status", "conditions")
	if reflect.DeepEqual(old, conds) {
		return nil
	}
	u := raw.DeepCopy()
	if err := unstructured.SetNestedSlice(u.Object, conds, "status", "conditions"); err != nil {
		return fmt.Errorf("set graph status: %w", err)
	}
	if err := k.c.Status().Update(ctx, u); err != nil && !apierrors.IsConflict(err) && !apierrors.IsNotFound(err) {
		return fmt.Errorf("update graph status: %w", err)
	}
	return nil
}

// stored is u as the store keeps it: for a kind the scheme types, through
// the Go type, which drops the zero values a template renders for
// omitempty fields (message: "", excludeAuthor: false). The API server keeps
// a custom resource's JSON as sent, so a template that renders them is no
// change; without this the executor would rewrite such objects every pass.
func (k *Kro) stored(u *unstructured.Unstructured) map[string]interface{} {
	s := k.c.Scheme()
	typed, err := s.New(u.GroupVersionKind())
	if err != nil {
		return u.Object
	}
	if _, isU := typed.(*unstructured.Unstructured); isU {
		return u.Object
	}
	if err := k8sruntime.DefaultUnstructuredConverter.FromUnstructured(u.Object, typed); err != nil {
		return u.Object
	}
	out, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(typed)
	if err != nil {
		return u.Object
	}
	return out
}
