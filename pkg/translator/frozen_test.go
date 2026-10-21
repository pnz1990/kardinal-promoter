// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// gateData is a Graph's PolicyGateData as the instances see it: each
// template by namespace/name, and each item by instance name with its
// template's namespace/name in place of the index t.
func gateData(t *testing.T, g *graph.Graph) (templates, items map[string]string) {
	t.Helper()
	templates, items = map[string]string{}, map[string]string{}
	for _, n := range g.Spec.Nodes {
		if n.ID != graph.NodePolicyGateData {
			continue
		}
		list, _ := n.Def["templates"].([]interface{})
		keys := make([]string, len(list))
		for i, raw := range list {
			tmpl := raw.(map[string]interface{})
			keys[i] = tmpl["templateNamespace"].(string) + "/" + tmpl["template"].(string)
			b, err := json.Marshal(tmpl)
			require.NoError(t, err)
			templates[keys[i]] = string(b)
		}
		for field, raw := range n.Def {
			l, ok := raw.([]interface{})
			if !ok || field == "templates" {
				continue
			}
			for _, it := range l {
				item := map[string]interface{}{}
				for k, v := range it.(map[string]interface{}) {
					item[k] = v
				}
				item["t"] = keys[index(item["t"])]
				b, err := json.Marshal(item)
				require.NoError(t, err)
				items[item["name"].(string)] = field + " " + string(b)
			}
		}
	}
	return templates, items
}

// index is a PolicyGateData item's template index.
func index(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return -1
}

// TestTranslate_InFlightKeepsRefusedGates (#2286 v0.14): a version in flight
// whose Pipeline's spec.policyNamespaces names a namespace that no longer
// agrees to share its PolicyGates is re-rendered (a gate edit, an upgrade's
// re-render) with that namespace's gates as its Graph had them: the
// templates and every instance item are equal, the namespace is not read
// again, and an edit or deletion there does not reach the version. An edit
// to a gate of a namespace that agreed (the Pipeline's own) still reaches it.
// While the Pipeline's generation is not checked yet, the version waits
// rather than read that namespace. The gates are a CEL gate with a gate-name label, an approval gate with
// required, users, agents, requiredWhen and fields, a verify gate with an
// explicit environment and an AgentRun, and a verify gate on two
// environments whose verified environment is each one's upstream.
//
// Covers GATE-POLNS-07.
func TestTranslate_InFlightKeepsRefusedGates(t *testing.T) {
	ctx := zerolog.Nop().WithContext(context.Background())
	gate := func(ns, name, appliesTo string, spec kardinalv1alpha1.PolicyGateSpec, labels ...string) *kardinalv1alpha1.PolicyGate {
		g := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 3, UID: types.UID("uid-" + name),
			Labels: map[string]string{"kardinal.io/applies-to": appliesTo}}, Spec: spec}
		for i := 0; i+1 < len(labels); i += 2 {
			g.Labels[labels[i]] = labels[i+1]
		}
		return g
	}
	exclude := false
	objs := []client.Object{
		gate("team-b", "b-cel", "prod", kardinalv1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend", Message: "no weekends",
			RecheckInterval: "5m", When: "pre-deploy"}, "kardinal.io/gate-name", "weekday-only"),
		gate("team-b", "b-approval", "prod", kardinalv1alpha1.PolicyGateSpec{Expression: "approvals.met",
			Approval: &kardinalv1alpha1.GateApprovalPolicy{Required: 2, AllowedUsers: []string{"alice", "bob"}, AllowedGroups: []string{"sre"},
				AllowAgents: true, AllowedAgents: []string{"fixer"}, RequiredWhen: "bundle.type == 'image'",
				Fields: []kardinalv1alpha1.ApprovalField{{Name: "ticket", Required: true, Pattern: "^CHG[0-9]+$"}}}}),
		gate("team-b", "b-verify", "prod", kardinalv1alpha1.PolicyGateSpec{Expression: "verdicts.met",
			Verify: &kardinalv1alpha1.GateVerifyPolicy{Check: "qa", Environment: "uat", Required: 1, ExcludeAuthor: &exclude,
				Run: &kardinalv1alpha1.GateVerifyRun{Template: "qa-agent"}, Timeout: "30m"}}),
		gate("team-b", "b-verify-upstream", "uat,prod", kardinalv1alpha1.PolicyGateSpec{Expression: "verdicts.met",
			Verify: &kardinalv1alpha1.GateVerifyPolicy{Check: "smoke", Required: 1}}),
		gate("team-a", "own-gate", "prod", kardinalv1alpha1.PolicyGateSpec{Expression: "true"}),
		gate("platform-policies", "org-gate", "prod", kardinalv1alpha1.PolicyGateSpec{Expression: "true"}),
	}
	listed := map[string]int{}
	c := fake.NewClientBuilder().WithScheme(translateScheme(t)).WithObjects(objs...).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			o := &client.ListOptions{}
			o.ApplyOptions(opts)
			if _, ok := list.(*kardinalv1alpha1.PolicyGateList); ok {
				listed[o.Namespace]++
			}
			return cl.List(ctx, list, opts...)
		}}).Build()
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"})
	gc := graph.NewGraphClient(dyn, zerolog.Nop())
	tr := New(gc, graph.NewBuilder(), c, []string{"platform-policies"}, zerolog.Nop()).
		WithIdentity(&graph.IdentityProvisioner{Writer: c, Reader: c})
	p := teamPipeline(kardinalv1alpha1.EnvironmentSpec{Name: "test"}, kardinalv1alpha1.EnvironmentSpec{Name: "uat"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod"})
	p.Generation = 1
	p.Spec.PolicyNamespaces = []string{"team-b"}
	p.Status.PolicyNamespaces = &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 1} // team-b agreed
	b := teamBundle(nil)

	name, err := tr.Translate(ctx, p, b)
	require.NoError(t, err)
	g1, err := gc.Get(ctx, b.Namespace, name)
	require.NoError(t, err)
	tmpl1, items1 := gateData(t, g1)
	require.Len(t, tmpl1, 6, "every gate is in the Graph")

	// The version is in flight; team-b no longer agrees; its gates change
	// and one is deleted; the Pipeline's own gate is edited.
	b.Status.GraphRef = name
	p.Status.PolicyNamespaces = &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 1, NotShared: []string{"team-b"}}
	var edited kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "team-b", Name: "b-cel"}, &edited))
	edited.Spec.Expression = "false"
	require.NoError(t, c.Update(ctx, &edited))
	require.NoError(t, c.Delete(ctx, &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: "team-b", Name: "b-approval"}}))
	var own kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "own-gate"}, &own))
	own.Spec.Expression = "bundle.type == 'image'"
	require.NoError(t, c.Update(ctx, &own))
	listed = map[string]int{}

	// A Pipeline generation not checked yet: the version waits, without
	// reading team-b, rather than take what team-b has now.
	p.Generation = 2
	_, err = tr.Translate(ctx, p, b)
	var pnsErr *PolicyNamespacesError
	require.ErrorAs(t, err, &pnsErr)
	assert.True(t, pnsErr.Unchecked, "%v", err)
	assert.Zero(t, listed["team-b"], "a namespace that must agree is not read before the check")
	p.Status.PolicyNamespaces.ObservedGeneration = 2

	_, err = tr.Translate(ctx, p, b)
	require.NoError(t, err)
	g2, err := gc.Get(ctx, b.Namespace, name)
	require.NoError(t, err)
	tmpl2, items2 := gateData(t, g2)

	assert.Zero(t, listed["team-b"], "a namespace refused is not read")
	for key, want := range tmpl1 {
		if key[:len("team-b/")] != "team-b/" {
			continue
		}
		assert.Equal(t, want, tmpl2[key], "%s: the template as the version's Graph had it", key)
	}
	compared := 0
	for inst, want := range items1 {
		var item map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(want[indexOfSpace(want)+1:]), &item))
		if tk, _ := item["t"].(string); len(tk) > 7 && tk[:7] == "team-b/" {
			compared++
			assert.Equal(t, want, items2[inst], "%s: the instance as the version's Graph had it", inst)
		}
	}
	// b-cel, b-approval and b-verify in prod, b-verify-upstream in uat and prod.
	assert.Equal(t, 5, compared, "every instance of team-b's gates is compared")
	assert.Contains(t, tmpl2["team-a/own-gate"], `bundle.type == 'image'`, "an edit to a gate of a namespace that agreed reaches the version")
	assert.NotContains(t, tmpl2["team-b/b-cel"], `"false"`, "an edit in the refused namespace does not")
	assert.Contains(t, tmpl2, "team-b/b-approval", "nor does a deletion")
}

// indexOfSpace is the index of the first space in s.
func indexOfSpace(s string) int {
	for i := range s {
		if s[i] == ' ' {
			return i
		}
	}
	return -1
}

// TestTranslate_InFlightGraphGoneFailsClosed (#2286 v0.14): a version in
// flight whose Graph was deleted, while its Pipeline names a namespace that
// no longer agrees to share its PolicyGates, is not built again: what that
// namespace's gates were when it started is known only from the Graph, and
// rebuilt without them the version would skip them. Once the namespace
// agrees, the Graph is built with its current gates.
// Covers GATE-POLNS-07.
func TestTranslate_InFlightGraphGoneFailsClosed(t *testing.T) {
	ctx := zerolog.Nop().WithContext(context.Background())
	c := fake.NewClientBuilder().WithScheme(translateScheme(t)).WithObjects(
		&kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "b-cel", Namespace: "team-b", Generation: 1,
			Labels: map[string]string{"kardinal.io/applies-to": "prod"}}, Spec: kardinalv1alpha1.PolicyGateSpec{Expression: "true"}},
	).Build()
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"})
	gc := graph.NewGraphClient(dyn, zerolog.Nop())
	tr := New(gc, graph.NewBuilder(), c, nil, zerolog.Nop()).WithIdentity(&graph.IdentityProvisioner{Writer: c, Reader: c})
	p := teamPipeline(kardinalv1alpha1.EnvironmentSpec{Name: "test"}, kardinalv1alpha1.EnvironmentSpec{Name: "prod"})
	p.Generation = 1
	p.Spec.PolicyNamespaces = []string{"team-b"}
	p.Status.PolicyNamespaces = &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 1, NotShared: []string{"team-b"}}
	b := teamBundle(nil)
	b.Status.GraphRef = "payments-payments-x7k2m" // deleted

	_, err := tr.Translate(ctx, p, b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keep the gates of team-b it started with")
	assert.False(t, IsPolicyNamespacesError(err), "not the wait of a new version: the version's Graph is gone")
	list, lErr := dyn.Resource(graph.GraphGVR).Namespace(b.Namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, lErr)
	assert.Empty(t, list.Items, "no Graph is built without the gates")

	p.Status.PolicyNamespaces.NotShared = nil // team-b agrees
	name, err := tr.Translate(ctx, p, b)
	require.NoError(t, err)
	g, err := gc.Get(ctx, b.Namespace, name)
	require.NoError(t, err)
	tmpl, _ := gateData(t, g)
	assert.Contains(t, tmpl, "team-b/b-cel")
}
