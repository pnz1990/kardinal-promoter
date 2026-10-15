// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/user"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestWorldAdmitsAsTheChart: a World with Actors serves every
// ValidatingAdmissionPolicy the chart installs, and each one decides, as
// the user a write is made as (groups and extra included), on the
// resource or subresource it names. Covers SCEN-HARNESS-01.
func TestWorldAdmitsAsTheChart(t *testing.T) {
	w := NewWorld(t, Options{Actors: true})
	c, err := RenderChart()
	require.NoError(t, err)
	var want []string
	for n := range c.VAPs {
		want = append(want, n)
	}
	sort.Strings(want)
	got := append([]string(nil), w.Policies()...)
	sort.Strings(got)
	assert.Equal(t, want, got, "the World serves every policy the chart installs")

	ns := "team-a"
	person := AsUser(w.Ctx, "dev@corp.example.com", "developers", "system:authenticated")
	kro := AsInfo(w.Ctx, GraphInfo(ns))
	// The Graph ServiceAccount's own token (kubectl create token): its
	// requests carry the credential extra, so they are not kro.
	minted := AsInfo(w.Ctx, &user.DefaultInfo{Name: GraphUser(ns), Groups: ServiceAccountGroups(ns),
		Extra: map[string][]string{"authentication.kubernetes.io/credential-id": {"JTI=minted"}}})
	controller := w.Ctx

	step := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "app-1-0-0-prod",
		Labels: map[string]string{"kardinal.io/bundle": "app-1-0-0", "kardinal.io/pipeline": "app", "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-1-0-0", Environment: "prod", StepType: "auto"}}
	verified := step.DeepCopy()
	verified.Status.State = "Verified"
	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "g-app-1-0-0",
		Labels: map[string]string{"kardinal.io/bundle": "app-1-0-0", "kardinal.io/pipeline": "app", "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true"}}
	pipeline := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "app"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}}}}
	paused := pipeline.DeepCopy()
	paused.Spec.PausedFleets = []string{"prod"}
	held := pipeline.DeepCopy()
	now := metav1.Now()
	held.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "app-1-0-0", Reason: "x", CreatedBy: "someone-else@corp.example.com", CreatedAt: &now}}

	cases := []struct {
		name   string
		err    error
		refuse string // part of the refusal; "" when admitted
	}{
		{"kro creates a PromotionStep", w.AdmitCreate(kro, step), ""},
		{"a person creates a PromotionStep", w.AdmitCreate(person, step), "only kardinal (the promotion Graph or the controller)"},
		{"the Graph ServiceAccount's minted token creates a PromotionStep", w.AdmitCreate(minted, step), "only kardinal (the promotion Graph or the controller)"},
		{"the controller writes a step's status", w.AdmitUpdate(controller, "status", verified, step), ""},
		{"a person writes a step's status", w.AdmitUpdate(person, "status", verified, step), "only kardinal (the promotion Graph or the controller)"},
		{"kro creates a gate instance", w.AdmitCreate(kro, gate), ""},
		{"a person creates a gate instance", w.AdmitCreate(person, gate), "only kardinal creates a gate instance"},
		{"a paused fleet that is not a fleet", w.AdmitCreate(person, paused), "which is not an environment with a fleet"},
		{"a hold in another's name", w.AdmitUpdate(person, "", held, pipeline), "every new spec.holds entry must have createdBy"},
	}
	for _, tc := range cases {
		if tc.refuse == "" {
			assert.NoError(t, tc.err, tc.name)
		} else if assert.Error(t, tc.err, tc.name) {
			assert.Contains(t, tc.err.Error(), tc.refuse, tc.name)
		}
	}
}
