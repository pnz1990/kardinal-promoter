// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	app := refreshObj("argoproj.io/v1alpha1", "Application", "apps", "web", map[string]interface{}{"project": "default",
		"destination": map[string]interface{}{"namespace": "web-prod"}})
	ks := refreshObj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "web",
		map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "repo"}, "targetNamespace": "web-test"})
	repo := refreshObj("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "repo", map[string]interface{}{"url": "u"})
	dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), app, ks, repo)
	d := health.NewAutoDetector(nil, dyn)

	tests := []struct {
		name  string
		opts  health.CheckOptions
		want  string
		dest  []string                                  // each target's Destination
		check map[schema.GroupVersionResource][2]string // gvr -> ns/name, annotation value
	}{
		{name: "argocd", opts: health.CheckOptions{Type: "argocd", ArgoCD: health.ArgoCDConfig{Name: "web", Namespace: "apps"}},
			want: "Application apps/web", dest: []string{"web-prod"},
			check: map[schema.GroupVersionResource][2]string{
				{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: {"apps/web", "normal"}}},
		{name: "flux", opts: health.CheckOptions{Type: "flux", Flux: health.FluxConfig{Name: "web"}},
			want: "GitRepository flux-system/repo, Kustomization flux-system/web", dest: []string{"web-test", "web-test"},
			check: map[schema.GroupVersionResource][2]string{
				{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}:   {"flux-system/repo", "2026-10-11T12:00:00Z team/step"},
				{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}: {"flux-system/web", "2026-10-11T12:00:00Z team/step"}}},
		{name: "resource has none", opts: health.CheckOptions{Type: "resource"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want != "", health.RefreshSupported(tt.opts.Type))
			targets, err := d.RefreshTargets(context.Background(), tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want, health.DescribeTargets(targets))
			var dest []string
			for _, tg := range targets {
				dest = append(dest, tg.Destination)
			}
			assert.Equal(t, tt.dest, dest, "the namespaces the objects deploy to (the refresh scope, #1781)")
			require.NoError(t, d.RequestRefresh(context.Background(), targets, health.FluxRequestValue(at, "team", "step")))
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

// TestClassifyRefreshError: per target, the operation and the class of the
// error, never the server's answer or address.
func TestClassifyRefreshError(t *testing.T) {
	app := health.RefreshTarget{Kind: "Application", Namespace: "argocd", Name: "web"}
	repo := health.RefreshTarget{Kind: "GitRepository", Namespace: "flux-system", Name: "repo"}
	gr := schema.GroupResource{Group: "argoproj.io", Resource: "applications"}
	secret := errors.New("SECRET from https://10.1.2.3:6443")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "forbidden", err: &health.RefreshError{Op: "annotate", Target: app, Err: apierrors.NewForbidden(gr, "web", secret)},
			want: "annotate Application argocd/web: forbidden (HTTP 403)"},
		{name: "not found", err: &health.RefreshError{Op: "get", Target: app, Err: apierrors.NewNotFound(gr, "web")},
			want: "get Application argocd/web: not found (HTTP 404)"},
		{name: "joined, unreachable", err: errors.Join(
			&health.RefreshError{Op: "annotate", Target: repo, Err: &url.Error{Op: "Patch", URL: "https://10.1.2.3:6443/x", Err: secret}},
			&health.RefreshError{Op: "annotate", Target: app, Err: context.DeadlineExceeded}),
			want: "annotate GitRepository flux-system/repo: request failed; annotate Application argocd/web: timed out"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := health.ClassifyRefreshError(tt.err)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "SECRET")
			assert.NotContains(t, got, "10.1.2.3")
		})
	}
}
