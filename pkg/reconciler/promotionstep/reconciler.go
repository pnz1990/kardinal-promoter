// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

// Package promotionstep implements the PromotionStep reconciler, which drives
// the promotion state machine from Pending through steps execution to Verified.
package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	builderutil "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"

	// Import built-in steps to trigger init() registration.
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

const (
	// StatesPending is the initial state — Graph controller created the step but reconciler hasn't started.
	StatePending = ""
	// StatePendingExplicit is the explicit Pending marker.
	StatePendingExplicit = "Pending"
	// StatePromoting — step engine executing.
	StatePromoting = "Promoting"
	// StateWaitingForMerge — open-pr step done, waiting for PR merge.
	StateWaitingForMerge = "WaitingForMerge"
	// StateHealthChecking — PR merged, health check running.
	StateHealthChecking = "HealthChecking"
	// StateVerified — terminal success.
	StateVerified = "Verified"
	// StateFailed — terminal failure.
	StateFailed = "Failed"
	// StateAbortedByAlarm — terminal: health alarm with onHealthFailure=abort (K-03).
	// Requires human intervention to resume or rollback.
	StateAbortedByAlarm = "AbortedByAlarm"
	// StateRollingBack — terminal: health alarm with onHealthFailure=rollback
	// created a rollback Bundle (K-03). The step takes no further action; the
	// rollback Bundle's own step carries the promotion on.
	StateRollingBack = "RollingBack"

	// requeueWaitForMerge is how often to requeue while waiting for a PR merge.
	requeueWaitForMerge = 30 * time.Second
	// requeueGateWait is the fallback requeue while a required gate holds a
	// Pending step. The gate's status write normally wakes the step first
	// (policyGateMapper).
	requeueGateWait = 30 * time.Second
	// requeueHealthCheck is how often to requeue during health checking. It is
	// also the minimum interval between two health checks of one step, however
	// many events arrive (C03-promotionstep-12).
	requeueHealthCheck = 10 * time.Second

	// maxStepRetries is how many times a step that failed with a retryable
	// (transient) error is re-run before the PromotionStep fails
	// (C03-promotionstep-06).
	maxStepRetries = 5
	// retryBaseDelay and retryMaxDelay bound the exponential backoff between
	// retries: 10s, 20s, 40s, 80s, 120s.
	retryBaseDelay = 10 * time.Second
	retryMaxDelay  = 2 * time.Minute
)

// Reconciler drives the PromotionStep state machine.
//
// State transitions:
//
//	"" / "Pending"    → "Promoting": initialize step sequence, set currentStepIndex=0
//	"Promoting"       → "WaitingForMerge": open-pr step completed (prURL in outputs)
//	"Promoting"       → "HealthChecking": every step completed without a PR
//	"Promoting"       → "Failed": a step failed permanently, or kept failing
//	                    transiently after maxStepRetries retries
//	"WaitingForMerge" → "HealthChecking": PRStatus reports the PR merged
//	"WaitingForMerge" → "Failed": PR closed without merge, or waitForMergeTimeout
//	"HealthChecking"  → "Verified": the health adapter reports the promoted
//	                    revision healthy (and any bake window completed)
//	"HealthChecking"  → "Failed" / "AbortedByAlarm" / "RollingBack": health
//	                    timeout, or a terminal failure under onHealthFailure
//	non-terminal      → "Failed": the parent Bundle was superseded
//
// Every transition goes through transition(), which also closes status.steps
// entries and writes the audit record, metrics and Event.
//
// In Promoting, one reconcile runs the remaining steps in a single
// Engine.ExecuteFrom call and then writes currentStepIndex, outputs and the
// PR URL in one status patch. A crash before that patch re-runs the steps from
// the last persisted index, so every step must be safe to repeat (the git and
// open-pr steps are).
type Reconciler struct {
	client.Client

	// SCM is the SCM provider for PR operations.
	SCM scm.SCMProvider

	// GitClient is the Git operations client.
	GitClient scm.GitClient

	// HealthDetector selects the health adapter for health checking.
	// If nil, the health-check step stub (always-success) is used.
	HealthDetector *health.AutoDetector

	// WorkDirFn returns the working directory for a given pipeline+bundle pair.
	// Tests set it; when nil a fixed path under the kardinal work root is used.
	WorkDirFn func(pipelineName, bundleName string) string

	// Recorder emits Kubernetes Events for PromotionStep state transitions.
	// When nil, event emission is skipped (backward-compatible).
	Recorder events.EventRecorder
}

// Reconcile processes one PromotionStep event.
// It is idempotent: safe to re-run after a crash at any point.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("promotionstep", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var ps v1alpha1.PromotionStep
	if err := r.Get(ctx, req.NamespacedName, &ps); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get promotionstep %s: %w", req.Name, err)
	}

	// Orphan guard: if the parent Bundle no longer exists, self-delete this
	// PromotionStep to stop the infinite reconcile error loop (#248).
	// This handles the case where a Bundle was deleted manually (e.g. during
	// development or testing) while its PromotionSteps are still present.
	// Graph-first: we delete our OWN resource (PromotionStep), not anyone else's.
	if ps.Spec.BundleName != "" {
		var parentBundle v1alpha1.Bundle
		if err := r.Get(ctx, types.NamespacedName{
			Name:      ps.Spec.BundleName,
			Namespace: ps.Namespace,
		}, &parentBundle); err != nil {
			if apierrors.IsNotFound(err) {
				log.Info().
					Str("bundle", ps.Spec.BundleName).
					Msg("parent bundle not found — self-deleting orphaned PromotionStep")
				if delErr := r.Delete(ctx, &ps); delErr != nil && !apierrors.IsNotFound(delErr) {
					return ctrl.Result{}, fmt.Errorf("delete orphaned promotionstep: %w", delErr)
				}
				return ctrl.Result{}, nil
			}
			// Transient error — requeue to retry later.
			return ctrl.Result{}, fmt.Errorf("check parent bundle %s: %w", ps.Spec.BundleName, err)
		}

		// Supersession guard: a superseded Bundle's steps must stop, whatever
		// phase they are in, and must not leave an open PR behind (#310,
		// C03-promotionstep-08). HealthChecking counts: without it a superseded
		// step could still turn Verified and, with onHealthFailure=rollback,
		// open a rollback of a version nobody promotes any more.
		if parentBundle.Status.Phase == "Superseded" && isCancellable(ps.Status.State) {
			return r.handleSuperseded(ctx, log, &ps)
		}
	}

	switch ps.Status.State {
	case StatePending, StatePendingExplicit:
		return r.handlePending(ctx, log, &ps)
	case StatePromoting:
		return r.handlePromoting(ctx, log, &ps)
	case StateWaitingForMerge:
		return r.handleWaitingForMerge(ctx, log, &ps)
	case StateHealthChecking:
		return r.handleHealthChecking(ctx, log, &ps)
	case StateVerified, StateFailed:
		// Terminal states — clean up workdir if present (ST-7/ST-8 short-term mitigation).
		r.cleanWorkDir(log, &ps)
		return ctrl.Result{}, nil
	case StateAbortedByAlarm:
		// Terminal human-intervention state — clean up workdir and stop reconciling.
		// Requires manual resume or rollback from a human operator.
		r.cleanWorkDir(log, &ps)
		return ctrl.Result{}, nil
	case StateRollingBack:
		// Managed state set by applyHealthFailurePolicy (K-03). The rollback Bundle
		// drives resolution externally; this reconciler takes no further action until
		// the rollback Bundle completes and the step is superseded or manually reset.
		r.cleanWorkDir(log, &ps)
		return ctrl.Result{}, nil
	default:
		log.Warn().Str("state", ps.Status.State).Msg("unknown state, resetting to Pending")
		if err := r.transition(ctx, ps.DeepCopy(), &ps, StatePendingExplicit, ""); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
}

// isCancellable reports whether a step in state can still be cancelled by
// supersession: every state that is not terminal.
func isCancellable(state string) bool {
	switch state {
	case StatePending, StatePendingExplicit, StatePromoting, StateWaitingForMerge, StateHealthChecking:
		return true
	}
	return false
}

// handleSuperseded closes the step's PR, if it opened one that is still open,
// and fails the step. A failed close is retried with backoff up to
// maxStepRetries times; after that the step fails anyway and the message
// tells the operator to close the PR by hand (C03-promotionstep-08). A step
// that never left Pending is failed without an AuditEvent (cancelUnstarted).
func (r *Reconciler) handleSuperseded(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	base := ps.DeepCopy()
	log.Info().
		Str("bundle", ps.Spec.BundleName).
		Str("env", ps.Spec.Environment).
		Str("state", ps.Status.State).
		Msg("parent bundle superseded — closing open PR and cancelling step")

	// A step still Pending never started: no PromotionStarted record, no
	// branch, no PR. It can exist when its Graph created it just before the
	// Bundle was superseded (E2E-R20). It is not a cancelled promotion, so it
	// is failed without a PromotionSuperseded record or step metrics.
	unstarted := base.Status.State == StatePending || base.Status.State == StatePendingExplicit
	msg := fmt.Sprintf("bundle %s was superseded — promotion cancelled", ps.Spec.BundleName)
	if unstarted {
		msg = fmt.Sprintf("bundle %s was superseded before this step started", ps.Spec.BundleName)
	}
	if closeErr := r.closeStepPR(ctx, ps, "bundle "+ps.Spec.BundleName+" was superseded by a newer Bundle"); closeErr != nil {
		if ps.Status.RetryCount < maxStepRetries {
			ps.Status.RetryCount++
			ps.Status.Message = fmt.Sprintf("bundle %s was superseded; closing its PR failed, retrying (%d/%d): %v",
				ps.Spec.BundleName, ps.Status.RetryCount, maxStepRetries, closeErr)
			if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("patch supersession retry: %w", err)
			}
			return ctrl.Result{RequeueAfter: retryDelay(ps.Status.RetryCount)}, nil
		}
		msg += fmt.Sprintf("; closing its PR failed after %d retries (%v) — close it by hand", maxStepRetries, closeErr)
	}
	if unstarted {
		return ctrl.Result{}, r.cancelUnstarted(ctx, base, ps, msg)
	}
	if err := r.transitionAudit(ctx, base, ps, StateFailed, msg, AuditActionPromotionSuperseded); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// closeStepPR closes the PR this step opened, if it is still open, and leaves
// a comment with reason. The PR is found through the PRStatus spec, falling
// back to the step outputs when the PRStatus was never filled in (a crash
// between opening the PR and patching the PRStatus). A step that never opened
// a PR returns nil. Only the close can fail; the comment is best-effort.
func (r *Reconciler) closeStepPR(ctx context.Context, ps *v1alpha1.PromotionStep, reason string) error {
	repo, num := "", 0
	if ps.Spec.PRStatusRef != "" {
		var prs v1alpha1.PRStatus
		err := r.Get(ctx, types.NamespacedName{Name: ps.Spec.PRStatusRef, Namespace: ps.Namespace}, &prs)
		switch {
		case err == nil:
			if prs.Status.Merged || prstatus.IsClosed(&prs.Status) {
				return nil // merged or already closed: nothing to close
			}
			repo, num = prs.Spec.Repo, prs.Spec.PRNumber
		case !apierrors.IsNotFound(err):
			return fmt.Errorf("get prstatus %s: %w", ps.Spec.PRStatusRef, err)
		}
	}
	if num == 0 {
		prURL := ps.Status.Outputs["prURL"]
		if prURL == "" {
			prURL = ps.Status.PRURL
		}
		if n, err := strconv.Atoi(ps.Status.Outputs["prNumber"]); err == nil {
			num = n
		} else {
			num = extractPRNumber(prURL)
		}
		repo = extractRepo(prURL)
	}
	if num <= 0 {
		return nil
	}
	if r.SCM == nil {
		return fmt.Errorf("no SCM provider configured to close PR #%d", num)
	}
	if err := r.SCM.ClosePR(ctx, repo, num); err != nil {
		return fmt.Errorf("close PR #%d: %w", num, err)
	}
	log := zerolog.Ctx(ctx)
	log.Info().Int("pr", num).Str("step", ps.Name).Msg("closed PR of cancelled step")
	body := fmt.Sprintf("kardinal closed this PR: %s. Merging it would change environment %s "+
		"without a PromotionStep tracking it.", reason, ps.Spec.Environment)
	if err := r.SCM.CommentOnPR(ctx, repo, num, body); err != nil {
		log.Warn().Err(err).Int("pr", num).Msg("could not comment on the closed PR (non-fatal)")
	}
	return nil
}

// retryDelay is the backoff before retry n (1-based) of a transient failure.
func retryDelay(n int) time.Duration {
	d := retryBaseDelay
	for i := 1; i < n && d < retryMaxDelay; i++ {
		d *= 2
	}
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	return d
}

// handlePending initializes the step sequence and transitions to Promoting.
// Before transitioning, it re-checks every PolicyGate in spec.requiredGates
// (checkRequiredGates): each must exist, be ready, and have been evaluated at
// or after the step was created. Otherwise the step stays in Pending and
// requeues; nothing is pushed and no PR is opened.
func (r *Reconciler) handlePending(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	base := ps.DeepCopy()
	pipeline, err := r.loadPipeline(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load pipeline: %w", err)
	}
	if held, res, holdErr := r.holdIfPaused(ctx, log, ps); held {
		return res, holdErr
	}
	if msg := unsupportedConfig(pipeline, findEnv(pipeline, ps.Spec.Environment), ps); msg != "" {
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
	}

	// Re-check every required gate before any git or Argo CD write (#1300,
	// #1323). The Graph created this step when every gate was ready, but that
	// result can be older than the step, and a gate can turn false before the
	// step starts. The PolicyGate reconciler re-evaluates a new step's gates
	// at once (it watches PromotionStep creates), and its status write wakes
	// this step (policyGateMapper). spec.when has no effect.
	if len(ps.Spec.RequiredGates) > 0 {
		msg, checkErr := r.checkRequiredGates(ctx, ps)
		if checkErr != nil {
			log.Warn().Err(checkErr).Msg("failed to check required gates — will retry")
			return ctrl.Result{RequeueAfter: requeueGateWait}, nil
		}
		if msg != "" {
			log.Info().
				Str("env", ps.Spec.Environment).
				Str("reason", msg).
				Msg("required gate holds the step — step stays in Pending")
			// Update message for visibility but do NOT change state.
			if ps.Status.Message != msg {
				ps.Status.Message = msg
				if patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base)); patchErr != nil {
					log.Warn().Err(patchErr).Msg("failed to patch gate wait message (non-fatal)")
				}
			}
			return ctrl.Result{RequeueAfter: requeueGateWait}, nil
		}
	}

	env := findEnv(pipeline, ps.Spec.Environment)
	approvalMode := env.Approval
	if approvalMode == "" {
		approvalMode = "auto"
	}

	// Load bundle to determine type (image vs config) for step sequence routing.
	bundle, bundleErr := r.loadBundle(ctx, ps)
	bundleType := ""
	updateStrategy := env.Update.Strategy
	if bundleErr != nil {
		log.Warn().Err(bundleErr).Msg("could not load bundle for sequence routing; using default kustomize sequence")
	} else if bundle != nil {
		bundleType = bundle.Spec.Type
	}

	seq := steps.DefaultSequenceForBundle(approvalMode, bundleType, updateStrategy, env.Layout)
	log.Info().
		Str("env", ps.Spec.Environment).
		Str("approval", approvalMode).
		Strs("steps", seq).
		Msg("initializing step sequence")

	ps.Status.CurrentStepIndex = 0
	// Initialize per-step status: all steps start as Pending.
	ps.Status.Steps = initStepStatuses(seq)
	// Persist workDir to status (ST-7/ST-8/ST-9 short-term mitigation):
	// a restarted controller reads this field instead of recomputing the path,
	// enabling crash-recovery without re-cloning.
	ps.Status.WorkDir = r.workDir(ps)
	if err := r.transition(ctx, base, ps, StatePromoting, fmt.Sprintf("initialized with %d steps", len(seq))); err != nil {
		return ctrl.Result{}, fmt.Errorf("pending→promoting: %w", err)
	}
	return ctrl.Result{Requeue: true}, nil
}

// handlePromoting runs the step engine from the current index.
func (r *Reconciler) handlePromoting(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	base := ps.DeepCopy()
	pipeline, err := r.loadPipeline(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load pipeline: %w", err)
	}
	if held, res, holdErr := r.holdIfPaused(ctx, log, ps); held {
		return res, holdErr
	}
	bundle, err := r.loadBundle(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load bundle: %w", err)
	}
	env := findEnv(pipeline, ps.Spec.Environment)
	if msg := unsupportedConfig(pipeline, env, ps); msg != "" {
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
	}
	approvalMode := env.Approval
	if approvalMode == "" {
		approvalMode = "auto"
	}
	updateStrategy := env.Update.Strategy
	bundleType := ""
	if bundle != nil {
		bundleType = bundle.Spec.Type
	}
	seq := steps.DefaultSequenceForBundle(approvalMode, bundleType, updateStrategy, env.Layout)
	eng := steps.NewEngine(seq)

	// The working directory is always recomputed from the PromotionStep's
	// identity, never read back from status, so a status write cannot point
	// the step engine (or cleanWorkDir) at another checkout.
	workDir := r.workDir(ps)

	token := ""
	// Resolve git token from Pipeline.spec.git.secretRef if configured.
	if secretRef := pipeline.Spec.Git.SecretRef; secretRef != nil && secretRef.Name != "" {
		// Always the Pipeline's own namespace: unsupportedConfig has already
		// refused any other secretRef.namespace (C03-promotionstep-18).
		ns := pipeline.Namespace
		var secret corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Name: secretRef.Name, Namespace: ns}, &secret); err != nil {
			log.Warn().Err(err).Str("secret", secretRef.Name).Msg("failed to read git secret — git operations may fail")
		} else {
			// A token pasted with a trailing newline breaks every git and
			// SCM call (C06-scm-health-27).
			if t := strings.TrimSpace(string(secret.Data["token"])); t != "" {
				token = t
			}
		}
	}

	state := &steps.StepState{
		Pipeline:     pipeline.Spec,
		PipelineName: ps.Spec.PipelineName,
		Environment:  env,
		Bundle:       bundle.Spec,
		BundleName:   ps.Spec.BundleName,
		WorkDir:      workDir,
		Outputs:      cloneMap(ps.Status.Outputs),
		Git: steps.GitConfig{
			URL:         pipeline.Spec.Git.URL,
			Branch:      pipeline.Spec.Git.Branch,
			Token:       token,
			AuthorName:  "kardinal-promoter",
			AuthorEmail: "kardinal@kardinal.io",
		},
		SCM:                  r.SCM,
		GitClient:            r.GitClient,
		K8sClient:            r.Client,
		StepTimeoutSeconds:   env.StepTimeoutSeconds,
		GateResults:          r.collectGateResults(ctx, log, ps),
		UpstreamEnvironments: upstreamEnvironments(bundle, ps.Spec.Environment),
	}
	r.setRollbackState(ctx, log, state, bundle)

	prevIdx := ps.Status.CurrentStepIndex
	nextIdx, result, execErr := eng.ExecuteFrom(ctx, state, prevIdx)

	// Persist outputs regardless of result, so a PR opened in this reconcile is
	// never forgotten (C03-promotionstep-06).
	ps.Status.Outputs = state.Outputs
	ps.Status.CurrentStepIndex = nextIdx
	if nextIdx > prevIdx {
		ps.Status.RetryCount = 0 // progress resets the retry budget
	}
	if prURL := state.Outputs["prURL"]; prURL != "" {
		ps.Status.PRURL = prURL
	}

	if execErr != nil {
		return r.handleStepError(ctx, log, base, ps, eng.StepNames(), eng.Timings(), execErr)
	}
	updateStepStatuses(ps, eng.StepNames(), nextIdx, false, "", eng.Timings())

	switch result.Status {
	case steps.StepPending:
		if prURL := state.Outputs["prURL"]; prURL != "" {
			// The open-pr step (or similar) has opened a PR and is waiting for merge.
			// Transition to WaitingForMerge so the PRStatusReconciler can take over.
			if err := r.transition(ctx, base, ps, StateWaitingForMerge, result.Message); err != nil {
				return ctrl.Result{}, err
			}
			// Fill in the PRStatus spec so the PRStatusReconciler can poll it.
			// handleWaitingForMerge retries this when it fails here.
			if ps.Spec.PRStatusRef != "" {
				if prErr := r.patchPRStatusSpec(ctx, ps, state.Outputs); prErr != nil {
					log.Warn().Err(prErr).Msg("failed to patch PRStatus spec (non-fatal)")
				}
			}
			return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
		}

		// No prURL — this is a non-blocking retry (e.g. an SCM call to retry later).
		// Stay in Promoting state; use the step's requested RequeueAfter duration if set.
		ps.Status.Message = result.Message
		if patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base)); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch promoting retry: %w", patchErr)
		}
		requeue := result.RequeueAfter
		if requeue == 0 {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{RequeueAfter: requeue}, nil

	case steps.StepSuccess:
		if nextIdx >= len(seq) {
			// All steps completed — move to HealthChecking. Record the commit
			// the health check must see deployed (E2E-01).
			r.recordPushedCommit(ctx, log, ps, pipeline, env, workDir)
			if ps.Spec.PRStatusRef != "" && state.Outputs["prURL"] != "" {
				if prErr := r.patchPRStatusSpec(ctx, ps, state.Outputs); prErr != nil {
					log.Warn().Err(prErr).Msg("failed to patch PRStatus spec (non-fatal)")
				}
			}
			if err := r.transition(ctx, base, ps, StateHealthChecking, "all steps complete, running health check"); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		// More steps remain — persist index and requeue immediately.
		ps.Status.Message = fmt.Sprintf("completed step %d/%d", nextIdx, len(seq))
		if patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base)); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch step progress: %w", patchErr)
		}
		return ctrl.Result{Requeue: true}, nil

	default:
		// ExecuteFrom reports StepFailed with an error, so this is unreachable
		// unless a step returns an unknown status.
		return r.handleStepError(ctx, log, base, ps, eng.StepNames(), eng.Timings(),
			fmt.Errorf("step %d returned status %q: %s", nextIdx, result.Status, result.Message))
	}
}

// handleStepError decides what a step engine error means for the PromotionStep.
//
// The engine wraps an error returned by a step (network, API or git failures)
// with %w and reports a step that returned StepFailed on its own with a plain
// message. The first kind is retried with exponential backoff up to
// maxStepRetries times, unless the step marked it with steps.Permanent; the
// second, a permanent error, and a retried error once the retries are used up
// fail the step (C03-promotionstep-06). A failed step closes the PR it opened,
// so a later merge cannot deliver a change whose step is Failed.
func (r *Reconciler) handleStepError(ctx context.Context, log zerolog.Logger, base, ps *v1alpha1.PromotionStep,
	stepNames []string, timings map[int]steps.StepTiming, execErr error) (ctrl.Result, error) {
	retryable := errors.Unwrap(execErr) != nil && !errors.Is(execErr, steps.ErrPermanent)
	idx := ps.Status.CurrentStepIndex
	if retryable && ps.Status.RetryCount < maxStepRetries {
		ps.Status.RetryCount++
		delay := retryDelay(ps.Status.RetryCount)
		ps.Status.Message = fmt.Sprintf("retrying in %s (%d/%d) after error: %v",
			delay, ps.Status.RetryCount, maxStepRetries, execErr)
		updateStepStatuses(ps, stepNames, idx, false, "", timings)
		log.Warn().Err(execErr).Str("env", ps.Spec.Environment).
			Int("retry", ps.Status.RetryCount).Dur("delay", delay).Msg("step failed, will retry")
		if patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base)); patchErr != nil {
			if apierrors.IsNotFound(patchErr) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, fmt.Errorf("patch step retry: %w", patchErr)
		}
		return ctrl.Result{RequeueAfter: delay}, nil
	}

	msg := execErr.Error()
	if retryable {
		msg = fmt.Sprintf("%s (gave up after %d retries)", msg, maxStepRetries)
	}
	log.Error().Err(execErr).Str("env", ps.Spec.Environment).Msg("step engine failed")
	updateStepStatuses(ps, stepNames, idx, true, msg, timings)
	if closeErr := r.closeStepPR(ctx, ps, "the promotion failed: "+msg); closeErr != nil {
		msg += fmt.Sprintf("; closing the PR it opened failed (%v) — close it by hand", closeErr)
	}
	return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
}

// recordPushedCommit stores, as outputs.commitSHA, the commit the health check
// must find deployed (E2E-01). It is known here only when the step pushed
// straight to the branch the GitOps tool tracks; for pr-review the merge
// commit comes from the PRStatus instead.
func (r *Reconciler) recordPushedCommit(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep,
	pipeline *v1alpha1.Pipeline, env v1alpha1.EnvironmentSpec, workDir string) {
	if env.Approval == "pr-review" {
		return
	}
	target := pipeline.Spec.Git.Branch
	if target == "" {
		target = "main"
	}
	if pushed := ps.Status.Outputs["branch"]; pushed == "" || pushed != target {
		return
	}
	hr, ok := r.GitClient.(scm.HeadCommitReader)
	if !ok {
		return
	}
	sha, err := hr.HeadCommit(ctx, workDir)
	if err != nil || sha == "" {
		log.Warn().Err(err).Msg("could not read the pushed commit; health will check images only")
		return
	}
	if ps.Status.Outputs == nil {
		ps.Status.Outputs = map[string]string{}
	}
	ps.Status.Outputs["commitSHA"] = sha
}

// handleWaitingForMerge checks the PRStatus CRD (written by PRStatusReconciler)
// instead of polling GitHub directly. This eliminates the PS-4 logic leak.
//
// Architecture: the open-pr step created a PRStatus CR and set spec.prURL/prNumber/repo.
// The PRStatusReconciler polls GitHub and writes status.merged/open.
// This reconciler simply reads the CRD status — no GitHub API call here.
func (r *Reconciler) handleWaitingForMerge(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	base := ps.DeepCopy()
	pipeline, err := r.loadPipeline(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load pipeline: %w", err)
	}

	// Apply WaitForMerge timeout if configured (#905).
	// Graph-purity: same pattern as HealthCheckExpiry — time.Now() called only when
	// writing the expiry to CRD status; the subsequent comparison reads the stored value.
	env := findEnv(pipeline, ps.Spec.Environment)
	if env.WaitForMergeTimeout != "" {
		if d, err := time.ParseDuration(env.WaitForMergeTimeout); err == nil && d > 0 {
			// Set expiry once on first entry into WaitingForMerge (idempotent).
			if ps.Status.WaitForMergeExpiry == nil {
				expiry := metav1.NewTime(time.Now().Add(d))
				ps.Status.WaitForMergeExpiry = &expiry
				if patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base)); patchErr != nil {
					return ctrl.Result{}, fmt.Errorf("patch wait-for-merge expiry: %w", patchErr)
				}
				base = ps.DeepCopy()
				log.Info().
					Str("environment", ps.Spec.Environment).
					Dur("timeout", d).
					Time("expiry", expiry.Time).
					Msg("WaitForMerge timeout set")
			}
			// Check if timeout has elapsed.
			if time.Now().After(ps.Status.WaitForMergeExpiry.Time) {
				log.Warn().
					Time("expiry", ps.Status.WaitForMergeExpiry.Time).
					Dur("timeout", d).
					Msg("wait-for-merge timeout exceeded — failing step")
				msg := fmt.Sprintf("wait-for-merge timeout after %s: PR was not merged within the configured deadline", d)
				// Close the PR so a late merge cannot deliver a change whose
				// step already failed (C03-promotionstep-22).
				if closeErr := r.closeStepPR(ctx, ps, fmt.Sprintf("it was not merged within waitForMergeTimeout (%s)", d)); closeErr != nil {
					msg += fmt.Sprintf("; closing the PR failed (%v) — close it by hand", closeErr)
				}
				ps.Status.WaitForMergeExpiry = nil
				return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
			}
		}
	}

	prStatusName := ps.Spec.PRStatusRef
	if prStatusName == "" {
		// The Graph builder always sets spec.prStatusRef; without it nothing
		// reports the merge (C03-promotionstep-30).
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed,
			"spec.prStatusRef is empty: no PRStatus reports whether the PR merged; recreate the Bundle")
	}

	var prs v1alpha1.PRStatus
	if err := r.Get(ctx, types.NamespacedName{Name: prStatusName, Namespace: ps.Namespace}, &prs); err != nil {
		if apierrors.IsNotFound(err) {
			// PRStatus not yet created by the Graph — requeue.
			log.Debug().Str("prStatusRef", prStatusName).Msg("PRStatus not found yet, requeueing")
			return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get prstatus %s: %w", prStatusName, err)
	}

	// The spec patch in handlePromoting is best-effort; until it lands the
	// PRStatusReconciler has no PR to poll and the step would wait forever
	// (C03-promotionstep-07).
	if prs.Spec.PRNumber == 0 && ps.Status.Outputs["prURL"] != "" {
		if prErr := r.patchPRStatusSpec(ctx, ps, ps.Status.Outputs); prErr != nil {
			log.Warn().Err(prErr).Msg("failed to patch PRStatus spec, will retry")
		}
		return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
	}

	if prs.Status.Merged {
		log.Info().
			Str("prStatusRef", prStatusName).
			Int("prNumber", prs.Spec.PRNumber).
			Msg("PRStatus reports merged — advancing to HealthChecking")
		if prs.Status.MergeCommitSHA != "" {
			// The revision the health check must find deployed (E2E-01).
			if ps.Status.Outputs == nil {
				ps.Status.Outputs = map[string]string{}
			}
			ps.Status.Outputs["mergeCommitSHA"] = prs.Status.MergeCommitSHA
		}
		ps.Status.WaitForMergeExpiry = nil // clear expiry on successful transition
		if err := r.transition(ctx, base, ps, StateHealthChecking,
			fmt.Sprintf("PR #%d merged", prs.Spec.PRNumber)); err != nil {
			return ctrl.Result{}, err
		}
		// Emit PR duration histogram: time from PRStatus creation (PR opened) to now (PR merged).
		if prDuration := time.Since(prs.CreationTimestamp.Time).Seconds(); prDuration > 0 {
			observability.PRDurationSeconds.Observe(prDuration)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// The PR stayed closed through the PRStatus grace window (#1306), or an
	// older release recorded it closed without one.
	if prstatus.IsClosedFinal(&prs.Status) {
		log.Info().
			Str("prStatusRef", prStatusName).
			Int("prNumber", prs.Spec.PRNumber).
			Msg("PRStatus reports PR closed without merge — failing")
		msg := fmt.Sprintf("PR #%d was closed without merging", prs.Spec.PRNumber)
		if prs.Status.ClosedFinal {
			msg += fmt.Sprintf(" and not reopened within %s", prstatus.ClosedGracePeriod)
		}
		ps.Status.WaitForMergeExpiry = nil // clear expiry on transition out
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
	}

	// The PRStatusReconciler got an SCM error that polling again cannot fix
	// (401, 403 that is not a rate limit, 404, 410). Fail now, as the
	// wait-for-merge step does, instead of waiting for a merge that will never
	// be seen. The PR is not closed: the same token or repository would fail.
	if prs.Status.PollError != "" {
		log.Warn().
			Str("prStatusRef", prStatusName).
			Int("prNumber", prs.Spec.PRNumber).
			Str("pollError", prs.Status.PollError).
			Msg("PRStatus cannot poll the PR — failing")
		ps.Status.WaitForMergeExpiry = nil
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed,
			fmt.Sprintf("PR #%d cannot be polled: %s", prs.Spec.PRNumber, prs.Status.PollError))
	}

	// Closed but still in the grace window: the PRStatus reconciler keeps
	// polling, and a reopen resumes the wait. Only the message changes, and
	// only when it differs, since each patch is a watch event.
	closedMsg := fmt.Sprintf("PR #%d is closed; the step fails %s after closing unless it is reopened",
		prs.Spec.PRNumber, prstatus.ClosedGracePeriod)
	msg := ps.Status.Message
	switch {
	case prstatus.IsClosed(&prs.Status):
		msg = closedMsg
	case msg == closedMsg:
		msg = fmt.Sprintf("PR #%d is open, waiting for merge", prs.Spec.PRNumber)
	}
	if msg != ps.Status.Message {
		ps.Status.Message = msg
		if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch wait-for-merge message: %w", err)
		}
	}

	// PR is still open or PRStatus reconciler hasn't polled yet — requeue.
	return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
}

// handleHealthChecking verifies that the environment runs the promoted
// revision and is healthy, using the adapter the environment selects.
//
// The adapter type and the object it checks come from the Pipeline through
// health.OptionsForEnv, which the translator also uses for the Graph health
// ref nodes (C03-promotionstep-04, -19). The expected revision is the pushed
// or merged commit (expectedRevision) and the expected images are the Bundle
// images (C03-promotionstep-11, E2E-01). A flux check of a pr-review step
// that opened a PR is Progressing until the merge commit is known (#1307).
//
// health.timeout bounds the time until the first Healthy result. Reaching it
// is a health failure: it counts in status.consecutiveHealthFailures and
// applies onHealthFailure (Failed, AbortedByAlarm or a rollback Bundle), as a
// terminal result does. A crash-looping new image keeps a Deployment rolling
// out (Progressing) until its progressDeadlineSeconds, so without this the
// step would fail with no rollback. While a bake window runs the timeout does
// not apply, so a bake longer than the timeout can complete
// (C03-promotionstep-03). When the window stops (see handleBake) the timeout
// starts again from that moment.
//
// Health checks are spaced at least requeueHealthCheck apart, whatever the
// reconcile rate, and only Unhealthy or Terminal results (not Progressing
// ones) count toward status.consecutiveHealthFailures (C03-promotionstep-12).
func (r *Reconciler) handleHealthChecking(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	base := ps.DeepCopy()
	pipeline, err := r.loadPipeline(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load pipeline: %w", err)
	}
	bundle, err := r.loadBundle(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load bundle: %w", err)
	}
	env := findEnv(pipeline, ps.Spec.Environment)
	if msg := unsupportedConfig(pipeline, env, ps); msg != "" {
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
	}

	if r.HealthDetector == nil {
		// Both binaries always set HealthDetector; only unit tests of the
		// earlier phases run without one.
		return ctrl.Result{}, r.verify(ctx, base, ps, "Verified", "health check skipped: no health adapter configured")
	}

	// Parse timeout from environment config; default 10m.
	timeout := 10 * time.Minute
	if env.Health.Timeout != "" {
		if d, err := time.ParseDuration(env.Health.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}

	// Set status.healthCheckExpiry on first entry (idempotent). handleBake
	// moves it when a bake window stops.
	// This writes time-based state to the CRD so the Graph can observe it.
	// Graph-purity: eliminates PS-5 (time.Since() in reconciler hot path).
	if ps.Status.HealthCheckExpiry == nil {
		expiry := metav1.NewTime(time.Now().Add(timeout))
		ps.Status.HealthCheckExpiry = &expiry
		if patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base)); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch health check expiry: %w", patchErr)
		}
		base = ps.DeepCopy()
	}

	// Time-to-healthy timeout, compared against the stored expiry. It does not
	// apply while a bake window runs (the environment is healthy).
	// Never becoming healthy is a health failure: count it and apply
	// onHealthFailure.
	if ps.Status.BakeStartedAt == nil && time.Now().After(ps.Status.HealthCheckExpiry.Time) {
		log.Warn().
			Time("expiry", ps.Status.HealthCheckExpiry.Time).
			Dur("timeout", timeout).
			Str("onHealthFailure", env.OnHealthFailure).
			Msg("health check timeout")
		msg := fmt.Sprintf("health check timeout after %s", timeout)
		if last := ps.Status.Message; last != "" {
			msg += "; last result: " + last
		}
		ps.Status.ConsecutiveHealthFailures++
		return r.applyHealthFailurePolicy(ctx, log, base, ps, env, health.EffectiveType(env), msg)
	}

	// Space checks: a status patch re-enqueues the step at once, so without
	// this the adapter ran (and the failure counter climbed) at API speed.
	if last := ps.Status.LastHealthCheckAt; last != nil {
		if wait := requeueHealthCheck - time.Since(last.Time); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}

	opts := health.OptionsForEnv(pipeline.Name, env)
	opts.Timeout = timeout
	opts.ExpectedRevision = r.expectedRevision(ctx, ps)
	for _, img := range bundle.Spec.Images {
		opts.ExpectedImages = append(opts.ExpectedImages,
			health.ImageExpectation{Repository: img.Repository, Tag: img.Tag, Digest: img.Digest})
	}
	opts.Since = healthCheckStart(ps)
	if at := ps.Status.TargetUpdatedAt; at != nil {
		opts.TargetUpdatedAt = at.Time
	}

	adapter, err := r.HealthDetector.Select(ctx, opts.Type)
	if err != nil {
		// Only an unknown type reaches here; admission rejects it.
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, err.Error())
	}

	var result health.HealthStatus
	var checkErr error
	if adapter.Name() == "flux" && env.Approval == "pr-review" && opts.ExpectedRevision == "" &&
		ps.Status.Outputs["noChanges"] != "true" {
		// The flux adapter has no image check to fall back on: without the
		// merge commit, a Kustomization Ready on the previous commit would
		// pass. Wait for it; health.timeout ends the wait (#1307). With no
		// changes there is no PR and no merge commit, and the previous
		// commit already is the target.
		result = health.HealthStatus{Progressing: true,
			Reason: "merge commit of the PR not known yet (needed to check lastAppliedRevision)"}
	} else {
		result, checkErr = adapter.Check(ctx, opts)
	}
	if checkErr != nil {
		log.Error().Err(checkErr).Str("adapter", adapter.Name()).Msg("health adapter check error")
		return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil
	}
	checkedAt := metav1.NewTime(time.Now())
	ps.Status.LastHealthCheckAt = &checkedAt
	// The first check that finds the target on the Bundle images dates the
	// update: the flagger check counts a Failed phase only when Flagger set it
	// later. Every path below patches the status.
	if result.TargetUpdated && ps.Status.TargetUpdatedAt == nil {
		ps.Status.TargetUpdatedAt = &checkedAt
	}

	// A terminal result (ProgressDeadlineExceeded, Flagger canary Failed) will
	// not recover, bake or not.
	if result.Terminal {
		ps.Status.ConsecutiveHealthFailures++
		return r.applyHealthFailurePolicy(ctx, log, base, ps, env, adapter.Name(), result.Reason)
	}

	// K-01: Contiguous soak / bake tracking.
	// When env.Bake is configured, health must be healthy for Bake.Minutes
	// contiguously before transitioning to Verified.
	if env.Bake != nil {
		return r.handleBake(ctx, log, base, ps, env, result, adapter.Name(), timeout)
	}

	switch {
	case result.Healthy:
		log.Info().Str("env", ps.Spec.Environment).Str("adapter", adapter.Name()).Msg("health check passed, Verified")
		ps.Status.ConsecutiveHealthFailures = 0 // reset on success
		return ctrl.Result{}, r.verify(ctx, base, ps, "Verified",
			fmt.Sprintf("health check passed via %s: %s", adapter.Name(), result.Reason))
	case result.Progressing:
		// Rolling out or not synced yet: not a health failure.
		ps.Status.Message = fmt.Sprintf("waiting for %s: %s", adapter.Name(), result.Reason)
	default:
		// Unhealthy. The RollbackPolicyReconciler watches
		// status.consecutiveHealthFailures and decides on auto-rollback.
		ps.Status.ConsecutiveHealthFailures++
		ps.Status.Message = fmt.Sprintf("unhealthy via %s: %s", adapter.Name(), result.Reason)
	}
	log.Debug().Str("reason", result.Reason).Str("adapter", adapter.Name()).Msg("health check not yet passed, requeueing")
	if patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base)); patchErr != nil {
		return ctrl.Result{}, fmt.Errorf("patch health result: %w", patchErr)
	}
	return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil
}

// expectedRevision returns the git commit the health check must find
// deployed: the commit pushed straight to the tracked branch, or the PR merge
// commit. "" means it is not known, and the adapters fall back to checking
// the Bundle images.
func (r *Reconciler) expectedRevision(ctx context.Context, ps *v1alpha1.PromotionStep) string {
	if sha := ps.Status.Outputs["commitSHA"]; sha != "" {
		return sha
	}
	if sha := ps.Status.Outputs["mergeCommitSHA"]; sha != "" {
		return sha
	}
	if ps.Spec.PRStatusRef == "" {
		return ""
	}
	// The PRStatusReconciler may record the merge commit shortly after the merge.
	var prs v1alpha1.PRStatus
	if err := r.Get(ctx, types.NamespacedName{Name: ps.Spec.PRStatusRef, Namespace: ps.Namespace}, &prs); err != nil {
		return ""
	}
	if prs.Status.Merged {
		return prs.Status.MergeCommitSHA
	}
	return ""
}

// verify moves ps to Verified with a Verified condition.
func (r *Reconciler) verify(ctx context.Context, base, ps *v1alpha1.PromotionStep, reason, message string) error {
	ps.Status.Conditions = appendCondition(ps.Status.Conditions, "Verified", metav1.ConditionTrue,
		reason, "promotion complete", time.Now().UTC())
	return r.transition(ctx, base, ps, StateVerified, message)
}

// applyHealthFailurePolicy dispatches the onHealthFailure action (K-03).
// Called for a terminal health result, when health.timeout passes before the
// first Healthy result, and from handleBake when the bake policy is
// fail-on-alarm.
//
// "rollback": creates a rollback Bundle to the Bundle verified before this one
// and transitions step to RollingBack; AbortedByAlarm when there is none.
// "abort":    transitions step to AbortedByAlarm (human intervention required).
// "none":     transitions step to Failed (existing behavior).
//
// Graph-first: creates a new Bundle CRD (its own resource). Does not mutate
// any other CRD's status.
func (r *Reconciler) applyHealthFailurePolicy(
	ctx context.Context,
	log zerolog.Logger,
	base, ps *v1alpha1.PromotionStep,
	env v1alpha1.EnvironmentSpec,
	adapterName, reason string,
) (ctrl.Result, error) {
	switch env.OnHealthFailure {
	case "abort":
		log.Info().Str("env", ps.Spec.Environment).Msg("health failure: AbortedByAlarm")
		return ctrl.Result{}, r.transition(ctx, base, ps, StateAbortedByAlarm, fmt.Sprintf(
			"health alarm via %s (onHealthFailure=abort): %s — human intervention required",
			adapterName, reason))

	case "rollback":
		// A rollback Bundle whose health check fails is not rolled back in
		// turn: that would chain rollback Bundles, one per health.timeout.
		var bundle v1alpha1.Bundle
		getErr := r.Get(ctx, types.NamespacedName{Name: ps.Spec.BundleName, Namespace: ps.Namespace}, &bundle)
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			return ctrl.Result{}, fmt.Errorf("get bundle %s: %w", ps.Spec.BundleName, getErr)
		}
		if getErr == nil && isRollbackBundle(&bundle) {
			log.Info().Str("env", ps.Spec.Environment).Msg("health failure of a rollback Bundle: AbortedByAlarm")
			return ctrl.Result{}, r.transition(ctx, base, ps, StateAbortedByAlarm, fmt.Sprintf(
				"health alarm via %s (onHealthFailure=rollback): %s — Bundle %s is a rollback and is not rolled back again; human intervention required",
				adapterName, reason, ps.Spec.BundleName))
		}
		// Roll the environment back to the Bundle verified before the failing
		// one (createAutoRollback, the planner the CLI and UI use). The
		// rollback Bundle sets intent.targetEnvironment to this environment,
		// and its Graph keeps every environment upstream of it, so the old
		// artifacts are promoted through those first, with their gates and
		// soaks. Environments that are not upstream are not touched.
		// RollingBack is terminal for this step: the rollback Bundle carries
		// the environment from here. A refusal from the planner (nothing safe
		// to roll back to) stops the step for a human.
		rollbackName, refusal, rbErr := r.createAutoRollback(ctx, ps)
		if rbErr != nil {
			return ctrl.Result{}, rbErr
		}
		if refusal != nil {
			log.Warn().Err(refusal).Str("env", ps.Spec.Environment).
				Msg("health failure: nothing safe to roll back to, AbortedByAlarm")
			return ctrl.Result{}, r.transition(ctx, base, ps, StateAbortedByAlarm, fmt.Sprintf(
				"health alarm via %s (onHealthFailure=rollback): %s — no automatic rollback (%v); human intervention required",
				adapterName, reason, refusal))
		}
		log.Info().
			Str("env", ps.Spec.Environment).
			Str("rollbackBundle", rollbackName).
			Msg("health failure: rollback Bundle created, state=RollingBack")
		return ctrl.Result{}, r.transition(ctx, base, ps, StateRollingBack, fmt.Sprintf(
			"health alarm via %s (onHealthFailure=rollback): %s — rollback Bundle %s created",
			adapterName, reason, rollbackName))

	default: // "none" or unset
		log.Info().Str("env", ps.Spec.Environment).Msg("health failure: Failed")
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, fmt.Sprintf(
			"health alarm via %s (onHealthFailure=none): %s", adapterName, reason))
	}
}

// isRollbackBundle reports whether b is a rollback Bundle: one created by
// onHealthFailure=rollback, a RollbackPolicy, `kardinal rollback` or the UI.
// lifecycle.PlanRollback sets both the label and spec.provenance.rollbackOf;
// either is enough.
func isRollbackBundle(b *v1alpha1.Bundle) bool {
	return b.Labels[lifecycle.LabelRollback] == "true" || (b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != "")
}

// checkRequiredGates returns why a Pending step may not start yet, or "" when
// every gate in ps.Spec.RequiredGates allows it. A gate holds the step when
// it is missing, not ready, or its result is older than the step: nil
// status.lastEvaluatedAt, or lastEvaluatedAt before ps.CreationTimestamp.
//
// A result written at or after the step's creation was computed after the
// Graph decided to create the step, so it is fresh for this step. The check
// compares two stored times (the PolicyGate reconciler's status write and the
// API server's creationTimestamp, both with one-second precision); it does not
// read the clock. spec.when is not read: every gate is re-checked (#1323).
//
// Graph-first: reads CRD fields only — no external calls. The step writes
// only its own status.message. kro cannot hold a step that already exists
// without pruning it (docs/design/11-graph-purity-tech-debt.md, Accepted).
func (r *Reconciler) checkRequiredGates(ctx context.Context, ps *v1alpha1.PromotionStep) (string, error) {
	for _, gateName := range ps.Spec.RequiredGates {
		var gate v1alpha1.PolicyGate
		if getErr := r.Get(ctx, types.NamespacedName{Name: gateName, Namespace: ps.Namespace}, &gate); getErr != nil {
			if apierrors.IsNotFound(getErr) {
				// Gate not (yet) created by the Graph, or deleted — not ready.
				return fmt.Sprintf("waiting for gate %s", gateName), nil
			}
			return "", fmt.Errorf("get policy gate %s: %w", gateName, getErr)
		}
		if !gate.Status.Ready {
			// Name what the gate's author says it waits for, when its
			// expression is what blocks (GATE-MESSAGE-01).
			if msg := graph.BlockedMessage(&gate); msg != "" {
				return fmt.Sprintf("waiting for gate %s: %s", gateName, msg), nil
			}
			return fmt.Sprintf("waiting for gate %s", gateName), nil
		}
		if gate.Status.LastEvaluatedAt == nil || gate.Status.LastEvaluatedAt.Before(&ps.CreationTimestamp) {
			return fmt.Sprintf("waiting for gate %s to be re-evaluated", gateName), nil
		}
	}
	return "", nil
}

// healthCheckStart is when this promotion's health check started: the
// startedAt of its health-check step, which closeStepStatuses sets when the
// step enters HealthChecking. It is zero when the promotion changed nothing in
// git (the environment already had the Bundle, so an earlier status of the
// workload describes it) or the step sequence has no health-check step.
func healthCheckStart(ps *v1alpha1.PromotionStep) time.Time {
	if ps.Status.Outputs["noChanges"] == "true" {
		return time.Time{}
	}
	for _, s := range ps.Status.Steps {
		if s.Name == "health-check" && s.StartedAt != nil {
			return s.StartedAt.Time
		}
	}
	return time.Time{}
}

// handleBake implements the K-01 contiguous-healthy soak window.
//
// When env.Bake is configured, the step must be healthy for Bake.Minutes
// contiguously before transitioning to Verified. The window starts at the
// first Healthy result; until then a result that is not Healthy is just the
// rollout still converging, bounded by health.timeout.
//
// Once the window runs:
//   - A Waiting (Progressing) result, such as a canary paused at a step or a
//     spec not observed yet, stops the window without an alarm: it is not a
//     health failure, and it neither resets nor fails the step.
//   - An Unhealthy result is an alarm. policy=fail-on-alarm applies
//     onHealthFailure. policy=reset-on-alarm stops the window and increments
//     BakeResets.
//
// A stopped window starts again at the next Healthy result, and health.timeout
// starts again when the window stops (HealthCheckExpiry = now + timeout): a
// step that is not healthy again within the timeout applies onHealthFailure,
// so reset-on-alarm on a broken release ends.
//
// All time values are written to CRD status fields — Graph-first compliant.
func (r *Reconciler) handleBake(
	ctx context.Context,
	log zerolog.Logger,
	base, ps *v1alpha1.PromotionStep,
	env v1alpha1.EnvironmentSpec,
	result health.HealthStatus,
	adapterName string,
	timeout time.Duration,
) (ctrl.Result, error) {
	now := metav1.NewTime(time.Now().UTC())
	if !result.Progressing && !result.Healthy {
		ps.Status.ConsecutiveHealthFailures++
	}
	// stopWindow stops a running window; the time to the next Healthy result
	// is bounded by health.timeout again.
	stopWindow := func() {
		expiry := metav1.NewTime(now.Add(timeout))
		ps.Status.HealthCheckExpiry = &expiry
		ps.Status.BakeStartedAt = nil
		ps.Status.BakeElapsedMinutes = 0
	}

	switch {
	case !result.Healthy && ps.Status.BakeStartedAt == nil:
		// The bake window has not started: the environment was never healthy,
		// or not since the window stopped.
		verdict := "waiting for"
		if !result.Progressing {
			verdict = "unhealthy via"
		}
		ps.Status.Message = fmt.Sprintf("bake: waiting for the first healthy check (%s %s): %s",
			verdict, adapterName, result.Reason)
		if ps.Status.BakeResets > 0 {
			ps.Status.Message = fmt.Sprintf("bake: waiting for a healthy check to restart the window (resets=%d, %s %s): %s",
				ps.Status.BakeResets, verdict, adapterName, result.Reason)
		}

	case result.Progressing:
		// Not an alarm: the workload is changing, not failing. The window
		// needs contiguous healthy time, so it starts again.
		stopWindow()
		ps.Status.Message = fmt.Sprintf(
			"bake: window stopped, waiting for %s: %s; the %dm window restarts at the next healthy check (resets=%d)",
			adapterName, result.Reason, env.Bake.Minutes, ps.Status.BakeResets)
		log.Info().Str("env", ps.Spec.Environment).Str("reason", result.Reason).
			Msg("bake: waiting result, window stopped")

	case !result.Healthy:
		policy := env.Bake.Policy
		if policy == "" {
			policy = "reset-on-alarm"
		}
		if policy != "reset-on-alarm" {
			// fail-on-alarm: apply onHealthFailure policy (K-03).
			return r.applyHealthFailurePolicy(ctx, log, base, ps, env, adapterName, result.Reason)
		}
		stopWindow()
		ps.Status.BakeResets++
		ps.Status.Message = fmt.Sprintf(
			"bake: health alarm via %s — timer reset (resets=%d, need %dm contiguous): %s",
			adapterName, ps.Status.BakeResets, env.Bake.Minutes, result.Reason)
		log.Info().
			Str("env", ps.Spec.Environment).
			Int("bakeResets", ps.Status.BakeResets).
			Int("bakeMinutes", env.Bake.Minutes).
			Msg("bake: health alarm, timer reset")

	default:
		// Healthy. Start or advance the bake window.
		ps.Status.ConsecutiveHealthFailures = 0
		if ps.Status.BakeStartedAt == nil {
			ps.Status.BakeStartedAt = &now
			ps.Status.BakeElapsedMinutes = 0
		} else {
			// The window restarts on every alarm, so the time since it started
			// is the contiguous healthy time.
			ps.Status.BakeElapsedMinutes = int64(time.Since(ps.Status.BakeStartedAt.Time).Minutes())
		}
		if ps.Status.BakeElapsedMinutes >= int64(env.Bake.Minutes) {
			log.Info().
				Str("env", ps.Spec.Environment).
				Int64("elapsedMinutes", ps.Status.BakeElapsedMinutes).
				Int("requiredMinutes", env.Bake.Minutes).
				Msg("bake: complete, Verified")
			ps.Status.Conditions = appendCondition(ps.Status.Conditions,
				"Verified", metav1.ConditionTrue, "BakeComplete",
				fmt.Sprintf("contiguous soak %dm complete", env.Bake.Minutes), now.Time)
			return ctrl.Result{}, r.transition(ctx, base, ps, StateVerified, fmt.Sprintf(
				"bake complete: %dm contiguous healthy via %s (resets=%d)",
				env.Bake.Minutes, adapterName, ps.Status.BakeResets))
		}
		remaining := int64(env.Bake.Minutes) - ps.Status.BakeElapsedMinutes
		ps.Status.Message = fmt.Sprintf(
			"bake: %dm/%dm contiguous healthy via %s (~%dm remaining, resets=%d)",
			ps.Status.BakeElapsedMinutes, env.Bake.Minutes, adapterName,
			remaining, ps.Status.BakeResets)
	}

	if patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base)); patchErr != nil {
		return ctrl.Result{}, fmt.Errorf("patch bake progress: %w", patchErr)
	}
	return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil
}

// SetupWithManager registers the PromotionStep reconciler with controller-runtime.
//
// The PromotionStep watch reacts to spec, label or annotation changes only.
// The reconciler's own status patches no longer re-enqueue the step
// immediately; it requeues itself with RequeueAfter (C03-promotionstep-12).
//
// Additionally registers Watches on PRStatus and PolicyGate CRDs:
//   - PRStatus: re-enqueue the owning PromotionStep when status.merged changes.
//     Without this Watch, a step in WaitingForMerge would only react after
//     requeueWaitForMerge, defeating the purpose of the PRStatus CRD.
//   - PolicyGate: re-enqueue the unfinished PromotionSteps that require the
//     gate, so a Pending step starts as soon as its gates are re-evaluated
//     (checkRequiredGates).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PromotionStep{}, builderutil.WithPredicates(
			predicate.Or(predicate.GenerationChangedPredicate{},
				predicate.LabelChangedPredicate{}, predicate.AnnotationChangedPredicate{}),
		)).
		Watches(&v1alpha1.PRStatus{}, handler.EnqueueRequestsFromMapFunc(r.prStatusMapper)).
		Watches(&v1alpha1.PolicyGate{}, handler.EnqueueRequestsFromMapFunc(r.policyGateMapper)).
		Watches(&v1alpha1.Bundle{}, handler.EnqueueRequestsFromMapFunc(r.bundleMapper),
			builderutil.WithPredicates(predicate.NewPredicateFuncs(isSuperseded))).
		Complete(r)
}

// isSuperseded passes Bundle events of superseded Bundles.
func isSuperseded(obj client.Object) bool {
	b, ok := obj.(*v1alpha1.Bundle)
	return ok && b.Status.Phase == "Superseded"
}

// bundleMapper wakes the unfinished PromotionSteps of a superseded Bundle so
// the supersession guard closes their PRs at once. Without it a step in
// WaitingForMerge saw the new phase only at its next poll
// (requeueWaitForMerge), and the superseded PR stayed open, and mergeable,
// until then.
func (r *Reconciler) bundleMapper(ctx context.Context, obj client.Object) []reconcile.Request {
	var stepList v1alpha1.PromotionStepList
	if err := r.List(ctx, &stepList, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, step := range stepList.Items {
		if step.Spec.BundleName != obj.GetName() || !isCancellable(step.Status.State) {
			continue
		}
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: step.Name, Namespace: step.Namespace},
		})
	}
	return reqs
}

// prStatusMapper re-enqueues the PromotionStep that owns the changed PRStatus.
// The owning step is identified via ps.Spec.PRStatusRef == changed PRStatus name.
// This ensures the WaitingForMerge handler runs immediately when PRStatus.status.merged
// flips to true, rather than waiting for requeueWaitForMerge. (#644)
func (r *Reconciler) prStatusMapper(ctx context.Context, obj client.Object) []reconcile.Request {
	prs := obj.(*v1alpha1.PRStatus)
	var stepList v1alpha1.PromotionStepList
	if err := r.List(ctx, &stepList, client.InNamespace(prs.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, step := range stepList.Items {
		if step.Spec.PRStatusRef == prs.GetName() {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      step.Name,
					Namespace: step.Namespace,
				},
			})
		}
	}
	return reqs
}

// policyGateMapper re-enqueues the unfinished PromotionSteps whose spec.requiredGates names the changed gate. Enqueueing every step in
// the namespace on every gate evaluation made each step reconcile (and run
// its health check) once per gate tick (C03-promotionstep-26).
func (r *Reconciler) policyGateMapper(ctx context.Context, obj client.Object) []reconcile.Request {
	gate := obj.(*v1alpha1.PolicyGate)
	var stepList v1alpha1.PromotionStepList
	if err := r.List(ctx, &stepList, client.InNamespace(gate.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, step := range stepList.Items {
		if !isCancellable(step.Status.State) ||
			!slices.Contains(step.Spec.RequiredGates, gate.GetName()) {
			continue
		}
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      step.Name,
				Namespace: step.Namespace,
			},
		})
	}
	return reqs
}

// patchPRStatusSpec updates the spec of the companion PRStatus CRD with PR data
// from the open-pr step outputs. This is idempotent: if the PRStatus already has
// a PR number set, it is a no-op.
func (r *Reconciler) patchPRStatusSpec(ctx context.Context, ps *v1alpha1.PromotionStep, outputs map[string]string) error {
	var prs v1alpha1.PRStatus
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ps.Spec.PRStatusRef,
		Namespace: ps.Namespace,
	}, &prs); err != nil {
		return fmt.Errorf("get prstatus %s: %w", ps.Spec.PRStatusRef, err)
	}

	// Idempotent: already has PR data — skip.
	if prs.Spec.PRNumber > 0 {
		return nil
	}

	prURL := outputs["prURL"]
	prNumStr := outputs["prNumber"]
	if prURL == "" {
		return nil
	}

	prNum := 0
	if prNumStr != "" {
		if n, err := strconv.Atoi(prNumStr); err == nil {
			prNum = n
		}
	}
	if prNum == 0 {
		prNum = extractPRNumber(prURL)
	}
	repo := extractRepo(prURL)

	patch := client.MergeFrom(prs.DeepCopy())
	prs.Spec.PRURL = prURL
	prs.Spec.PRNumber = prNum
	prs.Spec.Repo = repo

	if err := r.Patch(ctx, &prs, patch); err != nil {
		return fmt.Errorf("patch prstatus spec %s: %w", ps.Spec.PRStatusRef, err)
	}
	return nil
}

// --- helpers ---

func (r *Reconciler) loadPipeline(ctx context.Context, ps *v1alpha1.PromotionStep) (*v1alpha1.Pipeline, error) {
	var pipeline v1alpha1.Pipeline
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ps.Spec.PipelineName,
		Namespace: ps.Namespace,
	}, &pipeline); err != nil {
		return nil, fmt.Errorf("get pipeline %s: %w", ps.Spec.PipelineName, err)
	}
	return &pipeline, nil
}

func (r *Reconciler) loadBundle(ctx context.Context, ps *v1alpha1.PromotionStep) (*v1alpha1.Bundle, error) {
	var bundle v1alpha1.Bundle
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ps.Spec.BundleName,
		Namespace: ps.Namespace,
	}, &bundle); err != nil {
		return nil, fmt.Errorf("get bundle %s: %w", ps.Spec.BundleName, err)
	}
	return &bundle, nil
}

// workDir returns the git working directory of one PromotionStep. It is keyed
// by namespace, pipeline, bundle and environment, so sibling environments of
// a Bundle and same-named Pipelines in two namespaces never share a checkout
// (C05-steps-01, C05-steps-02).
func (r *Reconciler) workDir(ps *v1alpha1.PromotionStep) string {
	if r.WorkDirFn != nil {
		return r.WorkDirFn(ps.Spec.PipelineName, ps.Spec.BundleName)
	}
	return steps.WorkDirFor(steps.DefaultWorkDirRoot,
		ps.Namespace, ps.Spec.PipelineName, ps.Spec.BundleName, ps.Spec.Environment)
}

// cleanWorkDir removes the working directory on disk when a PromotionStep reaches
// a terminal state (Verified or Failed). This ensures host-local git state does not
// accumulate across promotions (ST-7/ST-8 short-term mitigation).
// The directory is recomputed rather than read from status.workDir, so a
// status write can never make the controller delete an arbitrary path.
// The cleanup is best-effort — failure to remove the directory is logged but not fatal.
func (r *Reconciler) cleanWorkDir(log zerolog.Logger, ps *v1alpha1.PromotionStep) {
	if ps.Status.WorkDir == "" {
		// The step never started promoting, so nothing was cloned.
		return
	}
	dir := r.workDir(ps)
	for _, d := range []string{dir, steps.ConfigSourceDir(dir)} {
		if err := os.RemoveAll(d); err != nil {
			log.Warn().Err(err).Str("workDir", d).Msg("cleanWorkDir: failed to remove working directory")
		} else {
			log.Debug().Str("workDir", d).Msg("cleanWorkDir: removed working directory")
		}
	}
}

// findEnv returns the EnvironmentSpec for the named environment, or empty spec if not found.
func findEnv(pipeline *v1alpha1.Pipeline, envName string) v1alpha1.EnvironmentSpec {
	for _, e := range pipeline.Spec.Environments {
		if e.Name == envName {
			return e
		}
	}
	return v1alpha1.EnvironmentSpec{Name: envName}
}

// cloneMap returns a shallow copy of a string map (nil-safe).
func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// extractPRNumber extracts the PR number from a PR URL of any supported
// provider (see scm.ParsePRURL). It returns 0 when none is found.
func extractPRNumber(prURL string) int {
	_, n, err := scm.ParsePRURL(prURL)
	if err != nil {
		return 0
	}
	return n
}

// extractRepo returns the SCM repository identifier of a PR URL or a git
// remote URL, for any supported provider: "owner/repo", a GitLab project path
// with subgroups, or an Azure DevOps "org/project/repo" (C06-scm-health-03).
// It returns "" when the URL has no repository path.
func extractRepo(rawURL string) string {
	if repo, _, err := scm.ParsePRURL(rawURL); err == nil {
		return repo
	}
	repo, err := scm.RepoFromURL(rawURL)
	if err != nil {
		return ""
	}
	return repo
}

// appendCondition appends or updates a metav1.Condition.
func appendCondition(conditions []metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string, t time.Time) []metav1.Condition {
	now := metav1.NewTime(t)
	for i, c := range conditions {
		if c.Type == condType {
			conditions[i].Status = status
			conditions[i].Reason = reason
			conditions[i].Message = message
			conditions[i].LastTransitionTime = now
			return conditions
		}
	}
	return append(conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

// initStepStatuses returns a slice of StepStatus with every step in Pending state.
// Called when the PromotionStep transitions from Pending → Promoting.
func initStepStatuses(seq []string) []v1alpha1.StepStatus {
	ss := make([]v1alpha1.StepStatus, len(seq))
	for i, name := range seq {
		ss[i] = v1alpha1.StepStatus{
			Name:  name,
			State: v1alpha1.StepExecutionPending,
		}
	}
	return ss
}

// updateStepStatuses updates ps.Status.Steps to reflect the result of the most
// recent ExecuteFrom call.
//
// Contract:
//   - stepNames is the full ordered step sequence (len == len(ps.Status.Steps) or 0).
//   - currentIdx is the index returned by ExecuteFrom (next step to execute on
//     the next reconcile, or len(stepNames) if all steps completed successfully).
//   - failed == true means ExecuteFrom returned an error at currentIdx.
//   - timings is Engine.Timings() of that call: when each step it executed
//     started and returned. A step without a timing gets time.Now().
//
// A step keeps the startedAt of the reconcile it started in (wait-for-merge
// runs over many reconciles). Most steps start and finish within one
// reconcile, so their times come from timings; otherwise every such step
// would get startedAt == completedAt and no duration. Each step that becomes
// Completed or Failed sets durationMs and is observed once in
// kardinal_step_duration_seconds.
func updateStepStatuses(ps *v1alpha1.PromotionStep, stepNames []string, currentIdx int, failed bool,
	failMessage string, timings map[int]steps.StepTiming) {
	if len(ps.Status.Steps) == 0 {
		// Steps not yet initialized (e.g. crash before initStepStatuses ran).
		// Reconstruct the Pending slice so updates have something to apply to.
		ps.Status.Steps = initStepStatuses(stepNames)
	}

	now := time.Now()
	stamp := func(t time.Time) *metav1.Time {
		if t.IsZero() {
			t = now
		}
		mt := metav1.NewTime(t)
		return &mt
	}
	// finish records the end of step i and observes its duration.
	finish := func(i int, step *v1alpha1.StepStatus) {
		t, ran := timings[i]
		if step.StartedAt == nil {
			step.StartedAt = stamp(t.Started)
		}
		if ran || step.CompletedAt == nil {
			step.CompletedAt = stamp(t.Finished)
		}
		d := step.CompletedAt.Sub(step.StartedAt.Time)
		if d < 0 {
			d = 0
		}
		step.DurationMs = d.Milliseconds()
		observability.StepDurationSeconds.WithLabelValues(step.Name).Observe(d.Seconds())
	}

	for i := range ps.Status.Steps {
		step := &ps.Status.Steps[i]
		switch {
		case i < currentIdx:
			// Steps before the current index have completed, in this
			// reconcile or a previous one. Only update a step not already
			// marked Completed (idempotent), so each is observed once.
			if step.State != v1alpha1.StepExecutionCompleted {
				step.State = v1alpha1.StepExecutionCompleted
				finish(i, step)
			}
		case i == currentIdx && failed:
			if step.State != v1alpha1.StepExecutionFailed {
				step.State = v1alpha1.StepExecutionFailed
				finish(i, step)
				step.Message = failMessage
			}
		case i == currentIdx:
			// This step is in progress (StepPending result means "still running").
			if step.State == v1alpha1.StepExecutionPending {
				step.State = v1alpha1.StepExecutionInProgress
				step.StartedAt = stamp(timings[i].Started)
			}
		default:
			// Future steps: leave as Pending. When currentIdx ==
			// len(stepNames) every step is handled by the first case.
		}
	}
}

// collectGateResults reads the PolicyGate instances named in spec.requiredGates
// so the PR body can show what was evaluated. Best effort: a gate that cannot
// be read is logged and skipped, it never blocks the promotion.
func (r *Reconciler) collectGateResults(ctx context.Context, log zerolog.Logger,
	ps *v1alpha1.PromotionStep) []v1alpha1.GateResult {
	if len(ps.Spec.RequiredGates) == 0 {
		return nil
	}
	out := make([]v1alpha1.GateResult, 0, len(ps.Spec.RequiredGates))
	for _, name := range ps.Spec.RequiredGates {
		var g v1alpha1.PolicyGate
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ps.Namespace}, &g); err != nil {
			log.Warn().Err(err).Str("gate", name).Msg("could not read gate for PR body")
			continue
		}
		gr := v1alpha1.GateResult{
			GateName:      name,
			GateNamespace: g.Namespace,
			Result:        "Fail",
			Reason:        g.Status.Reason,
		}
		// Prefer the template's name over the generated instance name.
		if tmpl := g.Labels["kardinal.io/gate-name"]; tmpl != "" {
			gr.GateName = tmpl
		}
		if g.Status.Ready {
			gr.Result = "Pass"
		}
		if g.Status.LastEvaluatedAt != nil {
			gr.EvaluatedAt = *g.Status.LastEvaluatedAt
		} else {
			gr.EvaluatedAt = metav1.Now()
		}
		out = append(out, gr)
	}
	return out
}

// upstreamEnvironments returns the Bundle's environment evidence for every
// environment other than env, for the PR body's upstream verification table.
func upstreamEnvironments(bundle *v1alpha1.Bundle, env string) []v1alpha1.EnvironmentStatus {
	if bundle == nil {
		return nil
	}
	var out []v1alpha1.EnvironmentStatus
	for _, e := range bundle.Status.Environments {
		if e.Name != env {
			out = append(out, e)
		}
	}
	return out
}

// setRollbackState tells the steps who asked for the Bundle and, for a
// rollback Bundle, which Bundle it replaces, so the rollback PR can name both
// sides and the person who rolled back. The replaced Bundle may be gone; the PR
// then names it without its version.
func (r *Reconciler) setRollbackState(ctx context.Context, log zerolog.Logger, state *steps.StepState, bundle *v1alpha1.Bundle) {
	if bundle == nil {
		return
	}
	state.RequestedBy = bundle.Annotations[lifecycle.AnnotationRequestedBy]
	name := bundle.Annotations[lifecycle.AnnotationRollbackFrom]
	if name == "" {
		return
	}
	state.RollbackFrom = name
	var from v1alpha1.Bundle
	if err := r.Get(ctx, types.NamespacedName{Namespace: bundle.Namespace, Name: name}, &from); err != nil {
		log.Debug().Err(err).Str("rollbackFrom", name).Msg("replaced bundle not readable; the rollback PR names it without its version")
		return
	}
	state.RollbackFromBundle = &from.Spec
}
