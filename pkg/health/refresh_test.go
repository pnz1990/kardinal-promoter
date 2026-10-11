// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

func refreshObj(apiVersion, kind, ns, name string, spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]interface{}{"name": name, "namespace": ns,
			"annotations": map[string]interface{}{"keep": "me"}},
		"spec": spec,
	}}
}

// TestRefreshTargetsAndRequest: the objects each health type refreshes, and
// the annotation each gets; other annotations and the spec are kept.
func TestRefreshTargetsAndRequest(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	app := refreshObj("argoproj.io/v1alpha1", "Application", "apps", "web", map[string]interface{}{"project": "default"})
	ks := refreshObj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "web",
		map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "repo"}})
	repo := refreshObj("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "repo", map[string]interface{}{"url": "u"})
	dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), app, ks, repo)
	d := health.NewAutoDetector(nil, dyn)

	tests := []struct {
		name  string
		opts  health.CheckOptions
		want  string
		check map[schema.GroupVersionResource][2]string // gvr -> ns/name, annotation value
	}{
		{name: "argocd", opts: health.CheckOptions{Type: "argocd", ArgoCD: health.ArgoCDConfig{Name: "web", Namespace: "apps"}},
			want: "Application apps/web",
			check: map[schema.GroupVersionResource][2]string{
				{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: {"apps/web", "normal"}}},
		{name: "flux", opts: health.CheckOptions{Type: "flux", Flux: health.FluxConfig{Name: "web"}},
			want: "GitRepository flux-system/repo, Kustomization flux-system/web",
			check: map[schema.GroupVersionResource][2]string{
				{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}:   {"flux-system/repo", "2026-10-11T12:00:00Z"},
				{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}: {"flux-system/web", "2026-10-11T12:00:00Z"}}},
		{name: "resource has none", opts: health.CheckOptions{Type: "resource"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want != "", health.RefreshSupported(tt.opts.Type))
			targets, err := d.RefreshTargets(context.Background(), tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want, health.DescribeTargets(targets))
			require.NoError(t, d.RequestRefresh(context.Background(), targets, at))
			for gvr, want := range tt.check {
				ns, name, _ := strings.Cut(want[0], "/")
				obj, err := dyn.Resource(gvr).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
				require.NoError(t, err)
				key := health.FluxRequestedAtAnnotation
				if gvr.Group == "argoproj.io" {
					key = health.ArgoCDRefreshAnnotation
				}
				assert.Equal(t, want[1], obj.GetAnnotations()[key])
				assert.Equal(t, "me", obj.GetAnnotations()["keep"])
				assert.NotEmpty(t, obj.Object["spec"])
			}
		})
	}
}
