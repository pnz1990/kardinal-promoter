// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestScmProvider_StepUsesItsProvider (D3): a step with spec.scmProvider
// opens its PR with that provider's client, built from the provider's
// Secret, never the controller's; the PRStatus spec gets the identity so
// polling and webhooks use the same provider. A provider that is gone or no
// longer allows the repository fails the step instead of falling back.
func TestScmProvider_StepUsesItsProvider(t *testing.T) {
	tests := []struct {
		name      string
		uid       string   // of the step's identity
		allowed   []string // the provider's allowedRepositories
		wantState string
		wantMsg   string
	}{
		{name: "provider client", uid: "uid-1", wantState: "WaitingForMerge"},
		{name: "provider recreated", uid: "uid-old", wantState: "Failed", wantMsg: "deleted and created again"},
		{name: "repository no longer allowed", uid: "uid-1", allowed: []string{"other/*"}, wantState: "Failed",
			wantMsg: "spec.allowedRepositories"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pl, b := makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")
			step := builtStep(t, pl, b, "prod")
			id := &v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: "team-gh", UID: tt.uid}
			step.Spec.ScmProvider = id
			prov := &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "team-gh", UID: "uid-1"},
				Spec: v1alpha1.ScmProviderSpec{Type: "github", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "team-token"},
					AllowedRepositories: tt.allowed}}
			tok := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "team-token"},
				Data: map[string][]byte{"token": []byte("team")}}
			api := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
				WithObjects(step, pl, b, prov, tok, openPRStatus(step.Spec.PRStatusRef, "", 0)).Build()

			controllerSCM := &mockSCM{open: true}
			teamSCM := &mockSCM{open: true, prURL: "https://github.com/test/repo/pull/9", prNumber: 9}
			var tokens []string
			r := &promotionstep.Reconciler{Client: api, SCM: controllerSCM, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() },
				Providers: &scm.Registry{Client: api, New: func(_, token, _, _ string) (scm.SCMProvider, error) {
					tokens = append(tokens, token)
					return teamSCM, nil
				}}}

			for i := 0; i < 2; i++ {
				reconcileStep(t, r, step.Name)
			}
			got := getStep(t, api, step.Name)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Zero(t, controllerSCM.openCalled, "the controller's provider is never used for this step")
			if tt.wantState == "Failed" {
				assert.Contains(t, got.Status.Message, tt.wantMsg)
				assert.Zero(t, teamSCM.openCalled)
				return
			}
			assert.Equal(t, 1, teamSCM.openCalled)
			assert.Equal(t, []string{"team"}, tokens, "built once from the provider's Secret, then cached")
			var prs v1alpha1.PRStatus
			require.NoError(t, api.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: step.Spec.PRStatusRef}, &prs))
			assert.Equal(t, 9, prs.Spec.PRNumber)
			assert.Equal(t, id, prs.Spec.ScmProvider, "the PRStatus is polled through the same provider")

			// Idempotent: another reconcile opens nothing more.
			reconcileStep(t, r, step.Name)
			assert.Equal(t, 1, teamSCM.openCalled)
		})
	}
}
