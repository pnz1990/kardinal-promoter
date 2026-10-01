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

package prstatus_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone/objectgonetest"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// fakeSCM implements scm.SCMProvider for testing.
// Both GetPRStatus and GetPRReviewStatus are exercised by test cases.
type fakeSCM struct {
	merged        bool
	open          bool
	err           error
	calls         int
	approved      bool
	approvalCount int
	reviewErr     error
	reviewCalls   int
}

func (f *fakeSCM) GetPRStatus(_ context.Context, _ string, _ int) (merged, open bool, err error) {
	f.calls++
	return f.merged, f.open, f.err
}

func (f *fakeSCM) GetPRReviewStatus(_ context.Context, _ string, _ int) (approved bool, approvalCount int, err error) {
	f.reviewCalls++
	return f.approved, f.approvalCount, f.reviewErr
}

func (f *fakeSCM) OpenPR(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	return "", 0, nil
}

func (f *fakeSCM) ClosePR(_ context.Context, _ string, _ int) error {
	return nil
}

func (f *fakeSCM) CommentOnPR(_ context.Context, _ string, _ int, _ string) error {
	return nil
}

func (f *fakeSCM) ParseWebhookEvent(_ []byte, _ string) (scm.WebhookEvent, error) {
	return scm.WebhookEvent{}, nil
}

func (f *fakeSCM) AddLabelsToPR(_ context.Context, _ string, _ int, _ []string) error {
	return nil
}

// buildScheme creates a scheme with our CRDs registered.
func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func TestReconciler_OpenPR_PollsAndSetsMerged(t *testing.T) {
	tests := []struct {
		name          string
		merged        bool
		open          bool
		expectMerged  bool
		expectOpen    bool
		expectRequeue bool
	}{
		{
			name:          "open PR not yet merged",
			merged:        false,
			open:          true,
			expectMerged:  false,
			expectOpen:    true,
			expectRequeue: true,
		},
		{
			name:          "PR merged",
			merged:        true,
			open:          false,
			expectMerged:  true,
			expectOpen:    false,
			expectRequeue: false,
		},
		{
			// Still polled for the grace window (#1306).
			name:          "PR closed without merge",
			merged:        false,
			open:          false,
			expectMerged:  false,
			expectOpen:    false,
			expectRequeue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := buildScheme(t)

			prs := &v1alpha1.PRStatus{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pr",
					Namespace: "default",
				},
				Spec: v1alpha1.PRStatusSpec{
					PRURL:    "https://github.com/owner/repo/pull/42",
					PRNumber: 42,
					Repo:     "owner/repo",
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(prs).
				WithStatusSubresource(&v1alpha1.PRStatus{}).
				Build()

			scm := &fakeSCM{merged: tt.merged, open: tt.open}
			r := &prstatus.Reconciler{
				Client: fakeClient,
				SCM:    scm,
			}

			result, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "test-pr", Namespace: "default"},
			})
			require.NoError(t, err)

			// Check requeue behaviour
			if tt.expectRequeue {
				assert.Greater(t, result.RequeueAfter.Milliseconds(), int64(0), "expected RequeueAfter > 0")
			} else {
				assert.Zero(t, result.RequeueAfter, "expected no RequeueAfter")
			}

			// Verify status was written
			var updated v1alpha1.PRStatus
			require.NoError(t, fakeClient.Get(context.Background(),
				types.NamespacedName{Name: "test-pr", Namespace: "default"}, &updated))

			assert.Equal(t, tt.expectMerged, updated.Status.Merged, "status.merged")
			assert.Equal(t, tt.expectOpen, updated.Status.Open, "status.open")
			assert.NotNil(t, updated.Status.LastCheckedAt, "status.lastCheckedAt should be set")
			assert.Equal(t, 1, scm.calls, "GetPRStatus should be called once")
		})
	}
}

func TestReconciler_AlreadyMerged_IsNoOp(t *testing.T) {
	scheme := buildScheme(t)

	now := metav1.Now()
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pr",
			Namespace: "default",
		},
		Spec: v1alpha1.PRStatusSpec{
			PRURL:    "https://github.com/owner/repo/pull/42",
			PRNumber: 42,
			Repo:     "owner/repo",
		},
		Status: v1alpha1.PRStatusStatus{
			Merged:        true,
			Open:          false,
			LastCheckedAt: &now,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(prs).
		WithStatusSubresource(&v1alpha1.PRStatus{}).
		Build()

	scm := &fakeSCM{}
	r := &prstatus.Reconciler{
		Client: fakeClient,
		SCM:    scm,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-pr", Namespace: "default"},
	})
	require.NoError(t, err)

	// No SCM call should be made for already-merged PRs
	assert.Equal(t, 0, scm.calls, "GetPRStatus should NOT be called for already-merged PR")
}

func TestReconciler_NilSCM_RequeuesWithoutCrash(t *testing.T) {
	scheme := buildScheme(t)

	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pr",
			Namespace: "default",
		},
		Spec: v1alpha1.PRStatusSpec{
			PRURL:    "https://github.com/owner/repo/pull/42",
			PRNumber: 42,
			Repo:     "owner/repo",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(prs).
		WithStatusSubresource(&v1alpha1.PRStatus{}).
		Build()

	r := &prstatus.Reconciler{
		Client: fakeClient,
		SCM:    nil, // no SCM configured
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-pr", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter.Milliseconds(), int64(0), "should requeue when SCM nil")
}

func TestReconciler_IdempotentOnSecondReconcile(t *testing.T) {
	scheme := buildScheme(t)

	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pr",
			Namespace: "default",
		},
		Spec: v1alpha1.PRStatusSpec{
			PRURL:    "https://github.com/owner/repo/pull/42",
			PRNumber: 42,
			Repo:     "owner/repo",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(prs).
		WithStatusSubresource(&v1alpha1.PRStatus{}).
		Build()

	scm := &fakeSCM{merged: true, open: false}
	r := &prstatus.Reconciler{
		Client: fakeClient,
		SCM:    scm,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-pr", Namespace: "default"}}

	// First reconcile: sets merged=true
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, 1, scm.calls)

	// Second reconcile: should be a no-op (merged=true path)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, 1, scm.calls, "second reconcile should not call SCM again")
}

// TestReconciler_EmptySpec_NoSCMCall is a regression test for issue #276
// (PRStatus with empty spec — graph placeholder — must not trigger SCM calls).
//
// When the Graph creates a PRStatus Watch node as a placeholder (before the
// open-pr step fills in the PR URL and number), the spec fields are empty/zero.
// The reconciler must skip SCM polling for placeholder PRStatus objects.
// Calling GetPRStatus with prNumber=0 would cause an API error.
func TestReconciler_EmptySpec_NoSCMCall(t *testing.T) {
	scheme := buildScheme(t)

	// Placeholder PRStatus: all spec fields empty/zero (mirroring what Graph creates
	// before the open-pr step runs and sets the real PR URL+number).
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "placeholder-pr",
			Namespace: "default",
		},
		Spec: v1alpha1.PRStatusSpec{
			PRURL:    "", // empty — not yet opened
			PRNumber: 0,  // zero — not yet opened
			Repo:     "", // empty — not yet opened
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(prs).
		WithStatusSubresource(&v1alpha1.PRStatus{}).
		Build()

	scm := &fakeSCM{}
	r := &prstatus.Reconciler{
		Client: fakeClient,
		SCM:    scm,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "placeholder-pr", Namespace: "default"},
	})

	require.NoError(t, err, "empty-spec PRStatus must not error (#276 regression)")
	assert.Equal(t, 0, scm.calls, "GetPRStatus must NOT be called for placeholder PRStatus with empty spec (#276 regression)")
	// The spec patch that sets the PR triggers the next reconcile; see
	// TestReconciler_PlaceholderNotRequeued.
	assert.Equal(t, ctrl.Result{}, result, "placeholder PRStatus is not requeued")
}

// TestReconciler_PlaceholderNotRequeued checks that a PRStatus with no PR
// number is neither polled nor requeued, with or without an SCM provider: it
// was requeued every 30 seconds for as long as its Bundle lived (spike bug 7).
// Once the PromotionStep reconciler patches the spec (patchPRStatusSpec), the
// update event reconciles it again and the PR is polled.
func TestReconciler_PlaceholderNotRequeued(t *testing.T) {
	tests := []struct {
		name string
		spec v1alpha1.PRStatusSpec
		scm  bool
	}{
		{name: "empty spec", scm: true},
		{name: "URL without a PR number", spec: v1alpha1.PRStatusSpec{PRURL: "https://github.com/owner/repo/pull/x", Repo: "owner/repo"}, scm: true},
		{name: "no SCM configured", scm: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prs := &v1alpha1.PRStatus{
				ObjectMeta: metav1.ObjectMeta{Name: "app-prod-pr", Namespace: "default"},
				Spec:       tt.spec,
			}
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithObjects(prs).WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			fs := &fakeSCM{open: true}
			r := &prstatus.Reconciler{Client: c}
			if tt.scm {
				r.SCM = fs
			}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-prod-pr", Namespace: "default"}}

			result, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result, "a placeholder has no PR to poll and is not requeued")
			assert.Zero(t, fs.calls, "no SCM call for a placeholder")
			var got v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Nil(t, got.Status.LastCheckedAt, "a placeholder's status is not written")

			if !tt.scm {
				return
			}
			// What patchPRStatusSpec does once the open-pr step opened the PR.
			patch := client.MergeFrom(got.DeepCopy())
			got.Spec = v1alpha1.PRStatusSpec{PRURL: "https://github.com/owner/repo/pull/42", PRNumber: 42, Repo: "owner/repo"}
			require.NoError(t, c.Patch(context.Background(), &got, patch))

			result, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, 1, fs.calls, "the PR is polled once the spec names it")
			assert.Positive(t, result.RequeueAfter, "an open PR is polled again")
		})
	}
}

// TestReconciler_ZeroPRNumber_NoSCMCall is a regression test for issue #276.
// PRNumber=0 explicitly means the PR has not yet been created. The reconciler
// must not call GetPRStatus with prNumber=0 as that would cause a 404 from GitHub.
func TestReconciler_ZeroPRNumber_NoSCMCall(t *testing.T) {
	scheme := buildScheme(t)

	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "zero-pr-number",
			Namespace: "default",
		},
		Spec: v1alpha1.PRStatusSpec{
			PRURL:    "https://github.com/owner/repo/pull/0",
			PRNumber: 0, // explicitly zero — not yet assigned
			Repo:     "owner/repo",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(prs).
		WithStatusSubresource(&v1alpha1.PRStatus{}).
		Build()

	scm := &fakeSCM{}
	r := &prstatus.Reconciler{
		Client: fakeClient,
		SCM:    scm,
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "zero-pr-number", Namespace: "default"},
	})

	require.NoError(t, err, "zero prNumber must not error (#276 regression)")
	assert.Equal(t, 0, scm.calls,
		"GetPRStatus must NOT be called for PRStatus with prNumber=0 (#276 regression)")
}

// TestReconciler_ReviewStatus_PollsApprovedState verifies that the reconciler
// calls GetPRReviewStatus and writes status.approved + status.approvalCount.
func TestReconciler_ReviewStatus_PollsApprovedState(t *testing.T) {
	tests := []struct {
		name                string
		approved            bool
		approvalCount       int
		expectApproved      bool
		expectApprovalCount int
	}{
		{"no approvals", false, 0, false, 0},
		{"one approval", true, 1, true, 1},
		{"two approvals", true, 2, true, 2},
		{"change requested blocks", false, 1, false, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := buildScheme(t)
			prs := &v1alpha1.PRStatus{
				ObjectMeta: metav1.ObjectMeta{Name: "pr-reviews", Namespace: "default"},
				Spec: v1alpha1.PRStatusSpec{
					PRURL: "https://github.com/owner/repo/pull/7", PRNumber: 7, Repo: "owner/repo",
				},
			}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(prs).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			fakeSCM := &fakeSCM{
				merged:        false,
				open:          true,
				approved:      tt.approved,
				approvalCount: tt.approvalCount,
			}
			r := &prstatus.Reconciler{Client: fakeClient, SCM: fakeSCM}

			_, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "pr-reviews", Namespace: "default"},
			})
			require.NoError(t, err)

			var updated v1alpha1.PRStatus
			require.NoError(t, fakeClient.Get(context.Background(),
				types.NamespacedName{Name: "pr-reviews", Namespace: "default"}, &updated))

			assert.Equal(t, tt.expectApproved, updated.Status.Approved, "status.approved")
			assert.Equal(t, tt.expectApprovalCount, updated.Status.ApprovalCount, "status.approvalCount")
			assert.Equal(t, 1, fakeSCM.reviewCalls, "GetPRReviewStatus should be called once")
		})
	}
}

// TestReconciler_ReviewStatus_FallbackOnError verifies that when GetPRReviewStatus
// fails, the reconciler preserves the previous approved state and does not error.
func TestReconciler_ReviewStatus_FallbackOnError(t *testing.T) {
	scheme := buildScheme(t)
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "pr-fallback", Namespace: "default"},
		Spec: v1alpha1.PRStatusSpec{
			PRURL: "https://github.com/owner/repo/pull/9", PRNumber: 9, Repo: "owner/repo",
		},
		Status: v1alpha1.PRStatusStatus{
			Approved: true, ApprovalCount: 2, // pre-existing good state
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(prs).
		WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
	fakeSCM := &fakeSCM{
		merged:    false,
		open:      true,
		reviewErr: fmt.Errorf("GitHub API 503"),
	}
	r := &prstatus.Reconciler{Client: fakeClient, SCM: fakeSCM}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "pr-fallback", Namespace: "default"},
	})
	require.NoError(t, err, "review error must not propagate")

	var updated v1alpha1.PRStatus
	require.NoError(t, fakeClient.Get(context.Background(),
		types.NamespacedName{Name: "pr-fallback", Namespace: "default"}, &updated))

	// Must preserve previous approved state when review poll fails.
	assert.True(t, updated.Status.Approved, "approved must be preserved on review error")
	assert.Equal(t, 2, updated.Status.ApprovalCount, "approvalCount must be preserved on review error")
}

// TestReconciler_DeletedBeforeStatusWrite: a PRStatus deleted between its
// read and the status write after a poll ends the reconcile with no error,
// requeue or warn or error log.
func TestReconciler_DeletedBeforeStatusWrite(t *testing.T) {
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pr", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/owner/repo/pull/42", PRNumber: 42, Repo: "owner/repo"},
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(prs).WithStatusSubresource(prs).
		WithInterceptorFuncs(objectgonetest.DeleteOnWrite(t, nil)).Build()
	r := &prstatus.Reconciler{Client: c, SCM: &fakeSCM{open: true}}
	var logs bytes.Buffer
	res, err := r.Reconcile(objectgonetest.Context(&logs), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-pr", Namespace: "default"},
	})
	objectgonetest.AssertQuiet(t, res, err, &logs)
}
