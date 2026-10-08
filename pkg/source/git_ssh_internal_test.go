// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package source

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// sshGitServer serves git-upload-pack for the bare repositories under root
// over SSH, to clients with clientKey.
type sshGitServer struct {
	addr    string
	hostKey ssh.PublicKey
}

func startSSHGitServer(t *testing.T, root string, clientKey ssh.PublicKey) *sshGitServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	require.NoError(t, err)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) == string(clientKey.Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("unknown key")
	}}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var wg sync.WaitGroup
	t.Cleanup(wg.Wait) // runs after the listener is closed (cleanups are LIFO)
	t.Cleanup(func() { _ = ln.Close() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				serveSSHConn(conn, cfg, root)
			}()
		}
	}()
	return &sshGitServer{addr: ln.Addr().String(), hostKey: hostSigner.PublicKey()}
}

func serveSSHConn(conn net.Conn, cfg *ssh.ServerConfig, root string) {
	defer conn.Close() //nolint:errcheck
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
			defer ch.Close() //nolint:errcheck
			for req := range chReqs {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				_ = req.Reply(true, nil)
				n := binary.BigEndian.Uint32(req.Payload[:4])
				command := string(req.Payload[4 : 4+n])
				service, repo, _ := strings.Cut(command, " ")
				repo = strings.Trim(repo, "'")
				cmd := exec.Command("git", strings.TrimPrefix(service, "git-"), filepath.Join(root, repo))
				cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
				status := uint32(0)
				if err := cmd.Run(); err != nil {
					status = 1
				}
				_, _ = ch.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, status))
				return
			}
		}()
	}
}

// TestGitWatcher_SSH covers Git over SSH with a private key: the host key is
// checked against known_hosts (unknown and mismatched hosts are refused, a
// Secret without known_hosts is refused), the branch head is read from
// git-upload-pack, and pathGlob fetches over the same transport.
func TestGitWatcher_SSH(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	orig := sshAddrCheck
	sshAddrCheck = func(host string) (netip.Addr, error) { return netip.ParseAddr(host) }
	t.Cleanup(func() { sshAddrCheck = orig })

	root := t.TempDir()
	work := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(filepath.Join(work, "config"), 0o755))
	gitCmd(t, work, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(work, "config", "app.yaml"), []byte("a: 1"), 0o600))
	gitCmd(t, work, "add", "-A")
	gitCmd(t, work, "commit", "-q", "-m", "one")
	gitCmd(t, root, "clone", "-q", "--bare", work, filepath.Join(root, "repo.git"))
	head := gitCmd(t, work, "rev-parse", "HEAD")

	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(block)
	sshPub, err := ssh.NewPublicKey(clientPub)
	require.NoError(t, err)
	srv := startSSHGitServer(t, root, sshPub)
	host, port, _ := net.SplitHostPort(srv.addr)
	known := []byte(knownhosts.Line([]string{net.JoinHostPort(host, port)}, srv.hostKey) + "\n")
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(otherPriv)
	wrongKnown := []byte(knownhosts.Line([]string{net.JoinHostPort(host, port)}, otherSigner.PublicKey()) + "\n")
	otherHost := []byte(knownhosts.Line([]string{"git.example.com"}, srv.hostKey) + "\n")

	repoURL := fmt.Sprintf("ssh://git@%s/repo.git", srv.addr)
	tests := []struct {
		name    string
		creds   Credentials
		glob    string
		wantErr string
	}{
		{name: "known host", creds: Credentials{SSHPrivateKey: keyPEM, SSHKnownHosts: known}},
		{name: "pathGlob over ssh", creds: Credentials{SSHPrivateKey: keyPEM, SSHKnownHosts: known}, glob: "config/*.yaml"},
		{name: "no key", creds: Credentials{SSHKnownHosts: known}, wantErr: "needs secretRef to a Secret with keys ssh-privatekey and known_hosts"},
		{name: "no known_hosts", creds: Credentials{SSHPrivateKey: keyPEM}, wantErr: "has no known_hosts"},
		{name: "bad key", creds: Credentials{SSHPrivateKey: []byte("junk"), SSHKnownHosts: known}, wantErr: "not a valid unencrypted private key"},
		{name: "unknown host", creds: Credentials{SSHPrivateKey: keyPEM, SSHKnownHosts: otherHost}, wantErr: "is not in known_hosts"},
		{name: "host key mismatch", creds: Credentials{SSHPrivateKey: keyPEM, SSHKnownHosts: wrongKnown}, wantErr: "does not match known_hosts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewGitWatcher(repoURL, "main", tt.glob)
			w.Credentials = tt.creds
			ctx, cancel := context.WithTimeout(context.Background(), sshTimeout)
			defer cancel()
			res, err := w.Watch(ctx, "")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), "PRIVATE KEY")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, head, res.Digest)
		})
	}
}

// TestIsSSHURL covers the SSH remote forms.
func TestIsSSHURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"ssh://git@github.com/org/repo.git": true,
		"git@github.com:org/repo.git":       true,
		"https://github.com/org/repo":       false,
		"http://forgejo:3000/org/repo.git":  false,
		"github.com/org/repo":               false,
		"git@github.com:/abs":               false,
	} {
		assert.Equal(t, want, isSSHURL(raw), raw)
	}
}

var _ io.Reader = (*limitedPack)(nil)

// TestLimitedPack fails a read past the limit.
func TestLimitedPack(t *testing.T) {
	l := &limitedPack{r: strings.NewReader("0123456789"), left: 4}
	b, err := io.ReadAll(l)
	require.Error(t, err)
	assert.Equal(t, "0123", string(b))
	assert.Contains(t, err.Error(), "lower discoveryLimit")
}
