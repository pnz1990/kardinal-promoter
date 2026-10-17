//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestGate_ChangeRequestInstance (spec 23 §4, §6): a prod gate that reads
// changeRequests["prod-change"] gets, once test is Verified, the instance
// the Graph makes from the per-promotion template: named
// <template>-<env>--<bundle>, labelled with the Bundle's UID, its ticket
// template rendered. The ChangeRequest controller writes its status (here
// ProviderError: this controller serves no provider yet), and the gate
// blocks naming the change request. Nobody but kardinal may write an
// instance: a user with full RBAC on changerequests and the cluster admin
// cannot mark it approved or forge one. A superseded Bundle's instance is
// suspended.
//
// Covers CHANGE-REQUEST-02, CHANGE-REQUEST-03.
func TestGate_ChangeRequestInstance(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	tmpl := &v1alpha1.ChangeRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "prod-change", Namespace: a.ns},
		Spec: v1alpha1.ChangeRequestSpec{PerPromotion: true, AppliesTo: []string{"prod"}, Provider: "servicenow",
			ServiceNow: &v1alpha1.ServiceNowChangeRequest{InstanceURL: "https://kardinal-e2e.service-now.invalid",
				ChangeRequestAuth: v1alpha1.ChangeRequestAuth{CredentialsSecretRef: v1alpha1.SecretKeyRef{Name: "snow", Key: "token"}},
				Template:          map[string]string{"short_description": "Promote {{ pipeline.name }} {{ bundle.version }} to {{ environment.name }}"}}},
	}
	require.NoError(t, e.Client.Create(ctx, tmpl))
	e.CreateGate(t, framework.Gate(a.ns, "cab", "prod", `changeRequests["prod-change"].state == "approved"`, recheck))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

	name := "prod-change-prod--" + bundle
	var inst v1alpha1.ChangeRequest
	framework.Eventually(t, gateTimeout, "the ChangeRequest instance and its status", func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: name}, &inst); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(inst.Status.Conditions, "ProviderError")
		if c == nil {
			return false, fmt.Sprintf("conditions %v", inst.Status.Conditions)
		}
		return c.Status == metav1.ConditionTrue, c.Message
	})
	b := getBundle(t, e, a.ns, bundle)
	assert.Equal(t, map[string]string{"kardinal.io/pipeline": pipelineName, "kardinal.io/bundle": bundle,
		"kardinal.io/environment": "prod", graph.LabelBundleUID: string(b.UID), graph.LabelChangeRequestTemplate: "prod-change"},
		changeRequestLabels(inst.Labels))
	assert.False(t, inst.Spec.PerPromotion)
	assert.Equal(t, "servicenow", inst.Spec.Provider)
	assert.Equal(t, fmt.Sprintf("Promote %s %s to prod", pipelineName, fixtures.V2), inst.Spec.ServiceNow.Template["short_description"])
	assert.Equal(t, fmt.Sprintf("kardinal:%s/%s/prod/%s", a.ns, bundle, b.UID), inst.Status.CorrelationID)
	assert.Contains(t, meta.FindStatusCondition(inst.Status.Conditions, "ProviderError").Message,
		`provider "servicenow" is not available in this controller`)
	e.WaitGateReady(t, a.ns, bundle, "prod", "cab", false, "change request prod-change has no ticket yet", gateTimeout)

	// Only kardinal writes an instance.
	mallory := impersonating(t, e, a.ns, "mallory@example.com", nil,
		[]string{"changerequests", "changerequests/status"}, "get", "list", "create", "update", "patch")
	refused := func(err error, what string) {
		t.Helper()
		require.Error(t, err, what)
		assert.True(t, apierrors.IsForbidden(err), "%s: %v", what, err)
		assert.Contains(t, err.Error(), "only kardinal (the promotion Graph or the controller) creates or changes this object", what)
	}
	approve := client.RawPatch(types.MergePatchType, []byte(
		`{"status":{"state":"approved","ticket":{"key":"CHG0000001"},"validUntil":"2099-01-01T00:00:00Z"}}`))
	refused(mallory.Status().Patch(ctx, inst.DeepCopy(), approve), "a user marks the change request approved")
	refused(e.Client.Status().Patch(ctx, inst.DeepCopy(), approve), "the cluster admin marks it approved")
	forged := &v1alpha1.ChangeRequest{ObjectMeta: metav1.ObjectMeta{Name: "forged", Namespace: a.ns, Labels: map[string]string{
		"kardinal.io/bundle": bundle, "kardinal.io/environment": "prod", graph.LabelChangeRequestTemplate: "prod-change"}},
		Spec: v1alpha1.ChangeRequestSpec{Provider: "servicenow", ServiceNow: tmpl.Spec.ServiceNow.DeepCopy()}}
	refused(mallory.Create(ctx, forged), "a user forges an instance")
	// A user's own template is theirs to write.
	mine := tmpl.DeepCopy()
	mine.ObjectMeta = metav1.ObjectMeta{Name: "mallory-template", Namespace: a.ns}
	require.NoError(t, mallory.Create(ctx, mine), "a template (no kardinal.io/bundle) is the user's")

	// A superseded Bundle's instance stops: spec.suspend follows its phase.
	e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitBundlePhase(t, a.ns, bundle, "Superseded", promoteTimeout)
	framework.Eventually(t, gateTimeout, "the superseded Bundle's instance is suspended", func(ctx context.Context) (bool, string) {
		var cur v1alpha1.ChangeRequest
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: name}, &cur); err != nil {
			return apierrors.IsNotFound(err), err.Error() // pruned with the Graph is fine too
		}
		c := meta.FindStatusCondition(cur.Status.Conditions, "Ready")
		return cur.Spec.Suspend && c != nil && c.Reason == "Suspended", fmt.Sprintf("suspend=%v ready=%v", cur.Spec.Suspend, c)
	})
}

// changeRequestLabels is labels without the ones kro adds to the objects it applies.
func changeRequestLabels(labels map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range labels {
		if k == "kardinal.io/pipeline" || k == "kardinal.io/bundle" || k == "kardinal.io/environment" ||
			k == graph.LabelBundleUID || k == graph.LabelChangeRequestTemplate {
			out[k] = v
		}
	}
	return out
}
