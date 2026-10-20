// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// renderCorpusFile pins, for graph.RenderVersion, the hash of each corpus
// Graph's spec (TestRenderCorpus).
const renderCorpusFile = "testdata/render-corpus.json"

// updateRenderCorpusEnv set to 1 records the corpus hashes, for a new
// graph.RenderVersion or new entries.
const updateRenderCorpusEnv = "KARDINAL_UPDATE_RENDER_CORPUS"

// renderCorpusPin is renderCorpusFile's content.
type renderCorpusPin struct {
	RenderVersion int               `json:"renderVersion"`
	Graphs        map[string]string `json:"graphs"`
}

// renderCorpus are builder inputs as installed Pipelines, Bundles and gates
// have them: every gate collection (plain, skip permission, approval with
// and without a policy, approval readers, verify with and without a policy,
// and the mixed collections of #2271), agents environments, pr-review, the
// compact shape, a fleet and org-namespace gates; and, in both shapes,
// hooks, analyses, renders, agent verifier runs, per-promotion
// ChangeRequests and MetricChecks, image verification and an SCM provider
// (QA-2 on #2353). TestRenderCorpus_EveryNodeKind holds it to every node
// the builder can emit.
func renderCorpus() map[string]graph.BuildInput {
	gates := func(g ...kardinalv1alpha1.PolicyGate) []kardinalv1alpha1.PolicyGate { return g }
	approvalPolicy := func(g kardinalv1alpha1.PolicyGate, required int) kardinalv1alpha1.PolicyGate {
		g.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: required, AllowedGroups: []string{"release-managers"}, ExcludeAuthor: true}
		return g
	}
	plain := makePolicyGate("weekday", "platform-policies", "test", "!schedule.isWeekend")
	approval := approvalPolicy(makePolicyGate("two-approvers", "platform-policies", "prod", "true"), 2)
	readers := makePolicyGate("one-ok", "platform-policies", "test", "approvals.count >= 1")
	verify := verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "explore"})
	verifyApproval := approvalPolicy(verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "smoke"}), 1)
	verifyApproval.Name = "smoke-and-approve"

	agents := makeLinearPipeline("app", "test", "uat", "prod")
	agents.Spec.Environments[1].Agents = kardinalv1alpha1.AutonomyApprove
	agents.Spec.Environments[2].Agents = kardinalv1alpha1.AutonomyDeny
	prReview := makeLinearPipeline("app", "test", "prod")
	prReview.Spec.Environments[1].Approval = "pr-review"

	linear := func(envs ...string) *kardinalv1alpha1.Pipeline { return makeLinearPipeline("app", envs...) }
	compact := func(p *kardinalv1alpha1.Pipeline) *kardinalv1alpha1.Pipeline {
		if p.Annotations == nil {
			p.Annotations = map[string]string{}
		}
		p.Annotations[graph.AnnotationGraphShape] = graph.GraphShapeCompact
		return p
	}
	bundle := makeBundle("app-v1", "app")
	uidBundle := makeBundle("app-v1", "app")
	uidBundle.UID = "uid-1"
	analysisBundle := makeBundle("app-v1", "app")
	analysisBundle.Spec.Images = []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "1.4.0"}}
	analyses := analysisInput(smokeTemplate("AnalysisTemplate", "smoke", `sum(rate(ok{service="{{args.service}}"}[1m]))`),
		smokeTemplate("ClusterAnalysisTemplate", "slo", "1"))
	analysisRefs := []kardinalv1alpha1.AnalysisTemplateRef{{Name: "smoke"}, {Name: "slo", Kind: "ClusterAnalysisTemplate"}}
	hooked := func() *kardinalv1alpha1.Pipeline {
		return pipelineOf("app", kardinalv1alpha1.EnvironmentSpec{Name: "test"},
			kardinalv1alpha1.EnvironmentSpec{Name: "prod", DependsOn: []string{"test"},
				Hooks: []kardinalv1alpha1.HookSpec{hook("migrate", "pre", hookJob), hook("seed", "pre", hookJob), hook("smoke", "post", hookJob)}})
	}
	agentRuns := verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat", Required: 2,
		OnInconclusive: "retry", Retries: 1, Run: &kardinalv1alpha1.GateVerifyRun{Template: "qa-explorer", Count: 3}})
	changeGate := makePolicyGate("cab", "default", "prod", `changeRequests["prod-change"].state == "approved"`)
	changeRequests := []kardinalv1alpha1.ChangeRequest{crTemplate("prod-change", "prod")}
	metricGate := makePolicyGate("errors", "default", "prod", `metrics["error-rate"].result == "Pass"`)
	metricChecks := []kardinalv1alpha1.MetricCheck{metricTemplate("error-rate", `rate(errors{v="{{ bundle.version }}"}[5m])`)}
	ivBundled := ivBundle(kardinalv1alpha1.ImageRef{Repository: "ghcr.io/org/app", Digest: ivDigest})
	scm := &kardinalv1alpha1.ScmProviderIdentity{Kind: kardinalv1alpha1.KindScmProvider, Name: "gitlab", UID: "u-1"}
	return map[string]graph.BuildInput{
		"hooks":                        {Pipeline: hooked(), Bundle: uidBundle},
		"hooks, compact":               {Pipeline: compact(hooked()), Bundle: uidBundle},
		"analyses":                     {Pipeline: analysisPipeline(analysisRefs...), Bundle: analysisBundle, Analyses: analyses},
		"analyses, compact":            {Pipeline: compact(analysisPipeline(analysisRefs...)), Bundle: analysisBundle, Analyses: analyses},
		"render":                       {Pipeline: renderPipeline(), Bundle: uidBundle},
		"render, compact":              {Pipeline: compact(renderPipeline()), Bundle: uidBundle},
		"agent verifier runs":          {Pipeline: linear("test", "uat", "prod"), Bundle: bundle, PolicyGates: gates(agentRuns)},
		"agent verifier runs, compact": {Pipeline: compact(linear("test", "uat", "prod")), Bundle: bundle, PolicyGates: gates(agentRuns)},
		"change requests": {Pipeline: linear("test", "uat", "prod"), Bundle: uidBundle, PolicyGates: gates(changeGate),
			ChangeRequests: changeRequests},
		"change requests, compact": {Pipeline: compact(linear("test", "uat", "prod")), Bundle: uidBundle, PolicyGates: gates(changeGate),
			ChangeRequests: changeRequests},
		"metric checks": {Pipeline: linear("test", "uat", "prod"), Bundle: uidBundle, PolicyGates: gates(metricGate), MetricChecks: metricChecks},
		"metric checks, compact": {Pipeline: compact(linear("test", "uat", "prod")), Bundle: uidBundle, PolicyGates: gates(metricGate),
			MetricChecks: metricChecks},
		"image verification":          {Pipeline: ivPipeline("ghcr.io/org/*"), Bundle: ivBundled},
		"image verification, compact": {Pipeline: compact(ivPipeline("ghcr.io/org/*")), Bundle: ivBundled},
		"scm provider":                {Pipeline: linear("test", "prod"), Bundle: bundle, ScmProvider: scm},
		"scm provider, compact":       {Pipeline: compact(linear("test", "prod")), Bundle: bundle, ScmProvider: scm},
		"linear, no gates":            {Pipeline: linear("test", "uat", "prod"), Bundle: bundle},
		"plain gate":                  {Pipeline: linear("test", "prod"), Bundle: bundle, PolicyGates: gates(plain)},
		"approval gates":              {Pipeline: linear("test", "prod"), Bundle: bundle, PolicyGates: gates(approval, readers, plain)},
		"approval readers":            {Pipeline: linear("test", "prod"), Bundle: bundle, PolicyGates: gates(readers, plain)},
		"verify gate":                 {Pipeline: linear("uat", "prod"), Bundle: bundle, PolicyGates: gates(verify)},
		"verify with policy":          {Pipeline: linear("uat", "prod"), Bundle: bundle, PolicyGates: gates(verify, verifyApproval)},
		// #2271: a verify gate without a policy next to an approval gate with
		// one.
		"verify beside an approval policy": {Pipeline: linear("uat", "prod"), Bundle: bundle, PolicyGates: gates(verify, approval)},
		"skip permission": {Pipeline: linear("test", "staging", "prod"), Bundle: skipBundle("staging"),
			PolicyGates: gates(orgGate("staging-soak", "staging"),
				skipPermission("allow-staging-skip-for-hotfix", "platform-policies", "staging", `bundle.version.startsWith("hotfix-")`, true))},
		"org-namespace gate": {Pipeline: linear("test", "prod"), Bundle: bundle, PolicyGates: gates(orgGate("prod-freeze", "prod")),
			PolicyNamespaces: []string{"platform-policies"}},
		"agents": {Pipeline: agents, Bundle: agentBundle()},
		// A gated pr-review environment mirrors its gates onto the PR.
		"pr-review": {Pipeline: prReview, Bundle: bundle, PolicyGates: gates(makePolicyGate("prod-weekday", "platform-policies", "prod", "!schedule.isWeekend"))},
		"compact": {Pipeline: compactPipeline(kardinalv1alpha1.EnvironmentSpec{Name: "test"}, kardinalv1alpha1.EnvironmentSpec{Name: "uat"},
			kardinalv1alpha1.EnvironmentSpec{Name: "prod"}), Bundle: bundle, PolicyGates: gates(approval, verify, plain)},
		"fleet": {Pipeline: fleetPipeline(2), Bundle: bundle, PolicyGates: gates(makePolicyGate("change-freeze", "platform-policies", "prod", "true"))},
	}
}

// renderHash is the hash of the canonical JSON of g's spec: its nodes, their
// templates, includeWhen and readyWhen, and its ServiceAccount. Metadata is
// left out: the render-version label changes with every bump, and the
// Graph's name and owner are the Bundle's.
func renderHash(t *testing.T, g *graph.Graph) string {
	t.Helper()
	b, err := json.Marshal(g.Spec)
	require.NoError(t, err)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestRenderCorpus (#2296) pins how the builder renders the corpus, per
// graph.RenderVersion. The Bundle reconciler re-renders a Promoting Bundle's
// Graph whose render version differs (#2272): that is how a version stuck
// under a rendering bug recovers on upgrade, and it only happens if every
// change that renders an existing Graph differently bumps RenderVersion. A
// corpus Graph that renders differently at the same RenderVersion fails
// here. KARDINAL_UPDATE_RENDER_CORPUS=1 records the hashes for a new
// RenderVersion or for added entries, and refuses to change, rename or drop
// an existing entry without a bump.
func TestRenderCorpus(t *testing.T) {
	got := map[string]string{}
	for name, in := range renderCorpus() {
		res, err := graph.NewBuilder().Build(in)
		require.NoError(t, err, name)
		assertKroValid(t, res.Graph)
		h := renderHash(t, res.Graph)
		again, err := graph.NewBuilder().Build(in)
		require.NoError(t, err, name)
		require.Equal(t, h, renderHash(t, again.Graph), "%s: the builder renders the same input the same way", name)
		got[name] = h
	}

	var pin renderCorpusPin
	if b, err := os.ReadFile(renderCorpusFile); err == nil {
		require.NoError(t, json.Unmarshal(b, &pin), renderCorpusFile)
	} else if !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	var changed, added, removed []string
	for name, h := range got {
		switch old, ok := pin.Graphs[name]; {
		case !ok:
			added = append(added, name)
		case old != h:
			changed = append(changed, name)
		}
	}
	for name := range pin.Graphs {
		if _, ok := got[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(changed)
	sort.Strings(added)
	sort.Strings(removed)
	sameVersion := pin.RenderVersion == graph.RenderVersion
	update := "run " + updateRenderCorpusEnv + "=1 go test ./pkg/graph -run '^TestRenderCorpus$'"
	bump := fmt.Sprintf("Do these differences reach Graphs that exist before this change, the Graphs of Bundles "+
		"already promoting? If so, bump graph.RenderVersion (pkg/graph/gates.go, now %d) and say so in the changelog "+
		"fragment, so those Bundles re-render once on upgrade (#2272), then %s. If no existing Graph renders "+
		"differently (only an input that could not exist before), change these entries in %s by hand and say why in the PR",
		graph.RenderVersion, update, renderCorpusFile)

	if os.Getenv(updateRenderCorpusEnv) == "1" {
		require.False(t, sameVersion && len(changed) > 0,
			"refusing to record new hashes for %s at the same RenderVersion %d. %s", strings.Join(changed, ", "), graph.RenderVersion, bump)
		// A rename or a removal would re-record a Graph without a bump (QA-2
		// on #2353): at the same RenderVersion entries are only added.
		require.False(t, sameVersion && len(removed) > 0,
			"refusing to drop corpus entries %s at the same RenderVersion %d: renaming or removing an entry would record its "+
				"Graph again without a bump. Keep its name; to retire it, bump graph.RenderVersion, or edit %s by hand and say why in the PR",
			strings.Join(removed, ", "), graph.RenderVersion, renderCorpusFile)
		b, err := json.MarshalIndent(renderCorpusPin{RenderVersion: graph.RenderVersion, Graphs: got}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(renderCorpusFile), 0o755))
		require.NoError(t, os.WriteFile(renderCorpusFile, append(b, '\n'), 0o644))
		return
	}
	require.True(t, sameVersion, "graph.RenderVersion is %d, and %s records the corpus at %d: %s",
		graph.RenderVersion, renderCorpusFile, pin.RenderVersion, update)
	require.Empty(t, changed, "the builder renders these corpus Graphs differently at RenderVersion %d: %s. %s",
		graph.RenderVersion, strings.Join(changed, ", "), bump)
	require.Empty(t, added, "corpus entries with no recorded hash: %s; %s", strings.Join(added, ", "), update)
	require.Empty(t, removed, "recorded entries no longer in the corpus: %s; %s", strings.Join(removed, ", "), update)
}

// notInCorpus are node kinds TestRenderCorpus_EveryNodeKind does not ask the
// corpus for, with why. It is empty: every node kind the builder emits is in
// an installed Graph.
var notInCorpus = map[string]string{}

// envStepKind is the kind of a node whose ID is an environment's slug
// (CELSafeSlug(env)): the node shape's PromotionStep of each environment.
const envStepKind = "<an environment's step>"

// notEmitting are pkg/graph functions that build a GraphNode without
// emitting a node kind, with why.
var notEmitting = map[string]string{
	"nodeOf": "reads a node of an existing Graph back (RemoveHealthRefs); it emits none",
}

// TestRenderCorpus_EveryNodeKind (QA-2 on #2353): every node the builder can
// emit appears in a corpus Graph, so a rendering change to any of them fails
// TestRenderCorpus at the same RenderVersion. The node kinds are read from
// pkg/graph's source, type-checked (nodeKinds): every GraphNode it builds,
// whatever the form, and every Node* or *NodeID constant as a floor. An ID
// nodeKinds cannot follow fails here, so a node kind added later is
// required the day it lands; one that no installed Graph can have goes in
// notInCorpus, with why.
func TestRenderCorpus_EveryNodeKind(t *testing.T) {
	var ids []string
	envSteps := false
	for name, in := range renderCorpus() {
		res, err := graph.NewBuilder().Build(in)
		require.NoError(t, err, name)
		envIDs := map[string]bool{}
		for _, env := range res.Environments {
			envIDs[graph.CELSafeSlug(env)] = true
		}
		for _, n := range res.Graph.Spec.Nodes {
			ids = append(ids, n.ID)
			envSteps = envSteps || envIDs[n.ID]
		}
	}
	kinds, unknown := nodeKinds(t, ".")
	require.Empty(t, unknown, "GraphNode IDs nodeKinds cannot follow to a kind: use a Node* constant (or any string "+
		"constant), a chunkID of one, or a helper that returns \"<prefix>0\" + ...")
	var missing []string
	for kind, k := range kinds {
		if notInCorpus[kind] != "" {
			continue
		}
		found := kind == envStepKind && envSteps
		for _, id := range ids {
			if found {
				break
			}
			found = k.matches(kind, id)
		}
		if !found {
			missing = append(missing, kind+" ("+k.where+")")
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing, "no corpus Graph has these nodes: add an entry that renders them to renderCorpus, or, "+
		"if no installed Graph can have them, list them in notInCorpus with why")
	for kind := range notInCorpus {
		assert.Contains(t, kinds, kind, "notInCorpus names a node kind the builder no longer has")
	}
}

// nodeKind is where a node kind is set, and whether it is a prefix ("x0"
// helpers) rather than an ID.
type nodeKind struct {
	where  string
	prefix bool
}

// matches reports whether node ID id is of kind: the ID itself or, as
// chunkID numbers a collection's chunks, the ID and a number; a prefix kind
// any ID it starts.
func (k nodeKind) matches(kind, id string) bool {
	if k.prefix {
		return strings.HasPrefix(id, kind)
	}
	rest, ok := strings.CutPrefix(id, kind)
	return ok && strings.Trim(rest, "0123456789") == ""
}

// nodeKinds type-checks dir's non-test Go files with go/types (their imports
// stubbed: a node ID is pkg/graph's own, so a constant from elsewhere is not
// followed and fails) and returns the node kinds of every GraphNode the
// package builds, with where each is set, and the IDs it cannot follow.
//
// It reads the ID of every composite literal whose type is GraphNode or
// *GraphNode, its type written or elided (in a slice, an array, a map or a
// package-level var), and every assignment to a GraphNode's ID field, except
// in notEmitting's functions. An ID is a kind when it is a string constant
// expression (a literal, a constant of any name); chunkID(x, i) is x's
// kinds; CELSafeSlug(env) is envStepKind; a call to a pkg/graph function
// whose every return is "<lower>0" + ... is that prefix; a variable is what
// it is assigned, a parameter (of a function, a method or a function literal
// bound to a variable) its argument at every call, and a struct field its
// value in every literal and assignment. Any other form is returned in
// unknown, with its position.
//
// As a floor, every string constant named Node* or *NodeID is a kind too,
// and one that no GraphNode it follows uses is in unknown: the follower
// missed where it is set.
func nodeKinds(t *testing.T, dir string) (map[string]nodeKind, []string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	fset := token.NewFileSet()
	var parsed []*ast.File
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err, file)
		parsed = append(parsed, f)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	conf := types.Config{Importer: stubImporter{}, Error: func(error) {}} // errors only about imported names
	pkg, _ := conf.Check("github.com/kardinal-promoter/kardinal-promoter/pkg/graph", fset, parsed, info)
	gn := pkg.Scope().Lookup("GraphNode")
	require.NotNil(t, gn, "pkg/graph's GraphNode")
	nk := &nodeKindFinder{fset: fset, info: info, pkg: pkg, files: parsed, graphNode: gn.Type(),
		parents: map[ast.Node]ast.Node{}, funcs: map[types.Object]ast.Node{}, kinds: map[string]nodeKind{},
		seen: map[ast.Node]bool{}}
	nk.index()
	for _, site := range nk.sites() {
		nk.classify(site)
	}
	for _, name := range pkg.Scope().Names() {
		c, ok := pkg.Scope().Lookup(name).(*types.Const)
		if !ok || c.Val().Kind() != constant.String || strings.HasPrefix(name, "Label") ||
			!regexp.MustCompile(`^Node[A-Z]|NodeID$`).MatchString(name) {
			continue
		}
		v := constant.StringVal(c.Val())
		if _, ok := nk.kinds[v]; !ok {
			nk.unknown = append(nk.unknown, fmt.Sprintf("%s: %s (%q) is a node constant no GraphNode nodeKinds follows "+
				"uses: it missed where the node is built", fset.Position(c.Pos()), name, v))
			nk.kinds[v] = nodeKind{where: fset.Position(c.Pos()).String()}
		}
	}
	sort.Strings(nk.unknown)
	return nk.kinds, nk.unknown
}

// stubImporter gives every import an empty package: nodeKinds needs only
// pkg/graph's own types, constants and functions.
type stubImporter struct{}

func (stubImporter) Import(path string) (*types.Package, error) {
	name := path[strings.LastIndex(path, "/")+1:]
	if name == "" || strings.ContainsAny(name, ".-") || regexp.MustCompile(`^v[0-9]`).MatchString(name) {
		name = "p"
	}
	p := types.NewPackage(path, name)
	p.MarkComplete()
	return p, nil
}

// nodeKindFinder is nodeKinds' state.
type nodeKindFinder struct {
	fset      *token.FileSet
	info      *types.Info
	pkg       *types.Package
	files     []*ast.File
	graphNode types.Type
	parents   map[ast.Node]ast.Node
	funcs     map[types.Object]ast.Node // a function's object to its FuncDecl, or a bound FuncLit's variable to it
	kinds     map[string]nodeKind
	unknown   []string
	seen      map[ast.Node]bool
}

func (nk *nodeKindFinder) index() {
	for _, f := range nk.files {
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			if len(stack) > 0 {
				nk.parents[n] = stack[len(stack)-1]
			}
			stack = append(stack, n)
			switch n := n.(type) {
			case *ast.FuncDecl:
				nk.funcs[nk.info.Defs[n.Name]] = n
			case *ast.AssignStmt:
				for i, rhs := range n.Rhs {
					if lit, ok := rhs.(*ast.FuncLit); ok && i < len(n.Lhs) {
						if id, ok := n.Lhs[i].(*ast.Ident); ok {
							nk.funcs[nk.object(id)] = lit
						}
					}
				}
			}
			return true
		})
	}
}

func (nk *nodeKindFinder) object(id *ast.Ident) types.Object {
	if o := nk.info.Defs[id]; o != nil {
		return o
	}
	return nk.info.Uses[id]
}

func (nk *nodeKindFinder) isGraphNode(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	return t != nil && types.Identical(t, nk.graphNode)
}

// enclosingFunc is the name of the function declaration n is in, "" at
// package level.
func (nk *nodeKindFinder) enclosingFunc(n ast.Node) string {
	for p := nk.parents[n]; p != nil; p = nk.parents[p] {
		if fd, ok := p.(*ast.FuncDecl); ok {
			return fd.Name.Name
		}
	}
	return ""
}

// sites are the ID expressions of every GraphNode literal and every
// assignment to a GraphNode's ID, outside notEmitting's functions.
func (nk *nodeKindFinder) sites() []ast.Expr {
	var out []ast.Expr
	for _, f := range nk.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil || notEmitting[nk.enclosingFunc(n)] != "" {
				return true
			}
			switch n := n.(type) {
			case *ast.CompositeLit:
				if !nk.isGraphNode(nk.info.Types[n].Type) {
					return true
				}
				for i, el := range n.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "ID" {
							out = append(out, kv.Value)
						}
					} else if i == 0 {
						out = append(out, el) // unkeyed: ID is the first field
					}
				}
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || i >= len(n.Rhs) || len(n.Lhs) != len(n.Rhs) {
						continue
					}
					if s := nk.info.Selections[sel]; s != nil && s.Kind() == types.FieldVal && s.Obj().Name() == "ID" && nk.isGraphNode(s.Recv()) {
						out = append(out, n.Rhs[i])
					}
				}
			}
			return true
		})
	}
	return out
}

func (nk *nodeKindFinder) at(n ast.Node) string { return nk.fset.Position(n.Pos()).String() }

func (nk *nodeKindFinder) add(kind string, k nodeKind) {
	if _, ok := nk.kinds[kind]; !ok {
		nk.kinds[kind] = k
	}
}

func (nk *nodeKindFinder) fail(n ast.Node, why string) {
	nk.unknown = append(nk.unknown, nk.at(n)+": "+why)
}

// classify adds the kinds e can be, or records it in unknown.
func (nk *nodeKindFinder) classify(e ast.Expr) {
	if nk.seen[e] {
		return
	}
	nk.seen[e] = true
	if tv := nk.info.Types[e]; tv.Value != nil && tv.Value.Kind() == constant.String {
		nk.add(constant.StringVal(tv.Value), nodeKind{where: nk.at(e)})
		return
	}
	switch e := e.(type) {
	case *ast.ParenExpr:
		nk.classify(e.X)
	case *ast.CallExpr:
		nk.classifyCall(e)
	case *ast.Ident:
		nk.classifyVar(e, nk.object(e))
	case *ast.SelectorExpr:
		s := nk.info.Selections[e]
		if s == nil || s.Kind() != types.FieldVal {
			nk.fail(e, "a selector nodeKinds cannot follow (from another package?)")
			return
		}
		nk.classifyField(e, s.Obj())
	default:
		nk.fail(e, "a node ID form nodeKinds cannot follow")
	}
}

// callee is the pkg/graph function a call calls, or nil.
func (nk *nodeKindFinder) callee(call *ast.CallExpr) types.Object {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return nk.object(fn)
	case *ast.SelectorExpr:
		return nk.object(fn.Sel)
	}
	return nil
}

func (nk *nodeKindFinder) classifyCall(call *ast.CallExpr) {
	fn := nk.callee(call)
	if fn == nil || fn.Pkg() != nk.pkg {
		nk.fail(call, "a call nodeKinds cannot follow: use a helper that returns \"<prefix>0\" + ...")
		return
	}
	switch fn.Name() {
	case "chunkID":
		if len(call.Args) == 2 {
			nk.classify(call.Args[0])
			return
		}
	case "CELSafeSlug":
		nk.add(envStepKind, nodeKind{where: nk.at(call)})
		return
	}
	if prefix, ok := nk.prefixFunc(nk.funcs[fn]); ok {
		nk.add(prefix, nodeKind{where: nk.at(call), prefix: true})
		return
	}
	nk.fail(call, "a call nodeKinds cannot follow: use a helper that returns \"<prefix>0\" + ...")
}

// prefixFunc reports the prefix of a function whose every return is a
// constant "<lower>0" followed by + ...
func (nk *nodeKindFinder) prefixFunc(fn ast.Node) (string, bool) {
	fd, ok := fn.(*ast.FuncDecl)
	if !ok || fd.Body == nil {
		return "", false
	}
	prefix, all := "", true
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if _, lit := n.(*ast.FuncLit); lit {
			return false
		}
		r, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		p := ""
		if len(r.Results) == 1 {
			e := r.Results[0]
			for {
				b, ok := e.(*ast.BinaryExpr)
				if !ok || b.Op != token.ADD {
					break
				}
				e = b.X
			}
			if tv := nk.info.Types[e]; e != r.Results[0] && tv.Value != nil && tv.Value.Kind() == constant.String {
				p = constant.StringVal(tv.Value)
			}
		}
		if !regexp.MustCompile(`^[a-z]+0$`).MatchString(p) || prefix != "" && p != prefix {
			all = false
		}
		prefix = p
		return true
	})
	return prefix, all && prefix != ""
}

// classifyVar follows a variable: a parameter to its argument at every call,
// any other variable to what it is assigned.
func (nk *nodeKindFinder) classifyVar(id *ast.Ident, obj types.Object) {
	v, ok := obj.(*types.Var)
	if !ok {
		nk.fail(id, "a name nodeKinds cannot follow")
		return
	}
	if fn, i, ok := nk.paramOf(v); ok {
		nk.classifyParam(fn, i, id)
		return
	}
	var values []ast.Expr
	for _, f := range nk.files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				if len(n.Lhs) == len(n.Rhs) {
					for i, lhs := range n.Lhs {
						if l, ok := lhs.(*ast.Ident); ok && nk.object(l) == v {
							values = append(values, n.Rhs[i])
						}
					}
				}
			case *ast.ValueSpec:
				for i, name := range n.Names {
					if nk.info.Defs[name] == v && i < len(n.Values) {
						values = append(values, n.Values[i])
					}
				}
			}
			return true
		})
	}
	if len(values) == 0 {
		nk.fail(id, "a variable assigned nowhere nodeKinds can see (a range variable, a multi-value assignment?)")
		return
	}
	for _, e := range values {
		nk.classify(e)
	}
}

// paramOf is the function (a FuncDecl or FuncLit) v is the i-th parameter
// of.
func (nk *nodeKindFinder) paramOf(v *types.Var) (ast.Node, int, bool) {
	for _, f := range nk.files {
		var fn ast.Node
		idx := -1
		ast.Inspect(f, func(n ast.Node) bool {
			if fn != nil {
				return false
			}
			var params *ast.FieldList
			switch n := n.(type) {
			case *ast.FuncDecl:
				params = n.Type.Params
			case *ast.FuncLit:
				params = n.Type.Params
			default:
				return true
			}
			i := 0
			for _, field := range params.List {
				for _, name := range field.Names {
					if nk.info.Defs[name] == v {
						fn, idx = n, i
					}
					i++
				}
			}
			return true
		})
		if fn != nil {
			return fn, idx, true
		}
	}
	return nil, 0, false
}

// classifyParam classifies the i-th argument of every call of fn.
func (nk *nodeKindFinder) classifyParam(fn ast.Node, i int, id *ast.Ident) {
	var target types.Object
	for obj, n := range nk.funcs {
		if n == fn {
			target = obj
		}
	}
	if target == nil {
		nk.fail(id, "a parameter of a function literal not bound to a variable")
		return
	}
	calls := 0
	for _, f := range nk.files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || nk.callee(call) != target {
				return true
			}
			calls++
			if i < len(call.Args) {
				nk.classify(call.Args[i])
			} else {
				nk.fail(call, "a call with too few arguments")
			}
			return true
		})
	}
	if calls == 0 {
		nk.fail(id, "a parameter of a function nothing calls")
	}
}

// classifyField follows a struct field to its value in every composite
// literal that sets it and every assignment to it.
func (nk *nodeKindFinder) classifyField(sel *ast.SelectorExpr, field types.Object) {
	found := false
	for _, f := range nk.files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.KeyValueExpr:
				if k, ok := n.Key.(*ast.Ident); ok && nk.info.Uses[k] == field {
					found = true
					nk.classify(n.Value)
				}
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					if s, ok := lhs.(*ast.SelectorExpr); ok && len(n.Lhs) == len(n.Rhs) {
						if sl := nk.info.Selections[s]; sl != nil && sl.Obj() == field {
							found = true
							nk.classify(n.Rhs[i])
						}
					}
				}
			}
			return true
		})
	}
	if !found {
		nk.fail(sel, "a field set nowhere nodeKinds can see")
	}
}
