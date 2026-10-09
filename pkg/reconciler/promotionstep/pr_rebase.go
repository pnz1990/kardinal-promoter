// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
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

// historyDepth is how many commits of the base branch a PR branch refresh
// reads first to find the paths changed since the PR was built, and
// deepHistoryDepth how many it reads when the PR's base is not among them (a
// busy branch moves by more than historyDepth between two checks). Only when
// it is not among those either (a force-push) is the PR rebuilt without
// knowing which paths changed.
const (
	historyDepth     = 20
	deepHistoryDepth = 500
)

// historyTimeout bounds the history reads of one check. A read that takes
// longer (a slow or huge remote) counts as history not found: the PR branch
// is rebuilt, which is always safe, instead of holding the reconcile.
var historyTimeout = 30 * time.Second

// hintHistoryUnknown is added to the message of a rebuild done without
// knowing which paths the base branch changed.
const hintHistoryUnknown = "the base branch history since the PR was built could not be read (force-pushed, " +
	"or the read timed out), so the PR branch was rebuilt to be safe"

// changedSince returns the paths the base branch changed between since and
// head, reading historyDepth commits and then, if since is not among them
// and the branch had more, deepHistoryDepth. found is false when since is
// not in the deeper history either, or when the reads take longer than
// historyTimeout.
func (r *Reconciler) changedSince(ctx context.Context, rh scm.RemoteHeadReader, url, branch, head, since, token string) ([]string, bool, error) {
	hctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	for _, depth := range []int{historyDepth, deepHistoryDepth} {
		history, err := r.remotes.branchHistory(hctx, rh, url, branch, head, token, depth)
		if err != nil {
			if ctx.Err() == nil && errors.Is(hctx.Err(), context.DeadlineExceeded) {
				return nil, false, nil // too slow: history not found
			}
			return nil, false, err
		}
		if changed, found := scm.PathsChangedSince(history, since); found {
			return changed, true, nil
		}
		if len(history) < depth {
			break // the whole branch was read: since is not in it
		}
	}
	return nil, false, nil
}

// revisionContains is health.CheckOptions.RevisionContains for a step whose
// promoted commit is want (#1575). Argo CD and Flux sync the branch head,
// which on a branch many environments push to is often a later commit than
// the step's own. Nil when the git client cannot read remote history or
// there is no promoted commit. The Secret is read only when a check asks.
func (r *Reconciler) revisionContains(ctx context.Context, log zerolog.Logger, pipeline *v1alpha1.Pipeline,
	want string) func(context.Context, string) (bool, error) {
	rh, ok := r.GitClient.(scm.RemoteHeadReader)
	if !ok || want == "" || pipeline == nil {
		return nil
	}
	return func(ctx context.Context, rev string) (bool, error) {
		cred := r.resolveGitCredential(ctx, log, pipeline)
		return r.descends(ctx, rh, pipeline.Spec.Git.URL, baseBranch(pipeline), cred.token, rev, want)
	}
}

// descends reports whether rev contains want: both are in the first-parent
// history of branch, read through the shared remote cache (one ls-remote per
// repository per remoteHeadsTTL, one history per head), and rev is want or
// newer. A revision that is not on the branch (another branch, a
// force-push) does not count.
func (r *Reconciler) descends(ctx context.Context, rh scm.RemoteHeadReader, url, branch, token, rev, want string) (bool, error) {
	hctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	heads, err := r.remotes.remoteHeads(hctx, rh, url, token, r.now())
	if err != nil {
		return false, fmt.Errorf("read the heads of %s: %w", scm.RedactURL(url), err)
	}
	head := heads[branch]
	if head == "" {
		return false, nil
	}
	for _, depth := range []int{historyDepth, deepHistoryDepth} {
		history, err := r.remotes.branchHistory(hctx, rh, url, branch, head, token, depth)
		if err != nil {
			return false, fmt.Errorf("read the history of %s: %w", branch, err)
		}
		iRev, iWant := -1, -1
		for i, c := range history {
			if iRev < 0 && health.SameRevision(c.SHA, rev) {
				iRev = i
			}
			if iWant < 0 && health.SameRevision(c.SHA, want) {
				iWant = i
			}
		}
		if iRev >= 0 && iWant >= 0 {
			return iRev <= iWant, nil // newest first: rev is want or later
		}
		if len(history) < depth {
			return false, nil // the whole branch was read
		}
	}
	return false, nil
}

// outputPushedSHA is git-push's status.outputs.pushedSHA, the commit kardinal
// pushed to the PR branch: the lease a rebuild checks.
const outputPushedSHA = builtinsteps.OutputPushedSHA

// refreshPRBranch keeps an open promotion PR mergeable on the head of its
// base branch. It compares the base head (one ls-remote per repository per
// remoteHeadsTTL, shared by every waiting step) with the commit the PR was
// built on (status.outputs.baseSHA). When the base moved:
//
//   - If the commits since baseSHA changed none of the PR's paths (the
//     environment's path and a Helm valuesFile outside it), the PR still
//     merges cleanly: only baseSHA is recorded. Rebuilding every PR on every
//     move made each rebuild a move for the others' merges (livelock).
//   - If they changed one of its paths, or baseSHA is not in the base
//     branch's last deepHistoryDepth commits (a force-push), it
//     reruns the step list up to open-pr on a fresh clone of the new head and
//     force-pushes the PR branch, which is kardinal's own. The PR keeps its
//     number and branch.
//   - If the PR branch's head is not the commit kardinal pushed
//     (status.outputs.pushedSHA), someone else committed to it: nothing is
//     rebuilt, and the message says so.
//
// It reports whether it wrote the step's status. A failure is logged and
// shown in the message; the PR as it is stays valid and the next check tries
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
	url := pipeline.Spec.Git.URL
	heads, err := r.remotes.remoteHeads(ctx, rh, url, cred.token, r.now())
	if err != nil {
		log.Debug().Err(err).Msg("could not read the remote heads; the PR branch is not refreshed")
		return false, nil
	}
	head := heads[branch]
	if head == "" || head == built {
		return false, nil
	}
	pr := ps.Status.Outputs["prNumber"]
	if pushed, now := ps.Status.Outputs[outputPushedSHA], heads[ps.Status.Outputs["branch"]]; pushed != "" && now != "" && now != pushed {
		msg := withLabelsError(fmt.Sprintf("PR #%s is open, waiting for merge; its branch has commits kardinal did not push "+
			"(head %s, kardinal pushed %s), so it is not rebuilt on %s at %s", pr, short(now), short(pushed), branch, short(head)), ps.Status.Outputs)
		if ps.Status.Message == msg {
			return false, nil
		}
		ps.Status.Message = msg
		return true, r.Status().Patch(ctx, ps, client.MergeFrom(base))
	}
	changed, found, herr := r.changedSince(ctx, rh, url, branch, head, built, cred.token)
	unknown := herr != nil || !found
	switch {
	case herr != nil:
		log.Debug().Err(herr).Msg("could not read the base branch history; rebuilding the PR branch")
	case found && !touchesAny(changed, prPaths(env)):
		// The PR's paths did not change: it merges as it is.
		outputs := cloneMap(ps.Status.Outputs)
		outputs[builtinsteps.OutputBaseSHA] = head
		ps.Status.Outputs = outputs
		return true, r.Status().Patch(ctx, ps, client.MergeFrom(base))
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
	// The PR and its branch are the ones open-pr recorded; pushedSHA is the
	// rebuilt commit (git-push).
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
	} else if unknown {
		note += "; " + hintHistoryUnknown
	}
	ps.Status.Message = withLabelsError(fmt.Sprintf("PR #%s is open, waiting for merge (%s)", pr, note), outputs)
	if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil {
		return true, fmt.Errorf("patch rebuilt PR branch: %w", err)
	}
	log.Info().Str("from", built).Str("to", head).Msg("rebuilt the PR branch on the moved base branch")
	kubeevent.Emit(r.Recorder, ps, corev1.EventTypeNormal, ReasonPRBranchRebuilt, "Promote", note)
	return true, nil
}

// prPaths are the paths a promotion of env writes: its directory and a Helm
// valuesFile outside it.
func prPaths(env v1alpha1.EnvironmentSpec) []string {
	dir := env.Path
	if dir == "" {
		dir = "environments/" + env.Name
	}
	dir = path.Clean(strings.TrimPrefix(dir, "./"))
	out := []string{dir}
	if h := env.Update.Helm; env.Update.Strategy == "helm" && h != nil && h.ValuesFile != "" {
		out = append(out, path.Clean(path.Join(dir, h.ValuesFile)))
	}
	return out
}

// touchesAny reports whether a changed path is one of paths or inside one.
func touchesAny(changed, paths []string) bool {
	for _, c := range changed {
		for _, p := range paths {
			if p == "." || c == p || strings.HasPrefix(c, p+"/") {
				return true
			}
		}
	}
	return false
}

// short is a commit SHA's first 7 characters.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
