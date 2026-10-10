// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package renderjob

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestLockNetwork (QA round 2 on #1515, H1): in the render process every
// HTTP request through net/http's defaults is refused, so a generator or
// transformer that fetches a URL (kustomize's remote loader) reaches
// nothing; go-git keeps only the transport of spec.git.url, and its dialer
// reaches only that host and port, through the egress guard.
func TestLockNetwork(t *testing.T) {
	transport, client, protocols := http.DefaultTransport, http.DefaultClient, maps.Clone(gitclient.Protocols)
	t.Cleanup(func() {
		http.DefaultTransport, http.DefaultClient = transport, client
		for k := range gitclient.Protocols {
			delete(gitclient.Protocols, k)
		}
		maps.Copy(gitclient.Protocols, protocols)
	})
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("AKIA-stolen"))
	}))
	defer metadata.Close()

	require.NoError(t, LockNetwork("https://git.example.com/org/repo.git"))
	for _, get := range []func() (*http.Response, error){
		func() (*http.Response, error) { return http.Get(metadata.URL) },
		func() (*http.Response, error) {
			return http.DefaultClient.Get("http://169.254.169.254/latest/meta-data/")
		},
		func() (*http.Response, error) { return (&http.Client{}).Get(metadata.URL) },
	} {
		resp, err := get()
		if resp != nil {
			_ = resp.Body.Close()
		}
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNetworkRefused), "%v", err)
	}
	assert.Equal(t, []string{"https"}, keys(gitclient.Protocols), "only the git URL's transport is left")

	dial := gitTransport("git.example.com:443").DialContext
	_, err := dial(context.Background(), "tcp", "evil.example.com:443")
	assert.ErrorIs(t, err, ErrNetworkRefused, "another host")
	_, err = dial(context.Background(), "tcp", "git.example.com:22")
	assert.ErrorIs(t, err, ErrNetworkRefused, "another port")
	_, err = gitTransport(metadata.Listener.Addr().String()).DialContext(context.Background(), "tcp", metadata.Listener.Addr().String())
	require.Error(t, err, "the git host itself goes through the egress guard: loopback is refused")
	assert.NotErrorIs(t, err, ErrNetworkRefused)

	_, _, err = gitHostPort("git://git.example.com/org/repo.git")
	assert.Error(t, err, "http(s) or ssh only")
}

// TestGitHostPort_SSH (#1515): an ssh spec.git.url, ssh:// or scp-like, is
// locked to its host and port (22 by default), and LockNetwork leaves only
// go-git's ssh transport, with kardinal's ssh dial reaching only that host
// and port.
//
// Covers REND-SSH-02.
func TestGitHostPort_SSH(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"ssh://git@Git.Example.com/org/repo.git", "git.example.com:22"},
		{"ssh://git@forgejo.forgejo.svc.cluster.local:2222/kardinal/web.git", "forgejo.forgejo.svc.cluster.local:2222"},
		{"git@github.com:org/repo.git", "github.com:22"},
	} {
		scheme, hostPort, err := gitHostPort(tc.url)
		require.NoError(t, err, tc.url)
		assert.Equal(t, "ssh", scheme, tc.url)
		assert.Equal(t, tc.want, hostPort, tc.url)
	}

	transport, client, protocols := http.DefaultTransport, http.DefaultClient, maps.Clone(gitclient.Protocols)
	sshDial := scm.SetSSHDial(nil)
	scm.SetSSHDial(sshDial)
	t.Cleanup(func() {
		http.DefaultTransport, http.DefaultClient = transport, client
		for k := range gitclient.Protocols {
			delete(gitclient.Protocols, k)
		}
		maps.Copy(gitclient.Protocols, protocols)
		scm.SetSSHDial(sshDial)
	})
	require.NoError(t, LockNetwork("git@git.example.com:org/repo.git"))
	assert.Equal(t, []string{"ssh"}, keys(gitclient.Protocols), "only the git URL's transport is left")
	resp, err := http.Get("http://169.254.169.254/latest/meta-data/")
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.ErrorIs(t, err, ErrNetworkRefused, "http stays refused")

	dial := gitDial("git.example.com:22")
	_, err = dial(context.Background(), "tcp", "evil.example.com:22")
	assert.ErrorIs(t, err, ErrNetworkRefused, "another host")
	_, err = dial(context.Background(), "tcp", "127.0.0.1:22")
	assert.ErrorIs(t, err, ErrNetworkRefused, "loopback is another host too")
	_, err = gitDial("127.0.0.1:22")(context.Background(), "tcp", "127.0.0.1:22")
	require.Error(t, err, "the git host itself goes through the egress guard: loopback is refused")
	assert.NotErrorIs(t, err, ErrNetworkRefused)

	// kardinal's git client dials ssh through the locked dial: a clone of
	// another host is refused before any connection.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(priv, "")
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(priv)
	require.NoError(t, err)
	auth := scm.GitAuth{SSHPrivateKey: pem.EncodeToMemory(block),
		SSHKnownHosts: []byte("evil.example.com " + string(ssh.MarshalAuthorizedKey(signer.PublicKey())))}
	err = scm.NewGoGitClient().Clone(context.Background(), "git@evil.example.com:org/repo.git", "main", t.TempDir(), auth)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ErrNetworkRefused.Error(), "another host over ssh")
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
