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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// fakeSCM implements scm.SCMProvider for testing.
// Only GetPRStatus is exercised; all other methods are no-ops.
type fakeSCM struct {
	merged bool
	open   bool
	err    error
	calls  int
}

func (f *fakeSCM) GetPRStatus(_ context.Context, _ string, _ int) (merged, open bool, err error) {
	f.calls++
	return f.merged, f.open, f.err
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
			name:          "PR closed without merge",
			merged:        false,
			open:          false,
			expectMerged:  false,
			expectOpen:    false,
			expectRequeue: false,
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
