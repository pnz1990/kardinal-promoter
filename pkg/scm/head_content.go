// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
)

// HeadContentComparer is implemented by git clients that can tell whether
// a remote branch's head holds exactly what the local HEAD commit holds.
// git-push adopts such a head of kardinal's PR branch as its own (#2095):
// pushing over it changes nothing anyone would deploy.
type HeadContentComparer interface {
	// SameContentAsHead reports whether commit sha, the head of branch on
	// dir's remote, has the tree and the parents of HEAD in dir. It is
	// false when the branch's head is no longer sha, and when the head
	// cannot be read within bounds (maxHeadPeek): the caller then refuses
	// the head as someone else's.
	SameContentAsHead(ctx context.Context, dir, remote, branch string, auth GitAuth, sha string) (bool, error)
}

// maxHeadPeek bounds what SameContentAsHead reads of a remote head: the
// bytes of the pack, and the size of any one object and of all of them as
// the pack declares them. A head on kardinal's branch is anyone's who may
// push there, so its blobs are never kept, and a pack past the bound is not
// read further (#2095 QA). A var, so a test can lower it.
var maxHeadPeek int64 = 64 << 20

// drainBound is the least peekCommit reads after it cancels the fetch, to
// reach the transport through go-git's buffers above it (#2364).
const drainBound = 1 << 20

// errHeadTooLarge is a remote head that cannot be read within maxHeadPeek.
var errHeadTooLarge = errors.New("the head's pack is larger than kardinal reads to compare it")

// SameContentAsHead implements HeadContentComparer. It fetches the head
// commit object alone: depth 1, and with the filter tree:0 when the server
// supports partial clone, so that no tree or blob is sent. Equal tree
// hashes are equal content. A server without the filter sends the commit's
// tree and blobs too: the pack is read as a stream, keeping only the
// commit, and refused past maxHeadPeek. dir's repository is unchanged.
func (c *GoGitClient) SameContentAsHead(ctx context.Context, dir, remote, branch string, auth GitAuth, sha string) (bool, error) {
	repo, closeRepo, err := openRepo(dir)
	if err != nil {
		return false, err
	}
	defer func() { _ = closeRepo() }()
	head, err := repo.Head()
	if err != nil {
		return false, fmt.Errorf("resolve HEAD in %s: %w", dir, err)
	}
	local, err := repo.CommitObject(head.Hash())
	if err != nil {
		return false, fmt.Errorf("read HEAD commit in %s: %w", dir, err)
	}
	rem, err := repo.Remote(remote)
	if err != nil {
		return false, fmt.Errorf("get remote %s: %w", remote, err)
	}
	urls := rem.Config().URLs
	if len(urls) == 0 {
		return false, fmt.Errorf("remote %s has no URL", remote)
	}
	if !plumbing.IsHash(sha) {
		return false, nil
	}
	am, err := authMethod(urls[0], auth)
	if err != nil {
		return false, fmt.Errorf("git fetch %s %s: %w", remote, branch, err)
	}
	proxyOpts, release := sshScope(ctx, am)
	defer release()
	theirs, err := peekCommit(ctx, urls[0], am, proxyOpts, plumbing.NewBranchReferenceName(branch), plumbing.NewHash(sha), maxHeadPeek)
	switch {
	case errors.Is(err, errHeadTooLarge):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("git fetch %s %s: %s", remote, branch, gitErrorText(err))
	case theirs == nil:
		return false, nil // the branch's head is no longer sha
	}
	return theirs.TreeHash == local.TreeHash && slices.Equal(theirs.ParentHashes, local.ParentHashes), nil
}

// peekCommit fetches commit want, the head of ref on url, and returns it
// decoded; nil when ref's head is not want. It reads at most limit bytes of
// the pack and keeps no object but the commit (errHeadTooLarge past it).
func peekCommit(ctx context.Context, url string, am transport.AuthMethod, proxy transport.ProxyOptions,
	ref plumbing.ReferenceName, want plumbing.Hash, limit int64) (*object.Commit, error) {
	ep, err := transport.NewEndpoint(url)
	if err != nil {
		return nil, err
	}
	ep.Proxy = proxy
	cl, err := client.NewClient(ep)
	if err != nil {
		return nil, err
	}
	sess, err := cl.NewUploadPackSession(ep, am)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	adv, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil {
		return nil, err
	}
	if h, ok := adv.References[ref.String()]; !ok || h != want {
		return nil, nil
	}
	req := packp.NewUploadPackRequestFromCapabilities(adv.Capabilities)
	req.Capabilities.Delete(capability.ThinPack) // a thin pack's deltas need objects kardinal does not have
	req.Wants = []plumbing.Hash{want}
	if adv.Capabilities.Supports(capability.Shallow) {
		req.Depth = packp.DepthCommits(1)
		_ = req.Capabilities.Set(capability.Shallow)
	}
	if adv.Capabilities.Supports(capability.Filter) {
		req.Filter = packp.FilterTreeDepth(0) // the commit object alone
		_ = req.Capabilities.Set(capability.Filter)
	}
	// The scan stops once it has the commit or passes the bound, while
	// the server may still be sending: cancelling the fetch's context makes
	// the transport's next read fail, and go-git then kills the transport
	// (the local upload-pack process, the HTTP request, the ssh session)
	// instead of waiting for a server blocked on its writes. Read until a
	// read fails: two buffers above the transport (go-git's response and
	// its peek) may still hold packets of the pack, and a read they serve
	// does not reach it. One read, as before, could be served there; then
	// nothing killed a local upload-pack and Close waited for it for ever
	// (#2364). The reads have a bound of their own, drainBound at least:
	// the buffers hold a few KiB, and a bound below them (a small limit)
	// would stop before the transport again.
	fetchCtx, cancel := context.WithCancel(ctx)
	resp, err := sess.UploadPack(fetchCtx, req)
	if err != nil {
		cancel()
		return nil, err
	}
	defer func() {
		cancel()
		buf := make([]byte, 32<<10)
		for read, bound := int64(0), max(limit, drainBound); read <= bound; {
			n, err := resp.Read(buf)
			if err != nil {
				break
			}
			read += int64(n)
		}
		_ = resp.Close()
	}()
	var pack io.Reader = resp
	switch {
	case req.Capabilities.Supports(capability.Sideband64k):
		pack = sideband.NewDemuxer(sideband.Sideband64k, resp)
	case req.Capabilities.Supports(capability.Sideband):
		pack = sideband.NewDemuxer(sideband.Sideband, resp)
	}
	return scanForCommit(&io.LimitedReader{R: pack, N: limit}, want, limit)
}

// scanForCommit reads pack, a packfile stream, until it finds commit want,
// keeping no other object: an object, or all of them, declared larger than
// limit, or a stream that ends at its LimitedReader's bound, is
// errHeadTooLarge.
func scanForCommit(pack *io.LimitedReader, want plumbing.Hash, limit int64) (*object.Commit, error) {
	tooLarge := func(err error) error {
		if pack.N <= 0 {
			return errHeadTooLarge
		}
		return err
	}
	sc := packfile.NewScanner(pack)
	_, n, err := sc.Header()
	if err != nil {
		return nil, tooLarge(fmt.Errorf("read the pack header: %w", err))
	}
	var declared int64
	for range n {
		h, err := sc.NextObjectHeader()
		if err != nil {
			return nil, tooLarge(fmt.Errorf("read an object header: %w", err))
		}
		if declared += h.Length; h.Length > limit || declared > limit {
			return nil, errHeadTooLarge
		}
		if h.Type != plumbing.CommitObject {
			if _, _, err := sc.NextObject(io.Discard); err != nil {
				return nil, tooLarge(fmt.Errorf("skip an object: %w", err))
			}
			continue
		}
		var buf bytes.Buffer
		if _, _, err := sc.NextObject(&buf); err != nil {
			return nil, tooLarge(fmt.Errorf("read a commit: %w", err))
		}
		obj := &plumbing.MemoryObject{}
		obj.SetType(plumbing.CommitObject)
		if _, err := obj.Write(buf.Bytes()); err != nil {
			return nil, err
		}
		if obj.Hash() != want {
			continue
		}
		var c object.Commit
		if err := c.Decode(obj); err != nil {
			return nil, fmt.Errorf("decode commit %s: %w", want, err)
		}
		return &c, nil
	}
	return nil, fmt.Errorf("the pack has no commit %s", want)
}
