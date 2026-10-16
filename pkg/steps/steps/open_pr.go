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
	if adopted {
		message += " (already open, from an earlier run of this promotion)"
	}
	outputs := map[string]string{
		"prURL":    prURL,
		"prNumber": fmt.Sprintf("%d", prNum),
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

// openOrAdoptPR returns the open PR of branch into the base branch when
// there is one that is kardinal's, adopted, and otherwise opens it. A step
// re-run after a crash then adopts the PR it opened without relying on the
// SCM to refuse a second one (Bitbucket Cloud updates the existing PR
// instead, overwriting its title and description).
//
// Kardinal's PR is one from the repository's own branch (the lookup never
// returns a fork's PR of a branch with the same name) whose head commit is
// the one git-push just pushed (status.outputs.pushedSHA). An open PR of
// the branch with another head commit is not adopted and fails the step,
// loudly and retried: someone pushed to kardinal's branch, or the SCM has not
// caught up with the push yet. It is never handed to OpenPR either, whose
// refused duplicate would adopt it. Without a recorded commit (a git client
// that cannot read HEAD) nothing is adopted and OpenPR runs, as before.
func openOrAdoptPR(ctx context.Context, state *parentsteps.StepState, repo, title, body, branch string) (string, int, bool, error) {
	pushed := state.Outputs[OutputPushedSHA]
	if finder, ok := state.SCM.(scm.OpenPRFinder); ok && pushed != "" {
		pr, err := finder.FindOpenPR(ctx, repo, branch, state.Git.Branch)
		switch {
		case errors.Is(err, errors.ErrUnsupported):
		case err != nil:
			return "", 0, false, fmt.Errorf("look up an open PR of %s: %w", branch, err)
		case pr != nil && scm.SameCommit(pr.HeadSHA, pushed):
			return pr.URL, pr.Number, true, nil
		case pr != nil:
			return "", 0, false, fmt.Errorf("PR #%d (%s) is open from kardinal's branch %s, but its head commit %q is not %s, the commit kardinal pushed: "+
				"it is not adopted; if someone pushed to the branch, close the PR (a retry pushes the branch again)",
				pr.Number, pr.URL, branch, pr.HeadSHA, pushed)
		}
	}
	prURL, prNum, err := state.SCM.OpenPR(ctx, repo, title, body, branch, state.Git.Branch)
	return prURL, prNum, false, err
}

// prClockSkew is how much older than the PromotionStep a PR adoptMergedPR
// adopts may look: the SCM's clock and the cluster's can differ.
const prClockSkew = time.Minute

// adoptMergedPR handles a run of a PR-opening step whose git-commit found
// nothing to commit (#2065). The base branch already having the change is
// not, by itself, a promotion without a PR: an earlier run may have opened
// this promotion's PR (its head branch is deterministic) and a reviewer
// merged it before the step recorded it, when the controller crashed after
// open-pr. Such a PR is adopted when it is this promotion's: from the
// repository's own branch, not a fork's, with the commit the step pushed
// (pushedSHA) as its head, and opened after the step was created. Its
// number and URL become the step's
// outputs, so wait-for-merge hands it to the PRStatus, which records the
// merge and the SCM-verified merger like any other (the PRMerged
// AuditEvent, #1939). Without such a PR the step has no changes, as
// before. A lookup that fails is retried, never taken as "no PR"; a
// permanent one (401, 403, 404) fails the step.
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
	// Only a PR whose head is the commit this promotion pushed to its
	// branch is its own: git-push records it (pushedSHA) before open-pr
	// runs. Without it there is nothing to compare, and nothing is adopted.
	pushed := state.Outputs[OutputPushedSHA]
	if pushed == "" {
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
	return parentsteps.StepResult{
		Status:  parentsteps.StepSuccess,
		Message: fmt.Sprintf("PR #%d: %s (opened by an earlier run of this promotion, already merged)", pr.Number, pr.URL),
		Outputs: map[string]string{
			"prURL":         pr.URL,
			"prNumber":      fmt.Sprintf("%d", pr.Number),
			outputNoChanges: "false",
		},
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
