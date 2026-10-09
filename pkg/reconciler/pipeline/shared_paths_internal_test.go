// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestNormalizeRepoURL: the https, http, ssh:// and scp spellings of one
// repository, with or without userinfo, port, ".git" or a trailing "/", and
// in any case, are one repository; another host or path is another.
func TestNormalizeRepoURL(t *testing.T) {
	const want = "github.com/org/gitops"
	for _, u := range []string{
		"https://github.com/org/gitops",
		"https://github.com/org/gitops.git",
		"https://github.com/org/gitops/",
		"HTTPS://GitHub.com/Org/GitOps.git/",
		"https://x-access-token@github.com/org/gitops",
		"http://github.com:80/org/gitops",
		"ssh://git@github.com/org/gitops.git",
		"ssh://git@github.com:22/org/gitops",
		"git@github.com:org/gitops.git",
		"git@github.com:/org/gitops",
		"github.com:org/gitops",
	} {
		assert.Equal(t, want, normalizeRepoURL(u), u)
	}
	assert.NotEqual(t, want, normalizeRepoURL("git@gitlab.com:org/gitops.git"))
	assert.NotEqual(t, want, normalizeRepoURL("https://github.com/org/gitops-infra"))
}

func pl(ns, name, url string, envs ...kardinalv1alpha1.EnvironmentSpec) kardinalv1alpha1.Pipeline {
	return kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Generation: 1},
		Spec: kardinalv1alpha1.PipelineSpec{Git: kardinalv1alpha1.PipelineGit{URL: url, Branch: "main"}, Environments: envs}}
}

func ev(name, path string) kardinalv1alpha1.EnvironmentSpec {
	return kardinalv1alpha1.EnvironmentSpec{Name: name, Path: path}
}

func helmEnv(name, path, values string) kardinalv1alpha1.EnvironmentSpec {
	e := ev(name, path)
	e.Update = kardinalv1alpha1.UpdateConfig{Strategy: "helm", Helm: &kardinalv1alpha1.HelmUpdateConfig{ValuesFile: values}}
	return e
}

// TestPathConflict_Cases (#1504 QA): two environments of one Pipeline at
// overlapping paths conflict; a Helm valuesFile that leaves its environment
// path is a written path of its own (one inside it is not); the ssh and
// https URLs of one repository are one repository; a Pipeline of the same
// namespace is named, those of other namespaces are only counted. A fleet
// writes each target's path (D1).
//
// Covers FLEET-06.
func TestPathConflict_Cases(t *testing.T) {
	const https, ssh = "https://github.com/org/gitops", "git@github.com:org/gitops.git"
	fleet := func(path string, targets ...kardinalv1alpha1.FleetTarget) kardinalv1alpha1.EnvironmentSpec {
		e := ev("prod", path)
		e.Fleet = &kardinalv1alpha1.FleetSpec{Targets: targets}
		return e
	}
	tests := []struct {
		name   string
		p      kardinalv1alpha1.Pipeline
		others []kardinalv1alpha1.Pipeline
		want   []string // substrings of the message; nil: no condition
		not    []string
	}{
		{name: "own environments overlap", p: pl("a", "api", https, ev("test", "apps/api"), ev("prod", "apps/api/prod")),
			want: []string{"environments test (apps/api) and prod (apps/api/prod) of this Pipeline"}},
		{name: "own environments separate", p: pl("a", "api", https, ev("test", "apps/api/test"), ev("prod", "apps/api/prod"))},
		{name: "helm valuesFile escapes into another environment",
			p:    pl("a", "api", https, helmEnv("test", "apps/api/test", "../prod/values.yaml"), ev("prod", "apps/api/prod")),
			want: []string{"environments test (apps/api/prod/values.yaml) and prod (apps/api/prod) of this Pipeline"}},
		{name: "helm valuesFile escapes into a shared file",
			p:      pl("a", "api", https, helmEnv("prod", "apps/api/prod", "../../shared/values.yaml")),
			others: []kardinalv1alpha1.Pipeline{pl("a", "web", https, helmEnv("prod", "apps/web/prod", "../../shared/values.yaml"))},
			want:   []string{"environment prod (apps/shared/values.yaml) and Pipeline web environment prod (apps/shared/values.yaml)"}},
		{name: "helm valuesFile inside its path",
			p:      pl("a", "api", https, helmEnv("prod", "apps/api/prod", "values/prod.yaml")),
			others: []kardinalv1alpha1.Pipeline{pl("a", "web", https, ev("prod", "apps/web/prod"))}},
		{name: "ssh and https of one repository", p: pl("a", "api", https, ev("prod", "apps/prod")),
			others: []kardinalv1alpha1.Pipeline{pl("a", "web", ssh, ev("prod", "apps/prod"))},
			want:   []string{"environment prod (apps/prod) and Pipeline web environment prod (apps/prod)"}},
		{name: "other namespaces counted, not named", p: pl("a", "api", https, ev("prod", "apps/prod")),
			others: []kardinalv1alpha1.Pipeline{
				pl("b", "secret-web", ssh, ev("prod", "apps/prod")),
				pl("c", "secret-ops", https+".git", ev("ops", "apps")),
			},
			want: []string{"environment prod (apps/prod) and 2 other Pipeline(s) in other namespaces"},
			not:  []string{"secret", "b/", "c/", "ops"}},
		// D1: a fleet writes its targets' paths, each its own directory.
		{name: "fleet targets under the fleet path", p: pl("a", "api", https, ev("test", "apps/test"),
			fleet("apps/prod", kardinalv1alpha1.FleetTarget{Name: "eu"}, kardinalv1alpha1.FleetTarget{Name: "us"}))},
		{name: "fleet target path outside the fleet", p: pl("a", "api", https, ev("test", "apps/test"),
			fleet("apps/prod", kardinalv1alpha1.FleetTarget{Name: "eu", Path: "apps/test/eu"})),
			want: []string{"environments test (apps/test) and prod-eu (apps/test/eu) of this Pipeline"}},
		{name: "fleet target and another Pipeline", p: pl("a", "api", https,
			fleet("apps/prod", kardinalv1alpha1.FleetTarget{Name: "eu", Path: "clusters/eu"})),
			others: []kardinalv1alpha1.Pipeline{pl("a", "web", https, ev("prod", "clusters/eu"))},
			want:   []string{"environment prod-eu (clusters/eu) and Pipeline web environment prod (clusters/eu)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			all := append([]kardinalv1alpha1.Pipeline{tt.p}, tt.others...)
			cond := pathConflict(&all[0], all)
			if tt.want == nil {
				assert.Nil(t, cond)
				return
			}
			require.NotNil(t, cond)
			for _, w := range tt.want {
				assert.Contains(t, cond.Message, w)
			}
			for _, n := range tt.not {
				assert.NotContains(t, cond.Message, n)
			}
		})
	}
}

// TestPipelinePeers_Predicate: the Pipeline→Pipeline watch enqueues the
// Pipelines on the same repository and branch on create and delete, and on
// an update only when the spec changed (generation), then for the
// repository the Pipeline wrote before as well as the new one. A status
// write enqueues nothing.
func TestPipelinePeers_Predicate(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	const repoA, repoB = "https://github.com/org/a", "https://github.com/org/b"
	peerA, peerB := pl("x", "peer-a", repoA, ev("t", "t")), pl("y", "peer-b", repoB, ev("t", "t"))
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(&peerA, &peerB).Build()
	r := &Reconciler{Client: c}
	h := r.pipelinePeers()
	ctx := context.Background()
	drain := func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) []string {
		var out []string
		for q.Len() > 0 {
			it, _ := q.Get()
			out = append(out, it.Name)
			q.Done(it)
		}
		return out
	}
	newQ := func() workqueue.TypedRateLimitingInterface[reconcile.Request] {
		return workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	}
	old := pl("z", "mover", repoA, ev("t", "t"))

	q := newQ()
	h.Create(ctx, event.CreateEvent{Object: &old}, q)
	assert.Equal(t, []string{"peer-a"}, drain(q))

	statusOnly := old.DeepCopy()
	statusOnly.Status.Phase = "Promoting"
	h.Update(ctx, event.UpdateEvent{ObjectOld: &old, ObjectNew: statusOnly}, q)
	assert.Empty(t, drain(q), "a status write is not a spec change")

	moved := old.DeepCopy()
	moved.Generation = 2
	moved.Spec.Git.URL = repoB
	h.Update(ctx, event.UpdateEvent{ObjectOld: &old, ObjectNew: moved}, q)
	assert.ElementsMatch(t, []string{"peer-a", "peer-b"}, drain(q), "the old and the new repository's Pipelines")

	h.Delete(ctx, event.DeleteEvent{Object: moved}, q)
	assert.Equal(t, []string{"peer-b"}, drain(q))
}
