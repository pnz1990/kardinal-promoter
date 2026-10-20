// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
// where pkg/graph sets node IDs: every GraphNode{ID: ...} in its source
// (nodeKinds). An ID written in a form nodeKinds cannot follow fails here, so
// a node kind added later is required the day it lands; one that no
// installed Graph can have goes in notInCorpus, with why.
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
	if envSteps {
		ids = append(ids, envStepKind)
	}
	kinds, unknown := nodeKinds(t, ".")
	require.Empty(t, unknown, "node IDs nodeKinds cannot follow to a kind: use a Node* constant (or any string "+
		"constant), a chunkID of one, or a helper that returns \"<prefix>0\" + ...")
	require.NotEmpty(t, kinds)
	var missing []string
	for kind, where := range kinds {
		if notInCorpus[kind] != "" {
			continue
		}
		found := false
		for _, id := range ids {
			if id == kind || strings.HasPrefix(id, kind) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, kind+" ("+where+")")
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing, "no corpus Graph has these nodes: add an entry that renders them to renderCorpus, or, "+
		"if no installed Graph can have them, list them in notInCorpus with why")
	for kind := range notInCorpus {
		assert.Contains(t, kinds, kind, "notInCorpus names a node kind the builder no longer has")
	}
}

// nodeKinds follows the ID of every GraphNode composite literal in dir's
// non-test Go files (but notEmitting's) to the node kinds it can be, with
// where each is set. An ID is a kind when it is:
//   - a string literal, or a string constant of any name;
//   - chunkID(x, i): x's kinds;
//   - CELSafeSlug(env): envStepKind, the node shape's step of an
//     environment;
//   - a call to a function whose every return is "<lower>0" + ...: that
//     prefix;
//   - a local variable: the kinds of what it is assigned;
//   - a parameter of a function, a method or a function literal bound to a
//     variable: the kinds of its argument at every call;
//   - a field of a parameter of a named struct type: the kinds of that field
//     in every literal of the type.
//
// Any other form (fmt.Sprintf, a prefix without 0, a map lookup, ...) is
// returned in unknown, with its position.
func nodeKinds(t *testing.T, dir string) (kinds map[string]string, unknown []string) {
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
	nk := &nodeKindFinder{fset: fset, consts: map[string]string{}, funcs: map[string][]*ast.FuncDecl{},
		parents: map[ast.Node]ast.Node{}, kinds: map[string]string{}, seen: map[ast.Node]bool{}}
	for _, f := range parsed {
		nk.index(f)
	}
	for _, f := range parsed {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || notEmitting[fd.Name.Name] != "" {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if id, ok := cl.Type.(*ast.Ident); !ok || id.Name != "GraphNode" {
					return true
				}
				for _, el := range cl.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "ID" {
							nk.classify(kv.Value)
						}
					}
				}
				return true
			})
		}
	}
	sort.Strings(nk.unknown)
	return nk.kinds, nk.unknown
}

// nodeKindFinder is nodeKinds' state: the package's string constants, its
// functions by name, every node's parent (to find a name's declaration),
// the kinds found and the forms it could not follow.
type nodeKindFinder struct {
	fset    *token.FileSet
	consts  map[string]string
	funcs   map[string][]*ast.FuncDecl
	files   []*ast.File
	parents map[ast.Node]ast.Node
	kinds   map[string]string
	unknown []string
	seen    map[ast.Node]bool
}

func (nk *nodeKindFinder) index(f *ast.File) {
	nk.files = append(nk.files, f)
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
		return true
	})
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			if d.Tok != token.CONST {
				continue
			}
			for _, spec := range d.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							nk.consts[name.Name], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			}
		case *ast.FuncDecl:
			nk.funcs[d.Name.Name] = append(nk.funcs[d.Name.Name], d)
		}
	}
}

func (nk *nodeKindFinder) at(n ast.Node) string { return nk.fset.Position(n.Pos()).String() }

func (nk *nodeKindFinder) add(kind string, n ast.Node) {
	if _, ok := nk.kinds[kind]; !ok {
		nk.kinds[kind] = nk.at(n)
	}
}

func (nk *nodeKindFinder) fail(e ast.Expr, why string) {
	nk.unknown = append(nk.unknown, fmt.Sprintf("%s: %s", nk.at(e), why))
}

// classify adds the kinds e can be, or records it as unknown.
func (nk *nodeKindFinder) classify(e ast.Expr) {
	if nk.seen[e] {
		return
	}
	nk.seen[e] = true
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			v, _ := strconv.Unquote(e.Value)
			nk.add(v, e)
			return
		}
	case *ast.ParenExpr:
		nk.classify(e.X)
		return
	case *ast.Ident:
		nk.classifyName(e)
		return
	case *ast.CallExpr:
		if fn, ok := e.Fun.(*ast.Ident); ok {
			if fn.Name == "CELSafeSlug" {
				nk.add(envStepKind, e)
				return
			}
			if fn.Name == "chunkID" && len(e.Args) == 2 {
				nk.classify(e.Args[0])
				return
			}
			if prefix, ok := nk.prefixFunc(fn.Name); ok {
				nk.add(prefix, e)
				return
			}
		}
		nk.fail(e, "a call nodeKinds cannot follow: use a helper that returns \"<prefix>0\" + ...")
		return
	case *ast.SelectorExpr:
		if nk.classifyField(e) {
			return
		}
	}
	nk.fail(e, "a node ID form nodeKinds cannot follow")
}

// prefixFunc reports the prefix of a function whose every return is
// "<lower>0" + ...
func (nk *nodeKindFinder) prefixFunc(name string) (string, bool) {
	decls := nk.funcs[name]
	if len(decls) != 1 || decls[0].Body == nil {
		return "", false
	}
	prefix, ok := "", true
	ast.Inspect(decls[0].Body, func(n ast.Node) bool {
		if _, lit := n.(*ast.FuncLit); lit {
			return false
		}
		r, isReturn := n.(*ast.ReturnStmt)
		if !isReturn {
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
			if lit, isLit := e.(*ast.BasicLit); isLit && lit.Kind == token.STRING && e != r.Results[0] {
				p, _ = strconv.Unquote(lit.Value)
			}
		}
		if !regexp.MustCompile(`^[a-z]+0$`).MatchString(p) || prefix != "" && p != prefix {
			ok = false
		}
		prefix = p
		return true
	})
	return prefix, ok && prefix != ""
}

// classifyName follows an identifier to its declaration: a parameter, a
// local variable, a function literal's parameter, or a constant.
func (nk *nodeKindFinder) classifyName(id *ast.Ident) {
	for n := nk.parents[id]; n != nil; n = nk.parents[n] {
		var params *ast.FieldList
		var body *ast.BlockStmt
		switch fn := n.(type) {
		case *ast.FuncDecl:
			params, body = fn.Type.Params, fn.Body
		case *ast.FuncLit:
			params, body = fn.Type.Params, fn.Body
		default:
			continue
		}
		if i, ok := paramIndex(params, id.Name); ok {
			nk.classifyParam(n, i, id)
			return
		}
		if values := assignments(body, id.Name); len(values) > 0 {
			for _, v := range values {
				nk.classify(v)
			}
			return
		}
	}
	if v, ok := nk.consts[id.Name]; ok {
		nk.add(v, id)
		return
	}
	nk.fail(id, "a name nodeKinds cannot follow to a constant, an assignment or a call")
}

// classifyParam classifies the i-th argument of every call of fn, a
// function, a method or a function literal bound to a variable.
func (nk *nodeKindFinder) classifyParam(fn ast.Node, i int, id *ast.Ident) {
	var calls []*ast.CallExpr
	switch fn := fn.(type) {
	case *ast.FuncDecl:
		for _, f := range nk.files {
			ast.Inspect(f, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					switch c := call.Fun.(type) {
					case *ast.Ident:
						if c.Name == fn.Name.Name && fn.Recv == nil {
							calls = append(calls, call)
						}
					case *ast.SelectorExpr:
						if c.Sel.Name == fn.Name.Name && fn.Recv != nil {
							calls = append(calls, call)
						}
					}
				}
				return true
			})
		}
	case *ast.FuncLit:
		name := ""
		if as, ok := nk.parents[fn].(*ast.AssignStmt); ok {
			for j, rhs := range as.Rhs {
				if rhs == fn && j < len(as.Lhs) {
					if lhs, ok := as.Lhs[j].(*ast.Ident); ok {
						name = lhs.Name
					}
				}
			}
		}
		if name == "" {
			nk.fail(id, "a parameter of a function literal not bound to a variable")
			return
		}
		ast.Inspect(nk.enclosingBody(fn), func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if c, ok := call.Fun.(*ast.Ident); ok && c.Name == name {
					calls = append(calls, call)
				}
			}
			return true
		})
	}
	if len(calls) == 0 {
		nk.fail(id, "a parameter of a function nothing calls")
		return
	}
	for _, call := range calls {
		if i >= len(call.Args) {
			nk.fail(call, "a call with too few arguments")
			continue
		}
		nk.classify(call.Args[i])
	}
}

// enclosingBody is the body of the function that encloses n.
func (nk *nodeKindFinder) enclosingBody(n ast.Node) *ast.BlockStmt {
	for p := nk.parents[n]; p != nil; p = nk.parents[p] {
		switch fn := p.(type) {
		case *ast.FuncDecl:
			return fn.Body
		case *ast.FuncLit:
			return fn.Body
		}
	}
	return nil
}

// classifyField follows x.f, x a parameter of a named struct type, to the
// value of f in every composite literal of that type.
func (nk *nodeKindFinder) classifyField(sel *ast.SelectorExpr) bool {
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	typeName := ""
	for n := nk.parents[sel]; n != nil && typeName == ""; n = nk.parents[n] {
		if fd, ok := n.(*ast.FuncDecl); ok {
			for _, f := range fd.Type.Params.List {
				for _, name := range f.Names {
					if t, ok := f.Type.(*ast.Ident); ok && name.Name == x.Name {
						typeName = t.Name
					}
				}
			}
		}
	}
	if typeName == "" {
		return false
	}
	found := false
	for _, f := range nk.files {
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if t, ok := cl.Type.(*ast.Ident); !ok || t.Name != typeName {
				return true
			}
			for _, el := range cl.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok && k.Name == sel.Sel.Name {
						found = true
						nk.classify(kv.Value)
					}
				}
			}
			return true
		})
	}
	return found
}

// paramIndex is the position of name among params.
func paramIndex(params *ast.FieldList, name string) (int, bool) {
	i := 0
	for _, f := range params.List {
		for _, n := range f.Names {
			if n.Name == name {
				return i, true
			}
			i++
		}
	}
	return 0, false
}

// assignments are the values assigned to name in body (:=, = and var),
// outside nested function literals.
func assignments(body *ast.BlockStmt, name string) []ast.Expr {
	var out []ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
						out = append(out, n.Rhs[i])
					}
				}
			}
		case *ast.ValueSpec:
			for i, id := range n.Names {
				if id.Name == name && i < len(n.Values) {
					out = append(out, n.Values[i])
				}
			}
		}
		return true
	})
	return out
}
