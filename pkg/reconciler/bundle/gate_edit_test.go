// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// gateEditRig is a Bundle reconciler with the real Translator and a fake
// Graph API, so a test reads the gate data each Bundle's Graph was built with.
type gateEditRig struct {
	c client.Client
	// api is c without its interceptor funcs: what the API server holds.
	api client.Client
	dyn *dynfake.FakeDynamicClient
	gc  *graph.GraphClient
	r   *bundle.Reconciler
}

func newGateEditRig(t *testing.T, objs ...client.Object) *gateEditRig {
	t.Helper()
	return newGateEditRigWith(t, interceptor.Funcs{}, objs...)
}

// newGateEditRigWith is newGateEditRig whose client calls funcs first.
func newGateEditRigWith(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) *gateEditRig {
	t.Helper()
	base := indexedBuilder(newScheme()).
		WithIndex(&kardinalv1alpha1.PolicyGate{}, bundle.GateTemplateIndex, bundle.GateTemplateKey).
		WithObjects(objs...).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}, &kardinalv1alpha1.Pipeline{}, &kardinalv1alpha1.PromotionStep{}).
		Build()
	c := interceptor.NewClient(base, funcs)
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"})
	gc := graph.NewGraphClient(dyn, zerolog.Nop())
	r := &bundle.Reconciler{Client: c, GraphChecker: gc,
		Translator: translator.New(gc, graph.NewBuilder(), c, nil, zerolog.Nop())}
	r.UseTemplateIndex()
	return &gateEditRig{c: c, api: base, dyn: dyn, gc: gc, r: r}
}

// writes counts the Graph writes for Bundle b by verb: each Translate
// creates the Graph ("create", AlreadyExists once it exists), and updates it
// when its spec changed ("update").
func (g *gateEditRig) writes(b, verb string) int {
	name := graph.GraphNameFrom("app", b)
	n := 0
	for _, a := range g.dyn.Actions() {
		if a.GetVerb() != verb {
			continue
		}
		// Create and update actions both carry the object.
		w, ok := a.(k8stesting.CreateAction)
		if !ok {
			continue
		}
		obj := w.GetObject()
		if u, ok := obj.(*unstructured.Unstructured); ok && u.GetName() == name {
			n++
		}
	}
	return n
}

// gateData is the template entry of gate in b's Graph (PolicyGateData).
func (g *gateEditRig) gateData(t *testing.T, b, gate string) map[string]interface{} {
	t.Helper()
	got, err := g.gc.Get(context.Background(), "default", graph.GraphNameFrom("app", b))
	require.NoError(t, err)
	for _, n := range got.Spec.Nodes {
		if n.ID != graph.NodePolicyGateData {
			continue
		}
		for _, tmpl := range n.Def["templates"].([]interface{}) {
			m := tmpl.(map[string]interface{})
			if m["template"] == gate {
				return m
			}
		}
	}
	t.Fatalf("graph of %s has no gate %s", b, gate)
	return nil
}

func (g *gateEditRig) editGate(t *testing.T, name string, edit func(*kardinalv1alpha1.PolicyGate)) {
	t.Helper()
	var gate kardinalv1alpha1.PolicyGate
	require.NoError(t, g.c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &gate))
	edit(&gate)
	require.NoError(t, g.c.Update(context.Background(), &gate))
}

// gateEditBundle is a Bundle the Graph builder accepts.
func gateEditBundle(name, phase string, created time.Time) *kardinalv1alpha1.Bundle {
	b := lcBundle(name, "image", phase, created)
	b.Spec.Images = []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/acme/app", Tag: name}}
	return b
}

func gateEditPipeline() *kardinalv1alpha1.Pipeline {
	p := lcPipeline("app", lcEnvs("test", "prod")...)
	p.Spec.Git.URL = "https://github.com/acme/app"
	return p
}

// #1360: an edit to a PolicyGate template reaches the Bundles in flight. The
// Promoting Bundle's Graph is re-translated in place with the new expression
// and the new template stamp; a Verified Bundle's Graph keeps the gate it was
// held to. A second reconcile changes nothing, and an edit to a gate of
// another environment re-translates nothing.
func TestGateEdit_ReachesPromotingBundle(t *testing.T) {
	t0 := time.Now().UTC().Add(-time.Hour)
	hold := gateTemplate("hold", "prod", false)
	hold.Spec.Expression = "false"
	other := gateTemplate("staging-only", "staging", false)
	g := newGateEditRig(t, gateEditPipeline(), hold, other, gateEditBundle("app-v1", "Available", t0))

	// v1 is built and finishes; v2 is built and is in flight.
	lcReconcile(t, g.r, "app-v1")
	v1 := lcGet(t, g.c, "app-v1")
	require.Equal(t, "Promoting", v1.Status.Phase)
	require.NotEmpty(t, v1.Status.PolicyGatesHash, "the Graph records the gates it was built with")
	setPhase(t, g.c, "app-v1", "Verified")
	require.NoError(t, g.c.Create(context.Background(), gateEditBundle("app-v2", "", t0.Add(time.Minute))))
	lcReconcile(t, g.r, "app-v2")
	lcReconcile(t, g.r, "app-v2")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v2").Status.Phase)
	before := g.gateData(t, "app-v1", "hold")
	assert.Equal(t, "false", before["spec"].(map[string]interface{})["expression"])
	assert.Equal(t, before, g.gateData(t, "app-v2", "hold"))
	v1Writes, v2Writes := g.writes("app-v1", "create"), g.writes("app-v2", "create")

	// Nothing changed: nothing is re-translated or written.
	bundleRV := lcGet(t, g.c, "app-v2").ResourceVersion
	lcReconcile(t, g.r, "app-v2")
	assert.Equal(t, bundleRV, lcGet(t, g.c, "app-v2").ResourceVersion)
	assert.Equal(t, v2Writes, g.writes("app-v2", "create"))

	// A gate of an environment the Pipeline does not have: no re-translate.
	g.editGate(t, "staging-only", func(p *kardinalv1alpha1.PolicyGate) { p.Spec.Expression = "false" })
	lcReconcile(t, g.r, "app-v2")
	assert.Equal(t, v2Writes, g.writes("app-v2", "create"), "an unrelated gate does not re-translate")

	// The platform team fixes the gate.
	g.editGate(t, "hold", func(p *kardinalv1alpha1.PolicyGate) { p.Spec.Expression = "true" })
	assert.Contains(t, g.r.GateBundles(context.Background(), hold),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "app-v2", Namespace: "default"}},
		"the gate watch re-queues the Promoting Bundle")
	assert.NotContains(t, g.r.GateBundles(context.Background(), hold),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}},
		"nor the Verified one")
	lcReconcile(t, g.r, "app-v2")
	lcReconcile(t, g.r, "app-v1")

	after := g.gateData(t, "app-v2", "hold")
	assert.Equal(t, 1, g.writes("app-v2", "update"), "the in-flight Graph is updated in place")
	assert.Equal(t, "true", after["spec"].(map[string]interface{})["expression"])
	assert.NotEqual(t, before["digest"], after["digest"], "the instance stamp names the edited template")
	var edited kardinalv1alpha1.PolicyGate
	require.NoError(t, g.c.Get(context.Background(), types.NamespacedName{Name: "hold", Namespace: "default"}, &edited))
	assert.Equal(t, graph.TemplateDigest(edited), after["digest"])
	v2 := lcGet(t, g.c, "app-v2")
	assert.Equal(t, "Promoting", v2.Status.Phase)
	assert.NotEqual(t, v1.Status.PolicyGatesHash, v2.Status.PolicyGatesHash, "the new hash is recorded")

	assert.Equal(t, v1Writes, g.writes("app-v1", "create"), "a Verified Bundle is not re-translated")
	assert.Equal(t, before, g.gateData(t, "app-v1", "hold"), "its Graph keeps the gate it was held to")
	assert.Equal(t, v1.Status.PolicyGatesHash, lcGet(t, g.c, "app-v1").Status.PolicyGatesHash)

	// The next reconcile writes the GateChanged record (#2144) out of the
	// outbox; after that, a reconcile changes nothing.
	require.Len(t, v2.Status.PendingAuditEvents, 1)
	lcReconcile(t, g.r, "app-v2")
	var events kardinalv1alpha1.AuditEventList
	require.NoError(t, g.c.List(context.Background(), &events))
	require.Len(t, events.Items, 1, "one GateChanged, for v2 only")
	ev := events.Items[0].Spec
	assert.Equal(t, "GateChanged", ev.Action)
	assert.Equal(t, "app-v2", ev.BundleName)
	assert.Equal(t, "prod", ev.Environment)
	require.Len(t, ev.Gates, 1)
	assert.Equal(t, "hold", ev.Gates[0].Template)
	assert.Equal(t, before["digest"], ev.Gates[0].OldDigest)
	assert.Equal(t, after["digest"], ev.Gates[0].NewDigest)
	v2 = lcGet(t, g.c, "app-v2")
	assert.Empty(t, v2.Status.PendingAuditEvents)
	v2Writes = g.writes("app-v2", "create")
	lcReconcile(t, g.r, "app-v2")
	assert.Equal(t, v2.ResourceVersion, lcGet(t, g.c, "app-v2").ResourceVersion)
	require.NoError(t, g.c.List(context.Background(), &events))
	assert.Len(t, events.Items, 1, "written once")
	assert.Equal(t, v2Writes, g.writes("app-v2", "create"))
	assert.Equal(t, 1, g.writes("app-v2", "update"))
}

func setPhase(t *testing.T, c client.Client, name, phase string) {
	t.Helper()
	b := lcGet(t, c, name)
	b.Status.Phase = phase
	require.NoError(t, c.Status().Update(context.Background(), &b))
}

// #1360: a new gate and a deleted gate reach a Promoting Bundle too: the
// Graph gets the new gate, and loses the deleted one.
func TestGateEdit_CreateAndDeleteReachPromotingBundle(t *testing.T) {
	g := newGateEditRig(t, gateEditPipeline(), gateEditBundle("app-v1", "Available", time.Now().UTC()))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase, "%+v", lcGet(t, g.c, "app-v1").Status.Conditions)

	added := gateTemplate("freeze", "prod", false)
	require.NoError(t, g.c.Create(context.Background(), added))
	lcReconcile(t, g.r, "app-v1")
	data := g.gateData(t, "app-v1", "freeze")
	assert.Equal(t, "true", data["spec"].(map[string]interface{})["expression"])

	require.NoError(t, g.c.Delete(context.Background(), added))
	lcReconcile(t, g.r, "app-v1")
	got, err := g.gc.Get(context.Background(), "default", graph.GraphNameFrom("app", "app-v1"))
	require.NoError(t, err)
	for _, n := range got.Spec.Nodes {
		assert.NotEqual(t, graph.NodePolicyGateData, n.ID, "the deleted gate is gone from the Graph")
	}
}

// gateEditTranslator counts Translate calls and fails with graph.ErrInvalid
// while a gate's expression is "invalid".
type gateEditTranslator struct {
	c     client.Client
	calls int
}

func (m *gateEditTranslator) Translate(ctx context.Context, p *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) (string, error) {
	m.calls++
	gates, err := translator.CollectGates(ctx, m.c, nil, p)
	if err != nil {
		return "", err
	}
	for _, g := range gates {
		if g.Spec.Expression == "invalid" {
			return "", &translator.BuildError{Err: fmt.Errorf("build: gate %s: %w", g.Name, graph.ErrInvalid), Gates: gates}
		}
	}
	return graph.GraphNameFrom(p.Name, b.Name), nil
}

// #1360: only a Promoting Bundle is re-translated for a gate edit. Verified,
// Superseded and Failed ones keep the gates they had (a Failed one gets the
// current gates when it recovers into Promoting).
func TestGateEdit_OnlyPromotingBundles(t *testing.T) {
	for _, tc := range []struct {
		phase string
		want  int
	}{{"Promoting", 1}, {"Verified", 0}, {"Superseded", 0}, {"Failed", 0}} {
		t.Run(tc.phase, func(t *testing.T) {
			hold := gateTemplate("hold", "prod", false)
			b := lcBundle("app-v1", "image", tc.phase, time.Now().UTC())
			b.Status.GraphRef = graph.GraphNameFrom("app", "app-v1")
			c := lcClient(gateEditPipeline(), b, hold, lcStep("app-v1", "test", "s-test", "Verified"))
			tr := &gateEditTranslator{c: c}
			r := &bundle.Reconciler{Client: c, Translator: tr,
				GraphChecker: &graphStatusReader{g: &graph.Graph{ObjectMeta: metav1.ObjectMeta{Name: b.Status.GraphRef, Namespace: "default"}}}}
			// The Graph was built with hold as it is now.
			gates, err := translator.CollectGates(context.Background(), c, nil, gateEditPipeline())
			require.NoError(t, err)
			fresh := lcGet(t, c, "app-v1")
			b = &fresh
			b.Status.PipelineSpecHash = bundle.PipelineSpecHashFor(gateEditPipeline())
			b.Status.PolicyGatesHash = translator.GatesHash(gateEditPipeline(), gates)
			if tc.phase == "Failed" {
				meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{Type: "Failed", Status: metav1.ConditionTrue, Reason: "StepFailed"})
			}
			require.NoError(t, c.Status().Update(context.Background(), b))

			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(hold), hold))
			hold.Spec.Expression = "false"
			require.NoError(t, c.Update(context.Background(), hold))
			lcReconcile(t, r, "app-v1")
			assert.Equal(t, tc.want, tr.calls)
		})
	}
}

// #1360: a gate edit that cannot be built does not fail the Bundle in flight:
// one bad edit to an org gate would fail every version. The Graph keeps the
// gates it has, GraphSynced says why, and the re-translate is retried; once
// the gate is fixed the Graph gets it.
func TestGateEdit_InvalidEditKeepsBundlePromoting(t *testing.T) {
	hold := gateTemplate("hold", "prod", false)
	c := lcClient(gateEditPipeline(), hold, lcBundle("app-v1", "image", "Available", time.Now().UTC()),
		lcStep("app-v1", "test", "s-test", "Verified"))
	tr := &gateEditTranslator{c: c}
	r := &bundle.Reconciler{Client: c, Translator: tr,
		GraphChecker: &graphStatusReader{g: &graph.Graph{ObjectMeta: metav1.ObjectMeta{Name: graph.GraphNameFrom("app", "app-v1"), Namespace: "default"}}}}
	lcReconcile(t, r, "app-v1")
	built := lcGet(t, c, "app-v1")
	require.Equal(t, "Promoting", built.Status.Phase)

	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(hold), hold))
	hold.Spec.Expression = "invalid"
	require.NoError(t, c.Update(context.Background(), hold))
	res := lcReconcile(t, r, "app-v1")
	got := lcGet(t, c, "app-v1")
	assert.Equal(t, "Promoting", got.Status.Phase, "the Bundle is not failed")
	assert.False(t, meta.IsStatusConditionTrue(got.Status.Conditions, "InvalidSpec"))
	synced := meta.FindStatusCondition(got.Status.Conditions, "GraphSynced")
	require.NotNil(t, synced)
	assert.Equal(t, metav1.ConditionFalse, synced.Status)
	assert.Contains(t, synced.Message, "keeps the gates it has")
	assert.Equal(t, built.Status.PolicyGatesHash, got.Status.PolicyGatesHash, "kept, so the edit is retried")
	assert.Positive(t, res.RequeueAfter)
	// An edit that was not applied is not recorded (#2144): the record is
	// enqueued only after the re-translate succeeds.
	assert.Empty(t, pendingActions(got, "GateChanged"), "an unbuildable edit records no GateChanged")

	hold.Spec.Expression = "false"
	require.NoError(t, c.Update(context.Background(), hold))
	lcReconcile(t, r, "app-v1")
	got = lcGet(t, c, "app-v1")
	assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, "GraphSynced"))
	assert.NotEqual(t, built.Status.PolicyGatesHash, got.Status.PolicyGatesHash)
	assert.Equal(t, 3, tr.calls)
	assert.Len(t, pendingActions(got, "GateChanged"), 1, "the applied fix is recorded")
}

// pendingActions are the names of b's outbox entries with action.
func pendingActions(b kardinalv1alpha1.Bundle, action string) []string {
	var out []string
	for _, e := range b.Status.PendingAuditEvents {
		if e.Spec.Action == action {
			out = append(out, e.Name)
		}
	}
	return out
}

// #1360: a Promoting Bundle whose Graph was built before the gates hash was
// recorded is not re-translated for the gates on upgrade: the current hash
// is recorded, and the next edit re-translates. (A Graph an earlier
// controller rendered is re-translated once for its render version: #2271,
// TestRenderVersion_StuckVersionRetranslatesOnce.)
func TestGateEdit_UpgradeRecordsHash(t *testing.T) {
	hold := gateTemplate("hold", "prod", false)
	b := lcBundle("app-v1", "image", "Promoting", time.Now().UTC())
	b.Status.GraphRef = graph.GraphNameFrom("app", "app-v1")
	b.Status.PipelineSpecHash = bundle.PipelineSpecHashFor(gateEditPipeline())
	c := lcClient(gateEditPipeline(), b, hold, lcStep("app-v1", "test", "s-test", "Verified"))
	tr := &gateEditTranslator{c: c}
	r := &bundle.Reconciler{Client: c, Translator: tr,
		GraphChecker: &graphStatusReader{g: &graph.Graph{ObjectMeta: metav1.ObjectMeta{Name: b.Status.GraphRef, Namespace: "default"}}}}
	lcReconcile(t, r, "app-v1")
	assert.NotEmpty(t, lcGet(t, c, "app-v1").Status.PolicyGatesHash)
	assert.Zero(t, tr.calls)

	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(hold), hold))
	hold.Spec.Expression = "false"
	require.NoError(t, c.Update(context.Background(), hold))
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, 1, tr.calls)
}

// #2271: a Promoting Bundle whose Graph an earlier controller rendered (no
// render version label) is re-translated once, recording the gates hash, and
// not again; a Verified or a failed Bundle's Graph is never re-translated
// for it.
func TestRenderVersion_OnlyPromotingRetranslates(t *testing.T) {
	for _, tc := range []struct {
		phase string
		want  int
	}{{"Promoting", 1}, {"Verified", 0}, {"Failed", 0}} {
		t.Run(tc.phase, func(t *testing.T) {
			hold := gateTemplate("hold", "prod", false)
			b := lcBundle("app-v1", "image", tc.phase, time.Now().UTC())
			b.Status.GraphRef = graph.GraphNameFrom("app", "app-v1")
			b.Status.PipelineSpecHash = bundle.PipelineSpecHashFor(gateEditPipeline())
			if tc.phase == "Failed" {
				meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{Type: "Failed", Status: metav1.ConditionTrue, Reason: "StepFailed"})
			}
			c := lcClient(gateEditPipeline(), b, hold, lcStep("app-v1", "test", "s-test", "Verified"))
			tr := &gateEditTranslator{c: c}
			r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: &graphStatusReader{earlier: true,
				g: &graph.Graph{ObjectMeta: metav1.ObjectMeta{Name: b.Status.GraphRef, Namespace: "default"}}}}
			lcReconcile(t, r, "app-v1")
			assert.Equal(t, tc.want, tr.calls)
			if tc.want > 0 {
				assert.NotEmpty(t, lcGet(t, c, "app-v1").Status.PolicyGatesHash, "the gates hash is recorded")
			}
		})
	}
}

// The template index selects exactly the templates: the hash read through it
// is the hash of a plain read.
func TestGateEdit_TemplateIndex(t *testing.T) {
	inst := gateTemplate("app-v1-prod-hold", "prod", false)
	inst.Labels["kardinal.io/gate-template"] = "hold"
	gen := gateTemplate("freeze-x", "prod", false)
	gen.Spec.Generated = true
	assert.Equal(t, []string{"true"}, bundle.GateTemplateKey(gateTemplate("hold", "prod", false)))
	assert.Nil(t, bundle.GateTemplateKey(inst))
	assert.Nil(t, bundle.GateTemplateKey(gen))
	assert.Nil(t, bundle.GateTemplateKey(&kardinalv1alpha1.Bundle{}))
}

// #2145: an edit reverted before the version's reconcile saw it (A, B, A)
// leaves the same rule but a new template generation. The gates hash has
// the generation, so the Graph is re-translated and its instances carry the
// template's current generation; the record says the rule is the same.
//
// Covers GATE-EDIT-02.
func TestGateEdit_RevertedEditRefreshesGeneration(t *testing.T) {
	hold := gateTemplate("hold", "prod", false)
	hold.Generation = 1
	g := newGateEditRig(t, gateEditPipeline(), hold, gateEditBundle("app-v1", "Available", time.Now().UTC()))
	lcReconcile(t, g.r, "app-v1")
	before := g.gateData(t, "app-v1", "hold")
	require.Equal(t, "1", before["generation"])

	// Edited and edited back: same spec, generation 3.
	g.editGate(t, "hold", func(p *kardinalv1alpha1.PolicyGate) { p.Generation = 3 })
	lcReconcile(t, g.r, "app-v1")
	after := g.gateData(t, "app-v1", "hold")
	assert.Equal(t, "3", after["generation"], "the instances carry the current generation")
	assert.Equal(t, before["digest"], after["digest"], "the rule is the same")
	b := lcGet(t, g.c, "app-v1")
	require.Len(t, b.Status.PendingAuditEvents, 1)
	assert.Contains(t, b.Status.PendingAuditEvents[0].Spec.Message, "default/hold was edited back to the same rule (generation 1 to 3)")
}

// #2271: a version stuck under a rendering bug recovers on upgrade. Its
// Graph was rendered by an older controller: without the VerifyOnlyGates
// collection its verify gate needs, and without the render version label.
// The new controller re-translates the Promoting Bundle once, with no change
// to the Pipeline or a gate, and the Graph gets the current rendering and
// label; a second reconcile writes nothing, and a Verified Bundle keeps its
// Graph.
func TestRenderVersion_StuckVersionRetranslatesOnce(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-time.Hour)
	verify := gateTemplate("explore-qa", "prod", false)
	verify.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Required: 1}
	cab := gateTemplate("cab", "prod", false)
	cab.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	g := newGateEditRig(t, gateEditPipeline(), verify, cab, gateEditBundle("app-v1", "Available", t0))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	gvr := g.dyn.Resource(graph.GraphGVR).Namespace("default")
	name := graph.GraphNameFrom("app", "app-v1")
	read := func() *unstructured.Unstructured {
		u, err := gvr.Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err)
		return u
	}
	nodeIDs := func(u *unstructured.Unstructured) []string {
		nodes, _, _ := unstructured.NestedSlice(u.Object, "spec", "nodes")
		var ids []string
		for _, n := range nodes {
			ids = append(ids, fmt.Sprint(n.(map[string]interface{})["id"]))
		}
		return ids
	}
	current := graph.RenderLabel
	require.Contains(t, nodeIDs(read()), graph.NodeVerifyOnlyGates)
	require.Equal(t, current, read().GetLabels()[graph.LabelRenderVersion], "the Graph carries its render version")

	// What an older controller left: a Graph without that collection and
	// without the label.
	old := func() {
		u := read()
		nodes, _, _ := unstructured.NestedSlice(u.Object, "spec", "nodes")
		var kept []interface{}
		for _, n := range nodes {
			if n.(map[string]interface{})["id"] != graph.NodeVerifyOnlyGates {
				kept = append(kept, n)
			}
		}
		require.NoError(t, unstructured.SetNestedSlice(u.Object, kept, "spec", "nodes"))
		labels := u.GetLabels()
		delete(labels, graph.LabelRenderVersion)
		u.SetLabels(labels)
		_, err := gvr.Update(ctx, u, metav1.UpdateOptions{})
		require.NoError(t, err)
	}
	old()
	require.NotContains(t, nodeIDs(read()), graph.NodeVerifyOnlyGates)
	updates := g.writes("app-v1", "update")
	hash := lcGet(t, g.c, "app-v1").Status.PolicyGatesHash

	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, updates+1, g.writes("app-v1", "update"), "re-translated once, with no Pipeline or gate change")
	assert.Contains(t, nodeIDs(read()), graph.NodeVerifyOnlyGates, "the Graph has the current rendering")
	assert.Equal(t, current, read().GetLabels()[graph.LabelRenderVersion])
	got := lcGet(t, g.c, "app-v1")
	assert.Equal(t, hash, got.Status.PolicyGatesHash, "the gates did not change")
	assert.Empty(t, got.Status.PendingAuditEvents, "no gate changed: no GateChanged record")

	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, updates+1, g.writes("app-v1", "update"), "once")

	// A Verified Bundle keeps the Graph it was promoted with.
	setPhase(t, g.c, "app-v1", "Verified")
	old()
	updates = g.writes("app-v1", "update")
	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, updates, g.writes("app-v1", "update"), "a Verified Bundle is not re-translated")
	assert.NotContains(t, nodeIDs(read()), graph.NodeVerifyOnlyGates)
}

// gateChangedSpecs are the GateChanged records of Bundle b, written
// (AuditEvents) or still in its outbox.
func gateChangedSpecs(t *testing.T, c client.Client, b string) []kardinalv1alpha1.AuditEventSpec {
	t.Helper()
	var out []kardinalv1alpha1.AuditEventSpec
	for _, e := range lcGet(t, c, b).Status.PendingAuditEvents {
		if e.Spec.Action == bundle.AuditActionGateChanged {
			out = append(out, e.Spec)
		}
	}
	var evs kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &evs))
	for _, ev := range evs.Items {
		if ev.Spec.Action == bundle.AuditActionGateChanged && ev.Spec.BundleName == b {
			out = append(out, ev.Spec)
		}
	}
	return out
}

// #2271: the re-render of a Graph an earlier controller rendered records
// the rule changes it applies. v0.12 kept the rule each template had when
// the version started; a template edited since (window) takes its current
// rule at the upgrade, and one deleted since (gone) no longer applies: one
// GateChanged record names both, with no stamp before (v0.12 copies had
// none) and the template's after. A template whose rule did not change
// (cab), though its copy gains a stamp, is not in it. A second reconcile
// records nothing more.
//
// Covers GATE-EDIT-03.
func TestRenderVersion_UpgradeRecordsRuleChange(t *testing.T) {
	ctx := context.Background()
	cab := gateTemplate("cab", "prod", false)
	cab.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	window := gateTemplate("window", "prod", false)
	window.Generation = 4 // the fake client sets none
	g := newGateEditRig(t, gateEditPipeline(), cab, window, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	require.Empty(t, gateChangedSpecs(t, g.c, "app-v1"))
	gvr := g.dyn.Resource(graph.GraphGVR).Namespace("default")
	name := graph.GraphNameFrom("app", "app-v1")

	// What v0.12 left: no label, no stamps, window's rule from before an
	// edit, and a template deleted since.
	u, err := gvr.Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	nodes, _, _ := unstructured.NestedSlice(u.Object, "spec", "nodes")
	for _, n := range nodes {
		node := n.(map[string]interface{})
		if node["id"] != graph.NodePolicyGateData {
			continue
		}
		def := node["def"].(map[string]interface{})
		templates := def["templates"].([]interface{})
		for _, raw := range templates {
			tmpl := raw.(map[string]interface{})
			delete(tmpl, "digest")
			delete(tmpl, "generation")
			delete(tmpl, "uid")
			if tmpl["template"] == "window" {
				tmpl["spec"].(map[string]interface{})["expression"] = "false"
			}
		}
		def["templates"] = append(templates, map[string]interface{}{"template": "gone", "templateNamespace": "default",
			"spec": map[string]interface{}{"expression": "true", "generated": true}})
	}
	require.NoError(t, unstructured.SetNestedSlice(u.Object, nodes, "spec", "nodes"))
	labels := u.GetLabels()
	delete(labels, graph.LabelRenderVersion)
	u.SetLabels(labels)
	_, err = gvr.Update(ctx, u, metav1.UpdateOptions{})
	require.NoError(t, err)

	lcReconcile(t, g.r, "app-v1")
	got := gateChangedSpecs(t, g.c, "app-v1")
	require.Len(t, got, 1, "one record for the re-render")
	var current kardinalv1alpha1.PolicyGate
	require.NoError(t, g.c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "window"}, &current))
	require.Equal(t, int64(4), current.Generation)
	assert.Equal(t, []kardinalv1alpha1.AuditGateChange{
		{Template: "gone", Namespace: "default"},
		{Template: "window", Namespace: "default", NewDigest: graph.TemplateDigest(current), NewGeneration: 4},
	}, got[0].Gates)
	assert.Equal(t, "*", got[0].Environment, "window applies to prod, a deleted template to no environment: the record names several")
	assert.Contains(t, got[0].Message, "on upgrade")
	assert.Contains(t, got[0].Message, "default/window takes its current rule")
	assert.Contains(t, got[0].Message, "default/gone no longer applies")
	assert.NotContains(t, got[0].Message, "cab")

	lcReconcile(t, g.r, "app-v1")
	assert.Len(t, gateChangedSpecs(t, g.c, "app-v1"), 1, "recorded once")
}

// #2411: a Graph rendered before render version 4 copied a person's
// verdict onto a verify gate of agents only, which counted it; re-rendered on
// upgrade it no longer does, and the version's trail says why: one
// GateChanged for that gate, naming the rule change (the template is
// unchanged), and none for a verify gate that lists people or nobody, or
// for a gate without verify. A Graph already at the current render version
// records none, and a second reconcile records nothing more.
//
// Covers GATE-VERIFY-12.
func TestRenderVersion_UpgradeRecordsVerifyRuleChange(t *testing.T) {
	agents := gateTemplate("qa-only", "prod", false)
	agents.Generation = 2
	agents.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Required: 1, AllowedAgents: []string{"qa-explorer"}}
	lead := gateTemplate("qa-or-lead", "prod", false)
	lead.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "smoke", Required: 1, AllowedAgents: []string{"qa-explorer"},
		AllowedUsers: []string{"lead@example.com"}}
	anyone := gateTemplate("anyone", "prod", false)
	anyone.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "a11y", Required: 1}
	cab := gateTemplate("cab", "prod", false)
	cab.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	g := newGateEditRig(t, gateEditPipeline(), agents, lead, anyone, cab, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	require.Empty(t, gateChangedSpecs(t, g.c, "app-v1"))

	// A Graph at the current render version: nothing to say. Nor for one
	// rendered from render version 4 on, which the re-render still renders
	// again (a release branch's label, a later controller's): its gates
	// already count agents only (QA-5 on #2429).
	g.renderOlder(t, "app-v1", graph.RenderLabel, func(map[string]interface{}) {})
	lcReconcile(t, g.r, "app-v1")
	require.Empty(t, gateChangedSpecs(t, g.c, "app-v1"))
	for _, label := range []string{"4-v0.13.1", "5"} {
		updates := g.writes("app-v1", "update")
		g.renderOlder(t, "app-v1", label, func(map[string]interface{}) {})
		lcReconcile(t, g.r, "app-v1")
		require.Greater(t, g.writes("app-v1", "update"), updates+1, "label %s: rendered again", label)
		require.Empty(t, gateChangedSpecs(t, g.c, "app-v1"), "label %s: no rule change to record", label)
	}

	// A Graph rendered before #2411 (render version 3).
	g.renderOlder(t, "app-v1", "3", func(map[string]interface{}) {})
	lcReconcile(t, g.r, "app-v1")
	got := gateChangedSpecs(t, g.c, "app-v1")
	require.Len(t, got, 1, "one record, for the gate of agents only")
	var current kardinalv1alpha1.PolicyGate
	require.NoError(t, g.c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "qa-only"}, &current))
	d := graph.TemplateDigest(current)
	assert.Equal(t, []kardinalv1alpha1.AuditGateChange{{Template: "qa-only", Namespace: "default",
		OldDigest: d, NewDigest: d, OldGeneration: current.Generation, NewGeneration: current.Generation}}, got[0].Gates)
	assert.Equal(t, "prod", got[0].Environment)
	assert.Contains(t, got[0].Message, "the controller's verify rule changed (v0.13): only the agents listed in verify.allowedAgents count")

	lcReconcile(t, g.r, "app-v1")
	assert.Len(t, gateChangedSpecs(t, g.c, "app-v1"), 1, "recorded once")
}

// #2443: an upgrade's re-render stores its audit records before it writes
// the Graph with the current render label. While the Bundle's status write
// that would store them conflicts, the Graph stays as the earlier controller
// rendered it; once they are stored the next reconcile renders it again, and
// each record (a template rule changed under the version, #2271, and a
// verify gate of agents only, #2411) is written exactly once. Before #2443 a
// conflict after the relabel lost them for good: the next reconcile found a
// current Graph.
//
// Covers GATE-EDIT-05.
func TestRenderVersion_RecordsBeforeRelabel(t *testing.T) {
	ctx := context.Background()
	window := gateTemplate("window", "prod", false)
	window.Generation = 4
	agents := gateTemplate("qa-only", "prod", false)
	agents.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Required: 1, AllowedAgents: []string{"qa-explorer"}}
	conflicts := 0
	funcs := interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
		patch client.Patch, opts ...client.SubResourcePatchOption) error {
		if _, ok := obj.(*kardinalv1alpha1.Bundle); ok && sub == "status" && conflicts > 0 {
			conflicts--
			return apierrors.NewConflict(schema.GroupResource{Group: "kardinal.io", Resource: "bundles"}, obj.GetName(),
				errors.New("the object has been modified"))
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}}
	g := newGateEditRigWith(t, funcs, gateEditPipeline(), window, agents, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	// What a controller before render version 4 left, with window's rule
	// from before an edit.
	g.renderOlder(t, "app-v1", "3", func(tmpl map[string]interface{}) {
		if tmpl["template"] == "window" {
			tmpl["digest"], tmpl["generation"] = "sha256:old", "3"
			tmpl["spec"].(map[string]interface{})["expression"] = "false"
		}
	})
	label := func() string {
		got, err := g.gc.Get(ctx, "default", graph.GraphNameFrom("app", "app-v1"))
		require.NoError(t, err)
		return got.Labels[graph.LabelRenderVersion]
	}

	// The status write that would store the records conflicts: nothing is
	// recorded, and the Graph is not rendered again either.
	conflicts = 1
	res, err := g.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}})
	require.NoError(t, err, "a Conflict is requeued")
	require.Zero(t, conflicts, "the status write conflicted")
	assert.Equal(t, time.Second, res.RequeueAfter)
	assert.Equal(t, "3", label(), "not rendered again before its records are stored")
	assert.Empty(t, gateChangedSpecs(t, g.c, "app-v1"))

	// Stored first, the Graph still the earlier controller's.
	res = lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, "3", label())
	assert.Equal(t, time.Second, res.RequeueAfter, "rendered again at once")
	stored := gateChangedSpecs(t, g.c, "app-v1")
	require.Len(t, stored, 2)

	// Then rendered again; each record written once.
	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, graph.RenderLabel, label())
	lcReconcile(t, g.r, "app-v1")
	got := gateChangedSpecs(t, g.c, "app-v1")
	require.Len(t, got, 2, "each record once, stored and written")
	var messages []string
	for _, e := range got {
		messages = append(messages, e.Message)
	}
	assert.Contains(t, strings.Join(messages, "\n"), "default/window takes its current rule (generation 4)")
	assert.Contains(t, strings.Join(messages, "\n"), "the controller's verify rule changed (v0.13)")
	assert.Empty(t, lcGet(t, g.c, "app-v1").Status.PendingAuditEvents, "every record written")
}

// upgradeRig is a rig whose Bundle app-v1 is Promoting on a Graph an
// earlier controller (render version 3) rendered, with window's rule from
// before an edit and a verify gate of agents only: its re-render has two
// records to make. funcs run first on every client call.
func upgradeRig(t *testing.T, funcs interceptor.Funcs) (*gateEditRig, func() string) {
	t.Helper()
	window := gateTemplate("window", "prod", false)
	window.Generation = 4
	agents := gateTemplate("qa-only", "prod", false)
	agents.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Required: 1, AllowedAgents: []string{"qa-explorer"}}
	g := newGateEditRigWith(t, funcs, gateEditPipeline(), window, agents, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	g.renderOlder(t, "app-v1", "3", func(tmpl map[string]interface{}) {
		if tmpl["template"] == "window" {
			tmpl["digest"], tmpl["generation"] = "sha256:old", "3"
			tmpl["spec"].(map[string]interface{})["expression"] = "false"
		}
	})
	return g, func() string {
		got, err := g.gc.Get(context.Background(), "default", graph.GraphNameFrom("app", "app-v1"))
		require.NoError(t, err)
		return got.Labels[graph.LabelRenderVersion]
	}
}

// auditEventCreate is an interceptor Create that answers an AuditEvent's
// create with fail(dryRun) when it returns an error.
func auditEventCreate(fail func(dryRun bool) error) func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
	return func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*kardinalv1alpha1.AuditEvent); ok {
			o := &client.CreateOptions{}
			o.ApplyOptions(opts)
			if err := fail(len(o.DryRun) > 0); err != nil {
				return err
			}
		}
		return c.Create(ctx, obj, opts...)
	}
}

// #2443 (QA-5): a record the API server would never accept (a dry-run
// create refused as Invalid) is not waited for: it is dropped, with an
// AuditRecordRefused Warning Event and
// kardinal_audit_events_dropped_total{reason="refused"}, and the Graph is
// rendered again at once, so the re-render (#2271's recovery) cannot loop on
// it.
//
// Covers GATE-EDIT-05.
func TestRenderVersion_RefusedRecordIsDropped(t *testing.T) {
	refuse := true
	g, label := upgradeRig(t, interceptor.Funcs{Create: auditEventCreate(func(dryRun bool) error {
		if dryRun && refuse {
			return apierrors.NewInvalid(schema.GroupKind{Group: "kardinal.io", Kind: "AuditEvent"}, "x", nil)
		}
		return nil
	})})
	rec := events.NewFakeRecorder(10)
	g.r.Recorder = rec
	dropped := testutil.ToFloat64(audit.Dropped.WithLabelValues("Bundle", "refused"))

	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, graph.RenderLabel, label(), "rendered again without the refused records")
	assert.Empty(t, gateChangedSpecs(t, g.c, "app-v1"), "neither stored nor written")
	assert.Equal(t, dropped+2, testutil.ToFloat64(audit.Dropped.WithLabelValues("Bundle", "refused")))
	var warned []string
	for len(rec.Events) > 0 {
		warned = append(warned, <-rec.Events)
	}
	assert.Len(t, slices.DeleteFunc(warned, func(e string) bool { return !strings.Contains(e, "AuditRecordRefused") }), 2, "%v", warned)
}

// #2443 (QA-5): an error that may pass (the API server timed out the
// dry-run create) keeps the Graph as the earlier controller left it, and the
// reconcile backs off (30 s, as any Graph sync error) rather than looping
// every second; once it passes the records are stored, then the Graph is
// rendered again.
//
// Covers GATE-EDIT-05.
func TestRenderVersion_TransientRecordErrorBacksOff(t *testing.T) {
	timeout := true
	g, label := upgradeRig(t, interceptor.Funcs{Create: auditEventCreate(func(dryRun bool) error {
		if dryRun && timeout {
			return apierrors.NewServerTimeout(schema.GroupResource{Group: "kardinal.io", Resource: "auditevents"}, "create", 1)
		}
		return nil
	})})
	res := lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "backs off, no 1 s loop")
	assert.Equal(t, "3", label(), "kept until its records can be stored")
	assert.Empty(t, gateChangedSpecs(t, g.c, "app-v1"))

	timeout = false
	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, "3", label())
	require.Len(t, gateChangedSpecs(t, g.c, "app-v1"), 2, "stored")
	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, graph.RenderLabel, label())
	assert.Len(t, gateChangedSpecs(t, g.c, "app-v1"), 2, "each once")
}

// #2443 (QA-5): a record stored in the Bundle's outbox counts as recorded,
// whether or not its AuditEvent is written yet: the outbox writes it,
// retrying. Here the controller's cache has not seen the status write that
// stored the records (so this reconcile flushes nothing) and every
// AuditEvent write fails; read as the API server holds it, the outbox has
// them, and the Graph is rendered again all the same, the records still
// waiting in the outbox.
//
// Covers GATE-EDIT-05.
func TestRenderVersion_StoredRecordCountsAsRecorded(t *testing.T) {
	stale, failWrites := false, false
	g, label := upgradeRig(t, interceptor.Funcs{
		Create: auditEventCreate(func(dryRun bool) error {
			if !dryRun && failWrites {
				return apierrors.NewInternalError(errors.New("etcd unavailable"))
			}
			return nil
		}),
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if b, ok := obj.(*kardinalv1alpha1.Bundle); ok && stale {
				b.Status.PendingAuditEvents = nil // the cache has not seen them stored
			}
			return nil
		},
	})
	g.r.APIReader = g.api
	failWrites = true
	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, "3", label())
	stored := func() int {
		var b kardinalv1alpha1.Bundle
		require.NoError(t, g.api.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "app-v1"}, &b))
		n := 0
		for _, e := range b.Status.PendingAuditEvents {
			if e.Spec.Action == bundle.AuditActionGateChanged {
				n++
			}
		}
		return n
	}
	require.Equal(t, 2, stored(), "stored in the outbox, not written")

	stale = true
	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, graph.RenderLabel, label(), "rendered again: the stored records count as recorded")
	assert.Equal(t, 2, stored(), "the records still wait in the outbox, which writes them once the writes pass")
	var evs kardinalv1alpha1.AuditEventList
	require.NoError(t, g.api.List(context.Background(), &evs))
	for _, ev := range evs.Items {
		assert.NotEqual(t, bundle.AuditActionGateChanged, ev.Spec.Action, "no GateChanged written while the writes fail")
	}
}

// renderOlder rewrites Bundle b's Graph to what an earlier controller left:
// the render-version label set to label ("" removes it), and edit applied to
// each PolicyGateData template entry.
func (g *gateEditRig) renderOlder(t *testing.T, b, label string, edit func(tmpl map[string]interface{})) {
	t.Helper()
	ctx := context.Background()
	gvr := g.dyn.Resource(graph.GraphGVR).Namespace("default")
	name := graph.GraphNameFrom("app", b)
	u, err := gvr.Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	nodes, _, _ := unstructured.NestedSlice(u.Object, "spec", "nodes")
	for _, n := range nodes {
		node := n.(map[string]interface{})
		if node["id"] != graph.NodePolicyGateData {
			continue
		}
		for _, raw := range node["def"].(map[string]interface{})["templates"].([]interface{}) {
			edit(raw.(map[string]interface{}))
		}
	}
	require.NoError(t, unstructured.SetNestedSlice(u.Object, nodes, "spec", "nodes"))
	labels := u.GetLabels()
	if label == "" {
		delete(labels, graph.LabelRenderVersion)
	} else {
		labels[graph.LabelRenderVersion] = label
	}
	u.SetLabels(labels)
	_, err = gvr.Update(ctx, u, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// #2271: a template created under v0.12 after the version started reaches
// it at the upgrade: one record says it now applies, in its environment,
// with no stamp before. The Bundle is as v0.12 leaves a Promoting one (no
// gates hash), so the render-version re-render, not a gate-change rebuild,
// applies it.
//
// Covers GATE-EDIT-03.
func TestRenderVersion_UpgradeRecordsCreatedTemplate(t *testing.T) {
	ctx := context.Background()
	cab := gateTemplate("cab", "prod", false)
	cab.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	g := newGateEditRig(t, gateEditPipeline(), cab, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	g.renderOlder(t, "app-v1", "", func(tmpl map[string]interface{}) {
		delete(tmpl, "digest")
		delete(tmpl, "generation")
		delete(tmpl, "uid")
	})
	b := lcGet(t, g.c, "app-v1")
	b.Status.PolicyGatesHash = ""
	require.NoError(t, g.c.Status().Update(ctx, &b))
	late := gateTemplate("late", "prod", false)
	late.Generation = 3
	require.NoError(t, g.c.Create(ctx, late))

	lcReconcile(t, g.r, "app-v1")
	got := gateChangedSpecs(t, g.c, "app-v1")
	require.Len(t, got, 1)
	assert.Equal(t, []kardinalv1alpha1.AuditGateChange{
		{Template: "late", Namespace: "default", NewDigest: graph.TemplateDigest(*late), NewGeneration: 3},
	}, got[0].Gates)
	assert.Equal(t, "prod", got[0].Environment)
	assert.Contains(t, got[0].Message, "on upgrade")
	assert.Contains(t, got[0].Message, "default/late now applies (generation 3)")
}

// #2271: at a later RenderVersion bump the copies carry a stamp: the record
// names it as the one before, and the template's as the one after.
//
// Covers GATE-EDIT-03.
func TestRenderVersion_UpgradeRecordsOldStamp(t *testing.T) {
	window := gateTemplate("window", "prod", false)
	window.Generation = 4
	g := newGateEditRig(t, gateEditPipeline(), window, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	g.renderOlder(t, "app-v1", "2", func(tmpl map[string]interface{}) {
		tmpl["digest"], tmpl["generation"] = "sha256:old", "2"
		tmpl["spec"].(map[string]interface{})["expression"] = "false"
	})

	lcReconcile(t, g.r, "app-v1")
	got := gateChangedSpecs(t, g.c, "app-v1")
	require.Len(t, got, 1)
	assert.Equal(t, []kardinalv1alpha1.AuditGateChange{
		{Template: "window", Namespace: "default", OldDigest: "sha256:old", OldGeneration: 2, NewDigest: graph.TemplateDigest(*window), NewGeneration: 4},
	}, got[0].Gates)
	assert.Equal(t, "prod", got[0].Environment)
	assert.Contains(t, got[0].Message, "default/window takes its current rule (generation 4)")
}

// #2346: a Graph v0.12.3 rendered carries render version 2, which main
// renders differently from: it is re-rendered once, in place. A Graph whose
// rendering is otherwise the current one keeps every node and the same spec,
// gets only the current label, and records no GateChanged; a second
// reconcile writes nothing.
func TestRenderVersion_V0123LabelRetranslatesOnce(t *testing.T) {
	ctx := context.Background()
	verify := gateTemplate("explore-qa", "prod", false)
	verify.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Required: 1}
	cab := gateTemplate("cab", "prod", false)
	cab.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	g := newGateEditRig(t, gateEditPipeline(), verify, cab, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	gvr := g.dyn.Resource(graph.GraphGVR).Namespace("default")
	name := graph.GraphNameFrom("app", "app-v1")
	read := func() *unstructured.Unstructured {
		u, err := gvr.Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err)
		return u
	}
	before := read()
	g.renderOlder(t, "app-v1", "2", func(map[string]interface{}) {})
	require.Equal(t, "2", read().GetLabels()[graph.LabelRenderVersion])
	updates := g.writes("app-v1", "update")

	lcReconcile(t, g.r, "app-v1")
	after := read()
	assert.Equal(t, updates+1, g.writes("app-v1", "update"), "re-rendered once")
	assert.Equal(t, graph.RenderLabel, after.GetLabels()[graph.LabelRenderVersion])
	assert.Equal(t, before.GetUID(), after.GetUID(), "in place")
	beforeSpec, _, _ := unstructured.NestedMap(before.Object, "spec")
	afterSpec, _, _ := unstructured.NestedMap(after.Object, "spec")
	assert.Equal(t, beforeSpec, afterSpec, "the same nodes and data: kro applies it as a no-op")
	assert.Empty(t, gateChangedSpecs(t, g.c, "app-v1"), "no rule changed")

	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, updates+1, g.writes("app-v1", "update"), "once")
}

// #2338: relabelling a template's kardinal.io/applies-to onto another
// environment of a version in flight changes no rule (the digest and
// generation stay) but moves its gate: one GateChanged records the move.
//
// Covers GATE-EDIT-04.
func TestGateEdit_AppliesToMoveRecorded(t *testing.T) {
	window := gateTemplate("window", "prod", false)
	window.Generation = 2
	g := newGateEditRig(t, gateEditPipeline(), window, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	require.Empty(t, gateChangedSpecs(t, g.c, "app-v1"))

	g.editGate(t, "window", func(p *kardinalv1alpha1.PolicyGate) { p.Labels["kardinal.io/applies-to"] = "test,prod" })
	lcReconcile(t, g.r, "app-v1")
	got := gateChangedSpecs(t, g.c, "app-v1")
	require.Len(t, got, 1)
	require.Len(t, got[0].Gates, 1)
	gc := got[0].Gates[0]
	assert.Equal(t, "window", gc.Template)
	assert.Equal(t, gc.OldDigest, gc.NewDigest, "the rule did not change")
	assert.Equal(t, int64(2), gc.OldGeneration)
	assert.Equal(t, int64(2), gc.NewGeneration)
	assert.Equal(t, []string{"test"}, gc.EnvironmentsAdded)
	assert.Contains(t, got[0].Message, "default/window moved (generation 2): now also in test")

	lcReconcile(t, g.r, "app-v1")
	assert.Len(t, gateChangedSpecs(t, g.c, "app-v1"), 1, "recorded once")
}

// #2338: an applies-to edit made under v0.12 after the version started
// reaches it at the upgrade: the copied rule is the same, the environments
// are not, and the re-render records the move.
//
// Covers GATE-EDIT-04.
func TestRenderVersion_UpgradeRecordsMove(t *testing.T) {
	ctx := context.Background()
	window := gateTemplate("window", "prod", false)
	window.Generation = 2
	g := newGateEditRig(t, gateEditPipeline(), window, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	g.renderOlder(t, "app-v1", "", func(tmpl map[string]interface{}) {
		delete(tmpl, "digest")
		delete(tmpl, "generation")
		delete(tmpl, "uid")
	})
	b := lcGet(t, g.c, "app-v1")
	b.Status.PolicyGatesHash = ""
	require.NoError(t, g.c.Status().Update(ctx, &b))
	g.editGate(t, "window", func(p *kardinalv1alpha1.PolicyGate) { p.Labels["kardinal.io/applies-to"] = "test,prod" })

	lcReconcile(t, g.r, "app-v1")
	got := gateChangedSpecs(t, g.c, "app-v1")
	require.Len(t, got, 1)
	require.Len(t, got[0].Gates, 1)
	assert.Equal(t, []string{"test"}, got[0].Gates[0].EnvironmentsAdded)
	assert.Contains(t, got[0].Message, "on upgrade")
	assert.Contains(t, got[0].Message, "default/window moves (generation 2): now also in test")
}

// #2338: a Pipeline edit that lands before the version's first reconcile
// after the upgrade re-translates it in the regular path, which writes the
// current render version first: the comparison with what v0.12 copied runs
// there, and the rule a template edited under v0.12 now has is recorded
// once, as on upgrade.
//
// Covers GATE-EDIT-03.
func TestRenderVersion_PipelineEditBeforeFirstReconcile(t *testing.T) {
	ctx := context.Background()
	window := gateTemplate("window", "prod", false)
	window.Generation = 4
	g := newGateEditRig(t, gateEditPipeline(), window, gateEditBundle("app-v1", "Available", time.Now().UTC().Add(-time.Hour)))
	lcReconcile(t, g.r, "app-v1")
	g.renderOlder(t, "app-v1", "", func(tmpl map[string]interface{}) {
		delete(tmpl, "digest")
		delete(tmpl, "generation")
		delete(tmpl, "uid")
		tmpl["spec"].(map[string]interface{})["expression"] = "false"
	})
	b := lcGet(t, g.c, "app-v1")
	b.Status.PolicyGatesHash = ""
	require.NoError(t, g.c.Status().Update(ctx, &b))
	var p kardinalv1alpha1.Pipeline
	require.NoError(t, g.c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "app"}, &p))
	p.Spec.Environments[1].Bake = &kardinalv1alpha1.BakeConfig{Minutes: 1}
	require.NoError(t, g.c.Update(ctx, &p))

	lcReconcile(t, g.r, "app-v1")
	got := gateChangedSpecs(t, g.c, "app-v1")
	require.Len(t, got, 1)
	assert.Equal(t, []kardinalv1alpha1.AuditGateChange{
		{Template: "window", Namespace: "default", NewDigest: graph.TemplateDigest(*window), NewGeneration: 4},
	}, got[0].Gates)
	assert.Contains(t, got[0].Message, "on upgrade")
	assert.Contains(t, got[0].Message, "default/window takes its current rule (generation 4)")

	lcReconcile(t, g.r, "app-v1")
	assert.Len(t, gateChangedSpecs(t, g.c, "app-v1"), 1, "recorded once")
}
