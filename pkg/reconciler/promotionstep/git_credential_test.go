// Copyright 2026 The kardinal-promoter Authors.
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

package promotionstep_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// authGit is a git client whose clone and push fail the way go-git does
// against an HTTPS remote when no token is sent.
type authGit struct{ failPush bool }

func (g *authGit) Clone(_ context.Context, url, _, _, token string) error {
	if token == "" && !g.failPush {
		return fmt.Errorf("git clone %s: authentication required: Unauthorized", url)
	}
	return nil
}
func (g *authGit) CloneAt(_ context.Context, _, _, _, _ string) error   { return nil }
func (g *authGit) CommitAll(_ context.Context, _, _, _, _ string) error { return nil }
func (g *authGit) Push(_ context.Context, _, remote, branch, token string, _ bool) error {
	if token == "" {
		return fmt.Errorf("git push %s %s: authentication required: Unauthorized", remote, branch)
	}
	return nil
}

func eventsOf(rec *events.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// TestGitCredentialMissing covers B48: a Pipeline with no spec.git.secretRef,
// or whose Secret does not exist, used to fail git-clone or git-push with only
// "authentication required" (the controller logged a warning and nothing
// else), and the step gave up after 5 retries. The step message now says what
// is missing after the git error, one Warning Event is emitted however many
// times the step retries, and the step keeps retrying, so creating the Secret
// lets it continue with no other action.
func TestGitCredentialMissing(t *testing.T) {
	tests := []struct {
		name      string
		secretRef *v1alpha1.SecretRef
		failPush  bool // the repo clones without a token (public) and push fails
		wantMsg   string
		wantCond  string
	}{
		{name: "secretRef not set, push fails", failPush: true,
			wantMsg:  "step git-push: git push origin main: authentication required: Unauthorized (spec.git.secretRef is not set, so the push has no credentials)",
			wantCond: "SecretRefNotSet"},
		{name: "secretRef not set, clone fails",
			wantMsg:  "step git-clone: git clone https://github.com/test/repo: authentication required: Unauthorized (spec.git.secretRef is not set, so the clone has no credentials)",
			wantCond: "SecretRefNotSet"},
		{name: "Secret not found, push fails", secretRef: &v1alpha1.SecretRef{Name: "git-creds"}, failPush: true,
			wantMsg:  "step git-push: git push origin main: authentication required: Unauthorized (git Secret default/git-creds not found)",
			wantCond: "SecretNotFound"},
		{name: "Secret not found, clone fails", secretRef: &v1alpha1.SecretRef{Name: "git-creds"},
			wantMsg:  "(git Secret default/git-creds not found)",
			wantCond: "SecretNotFound"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pipeline := makePipeline("nginx-demo")
			pipeline.Spec.Git.SecretRef = tt.secretRef
			bundle := makeBundle("b1", "nginx-demo")
			ps := makeStep("step-cred", "nginx-demo", "b1", "test")
			ps.Status.State = "Promoting"
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(ps, pipeline, bundle).Build()
			rec := events.NewFakeRecorder(50)
			workDir := filepath.Join(t.TempDir(), "w")
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &authGit{failPush: tt.failPush},
				Recorder: rec, WorkDirFn: func(_, _ string) string { return workDir }}

			// Past the retry limit (5): the step still retries.
			for i := 1; i <= 7; i++ {
				reconcileStep(t, r, "step-cred")
				got := getStep(t, c, "step-cred")
				require.Equal(t, "Promoting", got.Status.State, got.Status.Message)
				assert.Equal(t, i, got.Status.RetryCount)
				assert.Contains(t, got.Status.Message, tt.wantMsg)
				assert.Equal(t, 1, strings.Count(got.Status.Message, "authentication required"), got.Status.Message)
			}
			got := getStep(t, c, "step-cred")
			assert.Contains(t, got.Status.Message, "(7, no limit while git has no credentials)")
			cond := meta.FindStatusCondition(got.Status.Conditions, promotionstep.ConditionGitCredentialMissing)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionTrue, cond.Status)
			assert.Equal(t, tt.wantCond, cond.Reason)

			var warnings []string
			for _, e := range eventsOf(rec) {
				if strings.Contains(e, promotionstep.ConditionGitCredentialMissing) {
					warnings = append(warnings, e)
				}
			}
			require.Len(t, warnings, 1, "one Warning Event, not one per retry")
			assert.True(t, strings.HasPrefix(warnings[0], "Warning GitCredentialMissing"), warnings[0])

			// Create the Secret (and the secretRef when it was not set): the
			// next retry has a token and the git steps go through.
			require.NoError(t, c.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"},
				Data:       map[string][]byte{"token": []byte("a-token")},
			}))
			if tt.secretRef == nil {
				var p v1alpha1.Pipeline
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pipeline), &p))
				p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-creds"}
				require.NoError(t, c.Update(ctx, &p))
			}
			reconcileStep(t, r, "step-cred")
			got = getStep(t, c, "step-cred")
			assert.NotContains(t, got.Status.Message, "authentication required")
			assert.NotContains(t, got.Status.Message, "a-token")
			assert.Equal(t, "HealthChecking", got.Status.State, "the git steps went through: %s", got.Status.Message)
			cond = meta.FindStatusCondition(got.Status.Conditions, promotionstep.ConditionGitCredentialMissing)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			for _, e := range eventsOf(rec) {
				assert.NotContains(t, e, promotionstep.ConditionGitCredentialMissing, "no Warning once git has a token")
			}
		})
	}
}

// TestGitCredentialPresent_KeepsTheRetryLimit: a git failure gets no
// credential note and still fails after 5 retries when git has a token, needs
// none (ssh, a password in the URL), or failed for a reason other than
// credentials.
func TestGitCredentialPresent_KeepsTheRetryLimit(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		secret   bool
		cloneErr string
	}{
		{name: "token set, auth refused", url: "https://github.com/test/repo", secret: true,
			cloneErr: "git clone: authentication required: Bad credentials"},
		{name: "ssh remote without secretRef", url: "ssh://git@github.com/test/repo.git"},
		{name: "password in the URL", url: "https://bot:pw@git.example/test/repo"},
		{name: "no secretRef, the remote is down", url: "https://github.com/test/repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := makePipeline("nginx-demo")
			pipeline.Spec.Git.URL = tt.url
			objs := []client.Object{pipeline, makeBundle("b1", "nginx-demo")}
			if tt.secret {
				pipeline.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-creds"}
				objs = append(objs, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"},
					Data:       map[string][]byte{"token": []byte("a-token")},
				})
			}
			ps := makeStep("step-cred", "nginx-demo", "b1", "test")
			ps.Status.State = "Promoting"
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(append(objs, ps)...).Build()
			rec := events.NewFakeRecorder(50)
			cloneErr := tt.cloneErr
			if cloneErr == "" {
				cloneErr = "git clone: 503 Service Unavailable"
			}
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{},
				GitClient: &mockGit{cloneErr: errors.New(cloneErr)},
				Recorder:  rec, WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}

			for range 6 {
				reconcileStep(t, r, "step-cred")
			}
			got := getStep(t, c, "step-cred")
			assert.Equal(t, "Failed", got.Status.State)
			assert.Contains(t, got.Status.Message, "gave up after 5 retries")
			assert.NotContains(t, got.Status.Message, "no credentials")
			assert.NotContains(t, got.Status.Message, "Secret")
			assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, promotionstep.ConditionGitCredentialMissing))
			for _, e := range eventsOf(rec) {
				assert.NotContains(t, e, promotionstep.ConditionGitCredentialMissing)
			}
		})
	}
}
