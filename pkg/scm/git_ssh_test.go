// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// sshKeyPair returns an ed25519 private key in OpenSSH PEM form and its
// public key.
func sshKeyPair(t *testing.T) ([]byte, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return pem.EncodeToMemory(block), sshPub
}

// TestGitAuth_SSH: an ssh remote (ssh:// or scp-like) authenticates with the
// Secret's private key as the URL's user (or git), and accepts only a host
// key recorded in knownHosts for that host and port; the host key
// algorithms offered are the recorded ones. A key without knownHosts, or no
// key, is an error naming the missing Secret key, and an unreadable key is
// an error that does not print it. HTTP(S) remotes keep token auth.
// Covers SCM-SSH-01.
func TestGitAuth_SSH(t *testing.T) {
	clientKey, _ := sshKeyPair(t)
	_, hostKey := sshKeyPair(t)
	_, otherHostKey := sshKeyPair(t)
	knownHosts := []byte("# kardinal e2e\n" +
		"git.example.com " + string(ssh.MarshalAuthorizedKey(hostKey)) +
		"[forgejo.forgejo.svc.cluster.local]:2222 " + string(ssh.MarshalAuthorizedKey(hostKey)))

	tests := []struct {
		name, url, wantUser, hostPort string
	}{
		{name: "scp-like", url: "git@git.example.com:acme/web.git", wantUser: "git", hostPort: "git.example.com:22"},
		{name: "ssh url with a port", url: "ssh://git@forgejo.forgejo.svc.cluster.local:2222/kardinal/web.git",
			wantUser: "git", hostPort: "forgejo.forgejo.svc.cluster.local:2222"},
		{name: "ssh url without a user", url: "ssh://git.example.com/acme/web.git", wantUser: "git", hostPort: "git.example.com:22"},
		{name: "another user", url: "ssh://deploy@git.example.com/acme/web.git", wantUser: "deploy", hostPort: "git.example.com:22"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			am, err := scm.AuthMethodForTest(tt.url, scm.GitAuth{Token: "ignored", SSHPrivateKey: clientKey, SSHKnownHosts: knownHosts})
			require.NoError(t, err)
			keys, ok := am.(*gogitssh.PublicKeys)
			require.True(t, ok, "%T", am)
			assert.Equal(t, tt.wantUser, keys.User)
			assert.Equal(t, []string{ssh.KeyAlgoED25519}, keys.HostKeyAlgorithms)
			addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:22")
			require.NoError(t, err)
			assert.NoError(t, keys.HostKeyCallback(tt.hostPort, addr, hostKey), "the recorded host key")
			assert.Error(t, keys.HostKeyCallback(tt.hostPort, addr, otherHostKey), "another host key")
			assert.Error(t, keys.HostKeyCallback("evil.example.com:22", addr, hostKey), "an unknown host")
		})
	}

	errs := []struct {
		name string
		auth scm.GitAuth
		want error
		text string
	}{
		{name: "no key", auth: scm.GitAuth{Token: "t"}, want: scm.ErrSSHKeyMissing},
		{name: "no known_hosts", auth: scm.GitAuth{SSHPrivateKey: clientKey}, want: scm.ErrSSHKnownHostsMissing},
		{name: "bad key", auth: scm.GitAuth{SSHPrivateKey: []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-material\n-----END OPENSSH PRIVATE KEY-----\n"), SSHKnownHosts: knownHosts},
			text: "read sshPrivateKey"},
		{name: "bad known_hosts", auth: scm.GitAuth{SSHPrivateKey: clientKey, SSHKnownHosts: []byte("host not-a-key")}, text: "parse knownHosts"},
	}
	for _, tt := range errs {
		t.Run(tt.name, func(t *testing.T) {
			_, err := scm.AuthMethodForTest("git@git.example.com:acme/web.git", tt.auth)
			require.Error(t, err)
			if tt.want != nil {
				assert.ErrorIs(t, err, tt.want)
			}
			assert.Contains(t, err.Error(), tt.text)
			assert.NotContains(t, err.Error(), "secret-material")
		})
	}

	am, err := scm.AuthMethodForTest("https://github.com/acme/web.git", scm.GitAuth{Token: "ghs_x", SSHPrivateKey: clientKey})
	require.NoError(t, err)
	assert.Equal(t, "x-access-token", scm.HTTPAuthUsernameForTest("https://github.com/acme/web.git", "ghs_x"))
	assert.NotNil(t, am)
	am, err = scm.AuthMethodForTest("file:///tmp/repo", scm.GitAuth{})
	require.NoError(t, err)
	assert.Nil(t, am)

	assert.True(t, scm.SameSSHHost("git@git.example.com:a/b.git", "ssh://git@git.example.com:22/c/d.git"))
	assert.False(t, scm.SameSSHHost("git@git.example.com:a/b.git", "ssh://git@git.example.com:2222/c/d.git"))
	assert.False(t, scm.SameSSHHost("git@git.example.com:a/b.git", "https://git.example.com/a/b.git"))
}

// sshGitServer serves the bare repository dir over ssh on 127.0.0.1: an
// exec of git-upload-pack or git-receive-pack runs the git binary on dir,
// for a client that authenticates with authorized.
func sshGitServer(t *testing.T, dir string, authorized ssh.PublicKey, receiveExited *atomic.Int32) (addr string, hostKey ssh.PublicKey) {
	return sshGitServerHook(t, dir, authorized, receiveExited, 300*time.Millisecond)
}

// sshGitServerHook is sshGitServer with the post-receive hook taking hook.
func sshGitServerHook(t *testing.T, dir string, authorized ssh.PublicKey, receiveExited *atomic.Int32, hook time.Duration) (addr string, hostKey ssh.PublicKey) {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(hostPriv)
	require.NoError(t, err)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(k.Marshal(), authorized.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("unknown key")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSHGit(conn, cfg, dir, receiveExited, hook)
		}
	}()
	return ln.Addr().String(), signer.PublicKey()
}

// serveSSHGit serves one connection. After git-receive-pack exits it waits
// a moment, as a post-receive hook would run, and counts in receiveExited
// the exit statuses the client was still there to receive.
func serveSSHGit(conn net.Conn, cfg *ssh.ServerConfig, dir string, receiveExited *atomic.Int32, hook time.Duration) {
	defer func() { _ = conn.Close() }()
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = ch.Close() }()
			for req := range chReqs {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &payload)
				service, _, _ := strings.Cut(payload.Command, " ")
				_ = req.Reply(true, nil)
				cmd := exec.Command("git", strings.TrimPrefix(service, "git-"), dir)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
				status := uint32(0)
				if err := cmd.Run(); err != nil {
					status = 1
				}
				if service == "git-receive-pack" {
					time.Sleep(hook) // the post-receive hook
				}
				_, err := ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				if err == nil && service == "git-receive-pack" {
					receiveExited.Add(1)
				}
				return
			}
		}()
	}
}

// TestGoGitClient_SSHRoundTrip clones and pushes over ssh with the Secret's
// key and known_hosts against an ssh server that runs git: the clone and the
// push work with the recorded host key, and fail with another host key
// recorded or with a client key the server does not know. The push stays
// connected until git-receive-pack exits, so a server that stops the command
// when the client hangs up (Gitea) still runs its post-receive hook.
// Covers SCM-SSH-01.
func TestGoGitClient_SSHRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the ssh server runs the git binary")
	}
	ctx := context.Background()
	remote := seedBareRemote(t, map[string]string{"env/prod/kustomization.yaml": "newTag: v1\n"})
	clientKey, clientPub := sshKeyPair(t)
	var receiveExited atomic.Int32
	addr, hostKey := sshGitServer(t, remote, clientPub, &receiveExited)
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	url := "ssh://git@" + addr + "/kardinal/web.git"
	knownHosts := []byte("[" + host + "]:" + port + " " + string(ssh.MarshalAuthorizedKey(hostKey)))
	auth := scm.GitAuth{SSHPrivateKey: clientKey, SSHKnownHosts: knownHosts}
	c := scm.NewGoGitClient()

	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, c.Clone(ctx, url, "main", work, auth))
	require.NoError(t, os.WriteFile(filepath.Join(work, "env/prod/kustomization.yaml"), []byte("newTag: v2\n"), 0o600))
	require.NoError(t, c.CommitAll(ctx, work, "promote", "kardinal", "k@example.com"))
	require.NoError(t, c.Push(ctx, work, "origin", "kardinal/b/prod", auth, false))
	out, err := exec.Command("git", "-C", remote, "show", "kardinal/b/prod:env/prod/kustomization.yaml").CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, "newTag: v2\n", string(out))
	assert.EqualValues(t, 1, receiveExited.Load(),
		"the push waits for git-receive-pack (and its post-receive hook) to exit before it closes the session")

	_, otherHost := sshKeyPair(t)
	wrongHosts := []byte("[" + host + "]:" + port + " " + string(ssh.MarshalAuthorizedKey(otherHost)))
	err = c.Clone(ctx, url, "main", filepath.Join(t.TempDir(), "w"), scm.GitAuth{SSHPrivateKey: clientKey, SSHKnownHosts: wrongHosts})
	require.Error(t, err, "a host key that is not the recorded one")
	assert.Contains(t, err.Error(), "knownhosts")

	otherClient, _ := sshKeyPair(t)
	err = c.Clone(ctx, url, "main", filepath.Join(t.TempDir(), "w"), scm.GitAuth{SSHPrivateKey: otherClient, SSHKnownHosts: knownHosts})
	require.Error(t, err, "a client key the server does not know")
	assert.Contains(t, err.Error(), "unable to authenticate")
}

// TestGoGitClient_RenderedBranchOverSSH (#1515): layout: branch over an
// ssh spec.git.url. RemoteBranchHead and CloneOrInit authenticate with the
// Secret's ssh key, as Clone and Push do: a missing rendered branch reads
// as "" and is created empty, its first push makes it, and from then on it
// is cloned and its head read. A token alone is refused for an ssh remote.
// Covers SCM-SSH-01.
func TestGoGitClient_RenderedBranchOverSSH(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the ssh server runs the git binary")
	}
	ctx := context.Background()
	remote := seedBareRemote(t, map[string]string{"environments/prod/kustomization.yaml": "newTag: v1\n"})
	clientKey, clientPub := sshKeyPair(t)
	var receiveExited atomic.Int32
	addr, hostKey := sshGitServer(t, remote, clientPub, &receiveExited)
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	url := "ssh://git@" + addr + "/kardinal/web.git"
	auth := scm.GitAuth{SSHPrivateKey: clientKey,
		SSHKnownHosts: []byte("[" + host + "]:" + port + " " + string(ssh.MarshalAuthorizedKey(hostKey)))}
	c := scm.NewGoGitClient()

	head, err := c.RemoteBranchHead(ctx, url, "env/prod", auth)
	require.NoError(t, err)
	assert.Empty(t, head, "the rendered branch does not exist yet")

	work := filepath.Join(t.TempDir(), "w")
	created, err := c.CloneOrInit(ctx, url, "env/prod", work, auth, 1)
	require.NoError(t, err)
	assert.True(t, created, "a missing branch is started empty")
	require.NoError(t, os.WriteFile(filepath.Join(work, "web.yaml"), []byte("kind: Deployment\n"), 0o600))
	require.NoError(t, c.CommitAll(ctx, work, "render", "kardinal", "k@example.com"))
	require.NoError(t, c.Push(ctx, work, "origin", "env/prod", auth, false))

	head, err = c.RemoteBranchHead(ctx, url, "env/prod", auth)
	require.NoError(t, err)
	out, err := exec.Command("git", "-C", remote, "rev-parse", "env/prod").CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, strings.TrimSpace(string(out)), head, "the head the server has")

	again := filepath.Join(t.TempDir(), "w2")
	created, err = c.CloneOrInit(ctx, url, "env/prod", again, auth, 1)
	require.NoError(t, err)
	assert.False(t, created, "the branch exists now and is cloned")
	b, err := os.ReadFile(filepath.Join(again, "web.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "kind: Deployment\n", string(b))

	tokenOnly := scm.GitAuth{Token: "t"}
	_, err = c.RemoteBranchHead(ctx, url, "env/prod", tokenOnly)
	assert.ErrorIs(t, err, scm.ErrSSHKeyMissing)
	_, err = c.CloneOrInit(ctx, url, "env/prod", filepath.Join(t.TempDir(), "w3"), tokenOnly, 1)
	assert.ErrorIs(t, err, scm.ErrSSHKeyMissing)
}

// TestGoGitClient_SSHBounded: a server that accepts the TCP connection and
// never runs the ssh handshake fails the clone and the push within the
// connect limit, and a post-receive hook that hangs does not hold the push
// past the receive-pack wait: the push returns once the refs are reported
// updated. Covers SCM-SSH-01.
func TestGoGitClient_SSHBounded(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the ssh server runs the git binary")
	}
	defer scm.SetSSHTimeoutsForTest(time.Second, 500*time.Millisecond)()
	ctx := context.Background()
	clientKey, clientPub := sshKeyPair(t)

	// A server that never answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	_, hostKey := sshKeyPair(t)
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	silent := scm.GitAuth{SSHPrivateKey: clientKey, SSHKnownHosts: []byte("[" + host + "]:" + port + " " + string(ssh.MarshalAuthorizedKey(hostKey)))}
	start := time.Now()
	err = scm.NewGoGitClient().Clone(ctx, "ssh://git@"+ln.Addr().String()+"/a/b.git", "main", filepath.Join(t.TempDir(), "w"), silent)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "the clone gives up at the connect limit: %v", err)

	// A hook that hangs.
	remote := seedBareRemote(t, map[string]string{"a.txt": "a\n"})
	var exited atomic.Int32
	addr, realHost := sshGitServerHook(t, remote, clientPub, &exited, time.Hour)
	h, p, _ := net.SplitHostPort(addr)
	auth := scm.GitAuth{SSHPrivateKey: clientKey, SSHKnownHosts: []byte("[" + h + "]:" + p + " " + string(ssh.MarshalAuthorizedKey(realHost)))}
	url := "ssh://git@" + addr + "/a/b.git"
	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, scm.NewGoGitClient().Clone(ctx, url, "main", work, auth))
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("b\n"), 0o600))
	require.NoError(t, scm.NewGoGitClient().CommitAll(ctx, work, "c", "k", "k@example.com"))
	start = time.Now()
	require.NoError(t, scm.NewGoGitClient().Push(ctx, work, "origin", "kardinal/b/prod", auth, false))
	assert.Less(t, time.Since(start), 5*time.Second, "the push does not wait for the hanging hook")
	out, err := exec.Command("git", "-C", remote, "show", "kardinal/b/prod:a.txt").CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, "b\n", string(out))
}
