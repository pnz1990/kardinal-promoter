// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// loopSCM is the PromotionStep reconciler's SCM provider in the loop test: it
// opens one PR. The webhook uses the real GitHub provider instead, so the
// signature check is the production one.
type loopSCM struct {
	mockSCMProvider
	prURL    string
	prNumber int
}

func (m *loopSCM) OpenPR(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	return m.prURL, m.prNumber, nil
}

type loopGit struct{}

func (loopGit) Clone(_ context.Context, _, _, _, _ string) error        { return nil }
func (loopGit) CloneAt(_ context.Context, _, _, _, _ string) error      { return nil }
func (loopGit) CommitAll(_ context.Context, _, _, _, _ string) error    { return nil }
func (loopGit) Push(_ context.Context, _, _, _, _ string, _ bool) error { return nil }

// TestPromotionLoop_PRReview_ViaWebhook drives a pr-review environment through
// the real SCM webhook handler: the PromotionStep reconciler opens the PR and
// waits for merge, an HMAC-signed GitHub pull_request event marks the PRStatus
// merged, and the reconciler then moves the step to HealthChecking and
// Verified. A request signed with the wrong key is rejected and changes
// nothing (#1290).
func TestPromotionLoop_PRReview_ViaWebhook(t *testing.T) {
	const (
		secret   = "loop-test-secret"
		ns       = "default"
		prStatus = "prstatus-step-prod"
	)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: ns},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/owner/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "prod", Approval: "pr-review"}},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-pr", Namespace: ns},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "step-prod",
			Namespace: ns,
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-pr",
			Environment:  "prod",
			StepType:     "pr-review",
			PRStatusRef:  prStatus,
		},
	}
	// The Graph creates the PRStatus before the PR exists; the reconciler
	// fills in its spec once open-pr returns.
	placeholder := &v1alpha1.PRStatus{ObjectMeta: metav1.ObjectMeta{Name: prStatus, Namespace: ns}}

	c := fake.NewClientBuilder().WithScheme(webhookScheme()).
		WithObjects(pipeline, bundle, step, placeholder).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}).
		Build()

	rec := &psrec.Reconciler{
		Client:    c,
		SCM:       &loopSCM{prURL: "https://github.com/owner/repo/pull/5", prNumber: 5},
		GitClient: loopGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}
	ctx := context.Background()
	stepKey := types.NamespacedName{Name: "step-prod", Namespace: ns}
	stepState := func() string {
		t.Helper()
		var ps v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, stepKey, &ps))
		return ps.Status.State
	}
	reconcileUntil := func(want string) {
		t.Helper()
		for i := 0; i < 20 && stepState() != want; i++ {
			_, err := rec.Reconcile(ctx, ctrl.Request{NamespacedName: stepKey})
			require.NoError(t, err)
		}
		require.Equal(t, want, stepState())
	}
	prsMerged := func() bool {
		t.Helper()
		var prs v1alpha1.PRStatus
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: prStatus, Namespace: ns}, &prs))
		return prs.Status.Merged
	}

	reconcileUntil("WaitingForMerge")
	var prs v1alpha1.PRStatus
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: prStatus, Namespace: ns}, &prs))
	require.Equal(t, 5, prs.Spec.PRNumber, "the reconciler fills in the PRStatus spec the webhook matches on")
	require.Equal(t, "owner/repo", prs.Spec.Repo)

	provider, err := scm.NewProvider("github", "", prAPI(t, "github", 5, false), secret)
	require.NoError(t, err)
	handler := newWebhookServerWithConfig(provider, c, zerolog.Nop(), true).Handler()
	payload := `{"action":"closed","pull_request":{"number":5,"merged":true},"repository":{"full_name":"owner/repo"}}`
	post := func(key string) int {
		t.Helper()
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(payload))
		req := httptest.NewRequest(http.MethodPost, "/webhook/scm", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		handler(w, req)
		return w.Code
	}

	// A forged merge event (wrong key) is rejected and the step keeps waiting.
	assert.Equal(t, http.StatusUnauthorized, post("not-the-secret"))
	assert.False(t, prsMerged(), "a badly signed event must not mark the PRStatus merged")
	_, err = rec.Reconcile(ctx, ctrl.Request{NamespacedName: stepKey})
	require.NoError(t, err)
	assert.Equal(t, "WaitingForMerge", stepState())

	// The signed event marks the PRStatus merged; the webhook does not touch the step.
	require.Equal(t, http.StatusNoContent, post(secret))
	require.True(t, prsMerged(), "the signed merge event marks PRStatus.status.merged")
	assert.Equal(t, "WaitingForMerge", stepState(), "the webhook writes only the PRStatus")

	// The PromotionStep reconciler reads the PRStatus and finishes the promotion.
	_, err = rec.Reconcile(ctx, ctrl.Request{NamespacedName: stepKey})
	require.NoError(t, err)
	assert.Equal(t, "HealthChecking", stepState())
	reconcileUntil("Verified")
}
