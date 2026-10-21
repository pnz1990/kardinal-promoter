// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	kardinalsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// requestWrite counts a new request of ps to have its environment written by
// its BranchWrite (await-write's OutputWriteRequest) in status.writeRequest,
// which the Graph lists on the BranchWrite. The request is the step's push
// intent (#1603): the BranchWrite may push for it from now on, so a step of
// an older Bundle for the same environment must not push any more.
func requestWrite(ps *v1alpha1.PromotionStep, now time.Time) {
	ps.Status.WriteRequest++
	delete(ps.Status.Outputs, steps.OutputWriteRequest)
	if ps.Status.Outputs == nil {
		ps.Status.Outputs = map[string]string{}
	}
	if ps.Status.Outputs[outputPushIntent] == "" {
		ps.Status.Outputs[outputPushIntent] = now.UTC().Format(time.RFC3339Nano)
	}
}

// maxRoundMembers bounds the environments one round writes: one reconcile's
// clone, updates, commits and push. The rest are written in the next round.
const maxRoundMembers = 50

// maxWriteAttempts bounds how often a round is written again on a branch
// that moved under it, as git-push's rebases are (6); then its members ask
// again.
const maxWriteAttempts = 6

// writeTrailer is the line every commit of a round ends with: the
// BranchWrite and round that made it. A round found in flight after a
// restart is recognised on the branch by it.
const writeTrailer = "Kardinal-Write: "

// writeHistoryDepth is how far back on the branch a round in flight is
// looked for after a restart (as a rebase looks for its base, #1606).
const writeHistoryDepth = 200

// BranchWriteReconciler writes the changes of a Bundle's batched
// environments (spec.git.writes: batched) to their branch: each round takes
// the branch's turn (the BranchQueue it shares with the PromotionStep
// reconciler, the G8 branch-turn exception), freezes its members in the
// BranchWrite's status, clones once, runs each member's update step and
// commits it with the environment's own commit message, and pushes once
// (docs/design/30-batched-branch-writes.md). It writes only the
// BranchWrite's status; the Graph mirrors each environment's result onto its
// step (spec.live.writes), where await-write reads it. Nothing it needs
// crosses a reconcile in memory: the round in flight, and whether it was
// about to push, are in status, and its commits carry a Kardinal-Write
// trailer.
type BranchWriteReconciler struct {
	client.Client

	// Steps is the PromotionStep reconciler: its git client, credentials,
	// work directory root, clock, push guard and branch turns.
	Steps *Reconciler

	// Workers is how many BranchWrites are reconciled at once; 0 is the
	// manager's default.
	Workers int
}

// writeTurnKey is the BranchQueue key of bw.
func writeTurnKey(bw *v1alpha1.BranchWrite) string {
	return writeTurnPrefix + bw.Namespace + "/" + bw.Name
}

// branchWritesResource is the BranchWrite resource, for objectgone.
var branchWritesResource = v1alpha1.GroupVersion.WithResource("branchwrites").GroupResource()

// Reconcile runs one round of bw when a request has no answer.
func (w *BranchWriteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, branchWritesResource, w.reconcile)
}

func (w *BranchWriteReconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("branchwrite", req.String()).Logger()
	var bw v1alpha1.BranchWrite
	if err := w.Get(ctx, req.NamespacedName, &bw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	pending := pendingRequests(&bw)
	if bw.Status.InFlight == nil && len(pending) == 0 {
		return ctrl.Result{}, nil
	}
	r := w.Steps
	key := normalizeRepoURL(bw.Spec.URL) + "#" + bw.Spec.Branch
	release, wait, got := r.turns().tryTurn(key, writeTurnKey(&bw), r.now())
	if !got {
		msg := fmt.Sprintf(waitingForTurnMessage, bw.Spec.Branch)
		if bw.Status.Message != msg {
			base := bw.DeepCopy()
			bw.Status.Message = msg
			if err := w.Status().Patch(ctx, &bw, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("patch branchwrite turn wait: %w", err)
			}
		}
		return ctrl.Result{RequeueAfter: wait, Priority: &lowPriority}, nil
	}
	defer func() { release(r.now()) }()

	if bw.Status.InFlight != nil {
		// A round in flight when the controller stopped: what it pushed is
		// on the branch, under its trailer.
		if err := w.resumeRound(ctx, log, &bw); err != nil {
			return ctrl.Result{}, err
		}
		pending = pendingRequests(&bw)
		if len(pending) == 0 {
			return ctrl.Result{}, nil
		}
	}

	base := bw.DeepCopy()
	bw.Status.Round++
	members := pending[:min(len(pending), maxRoundMembers)]
	bw.Status.InFlight = &v1alpha1.BranchWriteRound{Round: bw.Status.Round, Members: members}
	bw.Status.Message = fmt.Sprintf("round %d: writing %d environment(s)", bw.Status.Round, len(members))
	if err := w.Status().Patch(ctx, &bw, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("record round %d: %w", bw.Status.Round, err))
	}
	results, msg := w.writeRound(ctx, log, &bw, members)
	return ctrl.Result{}, w.recordRound(ctx, &bw, results, msg)
}

// pendingRequests are bw's requests that are environments of bw with no
// answer yet, in spec.environments order.
func pendingRequests(bw *v1alpha1.BranchWrite) []v1alpha1.BranchWriteMember {
	byStep := map[string]v1alpha1.BranchWriteRequest{}
	for _, rq := range bw.Spec.Requested {
		byStep[rq.Step] = rq
	}
	var out []v1alpha1.BranchWriteMember
	for _, e := range bw.Spec.Environments {
		rq, ok := byStep[e.Step]
		if !ok || rq.Request < 1 {
			continue
		}
		if res, done := bw.Status.Environments[e.Name]; done && res.Step == rq.Step && res.UID == rq.UID && res.Request >= rq.Request {
			continue
		}
		out = append(out, v1alpha1.BranchWriteMember{Environment: e.Name, Step: rq.Step, UID: rq.UID, Request: rq.Request})
	}
	return out
}

// recordRound records the round's results and ends it.
func (w *BranchWriteReconciler) recordRound(ctx context.Context, bw *v1alpha1.BranchWrite,
	results map[string]v1alpha1.BranchWriteResult, msg string) error {
	base := bw.DeepCopy()
	if bw.Status.Environments == nil {
		bw.Status.Environments = map[string]v1alpha1.BranchWriteResult{}
	}
	for env, res := range results {
		bw.Status.Environments[env] = res
	}
	bw.Status.InFlight = nil
	bw.Status.Message = msg
	if err := w.Status().Patch(ctx, bw, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return client.IgnoreNotFound(fmt.Errorf("record the results of round %d: %w", bw.Status.Round, err))
	}
	return nil
}

// writeRound writes members: their results, by environment, and what the
// round did.
func (w *BranchWriteReconciler) writeRound(ctx context.Context, log zerolog.Logger, bw *v1alpha1.BranchWrite,
	members []v1alpha1.BranchWriteMember) (map[string]v1alpha1.BranchWriteResult, string) {
	r := w.Steps
	round := bw.Status.Round
	results := map[string]v1alpha1.BranchWriteResult{}
	answer := func(m v1alpha1.BranchWriteMember, res v1alpha1.BranchWriteResult) {
		res.Step, res.UID, res.Request, res.Round = m.Step, m.UID, m.Request, round
		results[m.Environment] = res
	}
	failAll := func(ms []v1alpha1.BranchWriteMember, why string) (map[string]v1alpha1.BranchWriteResult, string) {
		for _, m := range ms {
			answer(m, v1alpha1.BranchWriteResult{Error: why})
		}
		return results, fmt.Sprintf("round %d: %s", round, why)
	}

	reader := r.reader()
	var pipeline v1alpha1.Pipeline
	if err := reader.Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: bw.Spec.PipelineName}, &pipeline); err != nil {
		return failAll(members, fmt.Sprintf("read pipeline %s: %v", bw.Spec.PipelineName, err))
	}
	live, msg := w.guard(ctx, bw, members, answer)
	if len(live) == 0 {
		return results, fmt.Sprintf("round %d: %s", round, msg)
	}
	var bundle v1alpha1.Bundle
	if err := reader.Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: bw.Spec.BundleName}, &bundle); err != nil {
		return failAll(live, fmt.Sprintf("read bundle %s: %v", bw.Spec.BundleName, err))
	}
	cred := r.resolveGitCredential(ctx, log, &pipeline)
	switch {
	case cred.refused != nil:
		return failAll(live, cred.refused.Error())
	case cred.reason != "":
		return failAll(live, fmt.Sprintf("git has no credentials for %s (%s)", scm.RedactURL(bw.Spec.URL), cred.reason))
	}
	workDir := w.workDir(bw)
	defer func() { _ = os.RemoveAll(workDir) }()

	skip := map[string]bool{}
	for attempt := 0; ; attempt++ {
		commits, err := w.commitRound(ctx, bw, &pipeline, &bundle, cred, workDir, live, skip, answer)
		if err != nil {
			return failAll(unanswered(live, results), err.Error())
		}
		if len(commits) == 0 {
			return results, fmt.Sprintf("round %d: nothing to push", round)
		}
		// The push guard again, right before the push: the round's members
		// are the push's, with their intents written before their requests.
		if guarded, gmsg := w.guard(ctx, bw, membersOf(live, commits), answer); len(guarded) < len(commits) {
			return failAll(unanswered(live, results), "the push guard changed the round ("+gmsg+"); its members ask again")
		}
		if bw.Status.InFlight.PushIntent == nil {
			base := bw.DeepCopy()
			now := metav1.NewTime(r.now())
			bw.Status.InFlight.PushIntent = &now
			if err := w.Status().Patch(ctx, bw, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
				return failAll(unanswered(live, results), fmt.Sprintf("record the push intent: %v", err))
			}
		}
		err = r.GitClient.Push(ctx, workDir, "origin", bw.Spec.Branch, cred.auth(), false)
		if err == nil {
			for _, m := range live {
				if sha, ok := commits[m.Environment]; ok {
					answer(m, v1alpha1.BranchWriteResult{CommitSHA: sha})
				}
			}
			return results, fmt.Sprintf("round %d: pushed %d commit(s) to %s", round, len(commits), bw.Spec.Branch)
		}
		if !errors.Is(err, scm.ErrNonFastForward) {
			return failAll(unanswered(live, results), fmt.Sprintf("push %s: %v", bw.Spec.Branch, err))
		}
		if attempt+1 >= maxWriteAttempts {
			return failAll(unanswered(live, results), fmt.Sprintf("base branch %s kept moving (%d attempts); the members ask again",
				bw.Spec.Branch, maxWriteAttempts))
		}
		// Another writer moved the branch: redo the members' updates on its
		// head (they set the Bundle's version, so redoing them is the
		// rebase), from a fresh clone.
		log.Info().Int("attempt", attempt+1).Str("branch", bw.Spec.Branch).Msg("branch moved; writing the round again on its new head")
	}
}

// commitRound clones the branch into workDir and commits each member's
// change with its own commit, in spec.environments order: the commits by
// environment. A member that needs no change is answered at once (its commit
// is the head the round started from); a member whose update fails is
// answered with the error and skipped, and the round starts over without it,
// so a half-made change is never in another member's commit.
func (w *BranchWriteReconciler) commitRound(ctx context.Context, bw *v1alpha1.BranchWrite, pipeline *v1alpha1.Pipeline,
	bundle *v1alpha1.Bundle, cred gitCredential, workDir string, live []v1alpha1.BranchWriteMember, skip map[string]bool,
	answer func(v1alpha1.BranchWriteMember, v1alpha1.BranchWriteResult)) (map[string]string, error) {
	r := w.Steps
	hr, ok := r.GitClient.(scm.HeadCommitReader)
	if !ok {
		return nil, errors.New("the git client cannot read a commit (HeadCommitReader)")
	}
again:
	if err := os.RemoveAll(workDir); err != nil {
		return nil, fmt.Errorf("clear the work directory: %w", err)
	}
	if err := r.GitClient.Clone(ctx, bw.Spec.URL, bw.Spec.Branch, workDir, cred.auth()); err != nil {
		return nil, err
	}
	head, err := hr.HeadCommit(ctx, workDir)
	if err != nil {
		return nil, fmt.Errorf("read the head of %s: %w", bw.Spec.Branch, err)
	}
	commits := map[string]string{}
	for _, m := range live {
		if skip[m.Environment] {
			continue
		}
		env, found := environmentOf(pipeline, m.Environment)
		if !found || !v1alpha1.BatchedWrite(pipeline.Spec, env, bundle.Spec.Type) {
			skip[m.Environment] = true
			answer(m, v1alpha1.BranchWriteResult{Error: fmt.Sprintf("%s is not a batched environment of Pipeline %s", m.Environment, pipeline.Name)})
			continue
		}
		state := &steps.StepState{
			Pipeline: pipeline.Spec, PipelineName: pipeline.Name, Environment: env, Bundle: bundle.Spec,
			BundleName: bundle.Name, Namespace: bw.Namespace, WorkDir: workDir, Outputs: map[string]string{},
			Git: steps.GitConfig{URL: bw.Spec.URL, Branch: bw.Spec.Branch, Token: cred.token, SSHPrivateKey: cred.sshKey,
				SSHKnownHosts: cred.knownHosts, AuthorName: "kardinal-promoter", AuthorEmail: "kardinal@kardinal.io"},
			GitClient: r.GitClient, K8sClient: r.Client, StepTimeoutSeconds: env.StepTimeoutSeconds,
		}
		if err := runUpdateSteps(ctx, state, steps.DefaultSequenceForBundle(env.Approval, bundle.Spec.Type,
			env.Update.Strategy, effectiveLayout(pipeline, env))); err != nil {
			skip[m.Environment] = true
			answer(m, v1alpha1.BranchWriteResult{Error: err.Error()})
			goto again
		}
		msg := kardinalsteps.PromoteMessage(bundle.Name, env.Name, pipeline.Name, bw.Namespace) +
			"\n\n" + writeTrailer + bw.Name + "/" + strconv.FormatInt(bw.Status.Round, 10)
		switch err := r.GitClient.CommitAll(ctx, workDir, msg, state.Git.AuthorName, state.Git.AuthorEmail); {
		case errors.Is(err, scm.ErrNothingToCommit), errors.Is(err, scm.ErrAlreadyCommitted):
			answer(m, v1alpha1.BranchWriteResult{CommitSHA: head, NoChanges: true})
		case err != nil:
			return nil, fmt.Errorf("commit %s: %w", m.Environment, err)
		default:
			sha, err := hr.HeadCommit(ctx, workDir)
			if err != nil {
				return nil, fmt.Errorf("read the commit of %s: %w", m.Environment, err)
			}
			commits[m.Environment] = sha
		}
	}
	return commits, nil
}

// runUpdateSteps runs the update steps of seq (the steps between git-clone
// and git-commit: the environment's image update) on state.
func runUpdateSteps(ctx context.Context, state *steps.StepState, seq []string) error {
	start, end := slices.Index(seq, "git-clone"), slices.Index(seq, "git-commit")
	if start < 0 || end <= start+1 {
		return fmt.Errorf("no update step for %s in %v", state.Environment.Name, seq)
	}
	for _, name := range seq[start+1 : end] {
		step, err := steps.Lookup(name)
		if err != nil {
			return err
		}
		res, err := step.Execute(ctx, state)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if res.Status != steps.StepSuccess {
			return fmt.Errorf("%s: %s", name, res.Message)
		}
		for k, v := range res.Outputs {
			state.Outputs[k] = v
		}
	}
	return nil
}

// guard is the push guard (#1603, #1681) for members: the Bundle is not
// superseded, rejected or gone, each member's step is still the one that
// asked, and no newer Bundle already pushed to its environment. It answers
// the members it cancels and returns the others, and why it cancelled any.
func (w *BranchWriteReconciler) guard(ctx context.Context, bw *v1alpha1.BranchWrite, members []v1alpha1.BranchWriteMember,
	answer func(v1alpha1.BranchWriteMember, v1alpha1.BranchWriteResult)) ([]v1alpha1.BranchWriteMember, string) {
	r := w.Steps
	reader := r.reader()
	cancel := func(why string) ([]v1alpha1.BranchWriteMember, string) {
		for _, m := range members {
			answer(m, v1alpha1.BranchWriteResult{Cancelled: true, Error: why})
		}
		return nil, why
	}
	var b v1alpha1.Bundle
	if err := reader.Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: bw.Spec.BundleName}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return cancel(fmt.Sprintf("bundle %s is gone", bw.Spec.BundleName))
		}
		return cancel(fmt.Sprintf("read bundle %s: %v", bw.Spec.BundleName, err))
	}
	switch {
	case b.Spec.Rejected != nil || b.Status.Phase == "Rejected":
		return cancel(fmt.Sprintf("bundle %s was rejected", b.Name))
	case b.Status.Phase == "Superseded":
		return cancel(fmt.Sprintf("bundle %s was superseded — promotion cancelled", b.Name))
	}
	var live []v1alpha1.BranchWriteMember
	var why []string
	for _, m := range members {
		var ps v1alpha1.PromotionStep
		if err := reader.Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: m.Step}, &ps); err != nil {
			if !apierrors.IsNotFound(err) {
				answer(m, v1alpha1.BranchWriteResult{Error: fmt.Sprintf("read step %s: %v", m.Step, err)})
				continue
			}
			answer(m, v1alpha1.BranchWriteResult{Cancelled: true, Error: "step " + m.Step + " is gone"})
			why = append(why, m.Environment+": its step is gone")
			continue
		}
		if m.UID != "" && string(ps.UID) != m.UID {
			answer(m, v1alpha1.BranchWriteResult{Cancelled: true, Error: "step " + m.Step + " was made again"})
			why = append(why, m.Environment+": its step was made again")
			continue
		}
		newer, err := r.newerPushed(ctx, reader, &ps, &b, false)
		if err != nil {
			answer(m, v1alpha1.BranchWriteResult{Error: err.Error()})
			continue
		}
		if newer != "" {
			answer(m, v1alpha1.BranchWriteResult{Cancelled: true, NewerBundle: newer,
				Error: fmt.Sprintf("newer bundle %s already pushed to %s", newer, m.Environment)})
			why = append(why, m.Environment+": newer bundle "+newer+" already pushed")
			continue
		}
		live = append(live, m)
	}
	return live, strings.Join(why, "; ")
}

// resumeRound ends the round bw had in flight when the controller stopped.
// Before it was about to push (no PushIntent), nothing reached the branch,
// and the round's members are still pending. Otherwise its commits are on
// the branch when it pushed: they are found by their trailer and recorded,
// and the members without one (the push never landed, or they needed no
// change) stay pending, for the next round to write again or find unchanged.
func (w *BranchWriteReconciler) resumeRound(ctx context.Context, log zerolog.Logger, bw *v1alpha1.BranchWrite) error {
	in := bw.Status.InFlight
	results := map[string]v1alpha1.BranchWriteResult{}
	msg := fmt.Sprintf("round %d was interrupted before its push; its members are written again", in.Round)
	if in.PushIntent != nil {
		found, err := w.roundCommits(ctx, bw, in.Round)
		if err != nil {
			return err
		}
		for _, m := range in.Members {
			if sha, ok := found[m.Environment]; ok {
				results[m.Environment] = v1alpha1.BranchWriteResult{Step: m.Step, UID: m.UID, Request: m.Request,
					Round: in.Round, CommitSHA: sha}
			}
		}
		msg = fmt.Sprintf("round %d was interrupted after its push intent: %d of its %d commit(s) found on %s",
			in.Round, len(results), len(in.Members), bw.Spec.Branch)
	}
	log.Info().Int64("round", in.Round).Int("found", len(results)).Msg(msg)
	return w.recordRound(ctx, bw, results, msg)
}

// roundCommits are the commits of round on bw's branch, by environment:
// those whose message ends with the round's trailer, among the branch's last
// writeHistoryDepth commits.
func (w *BranchWriteReconciler) roundCommits(ctx context.Context, bw *v1alpha1.BranchWrite, round int64) (map[string]string, error) {
	r := w.Steps
	cloner, ok := r.GitClient.(scm.BranchCloner)
	history, ok2 := r.GitClient.(scm.HistoryReader)
	if !ok || !ok2 {
		return nil, errors.New("the git client cannot read a branch's history (BranchCloner, HistoryReader)")
	}
	reader := r.reader()
	var pipeline v1alpha1.Pipeline
	if err := reader.Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: bw.Spec.PipelineName}, &pipeline); err != nil {
		return nil, fmt.Errorf("read pipeline %s: %w", bw.Spec.PipelineName, err)
	}
	cred := r.resolveGitCredential(ctx, *zerolog.Ctx(ctx), &pipeline)
	if cred.refused != nil {
		return nil, cred.refused
	}
	dir := w.workDir(bw) + "-history"
	_ = os.RemoveAll(dir)
	defer func() { _ = os.RemoveAll(dir) }()
	if _, err := cloner.CloneOrInit(ctx, bw.Spec.URL, bw.Spec.Branch, dir, cred.auth(), writeHistoryDepth); err != nil {
		return nil, fmt.Errorf("read the history of %s: %w", bw.Spec.Branch, err)
	}
	commits, err := history.CommitMessages(ctx, dir, writeHistoryDepth)
	if err != nil {
		return nil, fmt.Errorf("read the history of %s: %w", bw.Spec.Branch, err)
	}
	trailer := writeTrailer + bw.Name + "/" + strconv.FormatInt(round, 10)
	envs := map[string]bool{}
	for _, e := range bw.Spec.Environments {
		envs[e.Name] = true
	}
	found := map[string]string{}
	for _, c := range commits {
		msg := strings.TrimSpace(c.Message)
		if !strings.HasSuffix(msg, trailer) {
			continue
		}
		first, _, _ := strings.Cut(msg, "\n")
		prefix := "[kardinal] Promote " + bw.Spec.BundleName + " to "
		if env, ok := strings.CutPrefix(first, prefix); ok && envs[env] {
			if _, seen := found[env]; !seen {
				found[env] = c.SHA
			}
		}
	}
	return found, nil
}

// workDir is bw's checkout: under the PromotionStep reconciler's work
// directory root, beside the steps'.
func (w *BranchWriteReconciler) workDir(bw *v1alpha1.BranchWrite) string {
	root := w.Steps.WorkDirRoot
	if root == "" {
		root = steps.DefaultWorkDirRoot
	}
	return filepath.Join(root, bw.Namespace, "branchwrites", bw.Name)
}

// unanswered are the members of live results has no answer for.
func unanswered(live []v1alpha1.BranchWriteMember, results map[string]v1alpha1.BranchWriteResult) []v1alpha1.BranchWriteMember {
	var out []v1alpha1.BranchWriteMember
	for _, m := range live {
		if _, ok := results[m.Environment]; !ok {
			out = append(out, m)
		}
	}
	return out
}

// membersOf are the members of live that have a commit.
func membersOf(live []v1alpha1.BranchWriteMember, commits map[string]string) []v1alpha1.BranchWriteMember {
	var out []v1alpha1.BranchWriteMember
	for _, m := range live {
		if _, ok := commits[m.Environment]; ok {
			out = append(out, m)
		}
	}
	return out
}

// environmentOf is environment name of pipeline.
func environmentOf(pipeline *v1alpha1.Pipeline, name string) (v1alpha1.EnvironmentSpec, bool) {
	for _, e := range pipeline.Spec.Environments {
		if e.Name == name {
			return e, true
		}
	}
	return v1alpha1.EnvironmentSpec{}, false
}

// reader is r's API reader, or its client.
func (r *Reconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// SetupWithManager registers the BranchWrite reconciler. A BranchWrite
// waiting for its branch's turn is woken when the turn ends (the shared
// BranchQueue's wakeWrite).
func (w *BranchWriteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("branchwrite").
		For(&v1alpha1.BranchWrite{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: w.Workers}).
		WatchesRawSource(source.Channel(w.writeWakeups(), &handler.EnqueueRequestForObject{}))
	return shard.Active().Complete(b, tracing.WrapReconciler("branchwrite", w), &v1alpha1.BranchWriteList{})
}

// writeWakeups connects the branch queue's wakeWrite to a channel of events
// for the controller: a full buffer drops the wake, and the waiter's own
// requeue still comes.
func (w *BranchWriteReconciler) writeWakeups() <-chan event.GenericEvent {
	ch := make(chan event.GenericEvent, 256)
	q := w.Steps.turns()
	q.mu.Lock()
	q.wakeWrite = func(key string) {
		ns, name, _ := strings.Cut(key, "/")
		select {
		case ch <- event.GenericEvent{Object: &v1alpha1.BranchWrite{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}}:
		default:
		}
	}
	q.mu.Unlock()
	return ch
}
