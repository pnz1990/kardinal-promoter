// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
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
// BranchWrite and round that made it, for people reading the branch. A round
// found in flight is reconciled by the commits it recorded, not by it.
const writeTrailer = "Kardinal-Write: "

// writeHistoryDepth is how far back on the branch a round's commits are
// looked for (as a rebase looks for its base, #1606).
const writeHistoryDepth = 200

// BranchWriteReconciler writes the changes of a Bundle's batched
// environments (spec.git.writes: batched) to their branch: each round takes
// the branch's turn (the BranchQueue it shares with the PromotionStep
// reconciler, the G8 branch-turn exception), checks each member as its step
// would before its next git step (pause, freeze, environment hold) and as
// the push guard does, freezes the members it may write in the BranchWrite's
// status, clones once, runs each member's update step and commits it with
// the environment's own commit message, checks the members again, records
// the commits it is about to push, and pushes once
// (docs/design/30-batched-branch-writes.md). It writes only the
// BranchWrite's status; the Graph mirrors each environment's result onto its
// step (spec.live.writes), where await-write reads it. Nothing it needs
// crosses a reconcile in memory: the round in flight, the commits it was
// about to push and when, are in status.
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
	r := w.Steps
	// From the API server: the cache can lag the round this reconciler
	// just recorded, and a round resumed for nothing costs a history read.
	var bw v1alpha1.BranchWrite
	if err := r.reader().Get(ctx, req.NamespacedName, &bw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	pending := pendingRequests(&bw)
	if bw.Status.InFlight == nil && len(pending) == 0 {
		return ctrl.Result{}, nil
	}
	key := normalizeRepoURL(bw.Spec.URL) + "#" + bw.Spec.Branch
	release, wait, got := r.turns().tryTurn(key, writeTurnKey(&bw), r.now())
	if !got {
		msg := fmt.Sprintf(waitingForTurnMessage, bw.Spec.Branch)
		if bw.Status.Message != msg {
			if err := w.patchStatus(ctx, &bw, func(s *v1alpha1.BranchWriteStatus) { s.Message = msg }); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: wait, Priority: &lowPriority}, nil
	}
	defer func() { release(r.now()) }()

	if bw.Status.InFlight != nil {
		// A round in flight when the controller stopped, or whose push
		// could not be confirmed: its recorded commits say what it wrote.
		if err := w.resumeRound(ctx, log, &bw); err != nil {
			return ctrl.Result{}, err
		}
		pending = pendingRequests(&bw)
		if len(pending) == 0 {
			return ctrl.Result{}, nil
		}
	}

	members := pending[:min(len(pending), maxRoundMembers)]
	rd := &round{n: bw.Status.Round + 1, results: map[string]v1alpha1.BranchWriteResult{}}
	pipeline, bundle, live := w.prepare(ctx, log, &bw, rd, members)
	switch {
	case len(live) > 0:
		if err := w.patchStatus(ctx, &bw, func(s *v1alpha1.BranchWriteStatus) {
			s.Round = rd.n
			s.InFlight = &v1alpha1.BranchWriteRound{Round: rd.n, Members: live}
			s.Message = fmt.Sprintf("round %d: writing %d environment(s)", rd.n, len(live))
		}); err != nil {
			return ctrl.Result{}, err
		}
		if err := w.writeRound(ctx, log, &bw, rd, pipeline, bundle, live); err != nil {
			// The push may have landed: the round stays in flight, with
			// its commits recorded, and the next reconcile reads the
			// branch.
			return ctrl.Result{}, err
		}
		if err := w.recordRound(ctx, &bw, rd); err != nil {
			return ctrl.Result{}, err
		}
	case len(rd.results) > 0:
		if err := w.recordRound(ctx, &bw, rd); err != nil {
			return ctrl.Result{}, err
		}
	default:
		// Every member is held or waits for another's path: no round.
		msg := fmt.Sprintf("waiting: %d environment(s) held (pause, freeze or hold)", len(members))
		if bw.Status.Message != msg && rd.held {
			if err := w.patchStatus(ctx, &bw, func(s *v1alpha1.BranchWriteStatus) { s.Message = msg }); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
	switch {
	case rd.deferred || len(pending) > len(members):
		// The next round, now.
		return ctrl.Result{RequeueAfter: time.Millisecond}, nil
	case rd.held:
		// A held member is not answered: it is written once the pause,
		// freeze or hold ends (its step is held there too).
		return ctrl.Result{RequeueAfter: requeuePaused}, nil
	}
	return ctrl.Result{}, nil
}

// pendingRequests are bw's requests that are environments of bw with no
// answer yet, in spec.environments order: an answer is to the same step
// (name and UID) and a request at least as new.
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

// patchStatus changes bw's status with change and writes it as a merge
// patch of the status subresource, not locked on bw's resourceVersion: the
// Graph rewrites spec.requested whenever a step asks, and this reconciler is
// the status's only writer.
func (w *BranchWriteReconciler) patchStatus(ctx context.Context, bw *v1alpha1.BranchWrite, change func(*v1alpha1.BranchWriteStatus)) error {
	base := bw.DeepCopy()
	change(&bw.Status)
	if err := w.Status().Patch(ctx, bw, client.MergeFrom(base)); err != nil {
		// A NotFound is bw gone: objectgone ends the reconcile quietly.
		return fmt.Errorf("patch branchwrite %s status: %w", bw.Name, err)
	}
	return nil
}

// recordRound records rd's results and ends the round in flight.
func (w *BranchWriteReconciler) recordRound(ctx context.Context, bw *v1alpha1.BranchWrite, rd *round) error {
	return w.patchStatus(ctx, bw, func(s *v1alpha1.BranchWriteStatus) {
		if s.Environments == nil {
			s.Environments = map[string]v1alpha1.BranchWriteResult{}
		}
		for env, res := range rd.results {
			s.Environments[env] = res
		}
		s.Round = max(s.Round, rd.n)
		s.InFlight = nil
		s.Message = rd.message
		if s.Message == "" {
			s.Message = fmt.Sprintf("round %d: %d environment(s) answered", rd.n, len(rd.results))
		}
	})
}

// round is one round's number, answers by environment and what it did, and
// whether it left a member for later: held (written once the hold ends) or
// deferred (written in the next round).
type round struct {
	n        int64
	results  map[string]v1alpha1.BranchWriteResult
	message  string
	held     bool
	deferred bool
}

// answer answers m.
func (rd *round) answer(m v1alpha1.BranchWriteMember, res v1alpha1.BranchWriteResult) {
	res.Step, res.UID, res.Request, res.Round = m.Step, m.UID, m.Request, rd.n
	rd.results[m.Environment] = res
}

// done sets what the round did.
func (rd *round) done(format string, args ...any) {
	rd.message = fmt.Sprintf("round %d: ", rd.n) + fmt.Sprintf(format, args...)
}

// failAll answers each of ms that has no answer with why.
func (rd *round) failAll(ms []v1alpha1.BranchWriteMember, why string) {
	for _, m := range ms {
		if _, ok := rd.results[m.Environment]; !ok {
			rd.answer(m, v1alpha1.BranchWriteResult{Error: why})
		}
	}
	rd.done("%s", why)
}

// prepare reads members' Pipeline and Bundle and returns the members the
// round may write now: admitted (admit), and none sharing a path with an
// earlier one (deferSharedPaths). It answers the others it can answer.
func (w *BranchWriteReconciler) prepare(ctx context.Context, log zerolog.Logger, bw *v1alpha1.BranchWrite, rd *round,
	members []v1alpha1.BranchWriteMember) (*v1alpha1.Pipeline, *v1alpha1.Bundle, []v1alpha1.BranchWriteMember) {
	reader := w.Steps.reader()
	var pipeline v1alpha1.Pipeline
	if err := reader.Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: bw.Spec.PipelineName}, &pipeline); err != nil {
		rd.failAll(members, fmt.Sprintf("read pipeline %s: %v", bw.Spec.PipelineName, err))
		return nil, nil, nil
	}
	// Only the Pipeline's own repository and branch are written with its
	// credentials.
	if normalizeRepoURL(bw.Spec.URL) != normalizeRepoURL(pipeline.Spec.Git.URL) || bw.Spec.Branch != baseBranch(&pipeline) {
		rd.failAll(members, fmt.Sprintf("BranchWrite %s writes %s#%s, not Pipeline %s's %s#%s", bw.Name,
			scm.RedactURL(bw.Spec.URL), bw.Spec.Branch, pipeline.Name, scm.RedactURL(pipeline.Spec.Git.URL), baseBranch(&pipeline)))
		return nil, nil, nil
	}
	var bundle v1alpha1.Bundle
	if err := reader.Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: bw.Spec.BundleName}, &bundle); err != nil && !apierrors.IsNotFound(err) {
		rd.failAll(members, fmt.Sprintf("read bundle %s: %v", bw.Spec.BundleName, err))
		return nil, nil, nil
	}
	live := w.admit(ctx, log, bw, rd, members)
	live = deferSharedPaths(&pipeline, live, rd)
	if len(live) == 0 && rd.message == "" && len(rd.results) > 0 {
		rd.done("nothing to write (%d environment(s) answered)", len(rd.results))
	}
	return &pipeline, &bundle, live
}

// writeRound writes live, the round's members. An error means the push may
// have landed: the round is left in flight, with its commits recorded.
func (w *BranchWriteReconciler) writeRound(ctx context.Context, log zerolog.Logger, bw *v1alpha1.BranchWrite, rd *round,
	pipeline *v1alpha1.Pipeline, bundle *v1alpha1.Bundle, live []v1alpha1.BranchWriteMember) error {
	r := w.Steps
	cred := r.resolveGitCredential(ctx, log, pipeline)
	switch {
	case cred.refused != nil:
		rd.failAll(live, cred.refused.Error())
		return nil
	case cred.reason != "":
		rd.failAll(live, fmt.Sprintf("git has no credentials for %s (%s)", scm.RedactURL(bw.Spec.URL), cred.reason))
		return nil
	}
	workDir := w.workDir(bw)
	defer func() { _ = os.RemoveAll(workDir) }()

	skip := map[string]bool{}
	for refused := 0; ; {
		commits, err := w.commitRound(ctx, bw, pipeline, bundle, cred, workDir, live, skip, rd)
		if err != nil {
			rd.failAll(unanswered(live, rd.results), err.Error())
			return nil
		}
		if len(commits) == 0 {
			rd.done("nothing to push")
			return nil
		}
		// The holds and the push guard again, right before the push: a
		// member a pause, freeze or hold now holds, or the guard cancels,
		// is dropped, and the others are written again without it (its
		// commit is in their chain).
		pushing := membersOf(live, commits)
		if still := w.admit(ctx, log, bw, rd, pushing); len(still) < len(pushing) {
			kept := map[string]bool{}
			for _, m := range still {
				kept[m.Environment] = true
			}
			for _, m := range pushing {
				if !kept[m.Environment] {
					skip[m.Environment] = true
				}
			}
			continue
		}
		if err := w.patchStatus(ctx, bw, func(s *v1alpha1.BranchWriteStatus) {
			now := metav1.NewTime(r.now())
			s.InFlight.PushIntent = &now
			s.InFlight.Commits = commits
		}); err != nil {
			// Nothing was pushed: the round in flight is resumed as one
			// interrupted before this push.
			return fmt.Errorf("record the push intent: %w", err)
		}
		err = r.GitClient.Push(ctx, workDir, "origin", bw.Spec.Branch, cred.auth(), false)
		if errors.Is(err, scm.ErrNonFastForward) {
			if refused++; refused >= maxWriteAttempts {
				rd.failAll(unanswered(live, rd.results), fmt.Sprintf("base branch %s kept moving (%d attempts); the members ask again",
					bw.Spec.Branch, maxWriteAttempts))
				return nil
			}
			// Another writer moved the branch: write the members again on
			// its head (they set the Bundle's version, so writing them
			// again is the rebase), from a fresh clone.
			log.Info().Int("attempt", refused).Str("branch", bw.Spec.Branch).Msg("branch moved; writing the round again on its new head")
			continue
		}
		if err != nil {
			// Not a refusal: the push may have landed all the same. The
			// branch says which of the recorded commits are on it.
			landed, lerr := w.landed(ctx, bw, pipeline, commits)
			if lerr != nil {
				return fmt.Errorf("push %s: %w; its commits could not be looked for on the branch: %w", bw.Spec.Branch, err, lerr)
			}
			if len(landed) == 0 {
				rd.failAll(unanswered(live, rd.results), fmt.Sprintf("push %s: %v", bw.Spec.Branch, err))
				return nil
			}
			log.Info().Err(err).Int("commits", len(landed)).Msg("the push returned an error, and its commits are on the branch")
			commits = landed
		}
		for _, m := range live {
			if sha, ok := commits[m.Environment]; ok {
				rd.answer(m, v1alpha1.BranchWriteResult{CommitSHA: sha})
			}
		}
		rd.done("pushed %d commit(s) to %s", len(commits), bw.Spec.Branch)
		return nil
	}
}

// commitRound clones the branch into workDir and commits each member's
// change with its own commit, in spec.environments order: the commits by
// environment. A member that needs no change is answered at once (its commit
// is the head the round started from); a member whose update fails is
// answered with the error and skipped, and the round starts over without it,
// so a half-made change is never in another member's commit. The members in
// skip are left out.
func (w *BranchWriteReconciler) commitRound(ctx context.Context, bw *v1alpha1.BranchWrite, pipeline *v1alpha1.Pipeline,
	bundle *v1alpha1.Bundle, cred gitCredential, workDir string, live []v1alpha1.BranchWriteMember, skip map[string]bool,
	rd *round) (map[string]string, error) {
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
			rd.answer(m, v1alpha1.BranchWriteResult{Error: fmt.Sprintf("%s is not a batched environment of Pipeline %s", m.Environment, pipeline.Name)})
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
			rd.answer(m, v1alpha1.BranchWriteResult{Error: err.Error()})
			goto again
		}
		msg := kardinalsteps.PromoteMessage(bundle.Name, env.Name, pipeline.Name, bw.Namespace) +
			"\n\n" + writeTrailer + bw.Name + "/" + strconv.FormatInt(rd.n, 10)
		switch err := r.GitClient.CommitAll(ctx, workDir, msg, state.Git.AuthorName, state.Git.AuthorEmail); {
		case errors.Is(err, scm.ErrNothingToCommit), errors.Is(err, scm.ErrAlreadyCommitted):
			rd.answer(m, v1alpha1.BranchWriteResult{CommitSHA: head, NoChanges: true})
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

// admit returns the members of members the round may write now, in order.
// It answers the members the push guard cancels (#1603, #1681): the Bundle
// superseded, rejected or gone, the member's step gone or made again, or a
// newer Bundle already pushed to its environment. A member that a pause, a
// freeze or a hold of its environment holds is left unanswered, as its step
// holds itself before its next git step (holdIfPaused, holdIfFrozen,
// holdIfEnvironmentHeld; read-only here): it is written once the hold ends.
// A read that fails holds the member too: the freeze is a kill switch that
// fails closed.
func (w *BranchWriteReconciler) admit(ctx context.Context, log zerolog.Logger, bw *v1alpha1.BranchWrite,
	rd *round, members []v1alpha1.BranchWriteMember) []v1alpha1.BranchWriteMember {
	r := w.Steps
	reader := r.reader()
	if len(members) == 0 {
		return nil
	}
	cancel := func(why string) []v1alpha1.BranchWriteMember {
		for _, m := range members {
			rd.answer(m, v1alpha1.BranchWriteResult{Cancelled: true, Error: why})
		}
		rd.done("%s", why)
		return nil
	}
	hold := func(err error, what string) []v1alpha1.BranchWriteMember {
		log.Info().Err(err).Msgf("%s; the round's members wait", what)
		rd.held = true
		return nil
	}
	var b v1alpha1.Bundle
	if err := reader.Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: bw.Spec.BundleName}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return cancel(fmt.Sprintf("bundle %s is gone", bw.Spec.BundleName))
		}
		return hold(err, "read the bundle")
	}
	switch {
	case b.Spec.Rejected != nil || b.Status.Phase == "Rejected":
		return cancel(fmt.Sprintf("bundle %s was rejected", b.Name))
	case b.Status.Phase == "Superseded":
		return cancel(fmt.Sprintf("bundle %s was superseded — promotion cancelled", b.Name))
	}
	switch paused, err := lifecycle.IsPaused(ctx, reader, bw.Namespace, bw.Spec.PipelineName); {
	case err != nil:
		return hold(err, "check the pause")
	case paused:
		return hold(nil, lifecycle.PausedMessage(bw.Spec.PipelineName))
	}
	// Read again for its holds: one can come between two checks.
	var pipeline v1alpha1.Pipeline
	if err := reader.Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: bw.Spec.PipelineName}, &pipeline); err != nil {
		return hold(err, "read the pipeline's holds")
	}
	var live []v1alpha1.BranchWriteMember
	for _, m := range members {
		var ps v1alpha1.PromotionStep
		if err := reader.Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: m.Step}, &ps); err != nil {
			if apierrors.IsNotFound(err) {
				rd.answer(m, v1alpha1.BranchWriteResult{Cancelled: true, Error: "step " + m.Step + " is gone"})
				continue
			}
			hold(err, "read step "+m.Step)
			continue
		}
		if m.UID != "" && string(ps.UID) != m.UID {
			rd.answer(m, v1alpha1.BranchWriteResult{Cancelled: true, Error: "step " + m.Step + " was made again"})
			continue
		}
		if h := lifecycle.HeldFrom(&pipeline, ps.Spec.Environment, ps.Spec.BundleName); h != nil {
			hold(nil, fmt.Sprintf("%s is held on bundle %s", m.Environment, h.Bundle))
			continue
		}
		matches, err := r.freezesFor(ctx, &ps)
		if err != nil {
			hold(err, "check the freezes of "+m.Environment)
			continue
		}
		if held := changewindow.Holding(matches); len(held) > 0 {
			hold(nil, fmt.Sprintf("%s is frozen by ChangeWindow %s", m.Environment, held[0].Window.Name))
			continue
		}
		newer, err := r.newerPushed(ctx, reader, &ps, &b, false)
		if err != nil {
			hold(err, "check the newer bundles of "+m.Environment)
			continue
		}
		if newer != "" {
			rd.answer(m, v1alpha1.BranchWriteResult{Cancelled: true, NewerBundle: newer,
				Error: fmt.Sprintf("newer bundle %s already pushed to %s", newer, m.Environment)})
			continue
		}
		live = append(live, m)
	}
	return live
}

// deferSharedPaths leaves out of the round each member that writes a path
// an earlier member writes, or one inside or around it (two environments on
// one path, which the Pipeline reconciler reports as PathConflict): it is
// written in the next round, on a branch with the earlier one's change, so
// that its answer names a commit that has its version.
func deferSharedPaths(pipeline *v1alpha1.Pipeline, live []v1alpha1.BranchWriteMember, rd *round) []v1alpha1.BranchWriteMember {
	var out []v1alpha1.BranchWriteMember
	var taken []string
	for _, m := range live {
		env, _ := environmentOf(pipeline, m.Environment)
		paths := writtenPaths(env)
		if slices.ContainsFunc(paths, func(p string) bool {
			return slices.ContainsFunc(taken, func(t string) bool { return pathsOverlap(p, t) })
		}) {
			rd.deferred = true
			continue
		}
		taken = append(taken, paths...)
		out = append(out, m)
	}
	return out
}

// writtenPaths are the paths env's update writes, relative to the
// repository root and cleaned: its directory, and a Helm valuesFile outside
// it (as the Pipeline reconciler's PathConflict check sees them).
func writtenPaths(env v1alpha1.EnvironmentSpec) []string {
	clean := func(p string) string { return path.Clean(strings.TrimPrefix(strings.TrimSpace(p), "./")) }
	dir := "environments/" + env.Name
	if env.Path != "" {
		dir = clean(env.Path)
	}
	out := []string{dir}
	if h := env.Update.Helm; env.Update.Strategy == "helm" && h != nil && h.ValuesFile != "" {
		if f := clean(path.Join(dir, h.ValuesFile)); !pathsOverlap(dir, f) {
			out = append(out, f)
		}
	}
	return out
}

// pathsOverlap reports whether path a is b or contains it, or the reverse.
func pathsOverlap(a, b string) bool {
	if a == b || a == "." || b == "." {
		return true
	}
	return strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/")
}

// resumeRound ends the round bw had in flight: one the controller stopped
// in, or whose push it could not confirm. Before its push intent nothing
// reached the branch, and its members are still pending. After it, each
// commit the round recorded with the intent (status.inFlight.commits) that
// is on the branch was written and is recorded; the other members stay
// pending, for the next round to write again or find unchanged. A commit
// that only looks like the round's (its message and trailer) is not one.
func (w *BranchWriteReconciler) resumeRound(ctx context.Context, log zerolog.Logger, bw *v1alpha1.BranchWrite) error {
	in := bw.Status.InFlight
	rd := &round{n: in.Round, results: map[string]v1alpha1.BranchWriteResult{}}
	rd.message = fmt.Sprintf("round %d was interrupted before its push; its members are written again", in.Round)
	if in.PushIntent != nil && len(in.Commits) > 0 {
		var pipeline v1alpha1.Pipeline
		if err := w.Steps.reader().Get(ctx, client.ObjectKey{Namespace: bw.Namespace, Name: bw.Spec.PipelineName}, &pipeline); err != nil {
			return fmt.Errorf("read pipeline %s: %w", bw.Spec.PipelineName, err)
		}
		landed, err := w.landed(ctx, bw, &pipeline, in.Commits)
		if err != nil {
			return err
		}
		for _, m := range in.Members {
			if sha, ok := landed[m.Environment]; ok {
				rd.answer(m, v1alpha1.BranchWriteResult{CommitSHA: sha})
			}
		}
		rd.message = fmt.Sprintf("round %d was interrupted after its push intent: %d of its %d commit(s) found on %s",
			in.Round, len(rd.results), len(in.Commits), bw.Spec.Branch)
	}
	log.Info().Int64("round", in.Round).Int("found", len(rd.results)).Msg(rd.message)
	return w.recordRound(ctx, bw, rd)
}

// landed are the commits of commits (by environment) on bw's branch:
// reachable from its head, among its last writeHistoryDepth commits.
func (w *BranchWriteReconciler) landed(ctx context.Context, bw *v1alpha1.BranchWrite, pipeline *v1alpha1.Pipeline,
	commits map[string]string) (map[string]string, error) {
	r := w.Steps
	cloner, ok := r.GitClient.(scm.BranchCloner)
	history, ok2 := r.GitClient.(scm.HistoryReader)
	if !ok || !ok2 {
		return nil, errors.New("the git client cannot read a branch's history (BranchCloner, HistoryReader)")
	}
	cred := r.resolveGitCredential(ctx, *zerolog.Ctx(ctx), pipeline)
	if cred.refused != nil {
		return nil, cred.refused
	}
	dir := w.workDir(bw) + "-history"
	_ = os.RemoveAll(dir)
	defer func() { _ = os.RemoveAll(dir) }()
	if _, err := cloner.CloneOrInit(ctx, bw.Spec.URL, bw.Spec.Branch, dir, cred.auth(), writeHistoryDepth); err != nil {
		return nil, fmt.Errorf("read the history of %s: %w", bw.Spec.Branch, err)
	}
	log, err := history.CommitMessages(ctx, dir, writeHistoryDepth)
	if err != nil {
		return nil, fmt.Errorf("read the history of %s: %w", bw.Spec.Branch, err)
	}
	onBranch := map[string]bool{}
	for _, c := range log {
		onBranch[c.SHA] = true
	}
	found := map[string]string{}
	for env, sha := range commits {
		if onBranch[sha] {
			found[env] = sha
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
