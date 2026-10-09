// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
)

// Head returns the commit at the head of repo's branch.
func Head(e *framework.Env, repo gitserver.Repo) (string, error) {
	b, ok := e.Git.(gitserver.Brancher)
	if !ok {
		return "", fmt.Errorf("%s git server cannot read branch heads", e.Git.Kind())
	}
	return b.BranchHead(context.Background(), repo, repo.Branch)
}

// ForcePush rewrites repo's branch to commit sha, as `git push --force`
// does: the commits after sha are gone from the branch.
func ForcePush(t *testing.T, e *framework.Env, repo gitserver.Repo, sha string) {
	t.Helper()
	remote, token, err := gitserver.PushRemote(e.Git, repo)
	if err != nil {
		t.Fatalf("push remote: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	auth := &gogithttp.BasicAuth{Username: "x-access-token", Password: token}
	// The push carries the head the clone saw, and the server refuses it
	// ("incorrect old value provided") when kardinal pushed in between: clone
	// again and retry, as a person force-pushing would.
	var pushErr error
	for attempt := 0; attempt < 5; attempt++ {
		r, err := gogit.PlainCloneContext(ctx, t.TempDir(), false, &gogit.CloneOptions{URL: remote, Auth: auth})
		if err != nil {
			t.Fatalf("clone %s: %v", repo.Name, err)
		}
		local := plumbing.NewBranchReferenceName("rewind")
		if err := r.Storer.SetReference(plumbing.NewHashReference(local, plumbing.NewHash(sha))); err != nil {
			t.Fatalf("branch at %s: %v", sha, err)
		}
		spec := config.RefSpec(fmt.Sprintf("+%s:refs/heads/%s", local, repo.Branch))
		pushErr = r.PushContext(ctx, &gogit.PushOptions{Auth: auth, RefSpecs: []config.RefSpec{spec}, Force: true})
		if pushErr == nil || !strings.Contains(pushErr.Error(), "incorrect old value") {
			break
		}
		t.Logf("force-push %s raced a push (%v); cloning again", repo.Branch, pushErr)
	}
	if pushErr != nil {
		t.Fatalf("force-push %s to %s: %v", repo.Branch, sha, pushErr)
	}
	t.Logf("force-pushed %s/%s %s back to %s", repo.Owner, repo.Name, repo.Branch, sha)
}

// ForgejoPREvent is a Forgejo pull_request webhook payload for PR number of
// repo (owner/name).
func ForgejoPREvent(repo string, number int, action string, merged bool) []byte {
	state := "open"
	if action == "closed" {
		state = "closed"
	}
	body, _ := json.Marshal(map[string]interface{}{
		"action": action, "number": number,
		"pull_request": map[string]interface{}{"number": number, "merged": merged, "state": state},
		"repository":   map[string]interface{}{"full_name": repo},
	})
	return body
}

// ForgejoHeaders are the headers Forgejo sends with body, signed with the
// suite's webhook secret.
func ForgejoHeaders(body []byte) map[string]string {
	return map[string]string{
		"X-Forgejo-Event":     "pull_request",
		"X-Forgejo-Signature": framework.HMACHex(WebhookSecret(), body),
	}
}

// DeleteNamespace deletes ns and waits until it is gone.
func DeleteNamespace(t *testing.T, e *framework.Env, ns string, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	if err := e.Kube.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete namespace %s: %v", ns, err)
	}
	framework.Eventually(t, timeout, "namespace "+ns+" to be gone", func(ctx context.Context) (bool, string) {
		n, err := e.Kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		var conds []string
		for _, c := range n.Status.Conditions {
			if c.Status == "True" {
				conds = append(conds, fmt.Sprintf("%s: %s", c.Type, c.Message))
			}
		}
		return false, fmt.Sprintf("%s; %s", n.Status.Phase, strings.Join(conds, "; "))
	})
}

// NoKardinalLeftovers waits until no repo of targets has an open kardinal
// PR or a kardinal/ branch whose PR is not merged.
func NoKardinalLeftovers(t *testing.T, e *framework.Env, targets []invariants.Target, timeout time.Duration) {
	t.Helper()
	lister, canList := e.Git.(gitserver.BranchLister)
	framework.Eventually(t, timeout, "no open kardinal PR or unmerged kardinal branch left", func(ctx context.Context) (bool, string) {
		var left []string
		for _, tg := range targets {
			prs, err := e.Git.PullRequests(ctx, tg.Repo)
			if err != nil {
				return false, err.Error()
			}
			merged := map[string]bool{}
			for _, pr := range prs {
				if pr.State == "open" && strings.HasPrefix(pr.Head, "kardinal/") {
					left = append(left, fmt.Sprintf("%s PR #%d", tg.Repo.Name, pr.Number))
				}
				if pr.State == "merged" {
					merged[pr.Head] = true
				}
			}
			if !canList {
				continue
			}
			names, err := lister.Branches(ctx, tg.Repo)
			if err != nil {
				return false, err.Error()
			}
			for _, b := range names {
				if strings.HasPrefix(b, "kardinal/") && !merged[b] {
					left = append(left, fmt.Sprintf("%s branch %s", tg.Repo.Name, b))
				}
			}
		}
		return len(left) == 0, strings.Join(left, ", ")
	})
}
