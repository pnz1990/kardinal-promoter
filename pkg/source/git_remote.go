// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
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

package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/net/proxy"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

const (
	// defaultPathDiscoveryLimit is how many commits a pathGlob poll reads
	// when the Subscription sets no discoveryLimit.
	defaultPathDiscoveryLimit = 20
	// maxPathDiscoveryLimit is the highest discoveryLimit.
	maxPathDiscoveryLimit = 200
	// maxPackBytes bounds the pack a pathGlob poll reads.
	maxPackBytes = 64 << 20
	// sshTimeout bounds the SSH connection setup.
	sshTimeout = 30 * time.Second
)

// scpLikeURL matches the scp-like SSH syntax user@host:path.
var scpLikeURL = regexp.MustCompile(`^[A-Za-z0-9._~-]+@[A-Za-z0-9.-]+:[^/]`)

// isSSHURL reports whether raw is an SSH remote: ssh:// or user@host:path.
func isSSHURL(raw string) bool {
	if strings.Contains(raw, "://") {
		return strings.HasPrefix(raw, "ssh://")
	}
	return scpLikeURL.MatchString(raw)
}

// uploadPackSession opens a git-upload-pack session for w's remote with w's
// credentials: over HTTP(S) through w's (egress-guarded) client, over SSH to
// an address checked by the egress guard, with the host key checked against
// Credentials.SSHKnownHosts.
//
// The session is closed when ctx is done, and an SSH connection also carries
// ctx's deadline (sshDeadlineDialer), so a server that stalls, during the
// handshake or later, cannot hold the reconcile.
func (w *GitWatcher) uploadPackSession(ctx context.Context) (transport.UploadPackSession, func(), error) {
	ep, err := transport.NewEndpoint(w.RepoURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parse repoURL: %w", err)
	}
	var sess transport.UploadPackSession
	if ep.Protocol == "ssh" {
		auth, err := w.sshAuth(ep)
		if err != nil {
			return nil, nil, err
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			deadline = time.Now().Add(defaultSSHSessionTimeout)
		}
		ep.Proxy = transport.ProxyOptions{URL: fmt.Sprintf("%s://deadline/%d", sshDeadlineScheme, deadline.UnixNano())}
		if sess, err = gitssh.DefaultClient.NewUploadPackSession(ep, auth); err != nil {
			return nil, nil, fmt.Errorf("open ssh session: %w", err)
		}
	} else {
		if sess, err = w.httpUploadPackSession(ep); err != nil {
			return nil, nil, err
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
	return sess, func() { stop(); _ = sess.Close() }, nil
}

// httpUploadPackSession opens an HTTP(S) session with w's client.
func (w *GitWatcher) httpUploadPackSession(ep *transport.Endpoint) (transport.UploadPackSession, error) {
	if ep.Protocol != "http" && ep.Protocol != "https" {
		return nil, fmt.Errorf("unsupported repoURL scheme %q (want https, http or ssh)", ep.Protocol)
	}
	client := w.httpClient
	if client == nil {
		client = newHTTPClient()
	}
	var auth transport.AuthMethod
	if user, pass, ok := w.httpBasic(); ok {
		auth = &githttp.BasicAuth{Username: user, Password: pass}
		client = credentialRedirects(client, false)
	}
	sess, err := githttp.NewClient(client).NewUploadPackSession(ep, auth)
	if err != nil {
		return nil, fmt.Errorf("open http session: %w", err)
	}
	return sess, nil
}

// sshDeadlineScheme is the proxy scheme go-git's SSH transport is pointed at
// so that kardinal dials the connection: x/net/proxy dialers are looked up by
// scheme. It is not a proxy; the dialer connects directly.
const sshDeadlineScheme = "kardinal-ssh-deadline"

// defaultSSHSessionTimeout bounds an SSH session when ctx has no deadline.
const defaultSSHSessionTimeout = 2 * time.Minute

func init() {
	proxy.RegisterDialerType(sshDeadlineScheme, func(u *url.URL, _ proxy.Dialer) (proxy.Dialer, error) {
		nanos, err := strconv.ParseInt(strings.TrimPrefix(u.Path, "/"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid ssh deadline %q", u.Path)
		}
		return sshDeadlineDialer{deadline: time.Unix(0, nanos)}, nil
	})
}

// sshDeadlineDialer dials with the egress guard and sets the connection's
// deadline, which bounds the SSH handshake and every read and write after
// it: go-git's SSH transport sets none.
type sshDeadlineDialer struct{ deadline time.Time }

func (d sshDeadlineDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d sshDeadlineDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	nd := &net.Dialer{Timeout: sshTimeout, Control: sshDialControl}
	conn, err := nd.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(d.deadline); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set ssh deadline: %w", err)
	}
	return conn, nil
}

// sshDialControl is egress.Control; tests that run an SSH server on loopback
// replace it.
var sshDialControl = egress.Control

// sshAuth checks that the SSH host is allowed by the egress guard, points ep
// at the address it checked (so a second resolution cannot move it), and
// returns the public key auth with the known_hosts check.
func (w *GitWatcher) sshAuth(ep *transport.Endpoint) (transport.AuthMethod, error) {
	c := w.Credentials
	if len(c.SSHPrivateKey) == 0 {
		return nil, fmt.Errorf("an ssh repoURL needs secretRef to a Secret with keys ssh-privatekey and known_hosts")
	}
	if len(c.SSHKnownHosts) == 0 {
		return nil, fmt.Errorf("the credentials Secret has no known_hosts: the SSH host key is always verified")
	}
	user := ep.User
	if user == "" {
		user = "git"
	}
	keys, err := gitssh.NewPublicKeys(user, c.SSHPrivateKey, "")
	if err != nil {
		// The key is not echoed.
		return nil, fmt.Errorf("the credentials Secret's ssh-privatekey is not a valid unencrypted private key")
	}
	port := ep.Port
	if port <= 0 {
		port = 22
	}
	hostPort := net.JoinHostPort(ep.Host, strconv.Itoa(port))
	check, err := knownHostsCallback(c.SSHKnownHosts)
	if err != nil {
		return nil, err
	}
	ip, err := sshAddrCheck(ep.Host)
	if err != nil {
		return nil, err
	}
	if ip.Is6() {
		ep.Host = "[" + ip.String() + "]"
	} else {
		ep.Host = ip.String()
	}
	keys.HostKeyCallback = func(_ string, remote net.Addr, key ssh.PublicKey) error {
		// Checked against the host name of repoURL, not the address dialled.
		if err := check(hostPort, remote, key); err != nil {
			var keyErr *knownhosts.KeyError
			if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
				return fmt.Errorf("host %s is not in known_hosts", hostPort)
			}
			return fmt.Errorf("host key of %s does not match known_hosts: %w", hostPort, err)
		}
		return nil
	}
	return &sshKeyAuth{PublicKeys: keys}, nil
}

// sshKeyAuth is go-git's public key auth, whose HostKeyCallback checks the
// Secret's known_hosts (never ~/.ssh/known_hosts), with a connection timeout.
type sshKeyAuth struct {
	*gitssh.PublicKeys
}

// ClientConfig returns the SSH client config with the connection timeout.
func (a *sshKeyAuth) ClientConfig() (*ssh.ClientConfig, error) {
	cfg, err := a.PublicKeys.ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.Timeout = sshTimeout
	return cfg, nil
}

// knownHostsCallback parses known_hosts content. x/crypto reads known_hosts
// only from files, so the content goes through a temporary file that is
// removed at once.
func knownHostsCallback(content []byte) (ssh.HostKeyCallback, error) {
	f, err := os.CreateTemp("", "kardinal-known-hosts-*")
	if err != nil {
		return nil, fmt.Errorf("write known_hosts: %w", err)
	}
	defer os.Remove(f.Name()) //nolint:errcheck
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write known_hosts: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write known_hosts: %w", err)
	}
	cb, err := knownhosts.New(f.Name())
	if err != nil {
		// The message names the temporary file; name the Secret key instead.
		return nil, fmt.Errorf("the credentials Secret's known_hosts does not parse: %s",
			strings.ReplaceAll(err.Error(), f.Name(), "known_hosts"))
	}
	return cb, nil
}

// sshAddrCheck is checkedAddr; tests that run an SSH server on loopback
// replace it.
var sshAddrCheck = checkedAddr

// checkedAddr resolves host and applies the egress guard to every address.
// It returns the first one.
func checkedAddr(host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.WithZone(""), egress.CheckAddr(ip.WithZone(""))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, ip := range ips {
		if err := egress.CheckAddr(ip.WithZone("")); err != nil {
			return netip.Addr{}, fmt.Errorf("dial %s: %w (%q resolves to it)", host, err, host)
		}
	}
	return ips[0].WithZone(""), nil
}

// sshHead returns the SHA of refs/heads/<branch> from git-upload-pack's
// reference advertisement over SSH.
func (w *GitWatcher) sshHead(ctx context.Context, branch string) (string, error) {
	sess, done, err := w.uploadPackSession(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	ar, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil {
		return "", fmt.Errorf("read references: %w", err)
	}
	return branchHead(ar, branch)
}

// branchHead returns the SHA of refs/heads/<branch> in an advertisement.
func branchHead(ar *packp.AdvRefs, branch string) (string, error) {
	refs, err := ar.AllReferences()
	if err != nil {
		return "", fmt.Errorf("read references: %w", err)
	}
	ref, ok := refs[plumbing.NewBranchReferenceName(branch)]
	if !ok {
		if len(refs) == 0 {
			return "", fmt.Errorf("the repository advertises no refs (empty repository)")
		}
		return "", fmt.Errorf("branch %q not found (%d refs advertised)", branch, len(refs))
	}
	return ref.Hash().String(), nil
}

// watchPath reports the newest commit at or before head that changed a path
// matching PathGlob, reading back at most DiscoveryLimit commits and stopping
// at LastRevision (read by the previous poll).
//
//   - head is LastRevision: nothing new; lastDigest is kept.
//   - a matching commit is found: it is the Digest, Changed unless it is
//     lastDigest, this is the first poll, or LastRevision is empty (no read
//     position yet: a baseline).
//   - none is found: lastDigest is kept, or on the first poll head is the
//     baseline.
//
// Revision is always head, so the next poll starts there.
func (w *GitWatcher) watchPath(ctx context.Context, branch, head, lastDigest string) (*WatchResult, error) {
	keep := func(digest string) *WatchResult {
		return &WatchResult{Digest: digest, Tag: shortSHA(digest), Revision: head}
	}
	if head == w.LastRevision && lastDigest != "" {
		return keep(lastDigest), nil
	}
	limit := w.DiscoveryLimit
	if limit <= 0 {
		limit = defaultPathDiscoveryLimit
	}
	limit = min(limit, maxPathDiscoveryLimit)

	match, err := w.newestMatching(ctx, branch, head, limit)
	if err != nil {
		return nil, err
	}
	switch {
	case match == "" && lastDigest == "":
		return keep(head), nil
	case match == "":
		return keep(lastDigest), nil
	}
	res := keep(match)
	// Without a read position (a new or edited pathGlob, or a status from
	// before pathGlob) this poll is a baseline: the newest matching commit
	// may be older than the last Bundle.
	res.Changed = lastDigest != "" && w.LastRevision != "" && match != lastDigest
	return res, nil
}

// newestMatching fetches the last limit commits of head (the depth is one
// more, so the oldest one has its parent to diff against) and walks the
// first-parent chain. It returns the first commit that changed a matching
// path, or "" when none did before LastRevision or the limit.
func (w *GitWatcher) newestMatching(ctx context.Context, branch, head string, limit int) (string, error) {
	sess, done, err := w.uploadPackSession(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	ar, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil {
		return "", fmt.Errorf("read references: %w", err)
	}
	current, err := branchHead(ar, branch)
	if err != nil {
		return "", err
	}
	if current != head {
		// The branch moved since the ref listing; the next poll reads it.
		head = current
	}

	req := packp.NewUploadPackRequestFromCapabilities(ar.Capabilities)
	req.Wants = []plumbing.Hash{plumbing.NewHash(head)}
	req.Capabilities.Delete(capability.ThinPack)
	if ar.Capabilities.Supports(capability.Shallow) {
		_ = req.Capabilities.Set(capability.Shallow)
		req.Depth = packp.DepthCommits(limit + 1)
	}
	if ar.Capabilities.Supports(capability.Filter) {
		_ = req.Capabilities.Set(capability.Filter)
		req.Filter = packp.FilterBlobNone()
	}
	resp, err := sess.UploadPack(ctx, req)
	if err != nil {
		return "", fmt.Errorf("fetch %d commits: %w", limit+1, err)
	}
	defer resp.Close() //nolint:errcheck

	var pack io.Reader = resp
	switch {
	case req.Capabilities.Supports(capability.Sideband64k):
		pack = sideband.NewDemuxer(sideband.Sideband64k, resp)
	case req.Capabilities.Supports(capability.Sideband):
		pack = sideband.NewDemuxer(sideband.Sideband, resp)
	}
	st, err := readPack(pack)
	if err != nil {
		return "", err
	}

	hash := plumbing.NewHash(head)
	for range limit {
		if hash.String() == w.LastRevision {
			return "", nil
		}
		c, err := object.GetCommit(st, hash)
		if err != nil {
			return "", fmt.Errorf("read commit %s: %w", hash, err)
		}
		if c.NumParents() == 0 {
			ok, err := treeMatches(w.PathGlob, c, nil)
			if err != nil || ok {
				return matchResult(c, ok, err)
			}
			return "", nil
		}
		parent, err := c.Parent(0)
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return "", nil // the shallow boundary
		}
		if err != nil {
			return "", fmt.Errorf("read parent of %s: %w", c.Hash, err)
		}
		ok, err := treeMatches(w.PathGlob, c, parent)
		if err != nil || ok {
			return matchResult(c, ok, err)
		}
		hash = parent.Hash
	}
	return "", nil
}

func matchResult(c *object.Commit, ok bool, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if ok {
		return c.Hash.String(), nil
	}
	return "", nil
}

// treeMatches reports whether c changed a path matching glob relative to
// parent (nil: c is a root commit, every file is new).
func treeMatches(glob string, c, parent *object.Commit) (bool, error) {
	tree, err := c.Tree()
	if err != nil {
		return false, fmt.Errorf("read tree of %s: %w", c.Hash, err)
	}
	var parentTree *object.Tree
	if parent != nil {
		if parentTree, err = parent.Tree(); err != nil {
			return false, fmt.Errorf("read tree of %s: %w", parent.Hash, err)
		}
	}
	changes, err := object.DiffTree(parentTree, tree)
	if err != nil {
		return false, fmt.Errorf("diff %s: %w", c.Hash, err)
	}
	for _, ch := range changes {
		for _, name := range []string{ch.From.Name, ch.To.Name} {
			if name == "" {
				continue
			}
			if ok, _ := doublestar.Match(glob, name); ok {
				return true, nil
			}
		}
	}
	return false, nil
}

// limitedPack fails a read past left bytes instead of reading an unbounded
// pack into memory.
type limitedPack struct {
	r    io.Reader
	left int64
}

func (l *limitedPack) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, fmt.Errorf("the pack is larger than %d MiB; lower discoveryLimit", maxPackBytes>>20)
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}
