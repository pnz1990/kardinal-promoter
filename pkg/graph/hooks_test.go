// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

const hookJob = `{"backoffLimit":0,"template":{"spec":{"containers":[{"name":"m","image":"busybox:1.36",` +
	`"command":["sh","-c","echo migrate ${BUNDLE} \"quoted\" {braces}"]}]}}}`

func hook(name, phase, job string) kardinalv1alpha1.HookSpec {
	return kardinalv1alpha1.HookSpec{Name: name, Phase: phase, Job: runtime.RawExtension{Raw: []byte(job)}}
}

// hookPipeline is test → prod; prod has two pre hooks and one post hook.
func hookPipeline() *kardinalv1alpha1.Pipeline {
	p := makeLinearPipeline("app", "test", "prod")
	p.Spec.Environments[1].Hooks = []kardinalv1alpha1.HookSpec{
		hook("migrate", "pre", hookJob),
		hook("smoke", "post", hookJob),
		hook("seed", "pre", hookJob),
	}
	return p
}

func hookNode(t *testing.T, g *graph.Graph, id string) graph.GraphNode {
	t.Helper()
	for _, n := range g.Spec.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("no node %q", id)
	return graph.GraphNode{}
}

func hasNode(g *graph.Graph, id string) bool {
	for _, n := range g.Spec.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// TestBuilder_HookNodes: an environment with hooks gets one HookRun node per
// hook, a mirror patch node, and spec.preHooks / spec.postHooks on its step;
// the Graph gets the two read-back refs once. Environments without hooks
// are unchanged.
func TestBuilder_HookNodes(t *testing.T) {
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: hookPipeline(), Bundle: makeBundle("app-v1", "app")})
	require.NoError(t, err)
	g := res.Graph

	for _, id := range []string{"refHookRuns", "refSteps", "hook0pre0prod0migrate", "hook0pre0prod0seed",
		"hook0post0prod0smoke", "live0prod"} {
		assert.True(t, hasNode(g, id), "node %s", id)
	}
	for _, id := range []string{"hook0pre0test0migrate", "live0test"} {
		assert.False(t, hasNode(g, id), "test has no hooks: no node %s", id)
	}
	// 1 bundle + 2 refs + 2 envs × (PRStatus + step) + 3 hooks + 1 mirror
	assert.Equal(t, 11, res.NodeCount)

	migrate := graph.HookRunName("app", "app-v1", "prod", "pre", "migrate")
	seed := graph.HookRunName("app", "app-v1", "prod", "pre", "seed")
	smoke := graph.HookRunName("app", "app-v1", "prod", "post", "smoke")
	assert.True(t, strings.HasPrefix(migrate, "app-app-v1-prod-pre-migrate-"), migrate)

	spec := hookNode(t, g, "prod").Template["spec"].(map[string]interface{})
	assert.Equal(t, []interface{}{"${hook0pre0prod0migrate.metadata.name}", seed}, spec["preHooks"],
		"the first pre hook is a CEL edge, the rest literal names")
	assert.Equal(t, []interface{}{smoke}, spec["postHooks"])
	testSpec := hookNode(t, g, "test").Template["spec"].(map[string]interface{})
	assert.NotContains(t, testSpec, "preHooks")
	assert.NotContains(t, testSpec, "postHooks")

	hr := hookNode(t, g, "hook0pre0prod0migrate").Template
	assert.Equal(t, "HookRun", hr["kind"])
	md := hr["metadata"].(map[string]interface{})
	assert.Equal(t, map[string]interface{}{
		"kardinal.io/pipeline": "app", "kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod",
		"kardinal.io/hook-phase": "pre", "kardinal.io/hook": "migrate", "kardinal.io/bundle-uid": "",
	}, md["labels"])
	hrSpec := hr["spec"].(map[string]interface{})
	assert.Equal(t, "pre", hrSpec["phase"])
	assert.Equal(t, "migrate", hrSpec["hook"])
	assert.Equal(t, "app-v1", hrSpec["bundleName"])

	mirror := hookNode(t, g, "live0prod")
	require.NotNil(t, mirror.Patch)
	assert.Nil(t, mirror.Template)
	assert.Equal(t, map[string]interface{}{"name": "app-app-v1-prod"}, mirror.Patch["metadata"],
		"the mirror targets the step's literal name, not ${prod.metadata.name}")

	ref := hookNode(t, g, "refHookRuns").Ref
	assert.Equal(t, "HookRun", ref["kind"])
	assert.Equal(t, "default", ref["metadata"].(map[string]interface{})["namespace"],
		"a selector ref without a namespace lists every namespace")

	// The rendered Graph is valid JSON (it goes to the API server as such).
	_, err = json.Marshal(g)
	require.NoError(t, err)
}

// TestBuilder_NoHooksUnchanged: a Pipeline without hooks gets no hook nodes
// or refs at all.
func TestBuilder_NoHooksUnchanged(t *testing.T) {
	res, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline: makeLinearPipeline("app", "test", "prod"), Bundle: makeBundle("app-v1", "app")})
	require.NoError(t, err)
	assert.Equal(t, 5, res.NodeCount)
	assert.False(t, hasNode(res.Graph, "refHookRuns"))
}

// celEval compiles a "${...}" Graph expression with optional types (as kro
// does) and evaluates it against vars.
func celEval(t *testing.T, expr string, vars map[string]interface{}) (interface{}, error) {
	t.Helper()
	require.True(t, strings.HasPrefix(expr, "${") && strings.HasSuffix(expr, "}"), "expression %q", expr)
	var opts []cel.EnvOption
	// As kro's environment (pkg/cel/environment.go): optionals, lists and
	// strings extensions.
	opts = append(opts, cel.OptionalTypes(), ext.Lists(), ext.Strings())
	for k := range vars {
		opts = append(opts, cel.Variable(k, cel.DynType))
	}
	env, err := cel.NewEnv(opts...)
	require.NoError(t, err)
	ast, iss := env.Compile(expr[2 : len(expr)-1])
	require.NoError(t, iss.Err(), "compile %q", expr)
	prg, err := env.Program(ast)
	require.NoError(t, err)
	out, _, err := prg.Eval(vars)
	if err != nil {
		return nil, err
	}
	if _, isString := out.Value().(string); isString {
		return out.Value(), nil
	}
	// Lists and maps: through JSON, as kro writes them.
	pb, err := out.ConvertToNative(reflect.TypeOf(&structpb.Value{}))
	require.NoError(t, err)
	b, err := protojson.Marshal(pb.(*structpb.Value))
	require.NoError(t, err)
	var native interface{}
	require.NoError(t, json.Unmarshal(b, &native))
	return native, nil
}

func nameExpr(t *testing.T, g *graph.Graph, id string) string {
	t.Helper()
	return hookNode(t, g, id).Template["metadata"].(map[string]interface{})["name"].(string)
}

// TestBuilder_HookGating evaluates each HookRun's name expression: it
// resolves only when the hook may run, and is data-pending ("index out of
// bounds") otherwise, so kro does not create the HookRun.
func TestBuilder_HookGating(t *testing.T) {
	p := hookPipeline()
	gate := makePolicyGate("freeze", "platform-policies", "prod", "true")
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{gate}})
	require.NoError(t, err)
	g := res.Graph
	// The gate instance's name, from the step's requiredGates expression.
	required := hookNode(t, g, "prod").Template["spec"].(map[string]interface{})["requiredGates"].([]interface{})
	require.Len(t, required, 1)
	m := regexp.MustCompile(`\["([^"]+)"\]`).FindStringSubmatch(required[0].(string))
	require.Len(t, m, 2, "requiredGates %v", required)
	gateName := m[1]

	vars := func(phase, upstream string, gateReady bool, migrate string, steps []interface{}) map[string]interface{} {
		v := map[string]interface{}{
			"bundle": map[string]interface{}{"status": map[string]interface{}{"phase": phase}},
			"test":   map[string]interface{}{"status": map[string]interface{}{"state": upstream}},
			graph.NodePolicyGates: []interface{}{map[string]interface{}{
				"metadata": map[string]interface{}{"name": gateName},
				"status":   map[string]interface{}{"ready": gateReady}}},
			"hook0pre0prod0migrate": map[string]interface{}{"status": map[string]interface{}{"phase": migrate}},
			"refSteps":              steps,
		}
		return v
	}
	step := func(started bool) []interface{} {
		st := map[string]interface{}{"state": "Verifying"}
		if started {
			st["verificationStartedAt"] = "2026-10-09T00:00:00Z"
		}
		return []interface{}{
			map[string]interface{}{"metadata": map[string]interface{}{"name": "app-app-v1-test"}, "status": map[string]interface{}{}},
			map[string]interface{}{"metadata": map[string]interface{}{"name": "app-app-v1-prod"}, "status": st},
		}
	}

	cases := []struct {
		name    string
		node    string
		vars    map[string]interface{}
		pending bool
	}{
		{"pre: upstream verified, gate ready", "hook0pre0prod0migrate", vars("Promoting", "Verified", true, "", nil), false},
		{"pre: upstream not verified", "hook0pre0prod0migrate", vars("Promoting", "HealthChecking", true, "", nil), true},
		{"pre: gate closed", "hook0pre0prod0migrate", vars("Promoting", "Verified", false, "", nil), true},
		{"pre: bundle superseded", "hook0pre0prod0migrate", vars("Superseded", "Verified", true, "", nil), true},
		{"second pre: first still running", "hook0pre0prod0seed", vars("Promoting", "Verified", true, "Running", nil), true},
		{"second pre: first failed", "hook0pre0prod0seed", vars("Promoting", "Verified", true, "Failed", nil), true},
		{"second pre: first succeeded", "hook0pre0prod0seed", vars("Promoting", "Verified", true, "Succeeded", nil), false},
		{"post: step not verifying", "hook0post0prod0smoke", vars("Promoting", "Verified", true, "", step(false)), true},
		{"post: step verifying", "hook0post0prod0smoke", vars("Promoting", "Verified", true, "", step(true)), false},
		{"post: no step yet", "hook0post0prod0smoke", vars("Promoting", "Verified", true, "", []interface{}{}), true},
		{"post: superseded", "hook0post0prod0smoke", vars("Superseded", "Verified", true, "", step(true)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := celEval(t, nameExpr(t, g, tc.node), tc.vars)
			if tc.pending {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "index out of bounds")
				return
			}
			require.NoError(t, err)
			assert.Contains(t, out, "app-app-v1-prod-")
			assert.Empty(t, validation.IsDNS1123Label(out.(string)))
		})
	}
}

// TestBuilder_HookMirror evaluates the mirror expression: the environment's
// HookRuns, with "Pending" for one that has no status yet.
func TestBuilder_HookMirror(t *testing.T) {
	b := makeBundle("app-v1", "app")
	b.UID = "bundle-uid"
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: hookPipeline(), Bundle: b})
	require.NoError(t, err)
	tmpl := hookNode(t, res.Graph, "hook0pre0prod0migrate").Template
	assert.Equal(t, "bundle-uid", tmpl["metadata"].(map[string]interface{})["labels"].(map[string]interface{})[graph.LabelBundleUID])
	patch := hookNode(t, res.Graph, "live0prod").Patch
	expr := patch["spec"].(map[string]interface{})["live"].(map[string]interface{})["hooks"].(string)
	migrate := graph.HookRunName("app", "app-v1", "prod", "pre", "migrate")
	smoke := graph.HookRunName("app", "app-v1", "prod", "post", "smoke")
	hr := func(name, env, phase, hook string, labels map[string]interface{}, status map[string]interface{}) map[string]interface{} {
		o := map[string]interface{}{"metadata": map[string]interface{}{"name": name, "labels": labels},
			"spec": map[string]interface{}{"environment": env, "phase": phase, "hook": hook}}
		if status != nil {
			o["status"] = status
		}
		return o
	}
	ok := map[string]interface{}{graph.LabelKRONodeID: "x", graph.LabelBundleUID: "bundle-uid"}
	out, err := celEval(t, expr, map[string]interface{}{"refHookRuns": []interface{}{
		hr(migrate, "prod", "pre", "migrate", ok, map[string]interface{}{"phase": "Succeeded", "message": "done"}),
		hr(graph.HookRunName("app", "app-v1", "test", "pre", "migrate"), "test", "pre", "migrate", ok, map[string]interface{}{"phase": "Failed"}),
		hr(smoke, "prod", "post", "smoke", ok, nil),
		// Forged: a name this Graph did not render, no kro label, another Bundle's UID, no labels.
		hr("app-app-v1-prod-pre-forged", "prod", "pre", "migrate", ok, map[string]interface{}{"phase": "Succeeded"}),
		hr(migrate, "prod", "pre", "migrate", map[string]interface{}{graph.LabelBundleUID: "bundle-uid"}, map[string]interface{}{"phase": "Succeeded"}),
		hr(smoke, "prod", "post", "smoke", map[string]interface{}{graph.LabelKRONodeID: "x", graph.LabelBundleUID: "other"}, map[string]interface{}{"phase": "Succeeded"}),
		hr(smoke, "prod", "post", "smoke", nil, map[string]interface{}{"phase": "Succeeded"}),
	}})
	require.NoError(t, err)
	got, err := json.Marshal(out)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"name":"`+migrate+`","hook":"migrate","phase":"pre","result":"Succeeded","message":"done","specHash":""},
		{"name":"`+smoke+`","hook":"smoke","phase":"post","result":"Pending","message":"","specHash":""}]`, string(got),
		"only the HookRuns this Graph rendered, applied by kro for this Bundle (regression, QA #1493 round 2)")
}

// TestBuilder_HookJobStringsAreLiteral: a job string containing "${" (a
// shell variable) is rendered as a kro expression that evaluates to the
// string itself; kro has no other escape and would compile ${BUNDLE} as CEL.
func TestBuilder_HookJobStringsAreLiteral(t *testing.T) {
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: hookPipeline(), Bundle: makeBundle("app-v1", "app")})
	require.NoError(t, err)
	job := hookNode(t, res.Graph, "hook0pre0prod0migrate").Template["spec"].(map[string]interface{})["job"].(map[string]interface{})
	c := job["template"].(map[string]interface{})["spec"].(map[string]interface{})["containers"].([]interface{})[0].(map[string]interface{})
	cmd := c["command"].([]interface{})
	assert.Equal(t, "sh", cmd[0], "strings without ${ are unchanged")
	expr, ok := cmd[2].(string)
	require.True(t, ok)
	out, err := celEval(t, expr, map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, `echo migrate ${BUNDLE} "quoted" {braces}`, out)
	assert.Equal(t, json.Number("0"), job["backoffLimit"], "numbers stay numbers")
}

// TestBuilder_HookNamesNeverCollide: a Pipeline whose environment and hook
// names read the same once joined still builds (regression, QA #1493): every
// HookRun has its own name.
func TestBuilder_HookNamesNeverCollide(t *testing.T) {
	p := makeLinearPipeline("app", "prod", "prod-post")
	p.Spec.Environments[0].Hooks = []kardinalv1alpha1.HookSpec{hook("pre-smoke", "post", hookJob)}
	p.Spec.Environments[1].Hooks = []kardinalv1alpha1.HookSpec{hook("smoke", "pre", hookJob)}
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("v1", "app")})
	require.NoError(t, err)
	a := nameExpr(t, res.Graph, "hook0post0prod0preSmoke")
	b := nameExpr(t, res.Graph, "hook0pre0prodPost0smoke")
	assert.NotEqual(t, a, b)
}

// TestValidateNodeIDs_DuplicateObjectNames: two template nodes that render
// the same object, literally or through resolvableWhen, are refused.
func TestValidateNodeIDs_DuplicateObjectNames(t *testing.T) {
	obj := func(id, name string) graph.GraphNode {
		return graph.GraphNode{ID: id, Template: map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "HookRun",
			"metadata": map[string]interface{}{"name": name}}}
	}
	err := graph.ValidateNodeIDs([]graph.GraphNode{obj("a", "x"), obj("b", `${["x"].filter(x_, true)[0]}`)})
	require.Error(t, err)
	assert.ErrorIs(t, err, graph.ErrInvalid)
	assert.Contains(t, err.Error(), `both render HookRun "x"`)
	require.NoError(t, graph.ValidateNodeIDs([]graph.GraphNode{obj("a", "x"), obj("b", "y")}))
}

// TestBuilder_HookStepAdvanced evaluates spec.stepAdvanced: a pre hook's
// step has advanced once it left Pending, a post hook's once it finished.
func TestBuilder_HookStepAdvanced(t *testing.T) {
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: hookPipeline(), Bundle: makeBundle("app-v1", "app")})
	require.NoError(t, err)
	field := func(id string) string {
		return hookNode(t, res.Graph, id).Template["spec"].(map[string]interface{})["stepAdvanced"].(string)
	}
	steps := func(state string) map[string]interface{} {
		st := map[string]interface{}{}
		if state != "" {
			st["state"] = state
		}
		return map[string]interface{}{"refSteps": []interface{}{
			map[string]interface{}{"metadata": map[string]interface{}{"name": "app-app-v1-prod"}, "status": st}}}
	}
	cases := []struct {
		node, state string
		want        bool
	}{
		{"hook0pre0prod0migrate", "", false},
		{"hook0pre0prod0migrate", "Pending", false},
		{"hook0pre0prod0migrate", "Promoting", true},
		{"hook0pre0prod0migrate", "Verified", true},
		{"hook0post0prod0smoke", "Verifying", false},
		{"hook0post0prod0smoke", "HealthChecking", false},
		{"hook0post0prod0smoke", "Verified", true},
		{"hook0post0prod0smoke", "Failed", true},
	}
	for _, tc := range cases {
		out, err := celEval(t, field(tc.node), steps(tc.state))
		require.NoError(t, err)
		assert.Equal(t, tc.want, out, "%s with step %q", tc.node, tc.state)
	}
	// No step yet: not advanced.
	out, err := celEval(t, field("hook0pre0prod0migrate"), map[string]interface{}{"refSteps": []interface{}{}})
	require.NoError(t, err)
	assert.Equal(t, false, out)
}

// TestBuilder_HookValidation: Build rejects hooks the CRD schema cannot
// check: a job that is not a JobSpec, a job without containers, duplicate
// names, a bad timeout.
func TestBuilder_HookValidation(t *testing.T) {
	cases := []struct {
		name  string
		hooks []kardinalv1alpha1.HookSpec
		want  string
	}{
		{"unknown job field", []kardinalv1alpha1.HookSpec{hook("m", "pre", `{"templat":{}}`)}, "not a batch/v1 JobSpec"},
		{"no containers", []kardinalv1alpha1.HookSpec{hook("m", "pre", `{"template":{"spec":{}}}`)}, "no containers"},
		{"empty job", []kardinalv1alpha1.HookSpec{hook("m", "pre", ``)}, "job is empty"},
		{"duplicate", []kardinalv1alpha1.HookSpec{hook("m", "pre", hookJob), hook("m", "post", hookJob)}, "two hooks"},
		{"bad phase", []kardinalv1alpha1.HookSpec{hook("m", "during", hookJob)}, "not pre or post"},
		{"bad timeout", []kardinalv1alpha1.HookSpec{{Name: "m", Phase: "pre", Timeout: "-1m",
			Job: runtime.RawExtension{Raw: []byte(hookJob)}}}, "positive duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := makeLinearPipeline("app", "prod")
			p.Spec.Environments[0].Hooks = tc.hooks
			_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("app-v1", "app")})
			require.Error(t, err)
			assert.ErrorIs(t, err, graph.ErrInvalid)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestHookRunName: readable, always hash-suffixed (the readable part alone
// is not injective), always a DNS label (a Job name becomes a Pod label
// value).
func TestHookRunName(t *testing.T) {
	assert.True(t, strings.HasPrefix(graph.HookRunName("app", "v1", "prod", "post", "smoke"), "app-v1-prod-post-smoke-"))
	// Regression (QA #1493): env "prod" post hook "pre-smoke" and env
	// "prod-post" pre hook "smoke" read the same; the hash tells them apart.
	assert.NotEqual(t, graph.HookRunName("app", "v1", "prod", "post", "pre-smoke"),
		graph.HookRunName("app", "v1", "prod-post", "pre", "smoke"))
	long := strings.Repeat("x", 40)
	a := graph.HookRunName(long, "v1", "prod", "pre", "migrate")
	b := graph.HookRunName(long, "v2", "prod", "pre", "migrate")
	assert.NotEqual(t, a, b)
	for _, n := range []string{a, b, graph.HookRunName("app", "V1.2", "prod", "pre", "m")} {
		assert.Empty(t, validation.IsDNS1123Label(n), n)
	}
}

// TestBuilder_HookRecorded evaluates a HookRun's spec.recorded: the step's
// record of the hook (by name and phase), or {} without a step or a record
// (regression, #1544 review: a recreated HookRun ran its migration again).
func TestBuilder_HookRecorded(t *testing.T) {
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: hookPipeline(), Bundle: makeBundle("app-v1", "app")})
	require.NoError(t, err)
	exprs := hookNode(t, res.Graph, "hook0pre0prod0migrate").Template["spec"].(map[string]interface{})["recorded"].(map[string]interface{})
	step := func(records ...interface{}) map[string]interface{} {
		return map[string]interface{}{"metadata": map[string]interface{}{"name": "app-app-v1-prod"},
			"status": map[string]interface{}{"hookRecords": records}}
	}
	rec := map[string]interface{}{"hook": "migrate", "phase": "pre", "specHash": "abc", "result": "Succeeded", "message": "done"}
	cases := []struct {
		name  string
		steps []interface{}
		want  map[string]string
	}{
		{"no step", []interface{}{}, map[string]string{"specHash": "", "result": "", "message": ""}},
		{"no records", []interface{}{map[string]interface{}{"metadata": map[string]interface{}{"name": "app-app-v1-prod"}}},
			map[string]string{"specHash": "", "result": "", "message": ""}},
		{"other hook", []interface{}{step(map[string]interface{}{"hook": "seed", "phase": "pre", "specHash": "x", "result": "Failed"})},
			map[string]string{"specHash": "", "result": "", "message": ""}},
		{"recorded", []interface{}{step(rec)}, map[string]string{"specHash": "abc", "result": "Succeeded", "message": "done"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for f, want := range tc.want {
				out, err := celEval(t, exprs[f].(string), map[string]interface{}{"refSteps": tc.steps})
				require.NoError(t, err, f)
				assert.Equal(t, want, out, f)
			}
		})
	}
}
