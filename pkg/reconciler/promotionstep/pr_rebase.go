// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	builtinsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// outputPRRebuilds counts, in status.outputs, how often the PR branch was
// rebuilt on a moved base branch while the PR waited for its merge.
const outputPRRebuilds = "prBranchRebuilds"

// ReasonPRBranchRebuilt is the Event reason of a PR branch rebuilt on a
// moved base branch.
const ReasonPRBranchRebuilt = "PRBranchRebuilt"

// refreshPRBranch keeps an open promotion PR based on the head of its base
// branch. When the base branch moved past the commit the promotion was
// built on (status.outputs.baseSHA; a fast-forward by other writers, or a
// force-push that rewrote it), it runs the step list up to open-pr again
// from a fresh clone of the new head and force-pushes the result to the PR
// branch: the PR keeps its number and branch, and its one commit sits on the
// new base. Force is safe there, the branch is kardinal's own; the base
// branch is never forced.
//
// It reports whether it wrote the step's status. A failure (the remote is
// unreachable, an update step fails on the new base) is logged and shown in
// the message; the PR as it is stays valid and the next reconcile tries
// again. A step without baseSHA (started before v0.10.0) is left alone.
func (r *Reconciler) refreshPRBranch(ctx context.Context, log zerolog.Logger, base, ps *v1alpha1.PromotionStep,
	pipeline *v1alpha1.Pipeline, env v1alpha1.EnvironmentSpec) (bool, error) {
	built := ps.Status.Outputs[builtinsteps.OutputBaseSHA]
	if built == "" || !opensPR(ps) {
		return false, nil
	}
	rh, ok := r.GitClient.(scm.RemoteHeadReader)
	if !ok {
		return false, nil
	}
	seq := recordedSequence(ps)
	i := slices.Index(seq, openPRStep)
	if i <= 0 {
		return false, nil
	}
	cred := r.resolveGitCredential(ctx, log, pipeline)
	branch := baseBranch(pipeline)
	head, err := rh.RemoteBranchHead(ctx, pipeline.Spec.Git.URL, branch, cred.token)
	if err != nil || head == "" || head == built {
		if err != nil {
			log.Debug().Err(err).Msg("could not read the base branch head; the PR branch is not refreshed")
		}
		return false, nil
	}
	bundle, err := r.loadBundle(ctx, ps)
	if err != nil {
		return false, fmt.Errorf("load bundle: %w", err)
	}
	state := r.stepState(ctx, log, ps, pipeline, env, bundle, seq, r.workDir(ps), cred)
	// The rebuilt commit is computed afresh: forget the previous run's
	// "nothing to commit".
	delete(state.Outputs, "noChanges")
	eng := steps.NewEngine(seq[:i])
	pr := ps.Status.Outputs["prNumber"]
	if _, _, execErr := eng.ExecuteFrom(ctx, state, 0); execErr != nil {
		msg := fmt.Sprintf("PR #%s is open, waiting for merge; rebuilding its branch on %s at %s failed, retrying: %v",
			pr, branch, short(head), execErr)
		log.Warn().Err(execErr).Str("base", head).Msg("rebuild PR branch on the moved base branch")
		if ps.Status.Message == msg {
			return false, nil
		}
		ps.Status.Message = msg
		return true, r.Status().Patch(ctx, ps, client.MergeFrom(base))
	}
	n, _ := strconv.Atoi(ps.Status.Outputs[outputPRRebuilds])
	outputs := cloneMap(ps.Status.Outputs)
	for k, v := range state.Outputs {
		outputs[k] = v
	}
	// The PR and its branch are the ones open-pr recorded.
	for _, k := range []string{"branch", "prURL", "prNumber"} {
		if v, ok := ps.Status.Outputs[k]; ok {
			outputs[k] = v
		}
	}
	outputs[builtinsteps.OutputBaseSHA] = state.Outputs[builtinsteps.OutputBaseSHA]
	outputs[outputPRRebuilds] = strconv.Itoa(n + 1)
	ps.Status.Outputs = outputs
	note := fmt.Sprintf("base branch %s moved from %s to %s; rebuilt the PR branch %s on it",
		branch, short(built), short(head), outputs["branch"])
	if outputs["noChanges"] == "true" {
		note = fmt.Sprintf("base branch %s moved from %s to %s and already has this change; the PR branch is unchanged",
			branch, short(built), short(head))
	}
	ps.Status.Message = withLabelsError(fmt.Sprintf("PR #%s is open, waiting for merge (%s)", pr, note), outputs)
	if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil {
		return true, fmt.Errorf("patch rebuilt PR branch: %w", err)
	}
	log.Info().Str("from", built).Str("to", head).Msg("rebuilt the PR branch on the moved base branch")
	kubeevent.Emit(r.Recorder, ps, corev1.EventTypeNormal, ReasonPRBranchRebuilt, "Promote", note)
	return true, nil
}

// short is a commit SHA's first 7 characters.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
