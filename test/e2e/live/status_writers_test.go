//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestGate_StatusOnlyKardinal: only kardinal's controller writes the status
// of a gate, a Pipeline, a Bundle or a hand-written MetricCheck
// (<release>-status-writers, #2446). With a Bundle held at a blocking gate:
//
//   - the chart's admin role, bound in the namespace, grants no status write
//     (RBAC refuses it);
//   - a user with resources ["*"] on kardinal.io there, which RBAC lets write
//     every status, and the cluster admin are refused by the policy when they
//     mark the gate instance ready, rewrite the Pipeline's or the Bundle's
//     status, or forge a hand-written MetricCheck's result;
//   - the gate stays blocked and the Bundle does not promote;
//   - the controller's own status writes go on (it keeps evaluating the gate).
//
// Covers STATUS-WRITERS-02.
func TestGate_StatusOnlyKardinal(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := holdApp(t, e, e.Namespace(t), pipelineName)
	bundle, gate := holdBundle(t, a, pipelineName)

	mc := &v1alpha1.MetricCheck{ObjectMeta: metav1.ObjectMeta{Name: "hand-written", Namespace: a.ns},
		Spec: v1alpha1.MetricCheckSpec{Provider: "prometheus", PrometheusURL: "http://prometheus.invalid:9090", Query: "up",
			Threshold: v1alpha1.MetricThreshold{Operator: "gt", Value: 0}}}
	require.NoError(t, e.Client.Create(ctx, mc))

	// alice: the chart's admin role in the namespace. bob: any role with
	// resources ["*"] on kardinal.io (v0.13's admin-extra was one).
	bind := func(user, kind, role string) client.Client {
		_, err := e.Kube.RbacV1().RoleBindings(a.ns).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-" + user, Namespace: a.ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kind, Name: role},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		waitBoundNamed(t, e, a.ns, "e2e-"+user)
		cfg := rest.CopyConfig(e.Config)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: user}
		c, err := client.New(cfg, client.Options{Scheme: e.Client.Scheme()})
		require.NoError(t, err)
		return c
	}
	_, err := e.Kube.RbacV1().Roles(a.ns).Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "e2e-wildcard", Namespace: a.ns},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"*"}, Verbs: []string{"*"}}}},
		metav1.CreateOptions{})
	require.NoError(t, err)
	alice := bind("alice@example.com", "ClusterRole", framework.ControllerDeployment+"-admin")
	bob := bind("bob@example.com", "Role", "e2e-wildcard")

	forgeries := []struct {
		what  string
		obj   client.Object
		patch string
	}{
		{"mark the gate instance ready", &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: gate.Name, Namespace: a.ns}},
			`{"status":{"ready":true,"reason":"forged","lastEvaluatedAt":"` + time.Now().UTC().Format(time.RFC3339) + `"}}`},
		{"rewrite the Pipeline's status", &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: pipelineName, Namespace: a.ns}},
			`{"status":{"summary":"forged"}}`},
		{"rewrite the Bundle's status", &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: bundle, Namespace: a.ns}},
			`{"status":{"graphRef":"forged"}}`},
		{"forge a MetricCheck's result", &v1alpha1.MetricCheck{ObjectMeta: metav1.ObjectMeta{Name: mc.Name, Namespace: a.ns}},
			`{"status":{"result":"Pass","lastValue":"1","validUntil":"2099-01-01T00:00:00Z"}}`},
	}
	for _, f := range forgeries {
		patch := client.RawPatch(types.MergePatchType, []byte(f.patch))
		err := alice.Status().Patch(ctx, f.obj.DeepCopyObject().(client.Object), patch)
		require.Error(t, err, "admin role: %s", f.what)
		assert.True(t, apierrors.IsForbidden(err), "admin role: %s: %v", f.what, err)
		assert.NotContains(t, err.Error(), "only kardinal's controller writes", "admin role: %s: refused by RBAC", f.what)
		for who, c := range map[string]client.Client{"a resources * role": bob, "the cluster admin": e.Client} {
			err := c.Status().Patch(ctx, f.obj.DeepCopyObject().(client.Object), patch)
			require.Error(t, err, "%s: %s", who, f.what)
			assert.True(t, apierrors.IsForbidden(err), "%s: %s: %v", who, f.what, err)
			assert.Contains(t, err.Error(), "only kardinal's controller writes", "%s: %s: refused by the policy", who, f.what)
		}
	}

	// The gate is still blocked after a re-evaluation, and the Bundle's
	// test step has not started.
	var g v1alpha1.PolicyGate
	before := gate.Status.LastEvaluatedAt
	framework.Eventually(t, time.Minute, "the gate re-evaluated by the controller", func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(gate), &g); err != nil {
			return false, err.Error()
		}
		return g.Status.LastEvaluatedAt != nil && before != nil && g.Status.LastEvaluatedAt.After(before.Time), "not yet"
	})
	assert.False(t, g.Status.Ready, "the gate stays blocked")
	c := meta.FindStatusCondition(g.Status.Conditions, "Ready")
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	var steps v1alpha1.PromotionStepList
	require.NoError(t, e.Client.List(ctx, &steps, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/bundle": bundle}))
	for _, s := range steps.Items {
		assert.NotEqual(t, "Verified", s.Status.State, "step %s", s.Name)
	}

	// The controller's status writes are admitted (here, as the controller,
	// on the hand-written check).
	require.NoError(t, e.AsController(t).Status().Patch(ctx, &v1alpha1.MetricCheck{ObjectMeta: metav1.ObjectMeta{Name: mc.Name, Namespace: a.ns}},
		client.RawPatch(types.MergePatchType, []byte(`{"status":{"reason":"written by the controller"}}`))))
}
