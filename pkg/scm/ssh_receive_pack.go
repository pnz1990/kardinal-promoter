// Copyright 2026 The kardinal-promoter Authors.
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

package scm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	gogitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
)

// go-git's ssh transport closes the ssh session as soon as it has read the
// receive-pack report status, without waiting for the remote command to
// exit. git-receive-pack sends the report before it runs the post-receive
// hook, and a server that stops the command when the client hangs up
// (Gitea's built-in ssh server) then kills the hook: the pushed branch is in
// git but not in the server's branch list, so its API reports the PR's head
// as refs/pull/<n>/head. The git CLI waits for the command's exit status.
// sshTransport keeps go-git's ssh client for fetches and runs receive-pack
// itself, waiting for the exit status before it closes the session.
func init() {
	client.InstallProtocol("ssh", sshTransport{Transport: gogitssh.DefaultClient})
}

// receivePackWait bounds the wait for git-receive-pack (and its hooks) to
// exit after the report status.
const receivePackWait = time.Minute

// sshDialTimeout bounds the TCP connect and ssh handshake of a push.
const sshDialTimeout = 30 * time.Second

type sshTransport struct {
	transport.Transport
}

// NewReceivePackSession runs git-receive-pack over a new ssh connection.
func (t sshTransport) NewReceivePackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.ReceivePackSession, error) {
	a, ok := auth.(gogitssh.AuthMethod)
	if !ok {
		// Without kardinal's key auth, keep go-git's behaviour.
		return t.Transport.NewReceivePackSession(ep, auth)
	}
	cfg, err := a.ClientConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = sshDialTimeout
	}
	port := ep.Port
	if port <= 0 {
		port = gogitssh.DefaultPort
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(ep.Host, strconv.Itoa(port)), cfg)
	if err != nil {
		return nil, err
	}
	sess, err := conn.NewSession()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	s := &receivePackSession{conn: conn, sess: sess}
	if s.stdin, err = sess.StdinPipe(); err == nil {
		var out io.Reader
		if out, err = sess.StdoutPipe(); err == nil {
			s.stdout = out
			sess.Stderr = &s.stderr
			err = sess.Start("git-receive-pack " + shellQuote(ep.Path))
		}
	}
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// shellQuote quotes s for a POSIX shell, as git does (quote.c sq_quote_buf).
func shellQuote(s string) string {
	r := strings.NewReplacer("'", `'\''`, "!", `'\!'`)
	return "'" + r.Replace(s) + "'"
}

type receivePackSession struct {
	conn   *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader
	stderr syncBuffer

	advRefs   *packp.AdvRefs
	closeOnce sync.Once
}

// syncBuffer is a bytes.Buffer safe for the ssh library's stderr copier.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.b.String())
}

func (s *receivePackSession) AdvertisedReferences() (*packp.AdvRefs, error) {
	return s.AdvertisedReferencesContext(context.Background())
}

func (s *receivePackSession) AdvertisedReferencesContext(ctx context.Context) (*packp.AdvRefs, error) {
	if s.advRefs != nil {
		return s.advRefs, nil
	}
	stop := s.closeOnDone(ctx)
	defer stop()
	ar := packp.NewAdvRefs()
	if err := ar.Decode(s.stdout); err != nil {
		if errors.Is(err, packp.ErrEmptyInput) {
			if msg := s.stderr.String(); msg != "" {
				if strings.Contains(strings.ToLower(msg), "not found") || strings.Contains(msg, "does not exist") {
					return nil, transport.ErrRepositoryNotFound
				}
				return nil, fmt.Errorf("git-receive-pack: %s", msg)
			}
		}
		return nil, err
	}
	transport.FilterUnsupportedCapabilities(ar.Capabilities)
	s.advRefs = ar
	return ar, nil
}

func (s *receivePackSession) ReceivePack(ctx context.Context, req *packp.ReferenceUpdateRequest) (*packp.ReportStatus, error) {
	if _, err := s.AdvertisedReferencesContext(ctx); err != nil {
		return nil, err
	}
	stop := s.closeOnDone(ctx)
	defer stop()
	if err := req.Encode(s.stdin); err != nil {
		return nil, s.withStderr(err)
	}
	if err := s.stdin.Close(); err != nil {
		return nil, s.withStderr(err)
	}
	var report *packp.ReportStatus
	if req.Capabilities.Supports(capability.ReportStatus) {
		r := s.stdout
		var d *sideband.Demuxer
		switch {
		case req.Capabilities.Supports(capability.Sideband64k):
			d = sideband.NewDemuxer(sideband.Sideband64k, r)
		case req.Capabilities.Supports(capability.Sideband):
			d = sideband.NewDemuxer(sideband.Sideband, r)
		}
		if d != nil {
			d.Progress = req.Progress
			r = d
		}
		report = packp.NewReportStatus()
		if err := report.Decode(r); err != nil {
			return nil, s.withStderr(err)
		}
	}
	// Read the rest (hook output) and wait for git-receive-pack to exit:
	// closing the session now would stop its post-receive hook.
	_, _ = io.Copy(io.Discard, s.stdout)
	done := make(chan error, 1)
	go func() { done <- s.sess.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(receivePackWait):
		waitErr = errors.New("git-receive-pack did not exit")
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	if report != nil {
		if err := report.Error(); err != nil {
			return report, err
		}
		// The refs are updated once the report says so; a hook failing
		// after it does not undo the push.
		return report, nil
	}
	return nil, s.withStderr(waitErr)
}

// withStderr adds what the remote wrote to stderr to err.
func (s *receivePackSession) withStderr(err error) error {
	if err == nil {
		return nil
	}
	if msg := s.stderr.String(); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// closeOnDone closes the session when ctx ends before stop is called.
func (s *receivePackSession) closeOnDone(ctx context.Context) (stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func (s *receivePackSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.sess != nil {
			_ = s.sess.Close()
		}
		err = s.conn.Close()
		if err != nil && errors.Is(err, net.ErrClosed) {
			err = nil
		}
	})
	return err
}
