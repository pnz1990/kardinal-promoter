// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	k8scellib "k8s.io/apiserver/pkg/cel/library"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/library"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// The fuzz and property tests of graph.Builder (#1837): a Pipeline, Bundle
// and PolicyGates decoded from fuzz bytes either fail to build with an error
// that wraps graph.ErrInvalid, or build a Graph that kro v0.10.0-rc.0's
// compiler accepts. kroProblems checks the compiler's rules, read from
// kubernetes-sigs/kro at v0.10.0-rc.0 (cited per rule); assertKroValid and
// graph.CheckSize add the API server's (names, labels) and etcd's (size).

// fuzzSource hands out fuzz bytes; once they run out it returns zeros, so
// every input decodes to some build input.
type fuzzSource struct {
	data []byte
	pos  int
}

func (s *fuzzSource) byte() byte {
	if s.pos >= len(s.data) {
		return 0
	}
	b := s.data[s.pos]
	s.pos++
	return b
}

// intn returns a number in [0, n).
func (s *fuzzSource) intn(n int) int {
	if n <= 1 {
		return 0
	}
	return int(s.byte()) % n
}

func (s *fuzzSource) bool() bool { return s.byte()&1 == 1 }

// validEnvNames are DNS labels that the Pipeline CRD accepts (not reserved).
var validEnvNames = []string{
	"test", "uat", "prod", "dev", "qa", "staging", "canary", "pre-prod", "prod-eu", "prod-us",
	"prodeu", "eu-west-1", "us-east-2", "a", "z9", "team-a-prod", "kardinal-test-app-prod",
	"x" + strings.Repeat("y", 49), "e-" + strings.Repeat("9", 40),
}

// wildEnvNames are names the CRD refuses or that collide once they become
// node IDs: Build must refuse them with ErrInvalid or still emit a valid Graph.
var wildEnvNames = []string{
	"prod_eu", "prodEu", "Prod-eu", "prod.eu", "namespace", "bundle", "time", "spec", "each", "self",
	"item", "status", "graph", "in", "1abc", "-a", "a-", "", strings.Repeat("l", 70), "prod--eu",
	"PolicyGates", "PRStatuses", "PolicyGateData", "x y", "ünï",
}

var fleetTargetNames = []string{"eu", "us", "ap", "t1", "t-2", "region-" + strings.Repeat("r", 30), "a"}

var gateExpressions = []string{
	"true", "!schedule.isWeekend", "bundle.version != \"\"", "upstream.uat.soakMinutes >= 30",
	"metrics[\"error-rate\"].result == \"Pass\"", "!changewindow.isBlocked(\"freeze\")",
	"approvals.count >= 1", "json.marshal(bundle) != \"\"",
}

// fuzzBuildInput decodes data into a build input and a builder. In the valid
// domain (validDomain true) every name is a DNS label the CRD accepts, no
// two environments (fleet targets included) share a name, dependsOn only
// names earlier environments, and the Bundle skips nothing: such an input
// must build.
func fuzzBuildInput(data []byte) (graph.BuildInput, *graph.Builder, bool) {
	s := &fuzzSource{data: data}
	validDomain := s.byte()&3 != 0
	nEnv := 1 + s.intn(12)
	if s.byte()&0xF0 == 0xF0 {
		nEnv = 1 + s.intn(40)
	}
	const ns = "team-a"
	names := map[string]bool{}
	var envs []kardinalv1alpha1.EnvironmentSpec
	maxWave := 0
	for i := 0; i < nEnv; i++ {
		var name string
		switch {
		case validDomain:
			name = validEnvNames[s.intn(len(validEnvNames))]
			if names[name] {
				name = fmt.Sprintf("env-%d", i)
			}
		case s.bool():
			name = wildEnvNames[s.intn(len(wildEnvNames))]
		default:
			name = validEnvNames[s.intn(len(validEnvNames))]
		}
		env := kardinalv1alpha1.EnvironmentSpec{Name: name}
		switch s.intn(3) {
		case 1:
			env.Approval = "auto"
		case 2:
			env.Approval = "pr-review"
		}
		switch s.intn(4) {
		case 1: // dependsOn earlier environments (or any, outside the domain)
			for j := 0; j < 1+s.intn(3); j++ {
				limit := i
				if !validDomain {
					limit = nEnv + 1
				}
				if limit == 0 {
					break
				}
				k := s.intn(limit)
				if k < len(envs) {
					env.DependsOn = append(env.DependsOn, envs[k].Name)
				} else if !validDomain {
					env.DependsOn = append(env.DependsOn, "missing-"+fmt.Sprint(k))
				}
			}
		case 2:
			env.Wave = 1 + s.intn(4)
		}
		if s.intn(5) == 0 {
			fleet := &kardinalv1alpha1.FleetSpec{MaxConcurrent: s.intn(4)}
			if s.bool() {
				// A count of 1 to 3, or a percentage (#1835).
				mu := intstr.FromInt32(int32(1 + s.intn(3)))
				if s.bool() {
					mu = intstr.FromString([]string{"10%", "50%", "100%"}[s.intn(3)])
				}
				fleet.MaxUnavailable = &mu
			}
			seen := map[string]bool{}
			for j := 0; j < 1+s.intn(5); j++ {
				t := fleetTargetNames[s.intn(len(fleetTargetNames))]
				if !seen[t] {
					seen[t] = true
					fleet.Targets = append(fleet.Targets, kardinalv1alpha1.FleetTarget{Name: t})
				}
			}
			env.Fleet = fleet
		}
		if validDomain {
			if names[name] || (env.Fleet != nil && fleetCollides(names, name, env.Fleet)) {
				env.Fleet = nil
			}
			if names[name] {
				continue
			}
			if env.Wave > 0 && len(env.DependsOn) > 0 {
				env.DependsOn = nil
			}
			// Waves in list order: a lower wave listed after a higher one
			// is a cycle the Pipeline reconciler reports (DetectCycle).
			if env.Wave > 0 && env.Wave < maxWave {
				env.Wave = maxWave
			}
			if env.Wave > maxWave {
				maxWave = env.Wave
			}
		}
		names[name] = true
		if env.Fleet != nil {
			for _, t := range env.Fleet.Targets {
				names[graph.FleetTargetName(name, t.Name)] = true
			}
		}
		envs = append(envs, env)
	}
	if validDomain {
		envs = dropFleetCollisions(envs)
	}
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git:          kardinalv1alpha1.PipelineGit{URL: "https://github.com/org/gitops", Branch: "main"},
			Environments: envs,
		},
	}
	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: ns, UID: "uid-app-v1"},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app",
			Images: []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "1.2.3"}}},
	}
	envNames := graph.EnvironmentNames(pipeline)
	if len(envNames) > 0 {
		switch s.intn(4) {
		case 1:
			bundle.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: envNames[s.intn(len(envNames))]}
		case 2:
			if !validDomain {
				bundle.Spec.Intent = &kardinalv1alpha1.BundleIntent{SkipEnvironments: []string{envNames[s.intn(len(envNames))]}}
			}
		case 3:
			if !validDomain {
				bundle.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: "nowhere"}
			}
		}
	}
	var gates []kardinalv1alpha1.PolicyGate
	for i := 0; i < s.intn(5); i++ {
		gateNS, labels := ns, map[string]string{}
		if s.bool() {
			gateNS, labels["kardinal.io/scope"] = graph.DefaultPolicyNamespace, "org"
		}
		// One environment: a label value cannot hold a comma
		// (docs/concepts.md, PolicyGate applies-to).
		if len(envs) > 0 {
			labels["kardinal.io/applies-to"] = envs[s.intn(len(envs))].Name
		}
		gates = append(gates, kardinalv1alpha1.PolicyGate{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("gate-%d", i), Namespace: gateNS, Labels: labels},
			Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: gateExpressions[s.intn(len(gateExpressions))]},
		})
	}
	b := graph.NewBuilder()
	if s.bool() {
		b.CompactAbove = 0 // every Graph compact
	}
	return graph.BuildInput{Pipeline: pipeline, Bundle: bundle, PolicyGates: gates}, b, validDomain
}

// fleetCollides reports whether a fleet target of env would take a name
// already used.
func fleetCollides(names map[string]bool, env string, f *kardinalv1alpha1.FleetSpec) bool {
	for _, t := range f.Targets {
		n := graph.FleetTargetName(env, t.Name)
		if names[n] || len(n) > validation.DNS1123LabelMaxLength {
			return true
		}
	}
	return false
}

// dropFleetCollisions removes the fleets whose target names an environment
// declared later takes, and dependsOn entries that name a fleet target.
func dropFleetCollisions(envs []kardinalv1alpha1.EnvironmentSpec) []kardinalv1alpha1.EnvironmentSpec {
	plain := map[string]bool{}
	for _, e := range envs {
		plain[e.Name] = true
	}
	for i := range envs {
		if envs[i].Fleet == nil {
			continue
		}
		for _, t := range envs[i].Fleet.Targets {
			if plain[graph.FleetTargetName(envs[i].Name, t.Name)] {
				envs[i].Fleet = nil
				break
			}
		}
	}
	return envs
}

// kroEnv is a CEL environment with the declarations kro v0.10.0-rc.0 gives
// every Graph expression (pkg/cel/environment.go:128-157, coreDeclarations),
// minus library.Hash, Omit and Runtime, which kardinal's Graphs do not use.
func kroEnv(t *testing.T) *cel.Env {
	t.Helper()
	kroEnvOnce.Do(func() { kroEnvCached, kroEnvErr = newKroEnv() })
	require.NoError(t, kroEnvErr)
	return kroEnvCached
}

var (
	kroEnvOnce   sync.Once
	kroEnvCached *cel.Env
	kroEnvErr    error
)

func newKroEnv() (*cel.Env, error) {
	return cel.NewEnv(
		ext.TwoVarComprehensions(), ext.Lists(), ext.Strings(), ext.Bindings(), cel.OptionalTypes(), ext.Encoders(),
		k8scellib.Lists(), k8scellib.URLs(), k8scellib.Regex(), k8scellib.Quantity(), k8scellib.IP(), k8scellib.CIDR(),
		k8scellib.SemverLib(),
		library.Random(), library.Maps(), library.JSON(), library.Lists(),
	)
}

// kroCELReserved and kroReserved are kro's celReservedSymbols and
// reservedNodeIDs (pkg/graphengine/compiler/validation.go:32-51). Unlike
// kroReservedNodeIDs they hold no names kardinal reserves ahead of kro.
var (
	kroCELReserved = []string{"true", "false", "null", "in", "as", "break", "const", "continue", "else",
		"for", "function", "if", "import", "let", "loop", "package", "namespace", "return", "var", "void", "while"}
	kroReserved = append([]string{"apiVersion", "kind", "metadata", "namespace", "spec", "status",
		"graph", "graphengine", "kro", "each", "item", "items", "object", "self", "this", "context"}, kroCELReserved...)
)

func kroReservedID(id string) bool {
	for _, r := range kroReserved {
		if r == id {
			return true
		}
	}
	return false
}

// freeCache and compileCache memoize parsing and type-checking: the same
// expressions recur in every Graph, and parsing dominates the property's cost.
var (
	freeCache    sync.Map // expr -> []string
	compileCache sync.Map // expr + free variables and their types -> error
)

// cachedFree is freeIdentifiers in kroEnv, memoized.
func cachedFree(t *testing.T, expr string) []string {
	t.Helper()
	if v, ok := freeCache.Load(expr); ok {
		return v.([]string)
	}
	free := freeIdentifiers(t, kroEnv(t), expr)
	freeCache.Store(expr, free)
	return free
}

// kroCompile type-checks expr in kroEnv with vars declared: the way kro
// compiles a Graph expression with its node IDs in scope. An identifier vars
// does not declare fails, as in kro (compiler.go:795-870, unknown identifier).
func kroCompile(t *testing.T, expr string, vars map[string]*cel.Type) error {
	t.Helper()
	free := cachedFree(t, expr)
	var key strings.Builder
	key.WriteString(expr)
	var opts []cel.EnvOption
	for _, id := range free {
		typ, ok := vars[id]
		key.WriteString("\x00" + id + ":")
		if ok {
			key.WriteString(typ.String())
			opts = append(opts, cel.Variable(id, typ))
		}
	}
	if v, ok := compileCache.Load(key.String()); ok {
		if v == nil {
			return nil
		}
		return v.(error)
	}
	env, err := kroEnv(t).Extend(opts...)
	require.NoError(t, err)
	var cerr error
	if _, iss := env.Compile(expr); iss.Err() != nil {
		cerr = iss.Err()
	}
	if cerr == nil {
		compileCache.Store(key.String(), nil)
	} else {
		compileCache.Store(key.String(), cerr)
	}
	return cerr
}

// kroNodeIDRegex is kro's node ID grammar (validation.go:28).
var kroNodeIDRegex = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// kroProblems returns the rules of kro v0.10.0-rc.0's Graph compiler that g
// breaks, one message each; none means kro compiles the Graph's structure.
func kroProblems(t *testing.T, g *graph.Graph) []string {
	t.Helper()
	var bad []string
	add := func(format string, args ...interface{}) { bad = append(bad, fmt.Sprintf(format, args...)) }
	nodes := g.Spec.Nodes
	// api/v1alpha1/graph_types.go:40: at least one node.
	if len(nodes) == 0 {
		return []string{"graph has no nodes"}
	}
	ids := map[string]graph.GraphNode{}
	for i, n := range nodes {
		// validation.go:139-150 validateNodeID; :76-78 unique IDs.
		switch {
		case !kroNodeIDRegex.MatchString(n.ID):
			add("node[%d] id %q does not match %s", i, n.ID, kroNodeIDRegex)
		case kroReservedID(n.ID):
			add("node id %q is reserved", n.ID)
		}
		if _, dup := ids[n.ID]; dup {
			add("duplicate node id %q", n.ID)
		}
		ids[n.ID] = n
		// validation.go:154-176: exactly one of template/ref/def/graph/patch.
		set := 0
		for _, ok := range []bool{n.Template != nil, n.Ref != nil, n.Def != nil, n.Patch != nil} {
			if ok {
				set++
			}
		}
		if set != 1 {
			add("node %q sets %d of template/ref/def/patch", n.ID, set)
		}
		// validation.go:180-210 validateForEachShape.
		iters := map[string]bool{}
		for d, dim := range n.ForEach {
			if len(dim) != 1 {
				add("node %q forEach[%d] has %d entries", n.ID, d, len(dim))
			}
			for name := range dim {
				switch {
				case !kroNodeIDRegex.MatchString(name):
					add("node %q forEach iterator %q does not match the id grammar", n.ID, name)
				case name == n.ID:
					add("node %q forEach iterator collides with its id", n.ID)
				case name == "each":
					add("node %q forEach iterator %q is reserved for readyWhen", n.ID, name)
				case kroReservedID(name):
					add("node %q forEach iterator %q is reserved", n.ID, name)
				case iters[name]:
					add("node %q duplicate forEach iterator %q", n.ID, name)
				}
				iters[name] = true
			}
		}
		// validation.go:102-135 validateKindCompatibility.
		if n.Ref != nil && len(n.ForEach) > 0 {
			add("node %q: forEach on a ref node", n.ID)
		}
		if n.Patch != nil && len(n.ReadyWhen) > 0 {
			add("node %q: readyWhen on a patch node", n.ID)
		}
	}

	// Declarations: each node is dyn, a collection (forEach template, or a
	// selector ref) list(dyn) (compiler.go:333, :385). Patch nodes publish
	// nothing (compiler.go:818-823), so they are not declared and a
	// reference to one fails to compile.
	scope := map[string]*cel.Type{}
	for _, n := range nodes {
		if n.Patch != nil {
			continue
		}
		typ := cel.DynType
		md, _ := n.Ref["metadata"].(map[string]interface{})
		if (n.Template != nil && len(n.ForEach) > 0) || (md != nil && md["selector"] != nil) {
			typ = cel.ListType(cel.DynType)
		}
		scope[n.ID] = typ
	}

	deps := map[string]map[string]bool{}
	for _, n := range nodes {
		deps[n.ID] = map[string]bool{}
		var iterNames []string
		for _, dim := range n.ForEach {
			for name := range dim {
				iterNames = append(iterNames, name)
			}
		}
		withVars := func(names ...string) map[string]*cel.Type {
			out := make(map[string]*cel.Type, len(scope)+len(names))
			for k, v := range scope {
				out[k] = v
			}
			for _, v := range names {
				out[v] = cel.DynType
			}
			return out
		}
		iterScope := withVars(iterNames...)
		nodeScope := withVars()
		// check type-checks every expression of s with the variables of
		// vars and returns the node IDs it reads.
		check := func(where string, s string, vars map[string]*cel.Type) []string {
			var read []string
			for _, expr := range kroExpressions(t, s) {
				if err := kroCompile(t, expr, vars); err != nil {
					add("node %q %s: %q does not compile: %v", n.ID, where, expr, err)
					continue
				}
				for _, id := range cachedFree(t, expr) {
					if _, isNode := ids[id]; isNode {
						read = append(read, id)
					}
				}
			}
			return read
		}
		identityIters := map[string]bool{}
		walkVars := func(where string, v interface{}, identity bool) {
			eachStringPath(v, "", func(path, s string) {
				for _, id := range check(where, s, iterScope) {
					deps[n.ID][id] = true
				}
				if identity && (path == "metadata.name" || path == "metadata.namespace") {
					for _, expr := range kroExpressions(t, s) {
						for _, id := range cachedFree(t, expr) {
							identityIters[id] = true
						}
					}
				}
			})
		}
		walkVars("template", n.Template, true)
		walkVars("def", n.Def, false)
		walkVars("ref", n.Ref, false)
		walkVars("patch", n.Patch, true)
		// compiler.go:686-695: every iterator appears in metadata.name (or
		// metadata.namespace) of a template or patch node.
		if n.Template != nil || n.Patch != nil {
			for _, it := range iterNames {
				if !identityIters[it] {
					add("node %q: forEach iterator %q is not in metadata.name", n.ID, it)
				}
			}
		}
		// compiler.go:702-718: a forEach dimension reads nodes, not iterators.
		for _, dim := range n.ForEach {
			for _, s := range dim {
				for _, expr := range kroExpressions(t, s) {
					for _, id := range cachedFree(t, expr) {
						for _, it := range iterNames {
							if id == it {
								add("node %q: forEach %q reads iterator %q", n.ID, expr, it)
							}
						}
					}
				}
				for _, id := range check("forEach", s, nodeScope) {
					deps[n.ID][id] = true
				}
			}
		}
		// compiler.go:728-746: includeWhen does not read its own node.
		for _, s := range n.IncludeWhen {
			for _, id := range check("includeWhen", s, nodeScope) {
				if id == n.ID {
					add("node %q: includeWhen reads its own id", n.ID)
				}
				deps[n.ID][id] = true
			}
		}
		// compiler.go:752-770: readyWhen reads only the node itself (or each).
		for _, s := range n.ReadyWhen {
			for _, id := range check("readyWhen", s, withVars("each")) {
				if id != n.ID {
					add("node %q: readyWhen reads node %q", n.ID, id)
				}
			}
		}
	}

	// compiler.go:351-358: the dependency DAG is acyclic and has a root
	// (validation.go:223-235). A node reading itself in a template is a
	// cycle too.
	if cyc := depCycle(deps); cyc != nil {
		add("dependency cycle %s", strings.Join(cyc, " -> "))
	}
	root := false
	for _, d := range deps {
		root = root || len(d) == 0
	}
	if !root {
		add("no node without dependencies")
	}
	sort.Strings(bad)
	return bad
}

// eachStringPath is eachString with the dotted path of each string.
func eachStringPath(v interface{}, path string, f func(path, s string)) {
	switch x := v.(type) {
	case string:
		f(path, x)
	case map[string]interface{}:
		for k, e := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			eachStringPath(e, p, f)
		}
	case []interface{}:
		for _, e := range x {
			eachStringPath(e, path+"[]", f)
		}
	}
}

// depCycle returns a cycle of deps, or nil.
func depCycle(deps map[string]map[string]bool) []string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var found []string
	var visit func(n string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		keys := make([]string, 0, len(deps[n]))
		for d := range deps[n] {
			keys = append(keys, d)
		}
		sort.Strings(keys)
		for _, d := range keys {
			switch color[d] {
			case grey:
				for i, s := range stack {
					if s == d {
						found = append(append([]string{}, stack[i:]...), d)
					}
				}
				return true
			case white:
				if visit(d) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	ids := make([]string, 0, len(deps))
	for id := range deps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if color[id] == white && visit(id) {
			return found
		}
	}
	return nil
}

// checkBuildProperty is the property of FuzzBuild and TestBuild_Property.
func checkBuildProperty(t *testing.T, data []byte) {
	t.Helper()
	in, b, validDomain := fuzzBuildInput(data)
	var (
		res *graph.BuildResult
		err error
	)
	require.NotPanics(t, func() { res, err = b.Build(in) })
	if err != nil {
		require.ErrorIs(t, err, graph.ErrInvalid, "a Build error is permanent (wraps ErrInvalid)")
		require.False(t, validDomain, "a Pipeline the CRD accepts, with no skips and acyclic dependsOn, must build: %v\nenvironments: %s",
			err, describeEnvs(in.Pipeline))
		return
	}
	require.NotNil(t, res.Graph)
	require.Empty(t, kroProblems(t, res.Graph), "the Graph breaks kro's compiler rules; environments: %s", describeEnvs(in.Pipeline))
	assertKroValid(t, res.Graph)
	require.NoError(t, graph.CheckSize(res.Graph))
	// Building again gives the same Graph: the translator re-builds on
	// every Pipeline or gate change and must not churn the spec.
	again, err := b.Build(in)
	require.NoError(t, err)
	require.Equal(t, res.Graph.Spec, again.Graph.Spec, "Build is deterministic")
}

func describeEnvs(p *kardinalv1alpha1.Pipeline) string {
	var parts []string
	for _, e := range p.Spec.Environments {
		s := e.Name
		if len(e.DependsOn) > 0 {
			s += "<-" + strings.Join(e.DependsOn, "+")
		}
		if e.Wave > 0 {
			s += fmt.Sprintf("@w%d", e.Wave)
		}
		if e.Fleet != nil {
			var ts []string
			for _, tg := range e.Fleet.Targets {
				ts = append(ts, tg.Name)
			}
			s += "{" + strings.Join(ts, ",") + "}"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// FuzzBuild fuzzes graph.Builder.Build with checkBuildProperty. Run it with
// go test ./pkg/graph -run '^$' -fuzz '^FuzzBuild$' -fuzztime 2m.
func FuzzBuild(f *testing.F) {
	for _, seed := range buildSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkBuildProperty(t, data) })
}

// buildSeeds are hand-picked inputs: a linear chain, a fan-out, waves,
// fleets, gates, a compact Graph, and inputs outside the CRD's domain.
func buildSeeds() [][]byte {
	return [][]byte{
		{1, 0, 0, 0, 0, 1, 0, 0, 2, 0, 0},
		{1, 0xF0, 39, 0, 1, 1, 1},
		{2, 3, 0, 2, 0, 1, 2, 2, 2, 1},
		{3, 5, 4, 1, 0, 0, 0, 1, 1, 3, 1, 2, 4, 3, 0},
		{0, 6, 1, 3, 1, 0, 2, 1, 5, 1, 9, 1, 1},
		{0, 2, 1, 0, 1, 1, 1, 2, 3},
		{1, 4, 2, 2, 1, 0, 0, 1, 2, 1, 0, 3, 1, 4, 2, 0, 1, 3, 1, 1},
	}
}

// TestBuild_Property runs checkBuildProperty over 300 seeded random inputs
// and the fuzz seeds, so the property holds in every go test run, not only
// under -fuzz. Covers GRAPH-FUZZ-01, GRAPH-FUZZ-02.
func TestBuild_Property(t *testing.T) {
	inputs := buildSeeds()
	r := rand.New(rand.NewSource(1837))
	for i := 0; i < 300; i++ {
		b := make([]byte, 8+r.Intn(120))
		r.Read(b)
		inputs = append(inputs, b)
	}
	for i, in := range inputs {
		in := in
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			checkBuildProperty(t, in)
		})
	}
}

// TestKroProblems: each rule kroProblems checks refuses a Graph that breaks
// it, so TestBuild_Property cannot pass vacuously.
func TestKroProblems(t *testing.T) {
	bundle := graph.GraphNode{ID: "bundle", Ref: map[string]interface{}{"apiVersion": "v1", "kind": "Bundle",
		"metadata": map[string]interface{}{"name": "b"}}}
	tmpl := func(id string, spec map[string]interface{}) graph.GraphNode {
		return graph.GraphNode{ID: id, Template: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]interface{}{"name": id}, "spec": spec}}
	}
	for name, tc := range map[string]struct {
		nodes []graph.GraphNode
		want  string
	}{
		"valid":           {nodes: []graph.GraphNode{bundle, tmpl("a", map[string]interface{}{"x": "${bundle.metadata.name}"})}},
		"bad id":          {nodes: []graph.GraphNode{bundle, tmpl("a-b", nil)}, want: "does not match"},
		"reserved":        {nodes: []graph.GraphNode{bundle, tmpl("namespace", nil)}, want: "is reserved"},
		"duplicate":       {nodes: []graph.GraphNode{bundle, bundle}, want: "duplicate node id"},
		"two kinds":       {nodes: []graph.GraphNode{{ID: "a", Def: map[string]interface{}{"x": 1}, Ref: bundle.Ref}}, want: "sets 2 of"},
		"ref forEach":     {nodes: []graph.GraphNode{{ID: "a", Ref: bundle.Ref, ForEach: []map[string]string{{"i": "${[1]}"}}}}, want: "forEach on a ref node"},
		"patch ready":     {nodes: []graph.GraphNode{bundle, {ID: "p", Patch: map[string]interface{}{"metadata": map[string]interface{}{"name": "b"}}, ReadyWhen: []string{"${true}"}}}, want: "readyWhen on a patch"},
		"each iterator":   {nodes: []graph.GraphNode{{ID: "a", Template: map[string]interface{}{"metadata": map[string]interface{}{"name": "${each}"}}, ForEach: []map[string]string{{"each": "${[1]}"}}}}, want: "reserved for readyWhen"},
		"identity":        {nodes: []graph.GraphNode{{ID: "a", Template: map[string]interface{}{"metadata": map[string]interface{}{"name": "x"}}, ForEach: []map[string]string{{"i": "${[1]}"}}}}, want: "is not in metadata.name"},
		"unknown ident":   {nodes: []graph.GraphNode{tmpl("a", map[string]interface{}{"x": "${nope.y}"})}, want: "does not compile"},
		"patch read":      {nodes: []graph.GraphNode{bundle, {ID: "p", Patch: map[string]interface{}{"metadata": map[string]interface{}{"name": "b"}}}, tmpl("a", map[string]interface{}{"x": "${p.x}"})}, want: "does not compile"},
		"cycle":           {nodes: []graph.GraphNode{tmpl("a", map[string]interface{}{"x": "${b.x}"}), tmpl("b", map[string]interface{}{"x": "${a.x}"})}, want: "dependency cycle"},
		"ready reads":     {nodes: []graph.GraphNode{bundle, {ID: "a", Def: map[string]interface{}{"x": 1}, ReadyWhen: []string{"${bundle.x == 1}"}}}, want: "readyWhen reads node"},
		"include self":    {nodes: []graph.GraphNode{bundle, {ID: "a", Def: map[string]interface{}{"x": 1}, IncludeWhen: []string{"${a.x == 1}"}}}, want: "includeWhen reads its own id"},
		"forEach reads i": {nodes: []graph.GraphNode{{ID: "a", Template: map[string]interface{}{"metadata": map[string]interface{}{"name": "${i}"}}, ForEach: []map[string]string{{"i": "${[i]}"}}}}, want: "reads iterator"},
	} {
		t.Run(name, func(t *testing.T) {
			got := kroProblems(t, &graph.Graph{Spec: graph.GraphSpec{Nodes: tc.nodes}})
			if tc.want == "" {
				require.Empty(t, got)
				return
			}
			require.NotEmpty(t, got)
			require.Contains(t, strings.Join(got, "\n"), tc.want)
		})
	}
}
