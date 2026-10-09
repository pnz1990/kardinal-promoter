// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// TestPipelineReconciler_PathConflict: Pipelines on one repository and
// branch with separate environment paths have no PathConflict condition;
// an overlapping path (the same directory, a directory inside another, the
// default environments/<name>) sets it on both, naming the other Pipeline;
// another branch, another repository, or an argocd environment (no git
// write) is no conflict. Fixing the path removes the condition, and a second
// reconcile changes nothing (idempotent).
func TestPipelineReconciler_PathConflict(t *testing.T) {
	env := func(name, path, strategy string) kardinalv1alpha1.EnvironmentSpec {
		return kardinalv1alpha1.EnvironmentSpec{Name: name, Path: path, Update: kardinalv1alpha1.UpdateConfig{Strategy: strategy}}
	}
	mk := func(ns, name, url, branch string, envs ...kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
		p := newPipeline(name, envs)
		p.Namespace = ns
		p.Spec.Git.URL, p.Spec.Git.Branch = url, branch
		return p
	}
	const repo = "https://git.example.com/org/gitops.git"
	tests := []struct {
		name     string
		other    *kardinalv1alpha1.Pipeline
		conflict bool
	}{
		{name: "separate paths", other: mk("b", "web", repo, "main", env("prod", "apps/web/prod", ""))},
		{name: "same path", other: mk("b", "web", repo+"/", "", env("prod", "apps/api/prod", "")), conflict: true},
		{name: "nested path", other: mk("b", "web", repo, "main", env("prod", "apps/api", "")), conflict: true},
		{name: "default path", other: mk("b", "web", "https://GIT.example.com/org/gitops", "main", env("test", "", "")), conflict: true},
		{name: "other branch", other: mk("b", "web", repo, "release", env("prod", "apps/api/prod", ""))},
		{name: "other repo", other: mk("b", "web", "https://git.example.com/org/other", "main", env("prod", "apps/api/prod", ""))},
		{name: "argocd writes no git", other: mk("b", "web", repo, "main", env("prod", "apps/api/prod", "argocd"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := mk("a", "api", repo, "main", env("test", "environments/test", ""), env("prod", "apps/api/prod", ""))
			c := newClientWithIndex(newScheme(), api, tt.other)
			r := &pipeline.Reconciler{Client: c}
			for range 2 {
				for _, p := range []*kardinalv1alpha1.Pipeline{api, tt.other} {
					_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)})
					require.NoError(t, err)
				}
			}
			for _, p := range []*kardinalv1alpha1.Pipeline{api, tt.other} {
				var got kardinalv1alpha1.Pipeline
				require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(p), &got))
				cond := meta.FindStatusCondition(got.Status.Conditions, "PathConflict")
				if !tt.conflict {
					assert.Nil(t, cond, p.Name)
					continue
				}
				require.NotNil(t, cond, p.Name)
				assert.Equal(t, "OverlappingPath", cond.Reason)
				assert.Contains(t, cond.Message, "give each Pipeline its own environment paths")
			}
			if tt.conflict {
				var got kardinalv1alpha1.Pipeline
				require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(api), &got))
				assert.Contains(t, meta.FindStatusCondition(got.Status.Conditions, "PathConflict").Message, "b/web environment")

				// Fix the other Pipeline's path: the condition goes away.
				var o kardinalv1alpha1.Pipeline
				require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(tt.other), &o))
				o.Spec.Environments[0].Path = "apps/web/" + o.Spec.Environments[0].Name
				require.NoError(t, c.Update(context.Background(), &o))
				_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(api)})
				require.NoError(t, err)
				require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(api), &got))
				assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "PathConflict"))
			}
		})
	}
}
