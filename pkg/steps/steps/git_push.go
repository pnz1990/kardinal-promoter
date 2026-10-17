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

package steps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&gitPushStep{})
}

// PRBranchPrefix prefixes every branch kardinal pushes a promotion to and
// opens a PR from (PRBranch). The PromotionStep reconciler deletes the head
// branch of a PR it closed only when the branch is under this prefix.
const PRBranchPrefix = "kardinal/"

// PRBranch is the branch git-push pushes a pr-review promotion to and open-pr
// opens the PR from: kardinal/<namespace hash>/<bundle>/<env>, where the hash
// is the first 8 hex characters of the SHA-256 of the namespace. Bundle names
// are unique only within a namespace, and two namespaces' Pipelines can write
// one repository; the hash keeps their PR branches apart. An empty namespace
// gives the name releases before v0.10.0 used, kardinal/<bundle>/<env>.
func PRBranch(namespace, bundle, env string) string {
	if namespace == "" {
		return fmt.Sprintf("%s%s/%s", PRBranchPrefix, bundle, env)
	}
	sum := sha256.Sum256([]byte(namespace))
	return fmt.Sprintf("%s%s/%s/%s", PRBranchPrefix, hex.EncodeToString(sum[:])[:8], bundle, env)
}

// prBranchFor is the branch of the step's PR: the one an earlier run of
// this promotion pushed (status.outputs.branch), so a promotion started
// before the namespace hash keeps its branch and PR, else PRBranch.
func prBranchFor(state *parentsteps.StepState) string {
	if b := state.Outputs["branch"]; strings.HasPrefix(b, PRBranchPrefix) && len(b) > len(PRBranchPrefix) {
		return b
	}
	return PRBranch(state.Namespace, state.BundleName, state.Environment.Name)
}

// gitPushStep pushes the promotion commit. Which branch follows from the step
// sequence being run (StepState.Sequence), not from the live approval, so an
// approval edit made while the step runs cannot strand the commit:
//
//   - A sequence with open-pr (approval: pr-review) pushes to the
//     kardinal-owned branch PRBranch (kardinal/<ns hash>/<bundle>/<env>) with force, so a re-run
//     after a controller restart (which re-clones and re-commits) replaces
//     the earlier push instead of failing non-fast-forward.
//   - Any other sequence (approval: auto) pushes to the base branch without
//     force. If the base branch moved since the clone (another Pipeline or
//     environment pushed first), the step rebases its commit onto the new
//     head and pushes again, up to maxRebaseAttempts times (rebaseAndPush). The rebase replays only the files this
//     promotion changed; when the new commits on the branch changed one of
//     them, or the attempts run out, the step returns StepRestart and the
//     engine re-runs the sequence from a fresh clone, so the update steps
//     work on the other writer's version. Base pushes are never forced, so
//     no other writer's commit is lost.
//   - When git-commit found nothing to commit, nothing is pushed.
type gitPushStep struct{}

func (s *gitPushStep) Name() string { return "git-push" }

func (s *gitPushStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.GitClient == nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "GitClient not configured"}, nil
	}
	if noChanges(state) {
		return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "nothing to push: " + noChangesMessage}, nil
	}

	branch := prBranchFor(state)
	force := true
	if !state.OpensPR() {
		branch = state.Git.Branch
		force = false
	}

	if res, stop, err := beforePush(ctx, state, !force); stop {
		return res, err
	}
	// kardinal's PR branch is force-pushed only over its own commits: read
	// its head first, and push with a lease on it (#2095).
	lease, leased, res, err := prBranchLease(ctx, state, branch, force)
	if err != nil {
		return res, err
	}
	// The commit about to be pushed to the PR branch joins the step's push
	// history before the push: a controller that dies after the push and
	// before the step's next status write still has it, and a PR a reviewer
	// merged with it as its head is the step's own (#2065). A commit
	// recorded whose push then failed is harmless: no PR can carry it.
	if force && state.RecordPushed != nil {
		if hr, ok := state.GitClient.(scm.HeadCommitReader); ok {
			if sha, err := hr.HeadCommit(ctx, state.WorkDir); err == nil && sha != "" {
				history := strings.Join(appendPushed(PushedSHAs(state.Outputs), sha), ",")
				if err := state.RecordPushed(ctx, map[string]string{OutputPushedSHAs: history}); err != nil {
					return parentsteps.StepResult{Status: parentsteps.StepFailed,
						Message: fmt.Sprintf("recording the commit to push to %s failed: %v", branch, err)}, err
				}
			}
		}
	}
	if leased {
		err = state.GitClient.(scm.LeasedPusher).PushLeased(ctx, state.WorkDir, "origin", branch, state.Git.Auth(), lease)
		if errors.Is(err, scm.ErrLeaseBroken) {
			// Someone pushed between the read and the push.
			return foreignPush(ctx, state, branch)
		}
	} else {
		err = state.GitClient.Push(ctx, state.WorkDir, "origin", branch, state.Git.Auth(), force)
	}
	rebases := 0
	if !force && errors.Is(err, scm.ErrNonFastForward) {
		var restart string
		var baseMissing bool
		rebases, restart, baseMissing, err = rebaseAndPush(ctx, state, branch)
		if baseMissing {
			return baseMissingRestart(state, branch, restart)
		}
		if restart != "" {
			return parentsteps.StepResult{Status: parentsteps.StepRestart, Message: restart}, nil
		}
	}
	if errors.Is(err, parentsteps.ErrStalePush) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, parentsteps.Permanent(err)
	}
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("push failed: %v", err)}, err
	}

	outputs := map[string]string{"branch": branch}
	if force {
		// The commit on kardinal's PR branch (status.outputs.pushedSHA): a
		// later rebuild of the branch checks that nobody else pushed since.
		if hr, ok := state.GitClient.(scm.HeadCommitReader); ok {
			if sha, err := hr.HeadCommit(ctx, state.WorkDir); err == nil && sha != "" {
				outputs[OutputPushedSHA] = sha
			}
		}
		// Recorded before open-pr runs: a PR of the branch is kardinal's
		// only when its head is this commit, or, for a PR already merged,
		// one of the commits this step pushed before (open-pr, #2065).
		if outputs[OutputPushedSHA] != "" {
			outputs[OutputPushedSHAs] = strings.Join(appendPushed(PushedSHAs(state.Outputs), outputs[OutputPushedSHA]), ",")
		}
		if outputs[OutputPushedSHA] != "" && state.RecordPushed != nil {
			if err := state.RecordPushed(ctx, map[string]string{OutputPushedSHA: outputs[OutputPushedSHA],
				OutputPushedSHAs: outputs[OutputPushedSHAs], "branch": branch}); err != nil {
				return parentsteps.StepResult{Status: parentsteps.StepFailed,
					Message: fmt.Sprintf("pushed %s; recording the pushed commit failed: %v", branch, err)}, err
			}
		}
	}
	msg := "pushed " + branch
	if rebases > 0 {
		msg = fmt.Sprintf("pushed %s after rebasing onto %d newer commit(s) of other writers", branch, rebases)
		// Kept in status.outputs.rebases, so the contention is visible after
		// the step moved on.
		outputs[outputRebases] = strconv.Itoa(rebases)
	}
	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: msg,
		Outputs: outputs,
	}, nil
}

// prBranchLease reads the head of kardinal's PR branch before a force-push
// (force), when the git client can read it and push with a lease. leased
// says the push is to be made with PushLeased on lease (the head read, ""
// for an absent branch). A head that is not one of the step's own pushes
// (PushedSHAs) is someone else's commit: nothing is pushed over it, and the
// step fails with ForeignPRHeadError against its retry budget (#2095).
func prBranchLease(ctx context.Context, state *parentsteps.StepState, branch string,
	force bool) (lease string, leased bool, res parentsteps.StepResult, err error) {
	if !force {
		return "", false, res, nil
	}
	hr, readable := state.GitClient.(scm.BranchHeadReader)
	_, leases := state.GitClient.(scm.LeasedPusher)
	if !readable || !leases {
		return "", false, res, nil
	}
	head, err := hr.RemoteBranchHead(ctx, state.Git.URL, branch, state.Git.Auth())
	if err != nil {
		return "", false, parentsteps.StepResult{Status: parentsteps.StepFailed,
			Message: fmt.Sprintf("read the head of kardinal's branch %s before the push: %v", branch, err)}, fmt.Errorf("read the head of %s: %w", branch, err)
	}
	if head != "" && !OwnPush(state.Outputs, head) {
		adopted, res, err := adoptSameContent(ctx, state, branch, head)
		if err != nil || !adopted {
			if err == nil {
				res, err = foreignHead(state, branch, head)
			}
			return "", false, res, err
		}
	}
	return head, true, res, nil
}

// adoptSameContent adopts head, a commit of kardinal's PR branch the step
// did not push, when it has the tree and the parents of the commit about
// to be pushed (scm.HeadContentComparer): the branch already holds exactly
// what kardinal would push, as when a deleted PromotionStep's own push is
// found by the step kro created again (STEP-DELETE-PR-04), so pushing over
// it loses nothing. head joins the step's push history (RecordPushed)
// before anything else, and the push is then leased on it. Without a
// comparer or a recorder nothing is adopted (#2095).
func adoptSameContent(ctx context.Context, state *parentsteps.StepState, branch, head string) (bool, parentsteps.StepResult, error) {
	cmp, ok := state.GitClient.(scm.HeadContentComparer)
	if !ok || state.RecordPushed == nil {
		return false, parentsteps.StepResult{}, nil
	}
	same, err := cmp.SameContentAsHead(ctx, state.WorkDir, "origin", branch, state.Git.Auth(), head)
	if err != nil {
		return false, parentsteps.StepResult{Status: parentsteps.StepFailed,
			Message: fmt.Sprintf("read the head %s of kardinal's branch %s: %v", head, branch, err)}, fmt.Errorf("read the head of %s: %w", branch, err)
	}
	if !same {
		return false, parentsteps.StepResult{}, nil
	}
	history := strings.Join(appendPushed(PushedSHAs(state.Outputs), head), ",")
	if err := state.RecordPushed(ctx, map[string]string{OutputPushedSHAs: history}); err != nil {
		return false, parentsteps.StepResult{Status: parentsteps.StepFailed,
			Message: fmt.Sprintf("recording the head %s of %s, which holds this promotion's change, failed: %v", head, branch, err)}, err
	}
	if state.Outputs == nil {
		state.Outputs = map[string]string{}
	}
	state.Outputs[OutputPushedSHAs] = history
	return true, parentsteps.StepResult{}, nil
}

// OwnPush reports whether commit sha is one the step pushed (PushedSHAs):
// kardinal's PR branch at sha carries no one else's commit. git-push and the
// PR branch's rebuild (refreshPRBranch) both check it (#2095).
func OwnPush(outputs map[string]string, sha string) bool {
	return slices.ContainsFunc(PushedSHAs(outputs), func(p string) bool { return scm.SameCommit(p, sha) })
}

// foreignPush is the result of a leased push the remote refused: the
// branch's head moved since it was read. The head is read again for the
// error.
func foreignPush(ctx context.Context, state *parentsteps.StepState, branch string) (parentsteps.StepResult, error) {
	head, err := state.GitClient.(scm.BranchHeadReader).RemoteBranchHead(ctx, state.Git.URL, branch, state.Git.Auth())
	if err != nil || head == "" {
		head = "(moved during the push)"
	}
	return foreignHead(state, branch, head)
}

// foreignHead is the failed step of a push that found someone else's commit
// at the head of kardinal's PR branch.
func foreignHead(state *parentsteps.StepState, branch, head string) (parentsteps.StepResult, error) {
	pushed := PushedSHAs(state.Outputs)
	last := ""
	if len(pushed) > 0 {
		last = pushed[len(pushed)-1]
	}
	ferr := &parentsteps.ForeignPRHeadError{Branch: branch, Head: head, Pushed: last, Push: true}
	return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: ferr.Error()}, ferr
}

// beforePush runs state.BeforePush. stop is true when the push must not be
// made: a stale promotion fails the step permanently, any other error is
// returned for a retry.
func beforePush(ctx context.Context, state *parentsteps.StepState, direct bool) (parentsteps.StepResult, bool, error) {
	if state.BeforePush == nil {
		return parentsteps.StepResult{}, false, nil
	}
	err := state.BeforePush(ctx, direct)
	switch {
	case err == nil:
		return parentsteps.StepResult{}, false, nil
	case errors.Is(err, parentsteps.ErrStalePush):
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, true, parentsteps.Permanent(err)
	default:
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("check before push: %v", err)},
			true, fmt.Errorf("check before push: %w", err)
	}
}

// OutputPushedSHAs (status.outputs.pushedSHAs) lists, oldest first, the
// last maxPushedSHAs commits git-push pushed to the step's PR branch. A
// restarted step pushes a new commit (its commit time differs), so a PR an
// earlier run opened and a reviewer merged carries an earlier one: open-pr
// adopts a merged PR whose head is any of them (#2065).
const OutputPushedSHAs = "pushedSHAs"

// maxPushedSHAs bounds OutputPushedSHAs.
const maxPushedSHAs = 8

// PushedSHAs is the commits the step pushed to its PR branch: the
// pushedSHAs history, and pushedSHA (a step from before the history, or
// its latest push).
func PushedSHAs(outputs map[string]string) []string {
	var out []string
	for _, sha := range strings.Split(outputs[OutputPushedSHAs], ",") {
		if sha != "" {
			out = append(out, sha)
		}
	}
	if sha := outputs[OutputPushedSHA]; sha != "" && !slices.Contains(out, sha) {
		out = append(out, sha)
	}
	return out
}

// appendPushed adds sha to list, once, keeping the last maxPushedSHAs.
func appendPushed(list []string, sha string) []string {
	list = slices.DeleteFunc(list, func(s string) bool { return s == sha })
	list = append(list, sha)
	if len(list) > maxPushedSHAs {
		list = list[len(list)-maxPushedSHAs:]
	}
	return list
}

// OutputPushedSHA is the git-push output (status.outputs.pushedSHA) naming
// the commit pushed to a PR branch.
const OutputPushedSHA = "pushedSHA"

// outputRebases is the step output (status.outputs.rebases) that counts the
// rebases git-push made before its push landed; absent when none was needed.
const outputRebases = "rebases"

// OutputBaseMissingRestarts is the git-push output
// (status.outputs.baseMissingRestarts) counting the fresh clones git-push
// asked for because the clone lacked the commit to rebase from (#1606). It
// is in status, so the bound holds across reconciles and controller restarts.
const OutputBaseMissingRestarts = "baseMissingRestarts"

// maxBaseMissingRestarts bounds those fresh clones. Each one fetches the
// branch again, so a base that is still missing after this many is not a
// race with other writers: the step fails instead of retrying for ever as
// contention.
const maxBaseMissingRestarts = 5

// baseMissingRestart asks for a fresh clone after rebaseAndPush found the
// rebase base missing (reason), counted in OutputBaseMissingRestarts, or
// fails the step once maxBaseMissingRestarts were used.
func baseMissingRestart(state *parentsteps.StepState, branch, reason string) (parentsteps.StepResult, error) {
	n, _ := strconv.Atoi(state.Outputs[OutputBaseMissingRestarts])
	n++
	outputs := map[string]string{OutputBaseMissingRestarts: strconv.Itoa(n)}
	if n > maxBaseMissingRestarts {
		msg := fmt.Sprintf("push to %s failed: %d fresh clones lacked the commit to rebase from, even after fetching "+
			"the branch's recent history; the branch is probably force-pushed or rewritten while promoting (last: %s)",
			branch, maxBaseMissingRestarts, reason)
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: msg, Outputs: outputs},
			parentsteps.Permanent(errors.New(msg))
	}
	return parentsteps.StepResult{Status: parentsteps.StepRestart, Message: reason, Outputs: outputs}, nil
}

// maxRebaseAttempts bounds how often git-push rebases onto a moved base
// branch before it falls back to a fresh clone (StepRestart).
const maxRebaseAttempts = 6

// rebaseAndPush rebases the promotion commit onto the moved base branch and
// pushes, until the push lands or maxRebaseAttempts are used. It returns how
// many rebases it made, or a StepRestart message when the commit must be
// redone from a fresh clone (the branch changed the same files, the client
// cannot rebase, the attempts ran out, or, with baseMissing, the clone lacks
// the commit to rebase from), or the push error.
func rebaseAndPush(ctx context.Context, state *parentsteps.StepState, branch string) (n int, restart string, baseMissing bool, err error) {
	rb, ok := state.GitClient.(scm.Rebaser)
	if !ok {
		return 0, fmt.Sprintf("base branch %s moved while promoting; retrying from a fresh clone", branch), false, nil
	}
	// No wait between attempts: each one fetches and pushes over the
	// network, and the reconcile must not sleep. When the attempts run out
	// the sequence restarts from a fresh clone, and past the engine's
	// restarts the reconciler requeues the step with backoff and jitter.
	for ; n < maxRebaseAttempts; n++ {
		if err := ctx.Err(); err != nil {
			return n, "", false, fmt.Errorf("push %s: %w", branch, err)
		}
		if _, err := rb.RebaseOnRemote(ctx, state.WorkDir, "origin", branch, state.Git.Auth()); err != nil {
			if errors.Is(err, scm.ErrBranchNotMoved) {
				// Not contention: the push is refused for another reason.
				// A plain error, retried a bounded number of times.
				return n, "", false, fmt.Errorf("push %s was refused as non-fast-forward, but the remote branch did not move "+
					"(a lock file, a read-only repository or a full disk on the git server?): %w", branch, err)
			}
			if errors.Is(err, scm.ErrRebaseConflict) {
				return n, fmt.Sprintf("base branch %s moved and changed the files this promotion writes (%v); "+
					"redoing the change from a fresh clone", branch, err), false, nil
			}
			if errors.Is(err, scm.ErrRebaseBaseMissing) {
				// Retrying in this work directory fails the same way (#1606).
				return n, fmt.Sprintf("base branch %s moved, and the clone lacks the commit to rebase from (%v); "+
					"redoing the change from a fresh clone", branch, err), true, nil
			}
			return n, "", false, fmt.Errorf("rebase onto %s: %w", branch, err)
		}
		// The rebase moved this commit onto commits another writer pushed,
		// a newer Bundle's among them perhaps: check again before pushing.
		if state.BeforePush != nil {
			if err := state.BeforePush(ctx, true); err != nil {
				return n + 1, "", false, err
			}
		}
		err := state.GitClient.Push(ctx, state.WorkDir, "origin", branch, state.Git.Auth(), false)
		if err == nil {
			return n + 1, "", false, nil
		}
		if !errors.Is(err, scm.ErrNonFastForward) {
			return n + 1, "", false, err
		}
	}
	return maxRebaseAttempts, fmt.Sprintf("base branch %s kept moving (%d rebases); retrying from a fresh clone",
		branch, maxRebaseAttempts), false, nil
}
