//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// TestForgejo_SSHGit runs scmSSHGit on Forgejo's built-in ssh server.
//
// Covers SCM-SSH-FJ-01.
func TestForgejo_SSHGit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	scmSSHGit(t, e)
}

// TestGitea_SSHGit runs scmSSHGit on Gitea's built-in ssh server.
//
// Covers SCM-SSH-GT-01.
func TestGitea_SSHGit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	scmSSHGit(t, e)
}

// TestGitLab_SSHGit runs scmSSHGit on GitLab's OpenSSH server.
//
// Covers SCM-SSH-GL-01.
func TestGitLab_SSHGit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitlab")
	scmSSHGit(t, e)
}

// gitBotUser is the git server user whose token the suites give the
// controller (hack/e2e/components): the ssh key is added to it.
const gitBotUser = "kardinal-bot"

// scmSSHGit promotes prod through a PR with spec.git.url an ssh URL and the
// git Secret holding sshPrivateKey and knownHosts (#1460). With a known_hosts
// that records another host key, git-clone fails with a knownhosts error and
// nothing is pushed; once the Secret has the server's real host key, the
// step's next retry clones over ssh, pushes the PR branch over ssh, opens the
// PR through the API, and after the merge prod runs the new version.
func scmSSHGit(t *testing.T, e *framework.Env) {
	t.Helper()
	keys, ok := e.Git.(gitserver.SSHKeys)
	require.True(t, ok, "%s git server cannot add ssh keys", e.Git.Kind())
	a := newArgoApp(t, e, "prod")
	ctx := context.Background()

	privPEM, authorized := newSSHKey(t)
	remove, err := keys.AddSSHKey(ctx, gitBotUser, "e2e-"+a.ns, authorized)
	require.NoError(t, err)
	t.Cleanup(func() { _ = remove(context.Background()) })

	sshURL, err := gitserver.SSHCloneURL(a.repo)
	require.NoError(t, err)
	hostKey := serverHostKey(t)
	otherHost := otherHostKey(t, hostKey)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-ssh", Namespace: a.ns}, Data: map[string][]byte{
		"sshPrivateKey": privPEM,
		"knownHosts":    []byte(knownHostsLine(t, sshURL, otherHost)),
	}}
	require.NoError(t, e.Client.Create(ctx, secret))

	p := a.pipeline(map[string]string{"prod": "pr-review"})
	p.Spec.Git.URL = sshURL
	p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-ssh"}
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	e.WaitStep(t, a.ns, pipelineName, bundle, "prod", 2*time.Minute, "git-clone to refuse the unknown host key",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "Promoting" && strings.Contains(ps.Status.Message, "knownhosts"),
				fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
		})
	prs, err := e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	for _, pr := range prs {
		assert.NotEqual(t, prHead(a.ns, bundle, "prod"), pr.Head, "no PR while the host key is refused")
	}

	secret.Data["knownHosts"] = []byte(knownHostsLine(t, sshURL, hostKey))
	require.NoError(t, e.Client.Update(ctx, secret))
	_, pr := a.waitOpenPR(t, bundle, "prod")
	assert.Contains(t, e.ReadFile(t, a.repo, pr.Head, fixtures.Path("prod")+"/kustomization.yaml"), "newTag: "+fixtures.V2,
		"the PR branch was pushed over ssh")
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// newSSHKey returns an ed25519 private key (OpenSSH PEM) and its
// authorized_keys line.
func newSSHKey(t *testing.T) ([]byte, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return pem.EncodeToMemory(block), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}

// otherHostKey returns a new public key of the type of hostKey (an
// authorized_keys line), so the client offers the same host key algorithm
// and the knownhosts check, not the algorithm negotiation, refuses it.
func otherHostKey(t *testing.T, hostKey string) string {
	t.Helper()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	require.NoError(t, err)
	var raw interface{}
	switch pub.Type() {
	case ssh.KeyAlgoED25519:
		k, _, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		raw = k
	case ssh.KeyAlgoRSA:
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		raw = &k.PublicKey
	case ssh.KeyAlgoECDSA256:
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		raw = &k.PublicKey
	default:
		t.Fatalf("host key type %s", pub.Type())
	}
	other, err := ssh.NewPublicKey(raw)
	require.NoError(t, err)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(other)))
}

// errHostKeySeen ends the handshake that reads the server's host key.
var errHostKeySeen = errors.New("host key seen")

// serverHostKey returns the authorized_keys form of the host key the git
// server's ssh server presents, read with a handshake from the test runner.
func serverHostKey(t *testing.T) string {
	t.Helper()
	addr := os.Getenv(gitserver.EnvSSHAddr)
	require.NotEmpty(t, addr, "%s is not set", gitserver.EnvSSHAddr)
	var key ssh.PublicKey
	cfg := &ssh.ClientConfig{User: "git", Timeout: 20 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error { key = k; return errHostKeySeen }}
	framework.Eventually(t, time.Minute, "the ssh server at "+addr+" to present its host key", func(context.Context) (bool, string) {
		conn, err := ssh.Dial("tcp", addr, cfg)
		if conn != nil {
			_ = conn.Close()
		}
		return key != nil, fmt.Sprint(err)
	})
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// knownHostsLine records key for the host and port of sshURL.
func knownHostsLine(t *testing.T, sshURL, key string) string {
	t.Helper()
	rest := strings.TrimPrefix(sshURL, "ssh://")
	hostPort, _, _ := strings.Cut(rest[strings.Index(rest, "@")+1:], "/")
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		host, port = hostPort, "22"
	}
	if port == "22" {
		return host + " " + key + "\n"
	}
	return "[" + host + "]:" + port + " " + key + "\n"
}

// TestForgejo_RenderedBranchOverSSH promotes a layout: branch environment
// whose spec.git.url is Forgejo's ssh URL and whose git Secret holds only
// sshPrivateKey and knownHosts (#1515 after #1491). The render Job mounts
// those two keys (no token), creates the rendered branch env/test over ssh
// and pushes the render; the controller confirms its head with git
// ls-remote over ssh; Argo CD syncs the branch and test runs the release.
//
// Covers REND-SSH-01.
func TestForgejo_RenderedBranchOverSSH(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	renderedOverSSH(t, e)
}

// TestGitea_RenderedBranchOverSSH runs renderedOverSSH on Gitea's built-in
// ssh server, which stops git-receive-pack when the client hangs up: the
// render Job's push must wait for its post-receive hook (scm's ssh
// transport), or the rendered branch is in git but not in Gitea's branch
// list.
//
// Covers REND-SSH-GT-01.
func TestGitea_RenderedBranchOverSSH(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	renderedOverSSH(t, e)
}

// renderedOverSSH promotes a layout: branch environment over the git
// server's ssh URL with a Secret holding only sshPrivateKey and knownHosts.
func renderedOverSSH(t *testing.T, e *framework.Env) {
	t.Helper()
	keys, ok := e.Git.(gitserver.SSHKeys)
	require.True(t, ok, "%s git server cannot add ssh keys", e.Git.Kind())
	ctx := context.Background()
	a := renderedApp(t, e, fixtures.KustomizeRepo, "test")

	privPEM, authorized := newSSHKey(t)
	remove, err := keys.AddSSHKey(ctx, gitBotUser, "e2e-"+a.ns, authorized)
	require.NoError(t, err)
	t.Cleanup(func() { _ = remove(context.Background()) })
	sshURL, err := gitserver.SSHCloneURL(a.repo)
	require.NoError(t, err)
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-ssh", Namespace: a.ns},
		Data: map[string][]byte{"sshPrivateKey": privPEM, "knownHosts": []byte(knownHostsLine(t, sshURL, serverHostKey(t)))}}))

	p := a.renderedPipeline(nil)
	p.Spec.Git.URL = sshURL
	p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "git-ssh"}
	a.apply(t, p)
	v2 := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", v2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

	rr := renderRunOf(t, e, a.ns, bundle, "test")
	assertRenderJobSandboxed(t, e, rr)
	job, err := e.Kube.BatchV1().Jobs(rr.Namespace).Get(ctx, rr.Status.JobName, metav1.GetOptions{})
	require.NoError(t, err)
	var items []corev1.KeyToPath
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "git" && v.Secret != nil {
			items = v.Secret.Items
		}
	}
	assert.Equal(t, []corev1.KeyToPath{{Key: "sshPrivateKey", Path: "sshPrivateKey"}, {Key: "knownHosts", Path: "knownHosts"}}, items,
		"the render Job mounts the ssh keys only")
	assert.Equal(t, rr.Status.Result.CommitSHA, headSHA(t, e, "env/test", a.repo), "the render pushed over ssh")
	assert.Contains(t, e.ReadFile(t, a.repo, "env/test", a.ns+"_deployment-"+fixtures.Workload("test")+".yaml"), "image: "+v2)
	a.running(t, "test", v2, "test runs the release rendered over ssh")
}
