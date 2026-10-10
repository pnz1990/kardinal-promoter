// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// TestPipelineReconciler_Fleets (D1): the reconciler resolves an
// Application selector into status.fleets, re-reads it every minute, and
// reports Ready from what the Graph builder will see: Valid once the
// Applications are found, ValidationFailed while the selector cannot be
// resolved (Argo CD not installed) and for a static target whose
// environment name is not a DNS label.
//
// Covers FLEET-03.
func TestPipelineReconciler_Fleets(t *testing.T) {
	appGVK := schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(appGVK, meta.RESTScopeNamespace)
	app := func(name string) client.Object {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(appGVK)
		u.SetNamespace("argocd")
		u.SetName(name)
		u.SetLabels(map[string]string{"tier": "prod"})
		_ = unstructured.SetNestedField(u.Object, "clusters/"+name, "spec", "source", "path")
		_ = unstructured.SetNestedField(u.Object, "https://github.com/myorg/gitops.git", "spec", "source", "repoURL")
		return u
	}
	selectorFleet := []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{
		MaxConcurrent: 1, Selector: &kardinalv1alpha1.FleetSelector{MatchLabels: map[string]string{"tier": "prod"}}}}}
	reconcile := func(t *testing.T, p *kardinalv1alpha1.Pipeline, reader client.Reader) (kardinalv1alpha1.Pipeline, ctrl.Result) {
		t.Helper()
		c := newClientWithIndex(newScheme(), p)
		res, err := (&pipeline.Reconciler{Client: c, Reader: reader}).Reconcile(context.Background(),
			ctrl.Request{NamespacedName: types.NamespacedName{Name: p.Name, Namespace: p.Namespace}})
		require.NoError(t, err)
		var got kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: p.Name, Namespace: p.Namespace}, &got))
		return got, res
	}
	ready := func(p kardinalv1alpha1.Pipeline) metav1.Condition {
		c := meta.FindStatusCondition(p.Status.Conditions, "Ready")
		require.NotNil(t, c)
		return *c
	}

	apps := fake.NewClientBuilder().WithScheme(newScheme()).WithRESTMapper(mapper).WithObjects(app("eu"), app("us")).Build()
	got, res := reconcile(t, newPipeline("web", selectorFleet), apps)
	assert.Equal(t, metav1.ConditionTrue, ready(got).Status, ready(got).Message)
	require.Len(t, got.Status.Fleets, 1)
	assert.Equal(t, []string{"eu", "us"}, []string{got.Status.Fleets[0].Targets[0].Name, got.Status.Fleets[0].Targets[1].Name})
	assert.Equal(t, time.Minute, res.RequeueAfter, "a selector fleet is read again every minute")

	noArgo := fake.NewClientBuilder().WithScheme(newScheme()).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return &meta.NoKindMatchError{GroupKind: appGVK.GroupKind()}
		}}).Build()
	got, _ = reconcile(t, newPipeline("web", selectorFleet), noArgo)
	assert.Equal(t, "ValidationFailed", ready(got).Reason)
	assert.Contains(t, ready(got).Message, "Applications are not served")

	long := newPipeline("web", []kardinalv1alpha1.EnvironmentSpec{{Name: "production-environment-for-the-company"},
		{Name: "x", DependsOn: []string{"production-environment-for-the-company"}}})
	long.Spec.Environments[0].Fleet = &kardinalv1alpha1.FleetSpec{Targets: []kardinalv1alpha1.FleetTarget{{Name: "europe-west-1-a-zone-b-c-d"}}}
	got, _ = reconcile(t, long, nil)
	assert.Equal(t, "ValidationFailed", ready(got).Reason)
	assert.Contains(t, ready(got).Message, "is not a DNS label of at most 63 characters")
}
