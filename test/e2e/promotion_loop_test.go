// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	bundlerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// mockSCMForLoop simulates a GitHub SCM provider.
// OpenPR opens the PR; merged is set by the test that builds the mock. One
// mock is shared by the goroutines of runPromotionLoops, so mu guards every
// field the methods touch.
type mockSCMForLoop struct {
	mu         sync.Mutex
	merged     bool
	open       bool
	prURL      string
	prNumber   int
	openCalled int
}

func (m *mockSCMForLoop) OpenPR(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.openCalled++
	m.open = true
	return m.prURL, m.prNumber, nil
}
// openCount returns how many times OpenPR was called.
func (m *mockSCMForLoop) openCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openCalled
}
func (m *mockSCMForLoop) ClosePR(_ context.Context, _ string, _ int) error { return nil }
func (m *mockSCMForLoop) CommentOnPR(_ context.Context, _ string, _ int, _ string) error {
	return nil
}
func (m *mockSCMForLoop) GetPRStatus(_ context.Context, _ string, _ int) (bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.merged, m.open, nil
}
func (m *mockSCMForLoop) GetPRReviewStatus(_ context.Context, _ string, _ int) (bool, int, error) {
	return false, 0, nil
}
func (m *mockSCMForLoop) ParseWebhookEvent(payload []byte, _ string) (scm.WebhookEvent, error) {
	var raw struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number int  `json:"number"`
			Merged bool `json:"merged"`
		} `json:"pull_request"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return scm.WebhookEvent{}, err
	}
	return scm.WebhookEvent{
		EventType:    "pull_request",
		Action:       raw.Action,
		Merged:       raw.PullRequest.Merged,
		PRNumber:     raw.PullRequest.Number,
		RepoFullName: raw.Repository.FullName,
	}, nil
}
func (m *mockSCMForLoop) AddLabelsToPR(_ context.Context, _ string, _ int, _ []string) error {
	return nil
}

type mockGitForLoop struct{}

func (m *mockGitForLoop) Clone(_ context.Context, _, _, _, _ string) error        { return nil }
func (m *mockGitForLoop) CloneAt(_ context.Context, _, _, _, _ string) error      { return nil }
func (m *mockGitForLoop) CommitAll(_ context.Context, _, _, _, _ string) error    { return nil }
func (m *mockGitForLoop) Push(_ context.Context, _, _, _, _ string, _ bool) error { return nil }

func promotionLoopScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// TestPromotionLoop_AutoApproval verifies the full promotion loop for an auto-approval
// environment: Bundle → Promoting → PromotionStep runs → Verified.
func TestPromotionLoop_AutoApproval(t *testing.T) {
	s := promotionLoopScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto"},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "step-test",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "test",
			StepType:     "auto",
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, step).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}).
		Build()

	mockSCM := &mockSCMForLoop{prURL: "https://github.com/test/repo/pull/1", prNumber: 1}

	// Wire the PromotionStep reconciler.
	rec := &psrec.Reconciler{
		Client:    c,
		SCM:       mockSCM,
		GitClient: &mockGitForLoop{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	// Also wire the Bundle reconciler to drive supersession (not needed here, but included).
	bundleRec := &bundlerec.Reconciler{Client: c}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "bundle-1", Namespace: "default"}}

	// Drive Bundle → Available → Promoting (without real translator; just verify state machine works).
	_, err := bundleRec.Reconcile(ctx, req)
	require.NoError(t, err)

	// Drive PromotionStep through all states.
	stepReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"}}
	maxIter := 20
	for i := 0; i < maxIter; i++ {
		result, err := rec.Reconcile(ctx, stepReq)
		require.NoError(t, err)

		var ps v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, stepReq.NamespacedName, &ps))
		t.Logf("iteration %d: state=%s index=%d", i, ps.Status.State, ps.Status.CurrentStepIndex)

		if ps.Status.State == "Verified" || ps.Status.State == "Failed" {
			break
		}
		if !result.Requeue && result.RequeueAfter == 0 { //nolint:staticcheck
			// State machine should always requeue when not terminal.
			break
		}
		time.Sleep(1 * time.Millisecond) // let goroutines settle
	}

	var finalStep v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, stepReq.NamespacedName, &finalStep))
	assert.Equal(t, "Verified", finalStep.Status.State,
		"auto-approval step should reach Verified; got %s: %s", finalStep.Status.State, finalStep.Status.Message)
}

// TestPromotionLoop_Idempotency verifies that re-creating a PromotionStep does not
// duplicate PRs (idempotency via prURL in outputs).
func TestPromotionLoop_Idempotency(t *testing.T) {
	s := promotionLoopScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "prod", Approval: "pr-review"},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-idem", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}

	// PRStatus CRD already merged (simulating PRStatusReconciler having polled)
	now := metav1.Now()
	prStatus := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-step-idem", Namespace: "default"},
		Spec: v1alpha1.PRStatusSpec{
			PRURL:    "https://github.com/test/repo/pull/99",
			PRNumber: 99,
			Repo:     "test/repo",
		},
		Status: v1alpha1.PRStatusStatus{Merged: true, Open: false, LastCheckedAt: &now},
	}

	// Start the step mid-sequence with prURL already set (simulates crash recovery).
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-idem", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-idem",
			Environment:  "prod",
			StepType:     "pr-review",
			PRStatusRef:  "prstatus-step-idem",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:            "WaitingForMerge",
			PRURL:            "https://github.com/test/repo/pull/99",
			CurrentStepIndex: 5, // past open-pr
			Outputs: map[string]string{
				"prURL":    "https://github.com/test/repo/pull/99",
				"prNumber": "99",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, step, prStatus).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}).
		Build()

	// PR is already merged — SCM not needed for the new WaitingForMerge path.
	mockSCM := &mockSCMForLoop{prURL: "https://github.com/test/repo/pull/99", prNumber: 99, merged: true}
	rec := &psrec.Reconciler{
		Client:    c,
		SCM:       mockSCM,
		GitClient: &mockGitForLoop{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-idem", Namespace: "default"}}

	// Reconcile twice — should not call OpenPR at all (idempotent).
	for i := 0; i < 5; i++ {
		result, err := rec.Reconcile(ctx, req)
		require.NoError(t, err)

		var ps v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, req.NamespacedName, &ps))
		if ps.Status.State == "Verified" || ps.Status.State == "Failed" {
			break
		}
		if !result.Requeue && result.RequeueAfter == 0 { //nolint:staticcheck
			break
		}
	}

	var final v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, req.NamespacedName, &final))
	assert.Equal(t, "Verified", final.Status.State)
	// OpenPR must not be called (PR was already open).
	assert.Equal(t, 0, mockSCM.openCount(), "open-pr must not be called when prURL is already in outputs")
}

// TestMockSCMForLoop_ConcurrentUse checks that the mock is safe to share
// between goroutines, as runPromotionLoops shares one reconciler (and so one
// mock) across 100 of them. go test -race fails it when the counter or the
// PR state is written without the lock (#1354).
func TestMockSCMForLoop_ConcurrentUse(t *testing.T) {
	m := &mockSCMForLoop{prURL: "https://github.com/test/repo/pull/1", prNumber: 1}
	ctx := context.Background()
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, err := m.OpenPR(ctx, "", "", "", "", "")
			assert.NoError(t, err)
		}()
		go func() {
			defer wg.Done()
			_, _, err := m.GetPRStatus(ctx, "", 1)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, n, m.openCount())
	merged, open, err := m.GetPRStatus(ctx, "", 1)
	require.NoError(t, err)
	assert.False(t, merged)
	assert.True(t, open)
}
