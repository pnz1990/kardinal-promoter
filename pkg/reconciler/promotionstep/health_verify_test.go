// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

const (
	oldSHA = "0ld0000c0mmit11111111111111111111111111"
	newSHA = "4e3c0mmit2222222222222222222222222222222"
)

// healthCase is one HealthChecking reconcile of step "step" (pipeline "p",
// bundle "b1") against env.
type healthCase struct {
	env     v1alpha1.EnvironmentSpec
	images  []v1alpha1.ImageRef
	status  v1alpha1.PromotionStepStatus
	prsRef  string
	objs    []client.Object
	dynObjs []runtime.Object
	// bundle, when set, adjusts the Bundle b1 before the reconcile.
	bundle func(*v1alpha1.Bundle)
}

func (hc healthCase) run(t *testing.T) (client.Client, v1alpha1.PromotionStep, time.Duration) {
	t.Helper()
	pipeline := makePipeline("p")
	pipeline.Spec.Environments = []v1alpha1.EnvironmentSpec{hc.env}
	bundle := makeBundle("b1", "p")
	bundle.Spec.Images = hc.images
	if hc.bundle != nil {
		hc.bundle(bundle)
	}
	ps := labelled(makeStep("step", "p", "b1", hc.env.Name))
	ps.Spec.PRStatusRef = hc.prsRef
	ps.Status = hc.status
	ps.Status.State = "HealthChecking"
	c := newClient(t, append([]client.Object{pipeline, bundle, ps}, hc.objs...)...)
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme(), hc.dynObjs...))}
	res, err := r.Reconcile(context.Background(), reqFor("step"))
	require.NoError(t, err)
	return c, getStep(t, c, "step"), res.RequeueAfter
}

func unstructuredObj(apiVersion, kind, ns, name string, status map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"status":     status,
	}}
}

func argoApplication(name, revision string) *unstructured.Unstructured {
	return unstructuredObj("argoproj.io/v1alpha1", "Application", "argocd", name, map[string]interface{}{
		"health":         map[string]interface{}{"status": "Healthy"},
		"sync":           map[string]interface{}{"status": "Synced", "revision": revision},
		"operationState": map[string]interface{}{"phase": "Succeeded"},
	})
}

func canary(ns, name, phase string) *unstructured.Unstructured {
	return unstructuredObj("flagger.app/v1beta1", "Canary", ns, name, map[string]interface{}{"phase": phase})
}

func labelledDeployment(d *appsv1.Deployment, labels map[string]string) *appsv1.Deployment {
	d.Labels = labels
	return d
}

func rollingDeployment(name, ns string) *appsv1.Deployment {
	d := healthyDeployment(name, ns)
	d.Generation = 2 // the new template is not observed yet
	return d
}

func stalledDeployment(name, ns string) *appsv1.Deployment {
	d := healthyDeployment(name, ns)
	d.Status.Conditions = append(d.Status.Conditions, appsv1.DeploymentCondition{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
	})
	return d
}

// TestHealthTargetsFollowThePipeline proves that the check deciding Verified
// uses the target the Pipeline configures: C03-promotionstep-04 and -05,
// C01-graph-07, C06-scm-health-09 (resource, labelSelector, default type),
// C08-api-config-06 (argocd.name), C03-promotionstep-19 and C06-scm-health-08
// (flagger).
func TestHealthTargetsFollowThePipeline(t *testing.T) {
	web := map[string]string{"app": "web"}
	tests := []struct {
		name         string
		hc           healthCase
		wantState    string
		wantMsg      string
		wantFailures int
	}{
		{name: "no health.type checks the Deployment (C03-05)",
			hc:        healthCase{env: v1alpha1.EnvironmentSpec{Name: "test"}, objs: []client.Object{healthyDeployment("p", "test")}},
			wantState: "Verified", wantMsg: "via resource"},
		{name: "health.resource name and namespace are used (C03-04)",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{
				Resource: &v1alpha1.ResourceRef{Name: "web", Namespace: "apps"}}},
				objs: []client.Object{healthyDeployment("web", "apps")}},
			wantState: "Verified"},
		{name: "health.resource is not satisfied by the default Deployment (C03-04)",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{
				Resource: &v1alpha1.ResourceRef{Name: "web", Namespace: "apps"}}},
				objs: []client.Object{healthyDeployment("p", "test")}},
			wantState: "HealthChecking", wantMsg: "Deployment apps/web not found", wantFailures: 1},
		{name: "labelSelector needs every match healthy (C06-09)",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{LabelSelector: web}},
				objs: []client.Object{
					labelledDeployment(healthyDeployment("web-a", "test"), web),
					labelledDeployment(degradedDeployment("web-b", "test"), web),
				}},
			wantState: "HealthChecking", wantMsg: "web-b", wantFailures: 1},
		{name: "labelSelector with every match healthy (C06-09)",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{LabelSelector: web}},
				objs: []client.Object{
					labelledDeployment(healthyDeployment("web-a", "test"), web),
					labelledDeployment(healthyDeployment("web-b", "test"), web),
				}},
			wantState: "Verified", wantMsg: "2 Deployments"},
		{name: "health.argocd.name is used (C08-06)",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{
				Type: "argocd", ArgoCD: &v1alpha1.HealthTargetRef{Name: "custom"}}},
				dynObjs: []runtime.Object{argoApplication("custom", oldSHA)}},
			wantState: "Verified", wantMsg: "via argocd"},
		{name: "health.type flagger (C03-19)",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "flagger"}},
				dynObjs: []runtime.Object{canary("test", "p", "Succeeded")}},
			wantState: "Verified", wantMsg: "via flagger"},
		{name: "delivery.delegate flagger (C06-08)",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "test", Delivery: v1alpha1.DeliveryConfig{Delegate: "flagger"}},
				dynObjs: []runtime.Object{canary("test", "p", "Succeeded")}},
			wantState: "Verified", wantMsg: "via flagger"},
		{name: "health.flagger name is used",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{
				Type: "flagger", Flagger: &v1alpha1.HealthTargetRef{Name: "web", Namespace: "apps"}}},
				dynObjs: []runtime.Object{canary("apps", "web", "Succeeded")}},
			wantState: "Verified"},
		{name: "a progressing canary is not a failure (C06-08)",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "flagger"}},
				dynObjs: []runtime.Object{canary("test", "p", "Progressing")}},
			wantState: "HealthChecking", wantMsg: "waiting for flagger"},
		{name: "a failed canary is terminal (C06-08)",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "flagger"}},
				dynObjs: []runtime.Object{canary("test", "p", "Failed")}},
			wantState: "Failed", wantMsg: "health alarm via flagger", wantFailures: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, _ := tt.hc.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantFailures, got.Status.ConsecutiveHealthFailures)
		})
	}
}

// TestHealthVerifiesTheNewRevision proves E2E-01, C03-promotionstep-11 and
// C06-scm-health-07: Verified requires the promoted revision to be deployed,
// not merely a healthy environment.
func TestHealthVerifiesTheNewRevision(t *testing.T) {
	argoEnv := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "argocd"}}
	resEnv := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource"}}
	v2 := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	mergedPRS := openPRStatus("prs", "org/repo", 42)
	mergedPRS.Status.Open, mergedPRS.Status.Merged, mergedPRS.Status.MergeCommitSHA = false, true, newSHA
	tests := []struct {
		name      string
		hc        healthCase
		wantState string
		wantMsg   string
	}{
		{name: "Argo CD synced to the previous commit is not verified",
			hc: healthCase{env: argoEnv, status: v1alpha1.PromotionStepStatus{Outputs: map[string]string{"commitSHA": newSHA}},
				dynObjs: []runtime.Object{argoApplication("p-test", oldSHA)}},
			wantState: "HealthChecking", wantMsg: "waiting for " + newSHA[:7]},
		{name: "Argo CD synced to the pushed commit",
			hc: healthCase{env: argoEnv, status: v1alpha1.PromotionStepStatus{Outputs: map[string]string{"commitSHA": newSHA}},
				dynObjs: []runtime.Object{argoApplication("p-test", newSHA)}},
			wantState: "Verified"},
		{name: "Argo CD synced to the merge commit from the outputs",
			hc: healthCase{env: argoEnv, status: v1alpha1.PromotionStepStatus{Outputs: map[string]string{"mergeCommitSHA": newSHA}},
				dynObjs: []runtime.Object{argoApplication("p-test", newSHA)}},
			wantState: "Verified"},
		{name: "merge commit read from the PRStatus",
			hc: healthCase{env: argoEnv, prsRef: "prs", objs: []client.Object{mergedPRS},
				dynObjs: []runtime.Object{argoApplication("p-test", oldSHA)}},
			wantState: "HealthChecking", wantMsg: "waiting for " + newSHA[:7]},
		{name: "Deployment still runs the previous image",
			hc: healthCase{env: resEnv, images: v2,
				objs: []client.Object{withImage(healthyDeployment("p", "test"), "ghcr.io/org/app:v1")}},
			wantState: "HealthChecking", wantMsg: "not updated yet"},
		{name: "Deployment runs the promoted image",
			hc: healthCase{env: resEnv, images: v2,
				objs: []client.Object{withImage(healthyDeployment("p", "test"), "ghcr.io/org/app:v2")}},
			wantState: "Verified"},
		{name: "Deployment controller has not observed the new template",
			hc: healthCase{env: resEnv, images: v2,
				objs: []client.Object{withImage(rollingDeployment("p", "test"), "ghcr.io/org/app:v2")}},
			wantState: "HealthChecking", wantMsg: "observe generation 2"},
		{name: "ProgressDeadlineExceeded fails the step",
			hc: healthCase{env: resEnv, images: v2,
				objs: []client.Object{withImage(stalledDeployment("p", "test"), "ghcr.io/org/app:v2")}},
			wantState: "Failed", wantMsg: "ProgressDeadlineExceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, _ := tt.hc.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
		})
	}
}

// headGit is a GitClient that reports the commit it pushed.
type headGit struct {
	noopGit
	sha string
}

func (g *headGit) HeadCommit(_ context.Context, _ string) (string, error) { return g.sha, nil }

// TestPushedCommitRecorded proves the auto half of E2E-01: the commit pushed
// to the tracked branch is recorded as the revision to verify. A pr-review
// push goes to a promotion branch, so its merge commit is used instead.
func TestPushedCommitRecorded(t *testing.T) {
	tests := []struct {
		env  string
		want string
	}{
		{env: "test", want: newSHA},
		{env: "prod", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			ps := makeStep("step", "p", "b1", tt.env)
			ps.Status.State = "Promoting"
			c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
			r := &promotionstep.Reconciler{Client: c, GitClient: &headGit{sha: newSHA},
				SCM:       &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/3", prNumber: 3},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			reconcileStep(t, r, "step")
			got := getStep(t, c, "step")
			assert.Contains(t, []string{"HealthChecking", "WaitingForMerge"}, got.Status.State, got.Status.Message)
			assert.Equal(t, tt.want, got.Status.Outputs["commitSHA"])
		})
	}
}

// TestBakeOutlastsHealthTimeout proves C03-promotionstep-03 and -15:
// health.timeout bounds only the time to the first healthy check, so a bake
// window longer than the timeout completes, and a Verified reached through
// the bake writes the PromotionSucceeded AuditEvent.
func TestBakeOutlastsHealthTimeout(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"},
		Bake: &v1alpha1.BakeConfig{Minutes: 30, Policy: "reset-on-alarm"}}
	expired := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	ago := func(d time.Duration) *metav1.Time { t := metav1.NewTime(time.Now().Add(-d)); return &t }
	tests := []struct {
		name      string
		bakeStart *metav1.Time
		deploy    *appsv1.Deployment
		wantState string
		wantMsg   string
		wantAudit []string
	}{
		{name: "never healthy within the timeout fails", deploy: degradedDeployment("p", "test"),
			wantState: "Failed", wantMsg: "health check timeout after 1m0s", wantAudit: []string{"PromotionFailed"}},
		{name: "a running bake outlives the timeout", bakeStart: ago(5 * time.Minute), deploy: healthyDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "bake: 5m/30m", wantAudit: []string{}},
		{name: "a bake longer than the timeout completes", bakeStart: ago(31 * time.Minute), deploy: healthyDeployment("p", "test"),
			wantState: "Verified", wantMsg: "bake complete", wantAudit: []string{"PromotionSucceeded"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, got, _ := healthCase{env: env, objs: []client.Object{tt.deploy},
				status: v1alpha1.PromotionStepStatus{HealthCheckExpiry: &expired, BakeStartedAt: tt.bakeStart}}.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantAudit, auditActions(t, c))
		})
	}
}

// TestHealthCheckCadence proves C03-promotionstep-12: checks are spaced by
// the health interval whatever the reconcile rate, and only an unhealthy
// result, not a rollout in progress, counts as a health failure.
func TestHealthCheckCadence(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource"}}
	recent := metav1.NewTime(time.Now().Add(-2 * time.Second))
	tests := []struct {
		name         string
		last         *metav1.Time
		deploy       *appsv1.Deployment
		wantFailures int
		wantMsg      string
		wantChecked  bool
		wantRequeue  func(t *testing.T, d time.Duration)
	}{
		{name: "a check right after the last one is skipped", last: &recent, deploy: degradedDeployment("p", "test"),
			wantRequeue: func(t *testing.T, d time.Duration) {
				assert.Greater(t, d, time.Duration(0))
				assert.LessOrEqual(t, d, 8*time.Second)
			}},
		{name: "a rollout in progress is not a failure", deploy: rollingDeployment("p", "test"),
			wantMsg: "waiting for resource", wantChecked: true,
			wantRequeue: func(t *testing.T, d time.Duration) { assert.Equal(t, 10*time.Second, d) }},
		{name: "an unhealthy result counts once", deploy: degradedDeployment("p", "test"),
			wantFailures: 1, wantMsg: "unhealthy via resource", wantChecked: true,
			wantRequeue: func(t *testing.T, d time.Duration) { assert.Equal(t, 10*time.Second, d) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, requeue := healthCase{env: env, objs: []client.Object{tt.deploy},
				status: v1alpha1.PromotionStepStatus{LastHealthCheckAt: tt.last}}.run(t)
			assert.Equal(t, "HealthChecking", got.Status.State)
			assert.Equal(t, tt.wantFailures, got.Status.ConsecutiveHealthFailures)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			require.NotNil(t, got.Status.LastHealthCheckAt)
			assert.Equal(t, tt.wantChecked, got.Status.LastHealthCheckAt.After(recent.Time))
			tt.wantRequeue(t, requeue)
		})
	}
}

// TestShardOwnership proves C03-promotionstep-24 and C13b-design-03: a step
// labelled for an agent's shard is reconciled only by that agent, not by the
// hub controller (empty Shard), and the hub reconciles only unlabelled steps.
func TestShardOwnership(t *testing.T) {
	tests := []struct {
		name      string
		stepShard string
		ourShard  string
		wantState string
	}{
		{name: "hub skips a sharded step", stepShard: "eu", ourShard: "", wantState: ""},
		{name: "agent reconciles its shard", stepShard: "eu", ourShard: "eu", wantState: "Promoting"},
		{name: "agent skips an unlabelled step", stepShard: "", ourShard: "eu", wantState: ""},
		{name: "hub reconciles an unlabelled step", stepShard: "", ourShard: "", wantState: "Promoting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := makeStep("step", "p", "b1", "test")
			if tt.stepShard != "" {
				ps.Labels = map[string]string{"kardinal.io/shard": tt.stepShard}
			}
			c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{}, Shard: tt.ourShard,
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			reconcileStep(t, r, "step")
			assert.Equal(t, tt.wantState, getStep(t, c, "step").Status.State)
		})
	}
}
