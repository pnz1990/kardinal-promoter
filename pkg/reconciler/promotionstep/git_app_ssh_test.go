// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// authRecorder is a git client that records the auth of every clone and
// push, and refuses an HTTPS operation without a token as go-git does.
type authRecorder struct {
	mu    sync.Mutex
	auths []scm.GitAuth
}

func (g *authRecorder) record(url string, a scm.GitAuth, op string) error {
	g.mu.Lock()
	g.auths = append(g.auths, a)
	g.mu.Unlock()
	if a.Token == "" && len(a.SSHPrivateKey) == 0 {
		return fmt.Errorf("git %s %s: authentication required: Unauthorized", op, url)
	}
	return nil
}
func (g *authRecorder) Clone(_ context.Context, url, _, _ string, a scm.GitAuth) error {
	return g.record(url, a, "clone")
}
func (g *authRecorder) CloneAt(_ context.Context, _, _, _ string, _ scm.GitAuth) error { return nil }
func (g *authRecorder) CommitAll(_ context.Context, _, _, _, _ string) error           { return nil }
func (g *authRecorder) Push(_ context.Context, _, remote, _ string, a scm.GitAuth, _ bool) error {
	return g.record(remote, a, "push")
}

// appMintServer issues installation tokens; with status set it refuses.
func appMintServer(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var mints atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/777/access_tokens" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"A JSON web token could not be decoded"}`))
			return
		}
		n := mints.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"token":"ghs_git_%d","expires_at":"2999-01-01T00:00:00Z"}`, n)
	}))
	t.Cleanup(srv.Close)
	return srv, &mints
}

func appSecretData(t *testing.T) map[string][]byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return map[string][]byte{
		"githubAppID": []byte("4242"), "githubAppInstallationID": []byte("777"),
		"githubAppPrivateKey": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}),
	}
}

// TestGitCredential_GitHubApp: a git Secret with GitHub App credentials gives
// the clone and the push of an HTTPS remote an installation token, minted at
// the controller's GitHub API and shared by the steps that follow; a mint
// GitHub refuses is named in the step message and the GitCredentialMissing
// condition (reason GitHubAppTokenFailed).
// Covers SCM-GHAPP-03.
func TestGitCredential_GitHubApp(t *testing.T) {
	t.Run("token minted", func(t *testing.T) {
		srv, mints := appMintServer(t, 0)
		pipeline := makePipeline("nginx-demo")
		pipeline.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-app"}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-app", Namespace: "default"}, Data: appSecretData(t)}
		ps := asPromoting(makeStep("step-app", "nginx-demo", "b1", "test"), pipeline)
		c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
			WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
			WithObjects(ps, pipeline, makeBundle("b1", "nginx-demo"), secret).Build()
		git := &authRecorder{}
		workDir := filepath.Join(t.TempDir(), "w")
		r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
			WorkDirFn: func(_, _ string) string { return workDir }, NowFn: pastBackoff(),
			GitHubAppTokens: &scm.AppTokenCache{APIURL: srv.URL}}

		reconcileStep(t, r, "step-app")
		got := getStep(t, c, "step-app")
		assert.Equal(t, "HealthChecking", got.Status.State, got.Status.Message)
		require.Len(t, git.auths, 2, "a clone and a push")
		for _, a := range git.auths {
			assert.Equal(t, "ghs_git_1", a.Token)
		}
		assert.EqualValues(t, 1, mints.Load())
		assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, promotionstep.ConditionGitCredentialMissing))
	})

	t.Run("mint refused", func(t *testing.T) {
		srv, _ := appMintServer(t, http.StatusUnauthorized)
		pipeline := makePipeline("nginx-demo")
		pipeline.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-app"}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-app", Namespace: "default"}, Data: appSecretData(t)}
		ps := asPromoting(makeStep("step-app", "nginx-demo", "b1", "test"), pipeline)
		c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
			WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
			WithObjects(ps, pipeline, makeBundle("b1", "nginx-demo"), secret).Build()
		workDir := filepath.Join(t.TempDir(), "w")
		r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &authRecorder{},
			WorkDirFn: func(_, _ string) string { return workDir }, NowFn: pastBackoff(),
			GitHubAppTokens: &scm.AppTokenCache{APIURL: srv.URL}}

		reconcileStep(t, r, "step-app")
		got := getStep(t, c, "step-app")
		assert.Equal(t, "Promoting", got.Status.State)
		assert.Contains(t, got.Status.Message, "(git Secret default/git-app has GitHub App credentials, and no installation token could be minted: ")
		assert.Contains(t, got.Status.Message, "status 401")
		assert.NotContains(t, got.Status.Message, "PRIVATE KEY")
		cond := meta.FindStatusCondition(got.Status.Conditions, promotionstep.ConditionGitCredentialMissing)
		require.NotNil(t, cond)
		assert.Equal(t, "GitHubAppTokenFailed", cond.Reason)
	})
}

// TestGitCredential_SSH: for an ssh remote the git steps get the git
// Secret's sshPrivateKey and knownHosts, and no token is needed.
// Covers SCM-SSH-02.
func TestGitCredential_SSH(t *testing.T) {
	pipeline := makePipeline("nginx-demo")
	pipeline.Spec.Git.URL = "ssh://git@git.example.com:2222/test/repo.git"
	pipeline.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-ssh"}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-ssh", Namespace: "default"},
		Data: map[string][]byte{"sshPrivateKey": []byte("KEY"), "knownHosts": []byte("[git.example.com]:2222 ssh-ed25519 AAAA")}}
	ps := asPromoting(makeStep("step-ssh", "nginx-demo", "b1", "test"), pipeline)
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		WithObjects(ps, pipeline, makeBundle("b1", "nginx-demo"), secret).Build()
	git := &authRecorder{}
	workDir := filepath.Join(t.TempDir(), "w")
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
		WorkDirFn: func(_, _ string) string { return workDir }, NowFn: pastBackoff()}

	reconcileStep(t, r, "step-ssh")
	got := getStep(t, c, "step-ssh")
	assert.Equal(t, "HealthChecking", got.Status.State, got.Status.Message)
	require.Len(t, git.auths, 2)
	for _, a := range git.auths {
		assert.Equal(t, scm.GitAuth{SSHPrivateKey: []byte("KEY"), SSHKnownHosts: []byte("[git.example.com]:2222 ssh-ed25519 AAAA")}, a)
	}
	// Reconciling again runs no git step again.
	reconcileStep(t, r, "step-ssh")
	assert.Len(t, git.auths, 2)
}
