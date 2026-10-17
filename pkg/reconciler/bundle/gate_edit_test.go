// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// gateEditRig is a Bundle reconciler with the real Translator and a fake
// Graph API, so a test reads the gate data each Bundle's Graph was built with.
type gateEditRig struct {
	c   client.Client
	dyn *dynfake.FakeDynamicClient
	gc  *graph.GraphClient
	r   *bundle.Reconciler
}

func newGateEditRig(t *testing.T, objs ...client.Object) *gateEditRig {
	t.Helper()
	c := indexedBuilder(newScheme()).
		WithIndex(&kardinalv1alpha1.PolicyGate{}, bundle.GateTemplateIndex, bundle.GateTemplateKey).
		WithObjects(objs...).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}, &kardinalv1alpha1.Pipeline{}, &kardinalv1alpha1.PromotionStep{}).
		Build()
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"})
	gc := graph.NewGraphClient(dyn, zerolog.Nop())
	r := &bundle.Reconciler{Client: c, GraphChecker: gc,
		Translator: translator.New(gc, graph.NewBuilder(), c, nil, zerolog.Nop())}
	r.UseTemplateIndex()
	return &gateEditRig{c: c, dyn: dyn, gc: gc, r: r}
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
// recorded is not re-translated on upgrade: the current hash is recorded,
// and the next edit re-translates.
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
