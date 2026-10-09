// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// Outputs of the render step.
const (
	// outputRenderRun is the RenderRun the step waited for.
	outputRenderRun = "renderRun"
)

// renderWaitRequeue is the fallback poll while the RenderRun runs; the
// Graph's mirror of its status onto the step (spec.live.renders) wakes the
// step first.
const renderWaitRequeue = 30 * time.Second

// renderStep waits for the RenderRun of a layout: branch environment and
// takes its result. Rendering never runs in the controller: the RenderRun
// reconciler runs it as a Job in the Pipeline's namespace (git-clone, the
// image update, render-manifests, git-commit, git-push; see
// parentsteps.RenderJobSequence), and the Graph mirrors the RenderRun's
// status onto the PromotionStep, where this step reads it:
//
//   - first run: it asks for the render (OutputRenderRequested) and waits;
//   - Pending or Running: it waits;
//   - Succeeded: its outputs are the render's (the branch pushed, which
//     open-pr opens the PR from; the commit health checks; the DRY commit,
//     renderer and marker digest, which the step's status keeps);
//   - Failed: the step fails for good with the RenderRun's message. A new
//     Bundle renders again.
type renderStep struct{}

func init() {
	parentsteps.Register(&renderStep{})
}

func (s *renderStep) Name() string { return parentsteps.RenderStepName }

func (s *renderStep) Execute(ctx context.Context, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	if state.Outputs[parentsteps.OutputRenderRequested] != "true" {
		return parentsteps.StepResult{
			Status:  parentsteps.StepPending,
			Message: "rendering in a RenderRun Job",
			// The Graph writes the RenderRun from these: where to push follows
			// the step list this step runs, not the live approval.
			Outputs: map[string]string{parentsteps.OutputRenderRequested: "true",
				parentsteps.OutputRenderPullRequest: fmt.Sprint(state.OpensPR())},
		}, nil
	}
	var run *v1alpha1.LiveRenderRun
	if len(state.LiveRenders) > 0 {
		run = &state.LiveRenders[0]
	}
	if run == nil {
		return parentsteps.StepResult{Status: parentsteps.StepPending, RequeueAfter: renderWaitRequeue,
			Message: "waiting for the Graph to create the RenderRun"}, nil
	}
	switch run.Phase {
	case v1alpha1.RenderRunSucceeded:
	case v1alpha1.RenderRunFailed:
		err := parentsteps.Permanent(fmt.Errorf("render failed (RenderRun %s): %s", run.Name, run.Message))
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
	default:
		phase := run.Phase
		if phase == "" {
			phase = v1alpha1.RenderRunPending
		}
		return parentsteps.StepResult{Status: parentsteps.StepPending, RequeueAfter: renderWaitRequeue,
			Message: fmt.Sprintf("rendering in RenderRun %s: %s", run.Name, phase)}, nil
	}
	res := run.Result
	if res == nil {
		err := parentsteps.Permanent(errors.New("RenderRun " + run.Name + " succeeded without a result"))
		return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
	}
	// The Job's report is checked against the remote: the branch it names
	// must point at the commit it names (git ls-remote), so a report that is
	// not true never reaches open-pr or the health check.
	// A result that pushed nothing names the rendered branch's head, and that
	// head's marker must be one kardinal recorded.
	branch := res.Branch
	if res.NoChanges {
		branch = state.Git.Branch
		if known := run.KnownMarkerDigests; len(known) > 0 && !slices.Contains(known, res.MarkerDigest) {
			err := parentsteps.Permanent(fmt.Errorf("RenderRun %s reported %s already holds its render, but its marker "+
				"(sha256 %s) is not one of kardinal's recorded renders", run.Name, branch, shortSHA(res.MarkerDigest)))
			return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
		}
	}
	{
		if rh, ok := state.GitClient.(scm.RemoteHeadReader); ok {
			head, err := rh.RemoteBranchHead(ctx, state.Git.URL, branch, state.Git.Token)
			if err != nil {
				msg := "check the rendered commit: " + scm.RedactText(err.Error())
				return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: msg}, errors.New(msg)
			}
			if head != res.CommitSHA {
				err := parentsteps.Permanent(fmt.Errorf("RenderRun %s reported %s on %s, but the branch is at %q",
					run.Name, shortSHA(res.CommitSHA), branch, shortSHA(head)))
				return parentsteps.StepResult{Status: parentsteps.StepFailed, Message: err.Error()}, err
			}
		}
	}
	outputs := map[string]string{
		outputRenderRun:      run.Name,
		outputDryCommit:      res.DryCommit,
		"renderer":           res.Renderer,
		"renderedObjects":    fmt.Sprint(res.Objects),
		outputMarkerDigest:   res.MarkerDigest,
		outputRenderedBranch: state.Git.Branch,
		outputNoChanges:      fmt.Sprint(res.NoChanges),
	}
	if res.Branch != "" {
		outputs["branch"] = res.Branch
	}
	if res.DriftOverwritten != "" {
		outputs["driftOverwritten"] = res.DriftOverwritten
	}
	// The commit the health check must see deployed: what the Job pushed to
	// the rendered branch. A pr-review step takes the merge commit instead.
	if res.CommitSHA != "" && !res.NoChanges && !state.OpensPR() {
		outputs["commitSHA"] = res.CommitSHA
	}
	msg := fmt.Sprintf("rendered %d objects from %s with %s into %s (RenderRun %s)", res.Objects,
		shortSHA(res.DryCommit), res.Renderer, res.Branch, run.Name)
	if res.NoChanges {
		msg = fmt.Sprintf("%s already holds the render of %s (RenderRun %s)", state.Git.Branch, shortSHA(res.DryCommit), run.Name)
	}
	return parentsteps.StepResult{Status: parentsteps.StepSuccess, Message: msg, Outputs: outputs}, nil
}
