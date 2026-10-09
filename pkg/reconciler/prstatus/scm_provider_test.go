// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package prstatus_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestReconciler_PollsThroughItsProvider (D3): a PRStatus with
// spec.scmProvider is polled with that provider's client, never the
// controller's; one whose provider is gone records status.pollError, which
// fails the step, instead of polling another SCM for the same PR number.
func TestReconciler_PollsThroughItsProvider(t *testing.T) {
	tests := []struct {
		name          string
		uid           string
		wantMerged    bool
		wantPollError string
	}{
		{name: "provider client", uid: "uid-1", wantMerged: true},
		{name: "provider gone", uid: "uid-old", wantPollError: "deleted and created again"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := buildScheme(t)
			require.NoError(t, corev1.AddToScheme(s))
			prs := &v1alpha1.PRStatus{ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: "default"},
				Spec: v1alpha1.PRStatusSpec{PRURL: "https://gl.example/acme/app/-/merge_requests/3", PRNumber: 3, Repo: "acme/app",
					ScmProvider: &v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "gl", UID: tt.uid}}}
			prov := &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "gl", UID: "uid-1"},
				Spec: v1alpha1.ScmProviderSpec{Type: "gitlab", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok"}}}
			tok := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "tok"}, Data: map[string][]byte{"token": []byte("x")}}
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(prs, prov, tok).WithStatusSubresource(prs).Build()

			controller := &fakeSCM{open: true}
			team := &fakeSCM{merged: true}
			r := &prstatus.Reconciler{Client: c, SCM: controller,
				Providers: &scm.Registry{Client: c, New: func(_, _, _, _ string) (scm.SCMProvider, error) { return team, nil }}}
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "pr"}})
			require.NoError(t, err)

			var got v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "pr"}, &got))
			assert.Zero(t, controller.calls, "the controller's provider is never asked about this PR")
			assert.Equal(t, tt.wantMerged, got.Status.Merged)
			if tt.wantPollError != "" {
				assert.Contains(t, got.Status.PollError, tt.wantPollError)
				assert.Zero(t, team.calls)
				return
			}
			assert.Equal(t, 1, team.calls)
			assert.Empty(t, got.Status.PollError)
		})
	}
}
