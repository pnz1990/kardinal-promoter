// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func app(ns, name, path string, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"})
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetLabels(labels)
	if path != "" {
		_ = unstructured.SetNestedField(u.Object, path, "spec", "source", "path")
	}
	return u
}

func fleetPipeline(sel map[string]string) *kardinalv1alpha1.Pipeline {
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"},
		Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{
			{Name: "test"},
			{Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{Selector: &kardinalv1alpha1.FleetSelector{MatchLabels: sel}}},
		}},
	}
}

// TestResolveFleets checks how a fleet selector becomes status.fleets: every
// selected Application in the Argo CD namespace is a target with its path and
// argocd health, sorted by name; Applications the selector does not match or
// in another namespace are not; an Application without a path or with a name
// that cannot be a target fails the fleet with a message; and a cluster
// without Argo CD says so.
func TestResolveFleets(t *testing.T) {
	sel := map[string]string{"fleet": "prod"}
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}, meta.RESTScopeNamespace)
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	tests := []struct {
		name    string
		objs    []client.Object
		mapper  meta.RESTMapper
		noKind  bool
		want    []kardinalv1alpha1.FleetTarget
		wantMsg string
	}{
		{name: "selected Applications", mapper: mapper, objs: []client.Object{
			app("argocd", "us", "clusters/us", sel), app("argocd", "eu", "clusters/eu", sel),
			app("argocd", "other", "clusters/other", map[string]string{"fleet": "dev"}),
			app("elsewhere", "ap", "clusters/ap", sel),
		}, want: []kardinalv1alpha1.FleetTarget{
			{Name: "eu", Path: "clusters/eu", Health: &kardinalv1alpha1.HealthConfig{Type: "argocd",
				ArgoCD: &kardinalv1alpha1.HealthTargetRef{Name: "eu", Namespace: "argocd"}}},
			{Name: "us", Path: "clusters/us", Health: &kardinalv1alpha1.HealthConfig{Type: "argocd",
				ArgoCD: &kardinalv1alpha1.HealthTargetRef{Name: "us", Namespace: "argocd"}}},
		}},
		{name: "no path", mapper: mapper, objs: []client.Object{app("argocd", "eu", "", sel)},
			wantMsg: "Application argocd/eu cannot be a target: it has no spec.source.path"},
		{name: "Argo CD not installed", mapper: mapper, noKind: true,
			wantMsg: "argoproj.io/v1alpha1 Applications are not served"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(s).WithRESTMapper(tc.mapper).WithObjects(tc.objs...)
			if tc.noKind {
				// The API server's answer when the Application CRD is missing.
				b = b.WithInterceptorFuncs(interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
					return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "argoproj.io", Kind: "Application"}}
				}})
			}
			c := b.Build()
			r := &Reconciler{Client: c}
			got := r.resolveFleets(context.Background(), fleetPipeline(sel))
			require.Len(t, got, 1)
			assert.Equal(t, "prod", got[0].Environment)
			if tc.wantMsg != "" {
				assert.Contains(t, got[0].Message, tc.wantMsg)
				assert.Empty(t, got[0].Targets)
				return
			}
			assert.Empty(t, got[0].Message)
			assert.Equal(t, tc.want, got[0].Targets)
			// Idempotent: the same Applications give the same status.
			assert.True(t, fleetsEqual(got, r.resolveFleets(context.Background(), fleetPipeline(sel))))
		})
	}
	assert.Empty(t, (&Reconciler{}).resolveFleets(context.Background(), &kardinalv1alpha1.Pipeline{}), "no fleet, no status")
}

func clusterProfile(ns, name string, labels map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "multicluster.x-k8s.io", Version: "v1alpha1", Kind: "ClusterProfile"})
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetLabels(labels)
	return u
}

// TestResolveFleets_ClusterProfiles: a ClusterProfile selector makes every
// selected cluster of the cluster inventory in the Pipeline's namespace (or
// selector.namespace) a target named after it, with the fleet's path and
// health (none set on the target); matchExpressions select too; a cluster
// without the inventory CRD says so. A selector of kind Target needs no
// status.
//
// Covers FLEET-03.
func TestResolveFleets_ClusterProfiles(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(schema.GroupVersionKind{Group: "multicluster.x-k8s.io", Version: "v1alpha1", Kind: "ClusterProfile"}, meta.RESTScopeNamespace)
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).WithRESTMapper(mapper).WithObjects(
		clusterProfile("team-a", "prod-eu-1", map[string]string{"env": "prod", "region": "eu"}),
		clusterProfile("team-a", "prod-us-1", map[string]string{"env": "prod", "region": "us"}),
		clusterProfile("team-a", "dev-1", map[string]string{"env": "dev"}),
		clusterProfile("fleet-system", "prod-ap-1", map[string]string{"env": "prod"}),
	).Build()
	p := fleetPipeline(nil)
	p.Spec.Environments[1].Fleet.Selector = &kardinalv1alpha1.FleetSelector{Kind: kardinalv1alpha1.FleetSelectorClusterProfile,
		MatchLabels:      map[string]string{"env": "prod"},
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "region", Operator: metav1.LabelSelectorOpExists}}}
	r := &Reconciler{Client: c}
	got := r.resolveFleets(context.Background(), p)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].Message)
	assert.Equal(t, []kardinalv1alpha1.FleetTarget{{Name: "prod-eu-1"}, {Name: "prod-us-1"}}, got[0].Targets)

	p.Spec.Environments[1].Fleet.Selector.Namespace = "fleet-system"
	p.Spec.Environments[1].Fleet.Selector.MatchExpressions = nil
	got = r.resolveFleets(context.Background(), p)
	assert.Equal(t, []kardinalv1alpha1.FleetTarget{{Name: "prod-ap-1"}}, got[0].Targets)

	noCRD := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "multicluster.x-k8s.io", Kind: "ClusterProfile"}}
	}}).Build()
	got = (&Reconciler{Client: noCRD}).resolveFleets(context.Background(), p)
	assert.Contains(t, got[0].Message, "ClusterProfiles are not served")

	p.Spec.Environments[1].Fleet.Selector.Kind = kardinalv1alpha1.FleetSelectorTarget
	assert.Empty(t, r.resolveFleets(context.Background(), p), "a Target selector is resolved by the Graph builder")
	assert.False(t, hasSelectorFleet(p))
}
