// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func getStep(t *testing.T, c client.Client, name string) v1alpha1.PromotionStep {
	t.Helper()
	var ps v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "default"}, &ps))
	return ps
}

func reqFor(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}}
}

func reconcileStep(t *testing.T, r *promotionstep.Reconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), reqFor(name))
	require.NoError(t, err)
}

// TestSecretRefNamespace proves C03-promotionstep-18 / C08-api-config-01: a
// Pipeline must not be able to name another namespace's Secret and have the
// controller push with its token to a URL the Pipeline author controls. The
// refused Secret is never read, and no step runs.
func TestSecretRefNamespace(t *testing.T) {
	tests := []struct {
		name        string
		secretNS    string // secretRef.namespace in the Pipeline
		wantFailed  bool
		wantReads   []string // namespaces of the Secrets the controller read
		wantMessage string
	}{
		{name: "other namespace is refused", secretNS: "team-b",
			wantFailed: true, wantMessage: `git.secretRef.namespace "team-b" is not allowed`},
		{name: "controller namespace is refused", secretNS: "kardinal-system",
			wantFailed: true, wantMessage: "must be in the Pipeline's namespace"},
		{name: "same namespace is used", secretNS: "default", wantReads: []string{"default/github-token"}},
		{name: "empty namespace means the Pipeline's", secretNS: "", wantReads: []string{"default/github-token"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := makePipeline("nginx-demo")
			pipeline.Spec.Git.URL = "https://attacker.example/steal.git"
			pipeline.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "github-token", Namespace: tt.secretNS}
			var objs []client.Object
			for _, ns := range []string{"default", "team-b", "kardinal-system"} {
				objs = append(objs, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "github-token", Namespace: ns},
					Data:       map[string][]byte{"token": []byte("token-of-" + ns)},
				})
			}
			ps := asPromoting(makeStep("step-sec", "nginx-demo", "b1", "test"), pipeline)
			var reads []string
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}).
				WithObjects(append(objs, ps, pipeline, makeBundle("b1", "nginx-demo"))...).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*corev1.Secret); ok {
							reads = append(reads, key.String())
						}
						return c.Get(ctx, key, obj, opts...)
					},
				}).Build()
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}

			reconcileStep(t, r, "step-sec")
			got := getStep(t, c, "step-sec")
			assert.Equal(t, tt.wantReads, reads)
			if tt.wantFailed {
				assert.Equal(t, "Failed", got.Status.State)
				assert.Contains(t, got.Status.Message, tt.wantMessage)
				for _, s := range got.Status.Steps {
					assert.NotEqual(t, v1alpha1.StepExecutionCompleted, s.State, "no step may run: %s ran", s.Name)
				}
				assert.NotContains(t, got.Status.Message, "token-of-")
				return
			}
			assert.NotEqual(t, "Failed", got.Status.State, got.Status.Message)
		})
	}
}

// TestUnsupportedConfigFailsLoudly proves C08-api-config-12/13, C13b-design-05,
// C03-promotionstep-04 (cluster) and #1321 (shard): options that were accepted
// and silently ignored now fail the step with a message naming the field.
func TestUnsupportedConfigFailsLoudly(t *testing.T) {
	tests := []struct {
		name    string
		state   string
		mutate  func(env *v1alpha1.EnvironmentSpec, ps *v1alpha1.PromotionStep)
		message string
	}{
		{name: "health.cluster at start", state: "",
			mutate:  func(env *v1alpha1.EnvironmentSpec, _ *v1alpha1.PromotionStep) { env.Health.Cluster = "prod-kubeconfig" }, //nolint:staticcheck // SA1019: tests the rejection
			message: "health.cluster is not supported"},
		{name: "health.cluster while health checking", state: "HealthChecking",
			mutate:  func(env *v1alpha1.EnvironmentSpec, _ *v1alpha1.PromotionStep) { env.Health.Cluster = "prod-kubeconfig" }, //nolint:staticcheck // SA1019: tests the rejection
			message: "health.cluster is not supported"},
		{name: "region step left over from a pre-upgrade Graph", state: "",
			mutate:  func(_ *v1alpha1.EnvironmentSpec, ps *v1alpha1.PromotionStep) { ps.Spec.Region = "us-east-1" }, //nolint:staticcheck // SA1019: tests the rejection
			message: "regions is not supported; declare one environment per region"},
		// #1321: distributed mode was removed. A step left over with a shard
		// label used to be skipped by the controller and hang in Pending.
		{name: "shard step left over from distributed mode", state: "",
			mutate: func(env *v1alpha1.EnvironmentSpec, ps *v1alpha1.PromotionStep) {
				env.Shard = "eu" //nolint:staticcheck // SA1019: tests the rejection
				ps.Labels = map[string]string{"kardinal.io/shard": "eu"}
			},
			message: "shard is not supported: distributed mode was removed"},
		{name: "shard on the environment only", state: "",
			mutate:  func(env *v1alpha1.EnvironmentSpec, _ *v1alpha1.PromotionStep) { env.Shard = "eu" }, //nolint:staticcheck // SA1019: tests the rejection
			message: "shard is not supported"},
		{name: "shard while promoting", state: "Promoting",
			mutate:  func(env *v1alpha1.EnvironmentSpec, _ *v1alpha1.PromotionStep) { env.Shard = "eu" }, //nolint:staticcheck // SA1019: tests the rejection
			message: "shard is not supported"},
		{name: "resource kind other than Deployment", state: "HealthChecking",
			mutate: func(env *v1alpha1.EnvironmentSpec, _ *v1alpha1.PromotionStep) {
				env.Health.Resource = &v1alpha1.ResourceRef{Kind: "StatefulSet", Name: "db"}
			},
			message: `health.resource.kind "StatefulSet" is not supported`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "resource"}}
			ps := makeStep("step-cfg", "p", "b1", "test")
			ps.Status.State = tt.state
			tt.mutate(&env, ps)
			pipeline := makePipeline("p")
			pipeline.Spec.Environments = []v1alpha1.EnvironmentSpec{env}
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}).
				WithObjects(ps, pipeline, makeBundle("b1", "p"), healthyDeployment("p", "test")).Build()
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
				HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme())),
				WorkDirFn:      func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}

			for i := 0; i < 3; i++ {
				reconcileStep(t, r, "step-cfg")
			}
			got := getStep(t, c, "step-cfg")
			assert.Equal(t, "Failed", got.Status.State)
			assert.Contains(t, got.Status.Message, tt.message)
			assert.Empty(t, got.Status.Steps, "no step may run")
		})
	}
}

// TestRepositoryNotAllowed covers #1332: with --scm-allowed-repositories set,
// a step of a Pipeline without git.secretRef whose spec.git.url is not
// allowed fails before git-clone, from Pending and from Promoting, and opens
// no PR. An allowed URL, the Pipeline's own secretRef or an unset list runs.
func TestRepositoryNotAllowed(t *testing.T) {
	allow, err := scm.ParseRepositoryAllowlist([]string{"github.com/myorg/*"})
	require.NoError(t, err)
	tests := []struct {
		name       string
		allow      *scm.RepositoryAllowlist
		url        string
		secret     bool
		promoting  bool
		wantFailed bool
	}{
		{name: "not allowed, Pending", allow: allow, url: "https://github.com/other/repo.git", wantFailed: true},
		{name: "not allowed, Promoting", allow: allow, url: "https://github.com/other/repo.git", promoting: true, wantFailed: true},
		{name: "allowed", allow: allow, url: "https://github.com/myorg/repo.git", promoting: true},
		{name: "own secretRef", allow: allow, url: "https://github.com/other/repo.git", secret: true, promoting: true},
		{name: "unset", url: "https://github.com/other/repo.git", promoting: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := makePipeline("nginx-demo")
			pipeline.Spec.Git.URL = tt.url
			objs := []client.Object{pipeline, makeBundle("b1", "nginx-demo")}
			if tt.secret {
				pipeline.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "team-token"}
				objs = append(objs, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "team-token", Namespace: "default"},
					Data: map[string][]byte{"token": []byte("t")}})
			}
			ps := makeStep("step-repo", "nginx-demo", "b1", "test")
			if tt.promoting {
				ps = asPromoting(ps, pipeline)
			}
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}).
				WithObjects(append(objs, ps)...).Build()
			git := &cloneCountingGit{}
			m := &mockSCM{}
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: git, AllowedRepositories: tt.allow,
				WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}

			reconcileStep(t, r, "step-repo")
			got := getStep(t, c, "step-repo")
			if !tt.wantFailed {
				assert.NotEqual(t, "Failed", got.Status.State, got.Status.Message)
				return
			}
			assert.Equal(t, "Failed", got.Status.State)
			assert.Contains(t, got.Status.Message, "is not in the controller's allowed repositories (github.com/myorg/*)")
			assert.Zero(t, git.clones, "no git-clone")
			assert.Zero(t, m.openCalled, "no PR")
			// Idempotent: the Failed step stays Failed.
			reconcileStep(t, r, "step-repo")
			assert.Equal(t, got.Status.Message, getStep(t, c, "step-repo").Status.Message)
		})
	}
}

// cloneCountingGit is a mockGit that counts clones.
type cloneCountingGit struct {
	mockGit
	clones int
}

func (g *cloneCountingGit) Clone(ctx context.Context, url, branch, dir, token string) error {
	g.clones++
	return g.mockGit.Clone(ctx, url, branch, dir, token)
}

func (g *cloneCountingGit) CloneAt(ctx context.Context, url, ref, dir, token string) error {
	g.clones++
	return g.mockGit.CloneAt(ctx, url, ref, dir, token)
}
