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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// authGit is a git client whose clone and push fail the way go-git does
// against an HTTPS remote when no token is sent. pushErr, when set, is the
// error of every push instead, as when the remote is down.
type authGit struct {
	failPush bool
	cloneErr error
	pushErr  error
}

func (g *authGit) Clone(_ context.Context, url, _, _, token string) error {
	if g.cloneErr != nil {
		return g.cloneErr
	}
	if token == "" && !g.failPush {
		return fmt.Errorf("git clone %s: authentication required: Unauthorized", url)
	}
	return nil
}
func (g *authGit) CloneAt(_ context.Context, _, _, _, _ string) error   { return nil }
func (g *authGit) CommitAll(_ context.Context, _, _, _, _ string) error { return nil }
func (g *authGit) Push(_ context.Context, _, remote, branch, token string, _ bool) error {
	if g.pushErr != nil {
		return g.pushErr
	}
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
				assert.Equal(t, i, got.Status.GitCredentialRetries)
				assert.Zero(t, got.Status.RetryCount, "credential retries keep the retry budget")
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
			assert.Zero(t, got.Status.GitCredentialRetries)
			for _, e := range eventsOf(rec) {
				assert.NotContains(t, e, promotionstep.ConditionGitCredentialMissing, "no Warning once git has a token")
			}
		})
	}
}

// credentialWarnings returns the GitCredentialMissing Events of rec.
func credentialWarnings(rec *events.FakeRecorder) []string {
	var out []string
	for _, e := range eventsOf(rec) {
		if strings.Contains(e, promotionstep.ConditionGitCredentialMissing) {
			out = append(out, e)
		}
	}
	return out
}

// TestGitCredentialRetries_LeaveTheRetryLimit covers the B48 retry budget: the
// retries of a push refused while the git Secret was missing used up the 5
// retries, so after them a push that failed for another reason (the remote
// down, with the Secret still missing or just created) failed the step at
// once with "gave up after 5 retries". That error now gets its 5 retries.
func TestGitCredentialRetries_LeaveTheRetryLimit(t *testing.T) {
	for _, createSecret := range []bool{false, true} {
		t.Run(fmt.Sprintf("Secret created %v", createSecret), func(t *testing.T) {
			ctx := context.Background()
			pipeline := makePipeline("nginx-demo")
			pipeline.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-creds"}
			ps := makeStep("step-cred", "nginx-demo", "b1", "test")
			ps.Status.State = "Promoting"
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(ps, pipeline, makeBundle("b1", "nginx-demo")).Build()
			git := &authGit{failPush: true}
			workDir := filepath.Join(t.TempDir(), "w")
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
				Recorder: events.NewFakeRecorder(50), WorkDirFn: func(_, _ string) string { return workDir }}

			for range 8 {
				reconcileStep(t, r, "step-cred")
			}
			got := getStep(t, c, "step-cred")
			require.Equal(t, "Promoting", got.Status.State, got.Status.Message)
			require.Contains(t, got.Status.Message, "(8, no limit while git has no credentials)")
			require.Contains(t, got.Status.Message, "(git Secret default/git-creds not found)")

			if createSecret {
				require.NoError(t, c.Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "default"},
					Data:       map[string][]byte{"token": []byte("a-token")},
				}))
			}
			git.pushErr = errors.New("git push origin main: dial tcp: connection refused")
			for i := 1; i <= 5; i++ {
				reconcileStep(t, r, "step-cred")
				got = getStep(t, c, "step-cred")
				require.Equal(t, "Promoting", got.Status.State, "retry %d: %s", i, got.Status.Message)
				assert.Equal(t, i, got.Status.RetryCount)
				assert.Contains(t, got.Status.Message, fmt.Sprintf("(%d/5) after error: step git-push: git push origin main: dial tcp: connection refused", i))
			}
			if createSecret {
				assert.Zero(t, got.Status.GitCredentialRetries, "git has a token: the backoff starts over")
			}
			reconcileStep(t, r, "step-cred")
			got = getStep(t, c, "step-cred")
			assert.Equal(t, "Failed", got.Status.State)
			assert.Contains(t, got.Status.Message, "connection refused (gave up after 5 retries)")
		})
	}
}

// TestGitCredentialRetries_ResetOnProgress covers the B48 reset on progress:
// a step that gets past the step that retried starts the next one with no
// retries of either kind. Here the clone failed once on the network and then
// was refused while git had no credentials, then the repository could be
// cloned without them, and the push is refused: its first retry is counted as
// the first, and backs off 10s.
func TestGitCredentialRetries_ResetOnProgress(t *testing.T) {
	ps := makeStep("step-cred", "nginx-demo", "b1", "test")
	ps.Status.State = "Promoting"
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		WithObjects(ps, makePipeline("nginx-demo"), makeBundle("b1", "nginx-demo")).Build()
	git := &authGit{} // the clone needs a token
	workDir := filepath.Join(t.TempDir(), "w")
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
		Recorder: events.NewFakeRecorder(50), WorkDirFn: func(_, _ string) string { return workDir }}

	git.cloneErr = errors.New("git clone https://github.com/test/repo: dial tcp: connection refused")
	reconcileStep(t, r, "step-cred")
	git.cloneErr = nil
	for range 3 {
		reconcileStep(t, r, "step-cred")
	}
	got := getStep(t, c, "step-cred")
	require.Equal(t, 1, got.Status.RetryCount, got.Status.Message)
	require.Equal(t, 3, got.Status.GitCredentialRetries, got.Status.Message)
	require.Contains(t, got.Status.Message, "step git-clone: ")

	git.failPush = true // the clone needs no token now; the push still does
	res, err := r.Reconcile(context.Background(), reqFor("step-cred"))
	require.NoError(t, err)
	got = getStep(t, c, "step-cred")
	require.Contains(t, got.Status.Message, "step git-push: ")
	assert.Equal(t, 1, got.Status.GitCredentialRetries, got.Status.Message)
	assert.Zero(t, got.Status.RetryCount)
	assert.Contains(t, got.Status.Message, "retrying in 10s (1, no limit while git has no credentials)")
	assert.Equal(t, 10*time.Second, res.RequeueAfter)
}

// TestGitCredentialRetries_BackOffTogether covers the B48 backoff: the delay
// grows with the retries of both kinds, so a network error after credential
// retries, with the Secret still missing, does not start the backoff over,
// and neither does a credential retry after it.
func TestGitCredentialRetries_BackOffTogether(t *testing.T) {
	pipeline := makePipeline("nginx-demo")
	pipeline.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-creds"}
	ps := makeStep("step-cred", "nginx-demo", "b1", "test")
	ps.Status.State = "Promoting"
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		WithObjects(ps, pipeline, makeBundle("b1", "nginx-demo")).Build()
	git := &authGit{failPush: true}
	workDir := filepath.Join(t.TempDir(), "w")
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
		Recorder: events.NewFakeRecorder(50), WorkDirFn: func(_, _ string) string { return workDir }}

	steps := []struct {
		pushErr   error
		wantRetry int
		wantCred  int
		wantDelay time.Duration
		wantCount string
	}{
		{wantCred: 1, wantDelay: 10 * time.Second, wantCount: "(1, no limit while git has no credentials)"},
		{wantCred: 2, wantDelay: 20 * time.Second, wantCount: "(2, no limit while git has no credentials)"},
		{pushErr: errors.New("git push origin main: dial tcp: connection refused"),
			wantRetry: 1, wantCred: 2, wantDelay: 40 * time.Second, wantCount: "(1/5)"},
		{wantRetry: 1, wantCred: 3, wantDelay: 80 * time.Second, wantCount: "(3, no limit while git has no credentials)"},
	}
	for i, s := range steps {
		git.pushErr = s.pushErr
		res, err := r.Reconcile(context.Background(), reqFor("step-cred"))
		require.NoError(t, err)
		got := getStep(t, c, "step-cred")
		require.Equal(t, "Promoting", got.Status.State, "reconcile %d: %s", i+1, got.Status.Message)
		assert.Equal(t, s.wantRetry, got.Status.RetryCount, "reconcile %d", i+1)
		assert.Equal(t, s.wantCred, got.Status.GitCredentialRetries, "reconcile %d", i+1)
		assert.Equal(t, s.wantDelay, res.RequeueAfter, "reconcile %d", i+1)
		assert.Contains(t, got.Status.Message, fmt.Sprintf("retrying in %s %s", s.wantDelay, s.wantCount), "reconcile %d", i+1)
	}
}

// TestGitCredentialUnreadable_KeepsTheRetryLimit: a git Secret the controller
// cannot read (Forbidden, a timeout) is not fixed by creating it, so its step
// gets 5 retries, not unlimited ones. The message still names the Secret and
// the read error, and one Warning Event is emitted, also when the retries were
// used up by other errors before the first credential failure.
func TestGitCredentialUnreadable_KeepsTheRetryLimit(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "git-creds", errors.New("RBAC: access denied"))
	for _, otherErrorsFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("other errors first %v", otherErrorsFirst), func(t *testing.T) {
			pipeline := makePipeline("nginx-demo")
			pipeline.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-creds"}
			ps := makeStep("step-cred", "nginx-demo", "b1", "test")
			ps.Status.State = "Promoting"
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(ps, pipeline, makeBundle("b1", "nginx-demo")).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*corev1.Secret); ok {
							return forbidden
						}
						return cl.Get(ctx, key, obj, opts...)
					},
				}).Build()
			git := &authGit{failPush: true}
			rec := events.NewFakeRecorder(50)
			workDir := filepath.Join(t.TempDir(), "w")
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
				Recorder: rec, WorkDirFn: func(_, _ string) string { return workDir }}

			retries := 5
			if otherErrorsFirst {
				git.pushErr = errors.New("git push origin main: dial tcp: connection refused")
				for range 5 {
					reconcileStep(t, r, "step-cred")
				}
				git.pushErr, retries = nil, 0
			}
			for i := 1; i <= retries; i++ {
				reconcileStep(t, r, "step-cred")
				got := getStep(t, c, "step-cred")
				require.Equal(t, "Promoting", got.Status.State, got.Status.Message)
				assert.Contains(t, got.Status.Message, fmt.Sprintf("(%d/5) after error: ", i))
			}
			reconcileStep(t, r, "step-cred")
			got := getStep(t, c, "step-cred")
			assert.Equal(t, "Failed", got.Status.State)
			assert.Contains(t, got.Status.Message, "authentication required: Unauthorized (git Secret default/git-creds could not be read: "+forbidden.Error()+") (gave up after 5 retries)")
			assert.Zero(t, got.Status.GitCredentialRetries)
			cond := meta.FindStatusCondition(got.Status.Conditions, promotionstep.ConditionGitCredentialMissing)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionTrue, cond.Status)
			assert.Equal(t, "SecretUnreadable", cond.Reason)
			warnings := credentialWarnings(rec)
			require.Len(t, warnings, 1, "one Warning Event")
			assert.Contains(t, warnings[0], "could not be read")
		})
	}
}

// TestGitCredentialPresent_KeepsTheRetryLimit: a git failure gets no
// credential note and still fails after 5 retries when git has a token, needs
// none (ssh, a password in the URL), or failed for a reason other than
// credentials, even when the response body uses go-git's words for one.
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
		// The git client puts the body of an HTTP error other than 401, 403
		// and 404 after its status; a body that quotes go-git's words is not
		// the remote refusing git's credentials.
		{name: "no secretRef, a proxy page says authentication required", url: "https://github.com/test/repo",
			cloneErr: "git clone https://github.com/test/repo: HTTP 407 Proxy Authentication Required: Proxy authentication required"},
		{name: "no secretRef, a 500 page says authorization failed", url: "https://github.com/test/repo",
			cloneErr: "git clone https://github.com/test/repo: HTTP 500 Internal Server Error: upstream authorization failed"},
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
