// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
)

// promotedTranslator records the environments the Graph of each Translate
// would promote (graph.PromotedEnvironments of the Bundle it is given).
type promotedTranslator struct {
	pipeline *kardinalv1alpha1.Pipeline
	promoted [][]string
}

func (m *promotedTranslator) Translate(_ context.Context, p *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) (string, error) {
	envs, err := graph.PromotedEnvironments(p, b)
	if err != nil {
		return "", err
	}
	m.promoted = append(m.promoted, envs)
	return graph.GraphNameFrom(p.Name, b.Name), nil
}

var evidenceT0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// evidenceBundle is a Bundle of app deploying image tag, with the given
// environments Verified at evidenceT0 plus i hours.
func evidenceBundle(name, tag, phase string, verified ...string) *kardinalv1alpha1.Bundle {
	b := lcBundle(name, "image", phase, evidenceT0.Add(-24*time.Hour))
	b.Spec.Images = []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/acme/app", Tag: tag}}
	for i, env := range verified {
		at := metav1.NewTime(evidenceT0.Add(time.Duration(i) * time.Hour))
		b.Status.Environments = append(b.Status.Environments,
			kardinalv1alpha1.EnvironmentStatus{Name: env, Phase: "Verified", HealthCheckedAt: &at})
	}
	return b
}

// rollbackTo is a targetOnly Bundle for prod deploying tag.
func rollbackTo(tag string) *kardinalv1alpha1.Bundle {
	b := evidenceBundle("app-rollback", tag, "Available")
	b.CreationTimestamp = metav1.NewTime(evidenceT0.Add(48 * time.Hour))
	b.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: "prod", TargetOnly: true}
	return b
}

func evidenceRig(t *testing.T, objs ...client.Object) (*bundle.Reconciler, *promotedTranslator, client.Client) {
	t.Helper()
	p := lcPipeline("app", lcEnvs("test", "uat", "prod")...)
	c := lcClient(append([]client.Object{p}, objs...)...)
	tr := &promotedTranslator{pipeline: p}
	now := evidenceT0.Add(50 * time.Hour)
	return &bundle.Reconciler{Client: c, Translator: tr, NowFn: func() time.Time { return now }}, tr, c
}

// #1311: a rollback whose artifacts another Bundle verified in test and uat
// carries that verification, newest first, and promotes only prod. The
// decision and the evidence are written with the Promoting phase, and a
// later reconcile keeps them.
func TestEvidence_CarriesUpstreamVerification(t *testing.T) {
	older := evidenceBundle("app-v1", "1.0", "Superseded", "test", "uat", "prod")
	newer := evidenceBundle("app-v1-again", "1.0", "Superseded", "test")
	newer.Status.Environments[0].HealthCheckedAt = ptrTime(evidenceT0.Add(5 * time.Hour))
	other := evidenceBundle("app-v2", "2.0", "Verified", "test", "uat", "prod")
	r, tr, c := evidenceRig(t, older, newer, other, rollbackTo("1.0"))

	lcReconcile(t, r, "app-rollback")
	got := lcGet(t, c, "app-rollback")
	require.Equal(t, "Promoting", got.Status.Phase)
	require.Equal(t, [][]string{{"prod"}}, tr.promoted, "the Graph promotes only prod")
	cond := meta.FindStatusCondition(got.Status.Conditions, "UpstreamEvidence")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "Carried", cond.Reason)
	assert.Equal(t, "promotes only prod; these artifacts were verified in test by app-v1-again, uat by app-v1", cond.Message)
	require.Len(t, got.Status.UpstreamEvidence, 2)
	assert.Equal(t, "test", got.Status.UpstreamEvidence[0].Environment)
	assert.Equal(t, "app-v1-again", got.Status.UpstreamEvidence[0].Bundle, "the newest verification")
	assert.Equal(t, int64(45*60), got.Status.UpstreamEvidence[0].SoakMinutes)
	assert.Equal(t, "app-v1", got.Status.UpstreamEvidence[1].Bundle)
	assert.True(t, evidenceT0.Add(time.Hour).Equal(got.Status.UpstreamEvidence[1].VerifiedAt.Time))

	// The source going away changes nothing: the evidence is a copy.
	require.NoError(t, c.Delete(context.Background(), older))
	lcReconcile(t, r, "app-rollback")
	assert.Equal(t, got.Status.UpstreamEvidence[1].Bundle, lcGet(t, c, "app-rollback").Status.UpstreamEvidence[1].Bundle)
	assert.Len(t, tr.promoted, 1, "no second translate")
}

func ptrTime(t time.Time) *metav1.Time { m := metav1.NewTime(t); return &m }

// #1311: the rollback promotes through the upstream environments when one of
// them has no verification of the same artifacts by a Bundle that is not
// rejected, or when a verify gate on prod reads an upstream's verdicts. The
// condition says why, and the decision is kept.
func TestEvidence_PromotesThroughWithoutIt(t *testing.T) {
	rejected := evidenceBundle("app-v1", "1.0", "Rejected", "test", "uat")
	rejected.Spec.Rejected = &kardinalv1alpha1.BundleRejection{Reason: "bad", By: "ops"}
	for _, tc := range []struct {
		name, reason, message string
		objs                  []client.Object
	}{
		{name: "uat not verified", reason: "NotVerifiedUpstream",
			message: "no other version of pipeline app with these artifacts was verified in uat, so this version is promoted through test, uat first",
			objs:    []client.Object{evidenceBundle("app-v1", "1.0", "Failed", "test")}},
		{name: "other artifacts", reason: "NotVerifiedUpstream",
			message: "no other version of pipeline app with these artifacts was verified in test, uat, so this version is promoted through test, uat first",
			objs:    []client.Object{evidenceBundle("app-v2", "2.0", "Verified", "test", "uat")}},
		{name: "rejected source", reason: "NotVerifiedUpstream",
			message: "no other version of pipeline app with these artifacts was verified in test, uat, so this version is promoted through test, uat first",
			objs:    []client.Object{rejected}},
		{name: "verify gate on prod", reason: "VerifyGate",
			message: "verify gate default/smoke on prod reads the verdicts of an upstream environment, so this version is promoted through test, uat first",
			objs: []client.Object{evidenceBundle("app-v1", "1.0", "Superseded", "test", "uat"), func() *kardinalv1alpha1.PolicyGate {
				g := gateTemplate("smoke", "prod", false)
				g.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "smoke"}
				return g
			}()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, tr, c := evidenceRig(t, append(tc.objs, rollbackTo("1.0"))...)
			lcReconcile(t, r, "app-rollback")
			got := lcGet(t, c, "app-rollback")
			require.Equal(t, [][]string{{"test", "uat", "prod"}}, tr.promoted)
			assert.Empty(t, got.Status.UpstreamEvidence)
			cond := meta.FindStatusCondition(got.Status.Conditions, "UpstreamEvidence")
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tc.reason, cond.Reason)
			assert.Equal(t, tc.message, cond.Message)
		})
	}
}

// #1311: a verification a Bundle itself carries counts for the next one, so
// the evidence outlives the Bundle that made it; a Bundle without
// targetOnly never looks for any.
func TestEvidence_CarriedEvidenceCountsAndOnlyTargetOnly(t *testing.T) {
	first := rollbackTo("1.0")
	first.Name = "app-rollback-1"
	first.Status.Phase = "Superseded"
	first.Status.UpstreamEvidence = []kardinalv1alpha1.UpstreamEvidence{
		{Environment: "test", Bundle: "app-v1", VerifiedAt: metav1.NewTime(evidenceT0)},
		{Environment: "uat", Bundle: "app-v1", VerifiedAt: metav1.NewTime(evidenceT0.Add(time.Hour))},
	}
	plain := evidenceBundle("app-plain", "1.0", "Available")
	plain.CreationTimestamp = metav1.NewTime(evidenceT0.Add(49 * time.Hour))
	plain.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: "prod"}
	r, tr, c := evidenceRig(t, first, rollbackTo("1.0"))
	lcReconcile(t, r, "app-rollback")
	got := lcGet(t, c, "app-rollback")
	require.Len(t, got.Status.UpstreamEvidence, 2)
	assert.Equal(t, "app-v1", got.Status.UpstreamEvidence[1].Bundle, "names the Bundle that verified it")
	assert.Equal(t, [][]string{{"prod"}}, tr.promoted)

	r, tr, c = evidenceRig(t, evidenceBundle("app-v1", "1.0", "Superseded", "test", "uat"), plain)
	lcReconcile(t, r, "app-plain")
	got = lcGet(t, c, "app-plain")
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "UpstreamEvidence"))
	assert.Equal(t, [][]string{{"test", "uat", "prod"}}, tr.promoted)
}

// #1311: the carried soak keeps ticking while the Bundle promotes, and keeps
// the Bundle re-queued for it.
func TestEvidence_SoakTicks(t *testing.T) {
	b := rollbackTo("1.0")
	b.Status.Phase = "Promoting"
	b.Status.GraphRef = graph.GraphNameFrom("app", b.Name)
	b.Status.UpstreamEvidence = []kardinalv1alpha1.UpstreamEvidence{{Environment: "uat", Bundle: "app-v1",
		VerifiedAt: metav1.NewTime(evidenceT0), SoakMinutes: 1}}
	r, _, c := evidenceRig(t, b, lcStep(b.Name, "prod", "s-prod", "Promoting"))
	res, err := r.Reconcile(context.Background(), reconcileReq(b.Name))
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter)
	assert.Equal(t, int64(50*60), lcGet(t, c, b.Name).Status.UpstreamEvidence[0].SoakMinutes)
}

func reconcileReq(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}}
}

// #1311: a verify gate that names the fleet applies to each of its targets,
// so a rollback of one target with it promotes through the upstream too.
func TestEvidence_VerifyGateOnFleet(t *testing.T) {
	p := lcPipeline("app", lcEnvs("test", "prod")...)
	p.Spec.Environments[1].Fleet = &kardinalv1alpha1.FleetSpec{Targets: []kardinalv1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}
	g := gateTemplate("smoke", "prod", false)
	g.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "smoke"}
	rb := rollbackTo("1.0")
	rb.Spec.Intent.TargetEnvironment = "prod-eu"
	c := lcClient(p, g, evidenceBundle("app-v1", "1.0", "Superseded", "test", "prod-eu"), rb)
	tr := &promotedTranslator{pipeline: p}
	r := &bundle.Reconciler{Client: c, Translator: tr}
	lcReconcile(t, r, rb.Name)
	cond := meta.FindStatusCondition(lcGet(t, c, rb.Name).Status.Conditions, "UpstreamEvidence")
	require.NotNil(t, cond)
	assert.Equal(t, "VerifyGate", cond.Reason)
	assert.Equal(t, [][]string{{"test", "prod-eu"}}, tr.promoted)
}
