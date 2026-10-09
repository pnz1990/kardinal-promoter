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
	if layoutBranch(state) {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: layoutBranchNotImplemented},
			parentsteps.Permanent(errors.New(layoutBranchNotImplemented))
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

	err := state.GitClient.Push(ctx, state.WorkDir, "origin", branch, state.Git.Token, force)
	rebases := 0
	if !force && errors.Is(err, scm.ErrNonFastForward) {
		var restart string
		rebases, restart, err = rebaseAndPush(ctx, state, branch)
		if restart != "" {
			return parentsteps.StepResult{Status: parentsteps.StepRestart, Message: restart}, nil
		}
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

// OutputPushedSHA is the git-push output (status.outputs.pushedSHA) naming
// the commit pushed to a PR branch.
const OutputPushedSHA = "pushedSHA"

// outputRebases is the step output (status.outputs.rebases) that counts the
// rebases git-push made before its push landed; absent when none was needed.
const outputRebases = "rebases"

// maxRebaseAttempts bounds how often git-push rebases onto a moved base
// branch before it falls back to a fresh clone (StepRestart).
const maxRebaseAttempts = 6

// rebaseAndPush rebases the promotion commit onto the moved base branch and
// pushes, until the push lands or maxRebaseAttempts are used. It returns how
// many rebases it made, or a StepRestart message when the commit must be
// redone from a fresh clone (the branch changed the same files, the client
// cannot rebase, or the attempts ran out), or the push error.
func rebaseAndPush(ctx context.Context, state *parentsteps.StepState, branch string) (int, string, error) {
	rb, ok := state.GitClient.(scm.Rebaser)
	if !ok {
		return 0, fmt.Sprintf("base branch %s moved while promoting; retrying from a fresh clone", branch), nil
	}
	// No wait between attempts: each one fetches and pushes over the
	// network, and the reconcile must not sleep. When the attempts run out
	// the sequence restarts from a fresh clone, and past the engine's
	// restarts the reconciler requeues the step with backoff and jitter.
	for n := 0; n < maxRebaseAttempts; n++ {
		if err := ctx.Err(); err != nil {
			return n, "", fmt.Errorf("push %s: %w", branch, err)
		}
		if _, err := rb.RebaseOnRemote(ctx, state.WorkDir, "origin", branch, state.Git.Token); err != nil {
			if errors.Is(err, scm.ErrBranchNotMoved) {
				// Not contention: the push is refused for another reason.
				// A plain error, retried a bounded number of times.
				return n, "", fmt.Errorf("push %s was refused as non-fast-forward, but the remote branch did not move "+
					"(a lock file, a read-only repository or a full disk on the git server?): %w", branch, err)
			}
			if errors.Is(err, scm.ErrRebaseConflict) {
				return n, fmt.Sprintf("base branch %s moved and changed the files this promotion writes (%v); "+
					"redoing the change from a fresh clone", branch, err), nil
			}
			return n, "", fmt.Errorf("rebase onto %s: %w", branch, err)
		}
		err := state.GitClient.Push(ctx, state.WorkDir, "origin", branch, state.Git.Token, false)
		if err == nil {
			return n + 1, "", nil
		}
		if !errors.Is(err, scm.ErrNonFastForward) {
			return n + 1, "", err
		}
	}
	return maxRebaseAttempts, fmt.Sprintf("base branch %s kept moving (%d rebases); retrying from a fresh clone",
		branch, maxRebaseAttempts), nil
}
