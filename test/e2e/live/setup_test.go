//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package live holds the live e2e suites. Each test runs against the kind
// cluster hack/e2e/up.sh built for its suite; see test/e2e/README.md.
package live

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// pipelineName is the Pipeline every test creates in its own namespace.
const pipelineName = "podinfo"

// Timeouts. A kind cluster on a CI runner syncs Argo CD in seconds; these
// leave room for image pulls on a cold node.
const (
	syncTimeout    = 3 * time.Minute
	promoteTimeout = 5 * time.Minute
)

// app is one test's promotable app: a namespace, a GitOps repo seeded with
// fixtures.KustomizeRepo, and one synced Argo CD Application per environment.
type app struct {
	e    *framework.Env
	ns   string
	repo gitserver.Repo
	envs []string
}

// repoFunc creates a test's GitOps repo holding files, as framework.Env.Repo
// does.
type repoFunc func(t *testing.T, ns string, files map[string][]byte) gitserver.Repo

// newArgoApp sets up an app whose environments Argo CD deploys into the test
// namespace.
func newArgoApp(t *testing.T, e *framework.Env, envs ...string) *app {
	t.Helper()
	return newArgoAppIn(t, e, e.Repo, envs...)
}

// newArgoAppIn is newArgoApp with the repo made by mk, for a repo without the
// webhook or in a subgroup.
func newArgoAppIn(t *testing.T, e *framework.Env, mk repoFunc, envs ...string) *app {
	t.Helper()
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: envs,
		repo: mk(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))}
	for _, env := range envs {
		e.ArgoApp(t, a.argoApp(env), a.repo, fixtures.Path(env), ns)
	}
	for _, env := range envs {
		e.WaitArgoApp(t, a.argoApp(env), syncTimeout)
		e.WaitDeploymentImage(t, ns, fixtures.Workload(env), fixtures.Image+":"+fixtures.V1, syncTimeout)
	}
	return a
}

// argoApp is env's Argo CD Application name.
func (a *app) argoApp(env string) string { return a.ns + "-" + env }

// pipeline is a Pipeline over the app's repo. approval maps env to
// "pr-review"; every other env is auto. Health is argocd on env's
// Application.
func (a *app) pipeline(approval map[string]string) *v1alpha1.Pipeline {
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: pipelineName, Namespace: a.ns},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{
				URL:       a.repo.CloneURL,
				Branch:    a.repo.Branch,
				SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName},
			},
		},
	}
	for _, env := range a.envs {
		mode := approval[env]
		if mode == "" {
			mode = "auto"
		}
		p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{
			Name:     env,
			Path:     fixtures.Path(env),
			Approval: mode,
			Update:   v1alpha1.UpdateConfig{Strategy: "kustomize"},
			Health: v1alpha1.HealthConfig{
				Type:    "argocd",
				Timeout: "3m",
				ArgoCD:  &v1alpha1.HealthTargetRef{Name: a.argoApp(env), Namespace: framework.ArgoCDNamespace},
			},
		})
	}
	return p
}

// apply creates p.
func (a *app) apply(t *testing.T, p *v1alpha1.Pipeline) {
	t.Helper()
	if err := a.e.Client.Create(context.Background(), p); err != nil {
		t.Fatalf("create Pipeline: %v", err)
	}
}
