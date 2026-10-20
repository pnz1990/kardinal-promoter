// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsConcurrentUpdate (#2351): a push refused because another writer
// moved, or is moving, the branch is a lost race, as GitHub and git's
// receive-pack (GitLab's Gitaly, Forgejo, Bitbucket) word it through
// go-git's report-status error; a refusal for any other reason is not.
//
// Covers SCM-GIT-REBASE-INFLIGHT-01.
func TestIsConcurrentUpdate(t *testing.T) {
	const ref = "command error on refs/heads/main: "
	for _, msg := range []string{
		// GitHub, ten concurrent pushes to one branch (#2351): every loser.
		ref + "cannot lock ref 'refs/heads/main': is at 0d1c6a4e5f7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d but expected 5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f",
		// git's receive-pack on a files backend: another update holds the lock.
		ref + "cannot lock ref 'refs/heads/main': Unable to create '/srv/git/app.git/refs/heads/main.lock': File exists.",
		ref + "cannot update ref 'refs/heads/main': is at 0d1c6a4e but expected 5e6f7a8b",
		ref + "cannot lock ref 'refs/heads/main': reference already exists",
		ref + "cannot lock ref 'refs/heads/main': reference is missing but expected 5e6f7a8b",
		ref + "cannot lock references",
		ref + "denying non-fast-forward refs/heads/main (you should pull first)",
		ref + "cannot update ref 'refs/heads/main': reference already exists",
		ref + "failed to update ref",
		ref + "failed to lock",
		ref + "incorrect old value provided",
		"rejected refs/heads/main (fetch first)",
		"rejected refs/heads/main (stale info)",
		"non-fast-forward update: refs/heads/main",
	} {
		assert.True(t, isConcurrentUpdate(msg), msg)
	}
	for _, msg := range []string{
		ref + "pre-receive hook declined",
		ref + "protected branch hook declined",
		ref + "push declined due to repository rule violations",
		ref + "deny updating a hidden ref",
		ref + "denying ref deletion for refs/heads/main",
		"authentication required",
		"repository not found",
		"unexpected EOF",
	} {
		assert.False(t, isConcurrentUpdate(msg), msg)
	}
}
