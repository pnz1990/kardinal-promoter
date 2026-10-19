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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

func init() {
	parentsteps.Register(&openPRStep{})
}

// openPRStep opens a pull request via the SCM provider.
// It is idempotent: if a prNumber is already in outputs, it skips the creation.
type openPRStep struct{}

func (s *openPRStep) Name() string { return "open-pr" }

func (s *openPRStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.SCM == nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: "SCM provider not configured"}, nil
	}

	// Idempotency: skip if PR already opened. A PR this promotion recorded
	// is its change even when a later run finds the base branch already
	// has it (the PR merged): wait-for-merge records the merge (#2065).
	if prURL, ok := state.Outputs["prURL"]; ok && prURL != "" {
		out := map[string]string{"prURL": prURL}
		if noChanges(state) {
			out[outputNoChanges] = "false"
		}
		return parentsteps.StepResult{
			Status:  parentsteps.StepSuccess,
			Message: "PR already open: " + prURL,
			Outputs: out,
		}, nil
	}

	branch, ok := state.Outputs["branch"]
	if !ok || branch == "" {
		branch = PRBranch(state.Namespace, state.BundleName, state.Environment.Name)
	}

	if noChanges(state) {
		return adoptMergedPR(ctx, state, branch)
	}

	data := scm.PRBody{
		PipelineName:         state.PipelineName,
		Environment:          state.Environment.Name,
		BundleName:           state.BundleName,
		Bundle:               state.Bundle,
		GateResults:          state.GateResults,
		ChangeRequests:       state.ChangeRequests,
		Verifications:        state.Verifications,
		UpstreamEnvironments: buildPRBodyUpstreamEnvs(state.UpstreamEnvironments),
		Pipeline:             state.Pipeline,
	}
	title := fmt.Sprintf("[kardinal] Promote %s to %s", state.BundleName, state.Environment.Name)
	// A rollback PR says what it restores: the title names the restored
	// version (docs/rollback.md), and the note names the Bundle it replaces,
	// the Bundle it restores (RollbackOf, not the rollback Bundle itself) and
	// who asked for it.
	if state.Bundle.Provenance != nil && state.Bundle.Provenance.RollbackOf != "" {
		data.RollbackOf = state.Bundle.Provenance.RollbackOf
		data.RestoredVersion = scm.BundleVersion(state.Bundle)
		data.RollbackFrom = state.RollbackFrom
		if state.RollbackFromBundle != nil {
			data.RollbackFromVersion = scm.BundleVersion(*state.RollbackFromBundle)
		}
		// The verified creator (kardinal.io/created-by, pinned by
		// admission) when there is one; else the client-written
		// requested-by, marked unverified.
		switch {
		case state.Bundle.Actor != nil:
			data.RolledBackBy = actor.Describe(*state.Bundle.Actor)
		case state.CreatedBy != "":
			data.RolledBackBy = state.CreatedBy
		case state.RequestedBy != "":
			data.RolledBackBy = state.RequestedBy
			data.RolledBackByUnverified = true
		}
		restores := data.RestoredVersion
		if restores == "" {
			restores = data.RollbackOf
		}
		title = fmt.Sprintf("[kardinal] Rollback %s to %s (restores %s)", state.Environment.Name, state.BundleName, restores)
	} else {
		data.CreatedBy = state.CreatedBy
		data.RequestedBy = state.RequestedBy
		if a := state.Bundle.Actor; a != nil {
			data.Actor = actor.Describe(*a)
			data.ActorAsserted = actor.DescribeAsserted(*a)
		}
	}

	prCfg := state.Environment.PR
	// A control the provider cannot apply, or a template that does not
	// render, fails the step before the PR exists: a PR without the
	// reviewers or the auto-merge it was configured with must not wait for
	// a merge as if it had them.
	if err := scm.CheckPRSupport(prCfg, scm.SupportOf(state.SCM)); err != nil {
		msg := fmt.Sprintf("environment %s: %v", state.Environment.Name, err)
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: msg}, parentsteps.Permanent(errors.New(msg))
	}
	rendered, err := scm.RenderPR(prCfg, title, data)
	if err != nil {
		msg := fmt.Sprintf("environment %s: render PR: %v", state.Environment.Name, err)
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: msg}, parentsteps.Permanent(fmt.Errorf("render PR: %w", err))
	}

	repo, err := scm.RepoFromURL(state.Pipeline.Git.URL)
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
	}

	prURL, prNum, adopted, err := openOrAdoptPR(ctx, state, repo, rendered.Title, rendered.Body, branch)
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
	}

	// Apply standard kardinal labels to the PR. Every PR gets kardinal/promotion
	// (a rollback is a forward promotion). A rollback (Provenance.RollbackOf is set)
	// also gets kardinal/rollback so operators can find rollback PRs (#402,
	// docs/rollback.md, docs/pr-evidence.md). The environment's pr.labels follow.
	baseLabels := []string{"kardinal", "kardinal/promotion"}
	if state.Bundle.Provenance != nil && state.Bundle.Provenance.RollbackOf != "" {
		baseLabels = append(baseLabels, "kardinal/rollback")
	}
	for _, l := range rendered.Labels {
		if !slices.Contains(baseLabels, l) {
			baseLabels = append(baseLabels, l)
		}
	}
	message := fmt.Sprintf("PR #%d: %s", prNum, prURL)
	switch adopted {
	case adoptedOwnHead:
		message += " (already open, from an earlier run of this promotion)"
	case adoptedOnTop:
		message += " (already open, from an earlier run of this promotion; its branch has commits on top of kardinal's)"
	}
	outputs := map[string]string{
		"prURL":    prURL,
		"prNumber": fmt.Sprintf("%d", prNum),
	}
	if state.Outputs[parentsteps.OutputPRForeignHead] != "" {
		outputs[parentsteps.OutputPRForeignHead] = "" // resolved
	}
	if labelsErr := state.SCM.AddLabelsToPR(ctx, repo, prNum, baseLabels); labelsErr != nil {
		// Non-fatal: the PR exists; report the missing labels. The output
		// keeps the error once the step moves on to wait for the merge, whose
		// message replaces this one (docs/pr-evidence.md).
		zerolog.Ctx(ctx).Warn().Err(labelsErr).Int("pr", prNum).Strs("labels", baseLabels).Msg("add PR labels failed")
		message += fmt.Sprintf(" (adding labels failed: %v)", labelsErr)
		outputs[parentsteps.OutputPRLabelsError] = labelsErr.Error()
	} else if state.Outputs[parentsteps.OutputPRLabelsError] != "" {
		// An earlier PR's labels failed; this one's did not.
		outputs[parentsteps.OutputPRLabelsError] = ""
	}

	// The other controls, like the labels, do not fail the step once the PR
	// is open: the PR waits for a merge by hand, and the step message and
	// status.outputs.prControlsError say what was not applied.
	if controlsErr := applyPRControls(ctx, state, repo, prNum, prURL, rendered, data, outputs); controlsErr != nil {
		zerolog.Ctx(ctx).Warn().Err(controlsErr).Int("pr", prNum).Msg("apply PR controls failed")
		message += fmt.Sprintf(" (PR controls failed: %v)", controlsErr)
		outputs[parentsteps.OutputPRControlsError] = controlsErr.Error()
	} else if state.Outputs[parentsteps.OutputPRControlsError] != "" {
		outputs[parentsteps.OutputPRControlsError] = ""
	}

	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: message,
		Outputs: outputs,
	}, nil
}

// adoption says how open-pr got its PR.
type adoption int

const (
	opened         adoption = iota // OpenPR created it (or adopted a refused duplicate)
	adoptedOwnHead                 // an open PR whose head is kardinal's commit
	adoptedOnTop                   // an open PR whose head contains kardinal's commit
)

// prHistoryDepth bounds the history of the PR branch read to tell whether
// its head contains kardinal's commit.
const prHistoryDepth = 50

// openOrAdoptPR returns the open PR of branch into the base branch when
// there is one that is kardinal's, adopted, and otherwise opens it. A step
// re-run after a crash then adopts the PR it opened without relying on the
// SCM to refuse a second one (Bitbucket Cloud updates the existing PR
// instead, overwriting its title and description).
//
// Kardinal's PR is one from the repository's own branch (the lookup never
// returns a fork's PR of a branch with the same name) whose head is, or
// contains, the commit git-push just pushed (status.outputs.pushedSHA):
// commits on top of kardinal's are accepted, as wait-for-merge accepts them.
// An open PR of the branch whose head does not contain it (someone else
// committed to the branch, replacing kardinal's commit) is not adopted, the
// branch is not rebuilt over their commit, and no create is sent: it is a
// ForeignPRHeadError, which fails the step against its retry budget. A PR
// opened between the lookup and the create, which the SCM then refuses to
// duplicate (*scm.ExistingPRError), is adopted on the same rule
// (adoptIfOwn, #2079).
// Without a recorded commit (a git client that cannot read HEAD) nothing is
// adopted and OpenPR runs, as before.
func openOrAdoptPR(ctx context.Context, state *parentsteps.StepState, repo, title, body, branch string) (string, int, adoption, error) {
	pushed := state.Outputs[OutputPushedSHA]
	if pushed == "" {
		prURL, prNum, err := state.SCM.OpenPR(ctx, repo, title, body, branch, state.Git.Branch)
		// Without a recorded commit, the PR the SCM refused to duplicate
		// cannot be compared with kardinal's: it is not adopted (QA-4 on
		// #2240). The step fails against its retry budget, naming the PR.
		var ex *scm.ExistingPRError
		if errors.As(err, &ex) {
			return "", 0, opened, fmt.Errorf("PR #%d of %s is open already and kardinal recorded no pushed commit to compare its head %s with, so it does not adopt it: %w",
				ex.PR.Number, branch, ex.PR.HeadSHA, ex)
		}
		return prURL, prNum, opened, err
	}
	if finder, ok := state.SCM.(scm.OpenPRFinder); ok {
		pr, err := finder.FindOpenPR(ctx, repo, branch, state.Git.Branch)
		switch {
		case errors.Is(err, errors.ErrUnsupported):
		case err != nil:
			return "", 0, opened, fmt.Errorf("look up an open PR of %s: %w", branch, err)
		case pr != nil:
			return adoptIfOwn(ctx, state, branch, pr, pushed)
		}
	}
	// No PR of the branch: before opening one, its head must contain
	// kardinal's commit. Someone may have pushed over it, and then closed the
	// PR that carried their commit: a PR opened now would carry kardinal's
	// evidence without kardinal's change, and its merge would count as the
	// environment's approval. A git client that can read neither the
	// branch's head nor its history checks nothing, as before.
	contains, head, err := branchHeadContains(ctx, state, branch, pushed)
	switch {
	case errors.Is(err, errNoHistory):
	case err != nil:
		return "", 0, opened, fmt.Errorf("read the head of kardinal's branch %s before opening its PR: %w", branch, err)
	case !contains:
		return "", 0, opened, &parentsteps.ForeignPRHeadError{Branch: branch, Head: head, Pushed: pushed}
	}
	prURL, prNum, err := state.SCM.OpenPR(ctx, repo, title, body, branch, state.Git.Branch)
	// Someone opened a PR of kardinal's branch between the lookup and the
	// create, and the SCM refused a second: it is adopted only on the same
	// rule as one the lookup found (#2079).
	var ex *scm.ExistingPRError
	if errors.As(err, &ex) {
		return adoptIfOwn(ctx, state, branch, &ex.PR, pushed)
	}
	return prURL, prNum, opened, err
}

// adoptIfOwn adopts pr, the open PR of kardinal's branch, when it is
// kardinal's: its head is the commit git-push pushed (pushed), or has it
// among its parents (commits on top of kardinal's). Otherwise someone else
// committed to the branch, replacing kardinal's commit: a
// ForeignPRHeadError. A git client that cannot read the branch's history
// adopts only the PR whose head is pushed.
func adoptIfOwn(ctx context.Context, state *parentsteps.StepState, branch string, pr *scm.FoundPR, pushed string) (string, int, adoption, error) {
	if scm.SameCommit(pr.HeadSHA, pushed) {
		return pr.URL, pr.Number, adoptedOwnHead, nil
	}
	contains, _, err := branchContains(ctx, state, branch, pr.HeadSHA, pushed)
	switch {
	case errors.Is(err, errNoHistory):
	case err != nil:
		return "", 0, opened, fmt.Errorf("PR #%d's head %s is not kardinal's commit %s, and reading branch %s failed: %w",
			pr.Number, pr.HeadSHA, pushed, branch, err)
	case contains:
		return pr.URL, pr.Number, adoptedOnTop, nil
	}
	return "", 0, opened, &parentsteps.ForeignPRHeadError{PR: pr.Number, URL: pr.URL, Branch: branch, Head: pr.HeadSHA, Pushed: pushed}
}

// errNoHistory is a git client that cannot read a branch's head or history.
var errNoHistory = errors.New("the git client cannot read the branch's history")

// branchHeadContains reports whether branch's head on the remote, which it
// returns, is or contains commit pushed. The head is read first (git
// ls-remote), and the history only when it is not pushed.
func branchHeadContains(ctx context.Context, state *parentsteps.StepState, branch, pushed string) (bool, string, error) {
	var head string
	if hr, ok := state.GitClient.(scm.BranchHeadReader); ok {
		var err error
		head, err = hr.RemoteBranchHead(ctx, state.Git.URL, branch, state.Git.Auth())
		if err != nil {
			return false, "", err
		}
		if head == "" {
			return false, "", fmt.Errorf("branch %s does not exist on the remote", branch)
		}
		if scm.SameCommit(head, pushed) {
			return true, head, nil
		}
	}
	// From the head ls-remote read, or the history's tip without it.
	return branchContains(ctx, state, branch, head, pushed)
}

// branchContains reports whether commit head of branch (the branch's head
// when head is empty, which it returns) is commit pushed or has it among
// its last prHistoryDepth ancestors. A head that is not in what was read
// does not contain it: nothing that cannot be checked counts. A git client
// that cannot read the branch's history is errNoHistory.
func branchContains(ctx context.Context, state *parentsteps.StepState, branch, head, pushed string) (bool, string, error) {
	gr, ok := state.GitClient.(scm.BranchGraphReader)
	if !ok {
		return false, head, errNoHistory
	}
	tip, graph, err := gr.BranchGraph(ctx, state.Git.URL, branch, state.Git.Auth(), prHistoryDepth)
	if err != nil {
		return false, head, err
	}
	if head == "" {
		head = tip
	}
	var start string
	for sha := range graph {
		if scm.SameCommit(sha, head) {
			start = sha
		}
	}
	if start == "" {
		return false, head, nil
	}
	seen := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		sha := queue[0]
		queue = queue[1:]
		if seen[sha] {
			continue
		}
		seen[sha] = true
		if scm.SameCommit(sha, pushed) {
			return true, head, nil
		}
		queue = append(queue, graph[sha].Parents...)
	}
	return false, head, nil
}

// prClockSkew is how much older than the PromotionStep a PR adoptMergedPR
// adopts may look: the SCM's clock and the cluster's can differ.
const prClockSkew = time.Minute

// adoptMergedPR handles a run of a PR-opening step whose git-commit found
// nothing to commit (#2065). The base branch already having the change is
// not, by itself, a promotion without a PR: the step pushed its commit to
// its branch (the head branch is deterministic), a PR of that branch was
// merged before the step recorded it (the controller crashed after
// git-push or open-pr), and the base branch has exactly the files kardinal
// writes. Such a PR is adopted: from the repository's own branch, not a
// fork's, opened after the step was created, whose head is a commit the
// step pushed (pushedSHAs) or whose commits, as the SCM lists them, include
// one (a reviewer's fixup on top, #2092). kardinal may not have opened it
// (a person can open a PR of kardinal's branch), and it may carry commits
// kardinal did not push, so the evidence says what is known: the PR's head,
// who opened it, and every commit kardinal did not push with its author
// (AdoptedPRDetail), in the step's message, the PRMerged AuditEvent and the
// receipt. Its number and URL become the step's outputs, so wait-for-merge
// hands it to the PRStatus, which records the merge and the SCM-verified
// merger like any other (the PRMerged AuditEvent, #1939). Without such a PR
// the step has no changes, as before. A lookup that fails is retried, never
// taken as "no PR"; a permanent one (401, 403, 404 of the PR list) fails
// the step.
func adoptMergedPR(ctx context.Context, state *parentsteps.StepState, branch string) (parentsteps.StepResult, error) {
	noPR := parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: "no PR opened: " + noChangesMessage}
	finder, ok := state.SCM.(scm.MergedPRFinder)
	if !ok {
		return noPR, nil
	}
	repo, err := scm.RepoFromURL(state.Pipeline.Git.URL)
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
	}
	// Only a PR with a commit this promotion pushed to its branch, as its
	// head or among its commits, is adopted: git-push records them
	// (pushedSHA, and the pushedSHAs history, since a restarted run pushes
	// a new commit) before open-pr runs. Without one there is nothing to
	// compare, and nothing is adopted.
	pushed := PushedSHAs(state.Outputs)
	if len(pushed) == 0 {
		return noPR, nil
	}
	var since time.Time
	if !state.PromotionCreatedAt.IsZero() {
		since = state.PromotionCreatedAt.Add(-prClockSkew)
	}
	pr, err := finder.FindMergedPR(ctx, repo, branch, state.Git.Branch, pushed, since)
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		return noPR, nil
	case err != nil && scm.IsPermanentError(err):
		msg := fmt.Sprintf("nothing to commit; looking up a merged PR of %s failed: %s", branch, permanentSCMReason(err))
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: fmt.Sprintf("%s: %v", msg, err)},
			parentsteps.Permanent(fmt.Errorf("%s: %w", msg, err))
	case err != nil:
		return parentsteps.StepResult{
			Status:       parentsteps.StepPending,
			Message:      fmt.Sprintf("nothing to commit; looking up a merged PR of %s failed, retrying in %s: %v", branch, scmRetryInterval, err),
			RequeueAfter: scmRetryInterval,
		}, nil
	case pr == nil:
		return noPR, nil
	}
	zerolog.Ctx(ctx).Info().Int("pr", pr.Number).Str("branch", branch).
		Msg("nothing to commit: this promotion's PR was merged before the step recorded it; adopting it")
	outputs, err := adoptedOutputs(pr)
	if err != nil {
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
	}
	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("adopted PR #%d (%s): %s, already merged", pr.Number, AdoptedPRDetail(outputs), pr.URL),
		Outputs: outputs,
	}, nil
}

// buildPRBodyUpstreamEnvs converts []v1alpha1.EnvironmentStatus to []scm.PRBodyUpstreamEnv,
// pre-computing the elapsed time for each environment at call time rather than at
// template render time. This eliminates SCM-4: time.Since() inside PR template execution.
func buildPRBodyUpstreamEnvs(envs []v1alpha1.EnvironmentStatus) []scm.PRBodyUpstreamEnv {
	now := time.Now().UTC()
	result := make([]scm.PRBodyUpstreamEnv, 0, len(envs))
	for _, env := range envs {
		e := scm.PRBodyUpstreamEnv{
			Name:            env.Name,
			Phase:           env.Phase,
			HealthCheckedAt: env.HealthCheckedAt,
		}
		if env.HealthCheckedAt != nil && !env.HealthCheckedAt.IsZero() {
			e.Elapsed = scm.FormatElapsed(env.HealthCheckedAt.Time, now)
		}
		result = append(result, e)
	}
	return result
}

// applyPRControls requests the reviewers and assigns the assignees of the
// PR it opened, as the environment's pr config says, and renders the merge
// options of pr.merge.auto into outputs: the PromotionStep reconciler
// enables auto-merge while the step waits for the merge, and turns it off
// while the Pipeline is paused or a gate is closed (status.outputs
// prAutoMerge=pending). Each control is tried even when one before it
// failed; the error joins every failure.
func applyPRControls(ctx context.Context, state *parentsteps.StepState, repo string, prNum int, prURL string,
	rendered scm.RenderedPR, data scm.PRBody, outputs map[string]string) error {
	cfg := state.Environment.PR
	if cfg == nil {
		return nil
	}
	ctrl, ok := state.SCM.(scm.PRController)
	if !ok {
		// CheckPRSupport refused every control this provider lacks.
		return nil
	}
	var errs []error
	if len(rendered.Reviewers) > 0 || len(rendered.TeamReviewers) > 0 {
		if err := ctrl.RequestReviewers(ctx, repo, prNum, rendered.Reviewers, rendered.TeamReviewers); err != nil {
			errs = append(errs, fmt.Errorf("reviewers: %w", err))
		}
	}
	if len(rendered.Assignees) > 0 {
		if err := ctrl.AddAssignees(ctx, repo, prNum, rendered.Assignees); err != nil {
			errs = append(errs, fmt.Errorf("assignees: %w", err))
		}
	}
	if cfg.Merge != nil && cfg.Merge.Auto {
		opts, err := scm.RenderMergeOptions(cfg.Merge, data, scm.PRTemplatePR{Number: prNum, URL: prURL, Title: rendered.Title})
		if err == nil {
			var raw []byte
			if raw, err = json.Marshal(opts); err == nil {
				outputs[parentsteps.OutputPRMergeOptions] = string(raw)
				outputs[parentsteps.OutputPRAutoMerge] = parentsteps.AutoMergePending
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("auto-merge: %w", err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	return errors.New(strings.Join(msgs, "; "))
}
