// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"errors"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	// prsGetErr, when set, is the error the reconciler gets reading a PRStatus.
	prsGetErr error
	// remote, when set, is the reconciler's RemoteClusters.
	remote *health.RemoteClusters
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
	if ps.Status.Steps == nil {
		ps.Status.Steps = recordedSteps(hc.env.Approval)
	}
	ps.Status.State = "HealthChecking"
	c := newClient(t, append([]client.Object{pipeline, bundle, ps}, hc.objs...)...)
	rc := c
	if hc.prsGetErr != nil {
		rc = interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*v1alpha1.PRStatus); ok {
					return hc.prsGetErr
				}
				return cl.Get(ctx, key, obj, opts...)
			}})
	}
	r := &promotionstep.Reconciler{Client: rc, SCM: &mockSCM{}, GitClient: &mockGit{},
		HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme(), hc.dynObjs...)),
		RemoteClusters: hc.remote}
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

// fluxKustomization is a Ready Kustomization in flux-system that applied
// commit on main.
func fluxKustomization(name, commit string) *unstructured.Unstructured {
	ks := unstructuredObj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", name,
		map[string]interface{}{
			"observedGeneration":  int64(1),
			"lastAppliedRevision": "main@sha1:" + commit,
			"conditions":          []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}},
		})
	ks.SetGeneration(1)
	return ks
}

// TestFluxWaitsForTheMergeCommit proves #1307: a flux check of a pr-review
// step waits while the merge commit is unknown, instead of passing on a
// Kustomization Ready on the previous commit, and health.timeout then applies
// onHealthFailure with that reason. A step with no changes opened no PR, so
// it does not wait. Whether there is a PR follows from the step list recorded
// when the step started, not the live approval.
func TestFluxWaitsForTheMergeCommit(t *testing.T) {
	flux := v1alpha1.HealthConfig{Type: "flux", Timeout: "1m"}
	prReview := v1alpha1.EnvironmentSpec{Name: "prod", Approval: "pr-review", Health: flux}
	merged := func(sha string) *v1alpha1.PRStatus {
		prs := openPRStatus("prs", "org/repo", 42)
		prs.Status.Open, prs.Status.Merged, prs.Status.MergeCommitSHA = false, true, sha
		return prs
	}
	expired := metav1.NewTime(time.Now().Add(-time.Second))
	const waiting = "waiting for flux: merge commit of the PR not known yet"
	tests := []struct {
		name         string
		hc           healthCase
		wantState    string
		wantMsg      string
		wantFailures int
	}{
		{name: "merge commit unknown: waits, not a failure",
			hc: healthCase{env: prReview, prsRef: "prs", objs: []client.Object{merged("")},
				dynObjs: []runtime.Object{fluxKustomization("p-prod", oldSHA)}},
			wantState: "HealthChecking", wantMsg: waiting},
		{name: "merge commit unknown at health.timeout: onHealthFailure",
			hc: healthCase{env: prReview, prsRef: "prs", objs: []client.Object{merged("")},
				status:  v1alpha1.PromotionStepStatus{HealthCheckExpiry: &expired, Message: waiting},
				dynObjs: []runtime.Object{fluxKustomization("p-prod", oldSHA)}},
			wantState: "Failed", wantMsg: "health check timeout after 1m0s; last result: " + waiting, wantFailures: 1},
		{name: "merge commit known and applied",
			hc: healthCase{env: prReview, prsRef: "prs", objs: []client.Object{merged(newSHA)},
				dynObjs: []runtime.Object{fluxKustomization("p-prod", newSHA)}},
			wantState: "Verified", wantMsg: "via flux"},
		{name: "merge commit known, previous commit applied",
			hc: healthCase{env: prReview, prsRef: "prs", objs: []client.Object{merged(newSHA)},
				dynObjs: []runtime.Object{fluxKustomization("p-prod", oldSHA)}},
			wantState: "HealthChecking", wantMsg: "waiting for " + newSHA[:7]},
		{name: "no changes: no PR to wait for",
			hc: healthCase{env: prReview, status: v1alpha1.PromotionStepStatus{
				Outputs: map[string]string{"noChanges": "true"}},
				dynObjs: []runtime.Object{fluxKustomization("p-prod", oldSHA)}},
			wantState: "Verified", wantMsg: "via flux"},
		{name: "auto approval is unchanged",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "prod", Health: flux},
				dynObjs: []runtime.Object{fluxKustomization("p-prod", oldSHA)}},
			wantState: "Verified", wantMsg: "via flux"},
		{name: "pr-review step, approval edited to auto: still waits",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "prod", Approval: "auto", Health: flux},
				prsRef: "prs", objs: []client.Object{merged("")},
				status:  v1alpha1.PromotionStepStatus{Steps: recordedSteps("pr-review")},
				dynObjs: []runtime.Object{fluxKustomization("p-prod", oldSHA)}},
			wantState: "HealthChecking", wantMsg: waiting},
		{name: "auto step, approval edited to pr-review: no PR to wait for",
			hc: healthCase{env: prReview, prsRef: "prs", objs: []client.Object{openPRStatus("prs", "", 0)},
				status:  v1alpha1.PromotionStepStatus{Steps: recordedSteps("auto")},
				dynObjs: []runtime.Object{fluxKustomization("p-prod", oldSHA)}},
			wantState: "Verified", wantMsg: "via flux"},
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

// argoApplicationWithImages is argoApplication whose status.summary.images
// lists images.
func argoApplicationWithImages(name, revision string, images ...string) *unstructured.Unstructured {
	app := argoApplication(name, revision)
	list := make([]interface{}, len(images))
	for i, img := range images {
		list[i] = img
	}
	_ = unstructured.SetNestedSlice(app.Object, list, "status", "summary", "images")
	return app
}

// TestArgoCDWaitsForTheMergeCommit proves B80: a webhook can mark the PR
// merged before the merge commit is known (a GitLab fast-forward merge, any
// Forgejo or Gitea merge). While the PRStatus can still learn it, an argocd
// check of a pr-review step waits instead of verifying on
// status.summary.images, so outputs.mergeCommitSHA is recorded and the synced
// revision is checked. Once the PRStatus records that the merge commit will
// not be known (status.mergeCommitUnavailable), the images decide, as before.
// health.timeout bounds the wait. A PRStatus that no longer describes the
// step's PR (B72) is not waited for: nothing points it back at that PR once
// the step is past WaitingForMerge.
func TestArgoCDWaitsForTheMergeCommit(t *testing.T) {
	argo := v1alpha1.HealthConfig{Type: "argocd", Timeout: "1m"}
	prReview := v1alpha1.EnvironmentSpec{Name: "prod", Approval: "pr-review", Health: argo}
	v2 := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	merged := func(sha string) *v1alpha1.PRStatus {
		prs := openPRStatus("prs", "org/repo", 42)
		prs.Status.Open, prs.Status.Merged, prs.Status.MergeCommitSHA = false, true, sha
		return prs
	}
	unavailable := merged("")
	unavailable.Status.MergeCommitUnavailable = true
	// kro recreates a deleted PRStatus from the Graph without a PR number.
	placeholder := &v1alpha1.PRStatus{ObjectMeta: metav1.ObjectMeta{Name: "prs", Namespace: "default"},
		Spec: v1alpha1.PRStatusSpec{Repo: "org/repo"}}
	stepPR := map[string]string{"prURL": "https://git.example/org/repo/pulls/42", "prNumber": "42"}
	expired := metav1.NewTime(time.Now().Add(-time.Second))
	const waiting = "waiting for argocd: merge commit of the PR not known yet"
	tests := []struct {
		name         string
		hc           healthCase
		wantState    string
		wantMsg      string
		wantFailures int
		wantOutputs  map[string]string
	}{
		{name: "merge commit not known yet: waits, not a failure (B80)",
			hc: healthCase{env: prReview, images: v2, prsRef: "prs", objs: []client.Object{merged("")},
				dynObjs: []runtime.Object{argoApplicationWithImages("p-prod", newSHA, "ghcr.io/org/app:v2")}},
			wantState: "HealthChecking", wantMsg: waiting},
		{name: "merge commit not known yet at health.timeout: onHealthFailure",
			hc: healthCase{env: prReview, images: v2, prsRef: "prs", objs: []client.Object{merged("")},
				status:  v1alpha1.PromotionStepStatus{HealthCheckExpiry: &expired, Message: waiting},
				dynObjs: []runtime.Object{argoApplicationWithImages("p-prod", newSHA, "ghcr.io/org/app:v2")}},
			wantState: "Failed", wantMsg: "health check timeout after 1m0s; last result: " + waiting, wantFailures: 1},
		{name: "merge commit recorded later: checked and copied to the outputs",
			hc: healthCase{env: prReview, images: v2, prsRef: "prs", objs: []client.Object{merged(newSHA)},
				dynObjs: []runtime.Object{argoApplicationWithImages("p-prod", newSHA, "ghcr.io/org/app:v2")}},
			wantState: "Verified", wantMsg: "via argocd", wantOutputs: map[string]string{"mergeCommitSHA": newSHA}},
		{name: "merge commit unavailable: the Bundle images decide",
			hc: healthCase{env: prReview, images: v2, prsRef: "prs", objs: []client.Object{unavailable},
				dynObjs: []runtime.Object{argoApplicationWithImages("p-prod", oldSHA, "ghcr.io/org/app:v2")}},
			wantState: "Verified", wantMsg: "via argocd"},
		{name: "merge commit unavailable, previous images: waits for them",
			hc: healthCase{env: prReview, images: v2, prsRef: "prs", objs: []client.Object{unavailable},
				dynObjs: []runtime.Object{argoApplicationWithImages("p-prod", oldSHA, "ghcr.io/org/app:v1")}},
			wantState: "HealthChecking", wantMsg: "waiting for argocd: health=Healthy"},
		{name: "no changes: no PR to wait for",
			hc: healthCase{env: prReview, images: v2, status: v1alpha1.PromotionStepStatus{
				Outputs: map[string]string{"noChanges": "true"}},
				dynObjs: []runtime.Object{argoApplicationWithImages("p-prod", oldSHA, "ghcr.io/org/app:v2")}},
			wantState: "Verified", wantMsg: "via argocd", wantOutputs: map[string]string{"noChanges": "true"}},
		{name: "direct push: no PR to wait for",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "prod", Health: argo}, images: v2,
				dynObjs: []runtime.Object{argoApplicationWithImages("p-prod", oldSHA, "ghcr.io/org/app:v2")}},
			wantState: "Verified", wantMsg: "via argocd"},
		{name: "PRStatus cannot be read: waits, not an image fallback",
			hc: healthCase{env: prReview, images: v2, prsRef: "prs", objs: []client.Object{merged("")},
				prsGetErr: errors.New("informer cache not synced"),
				dynObjs:   []runtime.Object{argoApplicationWithImages("p-prod", oldSHA, "ghcr.io/org/app:v2")}},
			wantState: "HealthChecking", wantMsg: waiting},
		{name: "PRStatus recreated as a placeholder: the Bundle images decide (B72)",
			hc: healthCase{env: prReview, images: v2, prsRef: "prs", objs: []client.Object{placeholder},
				status:  v1alpha1.PromotionStepStatus{Outputs: stepPR},
				dynObjs: []runtime.Object{argoApplicationWithImages("p-prod", oldSHA, "ghcr.io/org/app:v2")}},
			wantState: "Verified", wantMsg: "via argocd", wantOutputs: stepPR},
		{name: "PRStatus deleted: nothing left to learn it from",
			hc: healthCase{env: prReview, images: v2, prsRef: "prs",
				dynObjs: []runtime.Object{argoApplicationWithImages("p-prod", oldSHA, "ghcr.io/org/app:v2")}},
			wantState: "Verified", wantMsg: "via argocd"},
		{name: "resource adapter checks the images only: no wait",
			hc: healthCase{env: v1alpha1.EnvironmentSpec{Name: "prod", Approval: "pr-review",
				Health: v1alpha1.HealthConfig{Type: "resource"}}, images: v2, prsRef: "prs",
				objs: []client.Object{merged(""), withImage(healthyDeployment("p", "prod"), "ghcr.io/org/app:v2")}},
			wantState: "Verified", wantMsg: "via resource"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, _ := tt.hc.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantFailures, got.Status.ConsecutiveHealthFailures)
			assert.Equal(t, tt.wantOutputs, got.Status.Outputs)
		})
	}
}

// TestMergeCommitRecordedLate: a webhook can mark the PR merged before the
// merge commit is known, so the step leaves WaitingForMerge without it. Once
// the PRStatus records it, the health check copies it into
// status.outputs.mergeCommitSHA, as docs/health-adapters.md says. Outputs
// that already name a commit are kept.
func TestMergeCommitRecordedLate(t *testing.T) {
	prReview := v1alpha1.EnvironmentSpec{Name: "prod", Approval: "pr-review", Health: v1alpha1.HealthConfig{Type: "flux"}}
	merged := func(sha string) *v1alpha1.PRStatus {
		prs := openPRStatus("prs", "org/repo", 42)
		prs.Status.Open, prs.Status.Merged, prs.Status.MergeCommitSHA = false, true, sha
		return prs
	}
	tests := []struct {
		name    string
		prs     *v1alpha1.PRStatus
		outputs map[string]string
		applied string
		want    map[string]string
	}{
		{name: "recorded while Flux is on the previous commit", prs: merged(newSHA), applied: oldSHA,
			want: map[string]string{"mergeCommitSHA": newSHA}},
		{name: "recorded when the step is verified", prs: merged(newSHA), applied: newSHA,
			want: map[string]string{"mergeCommitSHA": newSHA}},
		{name: "not known yet", prs: merged(""), applied: oldSHA, want: nil},
		{name: "already recorded", prs: merged(newSHA), applied: oldSHA,
			outputs: map[string]string{"mergeCommitSHA": oldSHA}, want: map[string]string{"mergeCommitSHA": oldSHA}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, _ := healthCase{env: prReview, prsRef: "prs", objs: []client.Object{tt.prs},
				status:  v1alpha1.PromotionStepStatus{Outputs: tt.outputs},
				dynObjs: []runtime.Object{fluxKustomization("p-prod", tt.applied)}}.run(t)
			assert.Equal(t, tt.want, got.Status.Outputs, got.Status.Message)
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
			pl := makePipeline("p")
			ps := asPromoting(makeStep("step", "p", "b1", tt.env), pl)
			c := newClient(t, ps, pl, makeBundle("b1", "p"))
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

// TestShardLabelNoLongerSkipsStep proves #1321: distributed mode was removed,
// so the controller reconciles a step whose kardinal.io/shard label is left
// over from it. It used to skip the step, which then waited for a
// kardinal-agent forever.
func TestShardLabelNoLongerSkipsStep(t *testing.T) {
	ps := makeStep("step", "p", "b1", "test")
	ps.Labels = map[string]string{"kardinal.io/shard": "eu"}
	c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	reconcileStep(t, r, "step")
	assert.Equal(t, "Promoting", getStep(t, c, "step").Status.State)
}

// TestFlaggerPhaseFromThisPromotion proves the reconciler half of bugs 2
// and 3 of the delivery spike: the flagger check gets the start of the
// health-check step, so a Succeeded or Failed phase that Flagger set before
// it (about the previous release) neither verifies nor fails the step. A step
// that changed nothing in git keeps trusting the phase.
func TestFlaggerPhaseFromThisPromotion(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "flagger"}}
	started := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	steps := []v1alpha1.StepStatus{{Name: "health-check", State: v1alpha1.StepExecutionInProgress, StartedAt: &started}}
	at := func(phase string, d time.Duration) *unstructured.Unstructured {
		return unstructuredObj("flagger.app/v1beta1", "Canary", "test", "p", map[string]interface{}{
			"phase": phase, "lastTransitionTime": started.Add(d).UTC().Format(time.RFC3339)})
	}
	tests := []struct {
		name         string
		status       v1alpha1.PromotionStepStatus
		canary       *unstructured.Unstructured
		wantState    string
		wantMsg      string
		wantFailures int
	}{
		{name: "Succeeded before the health check started waits",
			status: v1alpha1.PromotionStepStatus{Steps: steps}, canary: at("Succeeded", -time.Hour),
			wantState: "HealthChecking", wantMsg: "Succeeded is for an earlier release"},
		{name: "Succeeded after the health check started verifies",
			status: v1alpha1.PromotionStepStatus{Steps: steps}, canary: at("Succeeded", 20*time.Second),
			wantState: "Verified", wantMsg: "via flagger"},
		{name: "Failed before the health check started is not a failure",
			status: v1alpha1.PromotionStepStatus{Steps: steps}, canary: at("Failed", -time.Hour),
			wantState: "HealthChecking", wantMsg: "Failed is for an earlier release"},
		{name: "Failed after the health check started fails",
			status: v1alpha1.PromotionStepStatus{Steps: steps}, canary: at("Failed", 20*time.Second),
			wantState: "Failed", wantMsg: "health alarm via flagger", wantFailures: 1},
		{name: "no changes in git: the earlier Succeeded describes the Bundle",
			status: v1alpha1.PromotionStepStatus{Steps: steps, Outputs: map[string]string{"noChanges": "true"}},
			canary: at("Succeeded", -time.Hour), wantState: "Verified"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, _ := healthCase{env: env, status: tt.status, dynObjs: []runtime.Object{tt.canary}}.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantFailures, got.Status.ConsecutiveHealthFailures)
		})
	}
}

// TestBakeWaitingIsNotAnAlarm proves bug 4 of the delivery spike fixed. A
// Waiting result during the bake window (a canary paused at a step) stops the
// window without an alarm, so fail-on-alarm no longer fails a good canary.
// A stopped window restarts health.timeout, so reset-on-alarm on a release
// that never gets healthy again ends at the timeout instead of resetting
// forever.
func TestBakeWaitingIsNotAnAlarm(t *testing.T) {
	bake := func(policy string) v1alpha1.EnvironmentSpec {
		return v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"},
			Bake: &v1alpha1.BakeConfig{Minutes: 30, Policy: policy}}
	}
	ago := func(d time.Duration) *metav1.Time { t := metav1.NewTime(time.Now().Add(-d)); return &t }
	running := func(resets int) v1alpha1.PromotionStepStatus {
		// The window runs for 5m; the time-to-healthy timeout passed long ago.
		return v1alpha1.PromotionStepStatus{HealthCheckExpiry: ago(20 * time.Minute), BakeStartedAt: ago(5 * time.Minute),
			BakeElapsedMinutes: 5, BakeResets: resets}
	}
	stopped := func(expiry *metav1.Time) v1alpha1.PromotionStepStatus {
		return v1alpha1.PromotionStepStatus{HealthCheckExpiry: expiry, BakeResets: 3}
	}
	tests := []struct {
		name         string
		env          v1alpha1.EnvironmentSpec
		status       v1alpha1.PromotionStepStatus
		deploy       *appsv1.Deployment
		wantState    string
		wantMsg      string
		wantFailures int
		wantResets   int
		wantWindow   bool // BakeStartedAt set
		wantExpiry   bool // HealthCheckExpiry moved to about now + timeout
	}{
		{name: "fail-on-alarm: Waiting stops the window, no alarm (bug 4b)", env: bake("fail-on-alarm"),
			status: running(0), deploy: rollingDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "bake: window stopped, waiting for resource", wantExpiry: true},
		{name: "reset-on-alarm: Waiting stops the window without a reset", env: bake("reset-on-alarm"),
			status: running(1), deploy: rollingDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "restarts at the next healthy check (resets=1)", wantResets: 1, wantExpiry: true},
		{name: "fail-on-alarm: Unhealthy applies onHealthFailure", env: bake("fail-on-alarm"),
			status: running(0), deploy: degradedDeployment("p", "test"),
			wantState: "Failed", wantMsg: "health alarm via resource", wantFailures: 1},
		{name: "reset-on-alarm: Unhealthy resets and restarts the timeout", env: bake("reset-on-alarm"),
			status: running(0), deploy: degradedDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "timer reset (resets=1", wantFailures: 1, wantResets: 1, wantExpiry: true},
		{name: "reset-on-alarm: unhealthy within the timeout after a reset waits", env: bake("reset-on-alarm"),
			status: stopped(&metav1.Time{Time: time.Now().Add(30 * time.Second)}), deploy: degradedDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "waiting for a healthy check to restart the window (resets=3, unhealthy via resource)",
			wantFailures: 1, wantResets: 3},
		{name: "reset-on-alarm: not healthy again within the timeout ends (bug 4c)", env: bake("reset-on-alarm"),
			status: stopped(ago(time.Second)), deploy: degradedDeployment("p", "test"),
			wantState: "Failed", wantMsg: "health check timeout after 1m0s", wantFailures: 1, wantResets: 3},
		{name: "a stopped window restarts at the next healthy check", env: bake("fail-on-alarm"),
			status: stopped(&metav1.Time{Time: time.Now().Add(30 * time.Second)}), deploy: healthyDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "bake: 0m/30m", wantResets: 3, wantWindow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now()
			_, got, _ := healthCase{env: tt.env, status: tt.status, objs: []client.Object{tt.deploy}}.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantFailures, got.Status.ConsecutiveHealthFailures)
			assert.Equal(t, tt.wantResets, got.Status.BakeResets)
			if tt.wantState != "HealthChecking" {
				return
			}
			assert.Equal(t, tt.wantWindow, got.Status.BakeStartedAt != nil)
			require.NotNil(t, got.Status.HealthCheckExpiry)
			moved := !got.Status.HealthCheckExpiry.Time.Before(before.Add(time.Minute).Truncate(time.Second))
			assert.Equal(t, tt.wantExpiry, moved, "healthCheckExpiry %s", got.Status.HealthCheckExpiry)
		})
	}
}

// TestBakeFlappingEnds proves #1423: under bake.policy reset-on-alarm, a
// release that turns healthy and then unhealthy within every health.timeout
// used to re-arm the timeout on each alarm and stay HealthChecking forever.
// The step must now complete one full window by
// status.bakeFirstStartedAt + bake.minutes + health.timeout: a window that
// stops at or after that deadline applies onHealthFailure, a stopped window's
// timeout never passes it, and a window running at the deadline may still
// complete.
func TestBakeFlappingEnds(t *testing.T) {
	env := func(policy string) v1alpha1.EnvironmentSpec {
		return v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"},
			Bake: &v1alpha1.BakeConfig{Minutes: 30, Policy: policy}}
	}
	withMax := func(max string) v1alpha1.EnvironmentSpec {
		e := env("reset-on-alarm")
		e.Bake.MaxDuration = max
		return e
	}
	ago := func(d time.Duration) *metav1.Time { t := metav1.NewTime(time.Now().Add(-d)); return &t }
	in := func(d time.Duration) *metav1.Time { t := metav1.NewTime(time.Now().Add(d)); return &t }
	// running is a window that started 20s ago, the first window at first.
	running := func(first *metav1.Time) v1alpha1.PromotionStepStatus {
		return v1alpha1.PromotionStepStatus{HealthCheckExpiry: ago(time.Hour), BakeStartedAt: ago(20 * time.Second),
			BakeFirstStartedAt: first, BakeResets: 7}
	}
	const deadlineMsg = "bake: no 30m contiguous healthy window within 31m0s of the first healthy check (bake.minutes + health.timeout; resets="
	tests := []struct {
		name       string
		env        v1alpha1.EnvironmentSpec
		status     v1alpha1.PromotionStepStatus
		deploy     *appsv1.Deployment
		wantState  string
		wantMsg    string
		wantResets int
		// wantExpiry, for a HealthChecking result, is when healthCheckExpiry
		// should be, give or take 5s.
		wantExpiry time.Duration
		wantFirst  *metav1.Time // bakeFirstStartedAt kept; nil: not checked
	}{
		{name: "an alarm after the deadline fails the flapping release", env: env("reset-on-alarm"),
			status: running(ago(40 * time.Minute)), deploy: degradedDeployment("p", "test"),
			wantState: "Failed", wantMsg: deadlineMsg + "8); last result: Deployment", wantResets: 8},
		{name: "an alarm before the deadline resets the window", env: env("reset-on-alarm"),
			status: running(ago(10 * time.Minute)), deploy: degradedDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "timer reset (resets=8", wantResets: 8, wantExpiry: time.Minute},
		{name: "a stopped window's timeout stops at the deadline", env: env("reset-on-alarm"),
			status: running(ago(30*time.Minute + 30*time.Second)), deploy: degradedDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "timer reset (resets=8", wantResets: 8, wantExpiry: 30 * time.Second},
		{name: "not healthy again by the deadline fails with the deadline", env: env("reset-on-alarm"),
			status: v1alpha1.PromotionStepStatus{HealthCheckExpiry: ago(time.Second), BakeFirstStartedAt: ago(31*time.Minute + time.Second),
				BakeResets: 7, Message: "bake: waiting for a healthy check to restart the window"}, deploy: degradedDeployment("p", "test"),
			wantState: "Failed", wantMsg: deadlineMsg + "7); last result: bake: waiting for a healthy check", wantResets: 7},
		{name: "a Waiting result after the deadline fails", env: env("fail-on-alarm"),
			status: running(ago(40 * time.Minute)), deploy: rollingDeployment("p", "test"),
			wantState: "Failed", wantMsg: deadlineMsg + "7); last result: Deployment", wantResets: 7},
		{name: "a window running at the deadline continues", env: env("reset-on-alarm"),
			status: running(ago(40 * time.Minute)), deploy: healthyDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "bake: 0m/30m", wantResets: 7, wantFirst: ago(40 * time.Minute)},
		{name: "the first window records its start", env: env("reset-on-alarm"),
			status: v1alpha1.PromotionStepStatus{HealthCheckExpiry: in(time.Minute)}, deploy: healthyDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "bake: 0m/30m", wantFirst: in(0)},
		{name: "a restarted window keeps the first start", env: env("reset-on-alarm"),
			status: v1alpha1.PromotionStepStatus{HealthCheckExpiry: in(time.Minute), BakeFirstStartedAt: ago(10 * time.Minute), BakeResets: 2},
			deploy: healthyDeployment("p", "test"), wantState: "HealthChecking", wantMsg: "bake: 0m/30m", wantResets: 2,
			wantFirst: ago(10 * time.Minute)},
		{name: "bake.maxDuration widens the deadline: an alarm resets", env: withMax("24h"),
			status: running(ago(40 * time.Minute)), deploy: degradedDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "timer reset (resets=8", wantResets: 8, wantExpiry: time.Minute},
		{name: "bake.maxDuration: an alarm after it fails", env: withMax("2h"),
			status: running(ago(3 * time.Hour)), deploy: degradedDeployment("p", "test"),
			wantState: "Failed", wantResets: 8,
			wantMsg: "bake: no 30m contiguous healthy window within 2h0m0s of the first healthy check (bake.maxDuration; resets=8)"},
		{name: "a bake.maxDuration shorter than the window counts as one window", env: withMax("10m"),
			status: running(ago(20 * time.Minute)), deploy: degradedDeployment("p", "test"),
			wantState: "HealthChecking", wantMsg: "timer reset (resets=8", wantResets: 8, wantExpiry: time.Minute},
		{name: "a window from before bakeFirstStartedAt existed sets the deadline", env: env("reset-on-alarm"),
			status: v1alpha1.PromotionStepStatus{HealthCheckExpiry: ago(time.Hour), BakeStartedAt: ago(32 * time.Minute)},
			deploy: degradedDeployment("p", "test"), wantState: "Failed", wantMsg: deadlineMsg + "1)", wantResets: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, got, _ := healthCase{env: tt.env, status: tt.status, objs: []client.Object{tt.deploy}}.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantResets, got.Status.BakeResets)
			if tt.wantFirst != nil {
				require.NotNil(t, got.Status.BakeFirstStartedAt)
				assert.WithinDuration(t, tt.wantFirst.Time, got.Status.BakeFirstStartedAt.Time, 5*time.Second)
			}
			if tt.wantState == "HealthChecking" && tt.wantExpiry != 0 {
				require.NotNil(t, got.Status.HealthCheckExpiry)
				assert.WithinDuration(t, time.Now().Add(tt.wantExpiry), got.Status.HealthCheckExpiry.Time, 5*time.Second)
			}
		})
	}
}
