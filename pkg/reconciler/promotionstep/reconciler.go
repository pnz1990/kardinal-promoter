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
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	builderutil "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"

	// Import built-in steps to trigger init() registration.
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	_ "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
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
//	"HealthChecking"  → "Verifying": the same, for a step with post-deploy
//	                    hooks (spec.postHooks)
//	"Verifying"       → "Verified": every post-deploy hook succeeded
//	"Verifying"       → "Failed" / "AbortedByAlarm" / "RollingBack": a post-
//	                    deploy hook failed (onHealthFailure)
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
	// Workers is how many objects are reconciled at once (--promotionstep-workers);
	// 0 is the manager's default. One object is never reconciled twice at
	// once: the work queue serializes it.
	Workers int

	client.Client

	// APIReader reads straight from the API server (mgr.GetAPIReader()). A
	// deleted step is read through it before its PR is closed, because the
	// informer cache can lag the finalizer removal of the previous reconcile
	// (handleDeleted), and so are the Bundle, namespace, Pipeline and Graph
	// that tell whether the step comes back (stepComeback). The supersession
	// guard reads a step through it too, before cancelling it (supersededStep).
	// When nil, Client is used (tests).
	APIReader client.Reader

	// SCM is the SCM provider for PR operations.
	SCM scm.SCMProvider

	// AllowedRepositories is --scm-allowed-repositories: a step of a Pipeline
	// that would need the shared SCM token for a spec.git.url it does not
	// allow fails before git-clone (repositoryNotAllowed, #1332). The SCM
	// provider is wrapped with the same list (RepositoryAllowlist.Guard), so
	// every SCM call is checked too. Nil allows every repository.
	AllowedRepositories *scm.RepositoryAllowlist

	// GitClient is the Git operations client.
	GitClient scm.GitClient

	// remotes caches the remote reads of PR branch refreshes (remoteCache).
	remotes remoteCache

	// HealthDetector selects the health adapter for health checking.
	// If nil, the health-check step stub (always-success) is used.
	HealthDetector *health.AutoDetector

	// RemoteClusters builds the health adapters of an environment with
	// health.kubeconfigSecretRef. Nil fails such a step.
	RemoteClusters *health.RemoteClusters

	// WorkDirFn returns the working directory for a given pipeline+bundle pair.
	// Tests set it; when nil a fixed path under the kardinal work root is used.
	WorkDirFn func(pipelineName, bundleName string) string

	// Recorder emits Kubernetes Events for PromotionStep state transitions.
	// When nil, event emission is skipped (backward-compatible).
	Recorder events.EventRecorder

	// NowFn returns the current time. When nil, time.Now is used. Tests set
	// it. It drives the deadline for closing a deleted step's PR and the
	// retry backoff (status.nextRetryAt).
	NowFn func() time.Time
}

// now returns the current time from NowFn, or time.Now when it is nil.
func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now()
}

// Reconcile processes one PromotionStep event.
// It is idempotent: safe to re-run after a crash at any point.
//
// A PromotionStep deleted while it is reconciled ends the reconcile (objectgone).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, promotionStepsResource, r.reconcile)
}

// promotionStepsResource is the resource objectgone matches a NotFound against.
var promotionStepsResource = v1alpha1.GroupVersion.WithResource("promotionsteps").GroupResource()

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	// A deleted step only closes its PR (FinalizerClosePR). Otherwise the
	// finalizer follows the state before and after this reconcile: it is on
	// before an open-pr step opens its PR and off once the step is past it.
	if !ps.DeletionTimestamp.IsZero() {
		return r.handleDeleted(ctx, log, &ps)
	}
	if err := r.syncPRFinalizer(ctx, &ps); err != nil {
		return prFinalizerSyncFailed(log, err)
	}
	// Hooks added too late for this step: say so on the step (any state);
	// hooks that ran: record them, so a recreated HookRun does not run again.
	base := ps.DeepCopy()
	skipped, recorded := recordSkippedHooks(&ps, r.now().UTC()), recordHookRuns(&ps)
	if skipped || recorded {
		// With the resourceVersion read: a merge patch of status.hookRecords
		// from a stale copy would drop records another reconcile wrote (QA
		// #1493). A conflict reads the step again.
		err := r.Status().Patch(ctx, &ps, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		switch {
		case apierrors.IsConflict(err):
			return ctrl.Result{RequeueAfter: time.Second}, nil
		case err != nil && !apierrors.IsNotFound(err):
			return ctrl.Result{}, fmt.Errorf("patch %s condition: %w", ConditionHooksSkipped, err)
		}
	}
	res, err := r.reconcileState(ctx, log, &ps)
	if err != nil {
		return res, err
	}
	if err := r.syncPRFinalizer(ctx, &ps); err != nil {
		return prFinalizerSyncFailed(log, err)
	}
	return res, nil
}

// reconcileState runs the orphan and supersession guards and then the
// handler of the step's state.
func (r *Reconciler) reconcileState(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
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
				if delErr := r.Delete(ctx, ps); delErr != nil && !apierrors.IsNotFound(delErr) {
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
		//
		// Rejection guard (#1451): a rejected Bundle's steps that have not
		// delivered the change stop the same way. A step already
		// HealthChecking keeps checking: the change is live in its
		// environment, and reject does not revert it (rollback does).
		if h := haltOf(&parentBundle); h != nil && h.cancels(ps.Status.State) {
			fresh, err := r.supersededStep(ctx, log, ps)
			if err != nil || fresh == nil {
				return ctrl.Result{}, err
			}
			*ps = *fresh
			switch {
			case !ps.DeletionTimestamp.IsZero():
				return ctrl.Result{}, nil // handleDeleted closes its PR
			case h.cancels(ps.Status.State):
				res, live, err := r.handleSuperseded(ctx, log, ps, h)
				if !live {
					return res, err
				}
				// The PR merged before the rejection: the change is live, so
				// the step goes on to its health check (DESIGN §6) and the
				// state's handler runs below.
			}
			// The cache lagged a transition out of a cancellable state: the
			// fresh state's handler runs below (RollingBack cleans the workdir).
		}
	}

	switch ps.Status.State {
	case StatePending, StatePendingExplicit:
		return r.handlePending(ctx, log, ps)
	case StatePromoting:
		return r.handlePromoting(ctx, log, ps)
	case StateWaitingForMerge:
		return r.handleWaitingForMerge(ctx, log, ps)
	case StateHealthChecking:
		return r.handleHealthChecking(ctx, log, ps)
	case StateVerifying:
		return r.handleVerifying(ctx, log, ps)
	case StateVerified, StateFailed:
		// Terminal states — clean up workdir if present (ST-7/ST-8 short-term mitigation).
		r.cleanWorkDir(log, ps)
		return ctrl.Result{}, nil
	case StateAbortedByAlarm:
		// Terminal human-intervention state — clean up workdir and stop reconciling.
		// Requires manual resume or rollback from a human operator.
		r.cleanWorkDir(log, ps)
		return ctrl.Result{}, nil
	case StateRollingBack:
		// Managed state set by applyHealthFailurePolicy (K-03). The rollback Bundle
		// drives resolution externally; this reconciler takes no further action until
		// the rollback Bundle completes and the step is superseded or manually reset.
		r.cleanWorkDir(log, ps)
		return ctrl.Result{}, nil
	default:
		log.Warn().Str("state", ps.Status.State).Msg("unknown state, resetting to Pending")
		if err := r.transition(ctx, ps.DeepCopy(), ps, StatePendingExplicit, ""); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
}

// isCancellable reports whether a step in state can still be cancelled by
// supersession: every state that is not terminal.
func isCancellable(state string) bool {
	switch state {
	case StatePending, StatePendingExplicit, StatePromoting, StateWaitingForMerge, StateHealthChecking, StateVerifying:
		return true
	}
	return false
}

// halt is why a Bundle's in-flight steps are cancelled: it was superseded by
// a newer Bundle, or rejected (kardinal reject).
type halt struct {
	// verb is the participle used in messages: "superseded" or "rejected".
	verb string
	// closeReason is the comment left on a closed PR.
	closeReason string
	// auditAction is the AuditEvent action of a cancelled started step.
	auditAction string
	// eventReason is the Kubernetes Event reason of a cancellation.
	eventReason string
	// healthChecking reports whether a HealthChecking step is cancelled.
	healthChecking bool
	// mergedIsLive reports whether a step whose PR has merged keeps going:
	// the change is in the environment, so it is health-checked, and
	// onHealthFailure and the deployed-Bundle history see it.
	mergedIsLive bool
}

// haltOf returns the halt for b, or nil when b's steps may continue.
// Supersession is read from status.phase, which only the Bundle reconciler
// writes. A rejection is read from spec.rejected as well as the phase, so a
// step stops before the Bundle reconciler has recorded the phase.
func haltOf(b *v1alpha1.Bundle) *halt {
	switch {
	case b.Spec.Rejected != nil || b.Status.Phase == "Rejected":
		by := ""
		if b.Spec.Rejected != nil {
			by = " by " + b.Spec.Rejected.By
		}
		return &halt{
			verb:         "rejected",
			closeReason:  "bundle " + b.Name + " was rejected" + by,
			auditAction:  AuditActionPromotionRejected,
			eventReason:  "Rejected",
			mergedIsLive: true,
		}
	case b.Status.Phase == "Superseded":
		return &halt{
			verb:           "superseded",
			closeReason:    "bundle " + b.Name + " was superseded by a newer Bundle",
			auditAction:    AuditActionPromotionSuperseded,
			eventReason:    "Superseded",
			healthChecking: true,
		}
	}
	return nil
}

// cancels reports whether a step in state is cancelled by h.
func (h *halt) cancels(state string) bool {
	if state == StateHealthChecking {
		return h.healthChecking
	}
	return isCancellable(state)
}

// supersededStep reads ps, whose Bundle is Superseded and whose cached state
// is cancellable, from the API server, so that the supersession guard cancels
// it from the status the server has, not the cached one. The cache can lag
// this reconciler's own status patch: the rollback Bundle that
// applyHealthFailurePolicy creates is a newer Bundle of the same pipeline and
// type, so the Bundle reconciler supersedes the parent at once, and that
// Bundle event wakes the step (bundleMapper) before the cache has its
// RollingBack transition. Cancelling from the cached HealthChecking overwrote
// RollingBack with Failed, "bundle ... was superseded — promotion cancelled"
// (ONFAIL-ROLLBACK-03). The guard then acts on the fresh copy: a step still
// in flight is cancelled from it, so a PR recorded since the cached read is
// closed too; one that moved on runs its state's handler. It returns nil when
// the step is gone.
func (r *Reconciler) supersededStep(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (*v1alpha1.PromotionStep, error) {
	fresh, err := r.readStep(ctx, client.ObjectKeyFromObject(ps))
	if err != nil || fresh == nil {
		return nil, err
	}
	if fresh.Status.State != ps.Status.State {
		log.Debug().
			Str("bundle", ps.Spec.BundleName).
			Str("cached", ps.Status.State).
			Str("state", fresh.Status.State).
			Msg("parent bundle superseded; the cached step lags its last status patch, using the API server's copy")
	}
	return fresh, nil
}

// ConditionSupersededCloseFailed is True while a superseded step retries
// closing its PR, and stays True on a step that failed after the last retry.
const ConditionSupersededCloseFailed = "SupersededCloseFailed"

// handleSuperseded closes the step's PR, if it opened one that is still open,
// and fails the step, because its Bundle was superseded or rejected (h). A failed close is retried with backoff up to
// maxStepRetries times; after that the step fails anyway and the message
// tells the operator to close the PR by hand (C03-promotionstep-08). A step
// that never left Pending is failed without an AuditEvent (cancelUnstarted).
//
// A close retry waits for status.nextRetryAt, as a step retry does (B87):
// the step's gates, PRStatus and Bundle wake it during the backoff, and each
// wake ran a retry (B89). The first failed close starts retryCount over, so
// the close gets its retries whatever the step used before it was
// superseded, and sets ConditionSupersededCloseFailed. Only with it is
// nextRetryAt the close's: a step superseded during a git retry's backoff is
// cancelled at once, not when that retry was due.
//
// With h.mergedIsLive (a rejection), a step whose PR has already merged is
// not cancelled: live is true and nothing is written, and the caller runs the
// state's handler, which moves it to HealthChecking on the merge.
func (r *Reconciler) handleSuperseded(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep, h *halt) (res ctrl.Result, live bool, err error) {
	// "bundle X was superseded" is lifecycle.SupersededMessage.
	return r.cancelStep(ctx, log, ps, fmt.Sprintf("bundle %s was %s", ps.Spec.BundleName, h.verb),
		h.closeReason, h.auditAction, h.eventReason, h.mergedIsLive)
}

// cancelStep is handleSuperseded for any reason: why opens the step's
// message, prComment is the comment on the closed PR, action the AuditEvent
// of a started step and eventReason the Event of an unstarted one. A close
// that fails is retried the same way, under ConditionSupersededCloseFailed.
// With mergedIsLive, a step whose PR already merged is left to its handler
// (live true).
func (r *Reconciler) cancelStep(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep,
	why, prComment, action, eventReason string, mergedIsLive bool) (ctrl.Result, bool, error) {
	closing := meta.IsStatusConditionTrue(ps.Status.Conditions, ConditionSupersededCloseFailed)
	if closing && ps.Status.NextRetryAt != nil {
		if wait := ps.Status.NextRetryAt.Sub(r.now()); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, false, nil
		}
	}
	base := ps.DeepCopy()
	ps.Status.NextRetryAt = nil
	log.Info().
		Str("bundle", ps.Spec.BundleName).
		Str("env", ps.Spec.Environment).
		Str("state", ps.Status.State).
		Str("why", why).
		Msg("closing open PR and cancelling step")

	// A step still Pending never started: no PromotionStarted record, no
	// branch, no PR. It can exist when its Graph created it just before the
	// Bundle was superseded (E2E-R20). It is not a cancelled promotion, so it
	// is failed without a PromotionSuperseded record or step metrics.
	unstarted := base.Status.State == StatePending || base.Status.State == StatePendingExplicit
	msg := why + " — promotion cancelled"
	if unstarted {
		msg = why + " before this step started"
	}
	merged, closeErr := r.closeStepPRMerged(ctx, ps, prComment, false)
	if closeErr == nil && merged && mergedIsLive {
		log.Info().Str("bundle", ps.Spec.BundleName).Str("env", ps.Spec.Environment).
			Msgf("the PR merged before this: %s; the change is live, so the step is health-checked", why)
		return ctrl.Result{}, true, nil
	}
	if closeErr != nil {
		if !closing {
			ps.Status.RetryCount = 0
		}
		meta.SetStatusCondition(&ps.Status.Conditions, metav1.Condition{
			Type:               ConditionSupersededCloseFailed,
			Status:             metav1.ConditionTrue,
			Reason:             "CloseFailed",
			Message:            closeErr.Error(),
			ObservedGeneration: ps.Generation,
			LastTransitionTime: metav1.NewTime(r.now().UTC()),
		})
		if ps.Status.RetryCount < maxStepRetries {
			ps.Status.RetryCount++
			delay := retryDelay(ps.Status.RetryCount)
			next := metav1.NewTime(r.now().Add(delay))
			ps.Status.NextRetryAt = &next
			ps.Status.Message = fmt.Sprintf("%s; closing its PR failed, retrying in %s (%d/%d): %v", why,
				delay, ps.Status.RetryCount, maxStepRetries, closeErr)
			if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil {
				return ctrl.Result{}, false, fmt.Errorf("patch supersession retry: %w", err)
			}
			return ctrl.Result{RequeueAfter: delay}, false, nil
		}
		msg += fmt.Sprintf("; closing its PR failed after %d retries (%v) — %s", maxStepRetries, closeErr, closeByHand(closeErr))
	} else {
		meta.RemoveStatusCondition(&ps.Status.Conditions, ConditionSupersededCloseFailed)
	}
	if unstarted {
		return ctrl.Result{}, false, r.cancelUnstarted(ctx, base, ps, msg, eventReason)
	}
	if err := r.transitionAudit(ctx, base, ps, StateFailed, msg, action); err != nil {
		return ctrl.Result{}, false, err
	}
	return ctrl.Result{}, false, nil
}

// closeStepPR closes the PR this step opened, if it is still open, and leaves
// a comment with reason. The PR is found through the PRStatus spec, falling
// back to the step outputs when the PRStatus is gone, was never filled in (a
// crash between opening the PR and patching the PRStatus), or still names or
// reports the PR from before the step was recreated (prStatusOfStepPR). A
// step that never opened a PR has its head branch deleted, unless keepBranch
// (deleteBranchWithoutPR): git-push may have pushed it, or an earlier step may
// have left it for this one.
//
// The SCM is asked first whether the PR is still open: the PRStatus can lag
// (it is polled), or be gone with its Graph. A PR that is merged or closed
// (by a human, or by an earlier attempt whose finalizer removal or response
// was lost) is left alone, with no comment; without the check a merged PR got
// a "kardinal closed this PR" comment, and Bitbucket and Azure DevOps were
// asked to decline or abandon it. Only a PRStatus that says merged is taken
// without asking, since a merge is final; a closed PR can be reopened, so a
// PRStatus that says closed is checked like an open one. The PR is closed
// before it is commented on, so a restart in between leaves no comment rather
// than two.
//
// Once the PR is closed and not merged, its head branch is deleted
// (deletePRBranch): GitHub merges a closed PR through the API, and does not
// once the branch is gone. A PR found closed (an earlier attempt closed it
// and then failed or crashed before the delete, or a human closed it) gets
// the delete too, so a retry finishes the job; a merged PR keeps its branch.
// With keepBranch the branch is kept: a deleted step that kro applies again
// pushes it again at once, and deleting it closed the new step's PR on
// Forgejo and Gitea (handleDeleted, B79). The status read, the close and the
// delete can fail; the comment is best-effort.
func (r *Reconciler) closeStepPR(ctx context.Context, ps *v1alpha1.PromotionStep, reason string, keepBranch bool) error {
	_, err := r.closeStepPRMerged(ctx, ps, reason, keepBranch)
	return err
}

// closeStepPRMerged is closeStepPR that also reports whether the step's PR
// had already merged, in which case nothing was closed.
func (r *Reconciler) closeStepPRMerged(ctx context.Context, ps *v1alpha1.PromotionStep, reason string, keepBranch bool) (bool, error) {
	merged, err := r.closeStepPRWithSCM(ctx, ps, reason, keepBranch)
	if errors.Is(err, scm.ErrRepositoryNotAllowed) {
		// The shared token may not act on this repository (#1332): kardinal
		// opened nothing there with it, and retrying cannot change that.
		zerolog.Ctx(ctx).Warn().Err(err).Str("step", ps.Name).
			Msg("left the PR and branch of the step alone: the repository is not allowed")
		return false, nil
	}
	return merged, err
}

// closeStepPRWithSCM is closeStepPRMerged without the allowlist handling.
func (r *Reconciler) closeStepPRWithSCM(ctx context.Context, ps *v1alpha1.PromotionStep, reason string, keepBranch bool) (bool, error) {
	repo, num := "", 0
	if ps.Spec.PRStatusRef != "" {
		var prs v1alpha1.PRStatus
		err := r.Get(ctx, types.NamespacedName{Name: ps.Spec.PRStatusRef, Namespace: ps.Namespace}, &prs)
		switch {
		case err == nil && prStatusOfStepPR(&prs, ps.Status.Outputs):
			if prs.Status.Merged {
				return true, nil // a merge is final: nothing to close
			}
			repo, num = prs.Spec.Repo, prs.Spec.PRNumber
		case err == nil:
			// The PRStatus still names, or reports, the PR from before the
			// step was recreated (B72): close the step's own PR, below.
		case !apierrors.IsNotFound(err):
			return false, fmt.Errorf("get prstatus %s: %w", ps.Spec.PRStatusRef, err)
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
		if keepBranch {
			zerolog.Ctx(ctx).Info().Str("step", ps.Name).Str("branch", prHeadBranch(ps)).
				Msg("kept the head branch of a step that opened no PR: the step comes back and pushes it again")
			return false, nil
		}
		return false, r.deleteBranchWithoutPR(ctx, ps)
	}
	if r.SCM == nil {
		return false, fmt.Errorf("no SCM provider configured to close PR #%d", num)
	}
	log := zerolog.Ctx(ctx)
	merged, open, err := r.SCM.GetPRStatus(ctx, repo, num)
	if err != nil {
		return false, fmt.Errorf("get PR #%d status: %w", num, err)
	}
	if !open {
		log.Info().Int("pr", num).Str("step", ps.Name).Bool("merged", merged).
			Msg("PR of cancelled step is no longer open; not closing it")
		if merged {
			return true, nil
		}
		return false, r.closedPRBranch(ctx, ps, repo, num, keepBranch)
	}
	if err := r.SCM.ClosePR(ctx, repo, num); err != nil {
		return false, fmt.Errorf("close PR #%d: %w", num, err)
	}
	log.Info().Int("pr", num).Str("step", ps.Name).Msg("closed PR of cancelled step")
	r.markPRStatusClosedByKardinal(ctx, ps, num)
	body := fmt.Sprintf("kardinal closed this PR: %s. Merging it would change environment %s "+
		"without a PromotionStep tracking it.", reason, ps.Spec.Environment)
	if err := r.SCM.CommentOnPR(ctx, repo, num, body); err != nil {
		log.Warn().Err(err).Int("pr", num).Msg("could not comment on the closed PR (non-fatal)")
	}
	return false, r.closedPRBranch(ctx, ps, repo, num, keepBranch)
}

// markPRStatusClosedByKardinal records on the step's PRStatus that kardinal
// closed PR num itself (prstatus.AnnotationClosedByKardinal), so the PRStatus
// reconciler does not comment "stopped tracking" on it when the grace window
// ends: the close comment already says why (#1351). It writes metadata only,
// never the PRStatus status, and only when the PRStatus spec names PR num.
// Best-effort: a failure costs a second comment, so it is logged.
func (r *Reconciler) markPRStatusClosedByKardinal(ctx context.Context, ps *v1alpha1.PromotionStep, num int) {
	if ps.Spec.PRStatusRef == "" {
		return
	}
	log := zerolog.Ctx(ctx)
	var prs v1alpha1.PRStatus
	if err := r.Get(ctx, types.NamespacedName{Name: ps.Spec.PRStatusRef, Namespace: ps.Namespace}, &prs); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Warn().Err(err).Int("pr", num).Msg("could not read the PRStatus to mark the PR closed by kardinal (non-fatal)")
		}
		return
	}
	want := strconv.Itoa(num)
	if prs.Spec.PRNumber != num || prs.Annotations[prstatus.AnnotationClosedByKardinal] == want {
		return
	}
	patch := client.MergeFrom(prs.DeepCopy())
	if prs.Annotations == nil {
		prs.Annotations = map[string]string{}
	}
	prs.Annotations[prstatus.AnnotationClosedByKardinal] = want
	if err := r.Patch(ctx, &prs, patch); err != nil && !apierrors.IsNotFound(err) {
		log.Warn().Err(err).Int("pr", num).Msg("could not mark the PRStatus closed by kardinal (non-fatal)")
	}
}

// withLabelsError appends the error of open-pr's failed attempt to label the
// PR (steps.OutputPRLabelsError) to a WaitingForMerge message. The wait-for-
// merge message replaces open-pr's, which carried it, so without this the
// step message never said the PR has no labels (docs/pr-evidence.md).
func withLabelsError(msg string, outputs map[string]string) string {
	if e := outputs[steps.OutputPRLabelsError]; e != "" {
		return msg + "; adding labels failed: " + e
	}
	return msg
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

// contendedDelay jitters the backoff of a step that lost to other writers of
// its branch, so writers that collided do not come back in lockstep: half the
// delay plus up to the whole of it again, at most retryMaxDelay.
func contendedDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return min(d/2+time.Duration(rand.Int64N(int64(d))), retryMaxDelay)
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
	if held, res, holdErr := r.holdIfEnvironmentHeld(ctx, log, ps, pipeline); held {
		return res, holdErr
	}
	if held, res, holdErr := r.holdForSlot(ctx, log, ps); held {
		return res, holdErr
	}
	if msg := unsupportedConfig(pipeline, findEnv(pipeline, ps.Spec.Environment), ps); msg != "" {
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
	}
	if msg, err := r.repositoryNotAllowed(ctx, pipeline); err != nil {
		return ctrl.Result{}, err
	} else if msg != "" {
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
	}

	// Pre-deploy hooks (docs/hooks.md) run before the step starts: wait for
	// every one to succeed, fail when one failed.
	if held, res, holdErr := r.holdForPreHooks(ctx, log, base, ps); held {
		return res, holdErr
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
				patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base))
				if apierrors.IsNotFound(patchErr) {
					log.Debug().Msg("step deleted before the gate wait message patch — ignoring")
					return ctrl.Result{}, nil
				}
				if patchErr != nil {
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

	// The Bundle type picks the step list: a config or mixed Bundle merges its
	// config first. A failed read is retried, not taken as an image Bundle:
	// the list recorded here is the one the step runs to the end, so an image
	// list would verify a config or mixed Bundle without its config change.
	bundle, err := r.loadBundle(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load bundle: %w", err)
	}

	seq := stepSequence(env, bundle)
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
//
// A step that retries waits until status.nextRetryAt. The retry's
// RequeueAfter alone did not hold the backoff: every PolicyGate status write
// (policyGateMapper), PRStatus change or restart reconciled the step at once,
// so a gate with recheckInterval 10s used up the five retries in about 40s
// instead of 4.5 minutes (B87).
func (r *Reconciler) handlePromoting(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	if ps.Status.NextRetryAt != nil {
		if wait := ps.Status.NextRetryAt.Sub(r.now()); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}
	base := ps.DeepCopy()
	// A status patch from base clears it, and a retry sets it again. One left
	// behind (the pause hold patches from its own copy) has passed already.
	ps.Status.NextRetryAt = nil
	pipeline, err := r.loadPipeline(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load pipeline: %w", err)
	}
	if held, res, holdErr := r.holdIfPaused(ctx, log, ps); held {
		return res, holdErr
	}
	if held, res, holdErr := r.holdIfEnvironmentHeld(ctx, log, ps, pipeline); held {
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
	if msg, err := r.repositoryNotAllowed(ctx, pipeline); err != nil {
		return ctrl.Result{}, err
	} else if msg != "" {
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
	}
	// Run the step list recorded when the step left Pending, never one rebuilt
	// from the live Pipeline: an approval edit made while the step runs would
	// otherwise move the current index into another sequence (skipping open-pr,
	// or opening a PR the finalizer was not added for). The edit applies from
	// the next Bundle. The in-place Graph rebuild that follows the edit does
	// not wait on a PR either: the PRStatus node has no readyWhen, so this
	// environment's nodes are ready once this step is Verified, with or
	// without a PR (B69).
	seq := recordedSequence(ps)
	if len(seq) == 0 {
		// A step is Promoting with no step list only if its status was edited
		// by hand or it started before status.steps existed. Record the list
		// and run it from the next reconcile: the finalizer sync at the end of
		// this one then adds kardinal.io/close-pr before open-pr can run.
		ps.Status.Steps = initStepStatuses(stepSequence(env, bundle))
		if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, fmt.Errorf("record step list: %w", err)
		}
		log.Info().Str("env", ps.Spec.Environment).Strs("steps", recordedSequence(ps)).
			Msg("recorded the step list of a Promoting step that had none")
		return ctrl.Result{Requeue: true}, nil
	}
	eng := steps.NewEngine(seq)

	// The working directory is always recomputed from the PromotionStep's
	// identity, never read back from status, so a status write cannot point
	// the step engine (or cleanWorkDir) at another checkout.
	workDir := r.workDir(ps)

	// The git token from Pipeline spec.git.secretRef. A git step that fails
	// without one says why (B48).
	cred := r.resolveGitCredential(ctx, log, pipeline)
	state := r.stepState(ctx, log, ps, pipeline, env, bundle, seq, workDir, cred)

	prevIdx := ps.Status.CurrentStepIndex
	nextIdx, result, execErr := eng.ExecuteFrom(ctx, state, prevIdx)

	// Persist outputs regardless of result, so a PR opened in this reconcile is
	// never forgotten (C03-promotionstep-06).
	ps.Status.Outputs = state.Outputs
	ps.Status.CurrentStepIndex = nextIdx
	if nextIdx > prevIdx {
		// Progress resets the retry budget.
		ps.Status.RetryCount, ps.Status.GitCredentialRetries, ps.Status.ContendedRetries = 0, 0, 0
	}
	if prURL := state.Outputs["prURL"]; prURL != "" {
		ps.Status.PRURL = prURL
	}
	// Every path below writes the status.
	clearGitCredentialMissing(ps, cred)

	if execErr != nil {
		return r.handleStepError(ctx, log, base, ps, eng.StepNames(), eng.Timings(), execErr, nil, cred)
	}
	closed := updateStepStatuses(ps, eng.StepNames(), nextIdx, false, "", eng.Timings())

	switch result.Status {
	case steps.StepPending:
		if prURL := state.Outputs["prURL"]; prURL != "" {
			// The open-pr step (or similar) has opened a PR and is waiting for merge.
			// Transition to WaitingForMerge so the PRStatusReconciler can take over.
			if _, err := r.transitionClosing(ctx, base, ps, StateWaitingForMerge,
				withLabelsError(result.Message, state.Outputs), "", closed); err != nil {
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
		closed.record()
		requeue := result.RequeueAfter
		if requeue == 0 {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{RequeueAfter: requeue}, nil

	case steps.StepSuccess:
		if nextIdx >= len(seq) {
			// All steps completed — move to HealthChecking. Record the commit
			// the health check must see deployed (E2E-01).
			r.recordPushedCommit(ctx, log, ps, pipeline, workDir)
			if ps.Spec.PRStatusRef != "" && state.Outputs["prURL"] != "" {
				if prErr := r.patchPRStatusSpec(ctx, ps, state.Outputs); prErr != nil {
					log.Warn().Err(prErr).Msg("failed to patch PRStatus spec (non-fatal)")
				}
			}
			if _, err := r.transitionClosing(ctx, base, ps, StateHealthChecking,
				"all steps complete, running health check", "", closed); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		// More steps remain — persist index and requeue immediately.
		ps.Status.Message = fmt.Sprintf("completed step %d/%d", nextIdx, len(seq))
		// Locked like the retry and state patches: a stale reconcile's
		// progress would rewrite the step statuses a newer one wrote. (Not
		// reached today: ExecuteFrom runs every remaining step and reports
		// success only with nextIdx == len(seq).)
		if patchErr := r.Status().Patch(ctx, ps, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); patchErr != nil {
			if apierrors.IsNotFound(patchErr) {
				return ctrl.Result{}, nil
			}
			if apierrors.IsConflict(patchErr) {
				log.Debug().Str("step", ps.Name).Msg("step changed since it was read; progress not written, requeueing")
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("patch step progress: %w", patchErr)
		}
		closed.record()
		return ctrl.Result{Requeue: true}, nil

	default:
		// ExecuteFrom reports StepFailed with an error, so this is unreachable
		// unless a step returns an unknown status.
		return r.handleStepError(ctx, log, base, ps, eng.StepNames(), eng.Timings(),
			fmt.Errorf("step %d returned status %q: %s", nextIdx, result.Status, result.Message), closed, cred)
	}
}

// stepState is the state the step engine runs seq with for ps.
func (r *Reconciler) stepState(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep,
	pipeline *v1alpha1.Pipeline, env v1alpha1.EnvironmentSpec, bundle *v1alpha1.Bundle, seq []string,
	workDir string, cred gitCredential) *steps.StepState {
	state := &steps.StepState{
		Pipeline:     pipeline.Spec,
		PipelineName: ps.Spec.PipelineName,
		Environment:  env,
		Bundle:       bundle.Spec,
		BundleName:   ps.Spec.BundleName,
		Namespace:    ps.Namespace,
		WorkDir:      workDir,
		Outputs:      cloneMap(ps.Status.Outputs),
		Git: steps.GitConfig{
			URL:         pipeline.Spec.Git.URL,
			Branch:      baseBranch(pipeline),
			Token:       cred.token,
			AuthorName:  "kardinal-promoter",
			AuthorEmail: "kardinal@kardinal.io",
		},
		SCM:                  r.SCM,
		GitClient:            r.GitClient,
		K8sClient:            r.Client,
		StepTimeoutSeconds:   env.StepTimeoutSeconds,
		GateResults:          r.collectGateResults(ctx, log, ps),
		UpstreamEnvironments: upstreamEnvironments(bundle, ps.Spec.Environment),
		Sequence:             seq,
	}
	r.setRollbackState(ctx, log, state, bundle)
	return state
}

// prOpenedAt is when the promotion PR was opened: when the open-pr step
// completed, else when wait-for-merge started. ok is false when the step
// statuses record neither.
func prOpenedAt(ps *v1alpha1.PromotionStep) (opened time.Time, ok bool) {
	for _, s := range ps.Status.Steps {
		if s.Name == openPRStep && s.CompletedAt != nil {
			return s.CompletedAt.Time, true
		}
	}
	for _, s := range ps.Status.Steps {
		if s.Name == "wait-for-merge" && s.StartedAt != nil {
			return s.StartedAt.Time, true
		}
	}
	return time.Time{}, false
}

// handleStepError decides what a step engine error means for the PromotionStep.
//
// The engine wraps an error returned by a step (network, API or git failures)
// with %w and reports a step that returned StepFailed on its own with a plain
// message. The first kind is retried with exponential backoff up to
// maxStepRetries times, unless the step marked it with steps.Permanent; the
// second, a permanent error, and a retried error once the retries are used up
// fail the step (C03-promotionstep-06). A failed step closes the PR it opened,
// so a later merge cannot deliver a change whose step is Failed. closed holds
// the steps this reconcile already closed; they are observed with the ones
// closed here once the status patch succeeds.
//
// When the remote refuses git-clone or git-push and git has no token (cred),
// the message names what is missing after the git error, and
// ConditionGitCredentialMissing turns True with one Warning Event. When the
// Secret or the secretRef is missing (cred.waitsForSecret), the step is
// retried with no limit, counted in status.gitCredentialRetries and not in
// status.retryCount: creating the Secret (or setting spec.git.secretRef) lets
// it continue at its next retry, and an error after that still gets all
// maxStepRetries retries (B48). A Secret that could not be read keeps the
// limit, since creating it does not help.
func (r *Reconciler) handleStepError(ctx context.Context, log zerolog.Logger, base, ps *v1alpha1.PromotionStep,
	stepNames []string, timings map[int]steps.StepTiming, execErr error, closed stepObservations,
	cred gitCredential) (ctrl.Result, error) {
	idx := ps.Status.CurrentStepIndex
	retryable := errors.Unwrap(execErr) != nil && !errors.Is(execErr, steps.ErrPermanent)
	// A git-clone or git-push that the remote refused for lack of
	// credentials, while the Pipeline gives git none, says which, and keeps
	// retrying past the limit so that creating the Secret is enough (B48).
	step, note := "", ""
	if idx >= 0 && idx < len(stepNames) {
		step = stepNames[idx]
		if retryable && isGitAuthError(execErr) {
			note = cred.note(step)
		}
	}
	emitCredential, waitForSecret := false, false
	if note != "" {
		execErr = fmt.Errorf("%w (%s)", execErr, note)
		emitCredential = markGitCredentialMissing(ps, cred.reason, note)
		waitForSecret = cred.waitsForSecret()
	}
	contended := retryable && errors.Is(execErr, steps.ErrContended)
	if retryable && (waitForSecret || contended || ps.Status.RetryCount < maxStepRetries) {
		var count string
		switch {
		case waitForSecret:
			ps.Status.GitCredentialRetries++
			count = fmt.Sprintf("%d, no limit while git has no credentials", ps.Status.GitCredentialRetries)
		case contended:
			// Losing to other writers is not the step's fault: it backs off
			// with no limit and does not use up retryCount.
			ps.Status.ContendedRetries++
			count = fmt.Sprintf("%d, no limit while other writers keep moving the branch", ps.Status.ContendedRetries)
		default:
			ps.Status.RetryCount++
			count = fmt.Sprintf("%d/%d", ps.Status.RetryCount, maxStepRetries)
		}
		// Every kind of retry backs off together.
		delay := retryDelay(ps.Status.RetryCount + ps.Status.GitCredentialRetries + ps.Status.ContendedRetries)
		if contended {
			delay = contendedDelay(delay)
		}
		next := metav1.NewTime(r.now().Add(delay))
		ps.Status.NextRetryAt = &next
		ps.Status.Message = fmt.Sprintf("retrying in %s (%s) after error: %v", delay, count, execErr)
		closed = append(closed, updateStepStatuses(ps, stepNames, idx, false, "", timings)...)
		log.Warn().Err(execErr).Str("env", ps.Spec.Environment).
			Int("retry", ps.Status.RetryCount).Int("gitCredentialRetries", ps.Status.GitCredentialRetries).
			Dur("delay", delay).Msg("step failed, will retry")
		// Locked on the resourceVersion read: a reconcile that read a stale
		// cached step (the retry status the one before it wrote not seen
		// yet) would mark the credential missing again and emit a second
		// Warning Event, and reset the backoff; its patch is refused with
		// a Conflict instead, and it runs again on the fresh copy.
		if patchErr := r.Status().Patch(ctx, ps, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); patchErr != nil {
			if apierrors.IsNotFound(patchErr) {
				return ctrl.Result{}, nil
			}
			if apierrors.IsConflict(patchErr) {
				log.Debug().Err(patchErr).Msg("step changed while it ran; retrying on the fresh copy")
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("patch step retry: %w", patchErr)
		}
		closed.record()
		if emitCredential {
			r.emitGitCredentialMissing(ps, step, note)
		}
		return ctrl.Result{RequeueAfter: delay}, nil
	}

	msg := execErr.Error()
	if retryable {
		msg = fmt.Sprintf("%s (gave up after %d retries)", msg, maxStepRetries)
	}
	log.Error().Err(execErr).Str("env", ps.Spec.Environment).Msg("step engine failed")
	closed = append(closed, updateStepStatuses(ps, stepNames, idx, true, msg, timings)...)
	if closeErr := r.closeStepPR(ctx, ps, "the promotion failed: "+msg, false); closeErr != nil {
		msg += fmt.Sprintf("; closing the PR it opened failed (%v) — %s", closeErr, closeByHand(closeErr))
	}
	if _, err := r.transitionClosing(ctx, base, ps, StateFailed, msg, "", closed); err != nil {
		return ctrl.Result{}, err
	}
	// Reached when a Secret that could not be read is the first credential
	// failure and the retries are used up already.
	if emitCredential {
		r.emitGitCredentialMissing(ps, step, note)
	}
	return ctrl.Result{}, nil
}

// recordPushedCommit stores, as outputs.commitSHA, the commit the health check
// must find deployed (E2E-01). It is known here only when the step pushed
// straight to the branch the GitOps tool tracks; when the recorded sequence
// opens a PR the merge commit comes from the PRStatus instead.
func (r *Reconciler) recordPushedCommit(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep,
	pipeline *v1alpha1.Pipeline, workDir string) {
	if opensPR(ps) {
		return
	}
	if pushed := ps.Status.Outputs["branch"]; pushed == "" || pushed != baseBranch(pipeline) {
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
// Architecture: the Graph creates the companion PRStatus next to each PromotionStep
// (buildPRStatusNode in pkg/graph/builder.go). After the open-pr step, this reconciler
// points its spec at the PR (patchPRStatusSpec). The PRStatusReconciler polls the SCM
// and writes status.merged/open.
// This reconciler simply reads the CRD status — no GitHub API call here.
func (r *Reconciler) handleWaitingForMerge(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	base := ps.DeepCopy()
	pipeline, err := r.loadPipeline(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load pipeline: %w", err)
	}

	// The environment is held on another Bundle's rollback (spec.holds,
	// #1528): this PR would deploy over it once merged, so the promotion is
	// cancelled as supersession cancels one, with its PR closed and a comment
	// saying why. A step already HealthChecking has merged; it finishes.
	if h := lifecycle.HeldFrom(pipeline, ps.Spec.Environment, ps.Spec.BundleName); h != nil {
		res, _, err := r.cancelStep(ctx, log, ps, lifecycle.HeldMessage(ps.Spec.PipelineName, h),
			fmt.Sprintf("environment %s is held on rollback %s (%s), so kardinal cancelled this promotion; "+
				"once the hold is released, a newer Bundle promotes here", h.Environment, h.Bundle, h.Reason),
			AuditActionPromotionFailed, "Superseded", false)
		return res, err
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
				if closeErr := r.closeStepPR(ctx, ps, fmt.Sprintf("it was not merged within waitForMergeTimeout (%s)", d), false); closeErr != nil {
					msg += fmt.Sprintf("; closing the PR failed (%v) — %s", closeErr, closeByHand(closeErr))
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
			// PRStatus not yet created by the Graph — requeue. The step names
			// its PRStatus literally (not through the PRStatuses collection),
			// so the step can get here first; say so in the message.
			log.Debug().Str("prStatusRef", prStatusName).Msg("PRStatus not found yet, requeueing")
			msg := fmt.Sprintf("waiting for PRStatus %s: the Graph has not created it yet "+
				"(see the Bundle's GraphReady condition)", prStatusName)
			if ps.Status.Message != msg {
				ps.Status.Message = msg
				if patchErr := r.Status().Patch(ctx, ps, client.MergeFrom(base)); patchErr != nil {
					return ctrl.Result{}, fmt.Errorf("patch waiting for prstatus: %w", patchErr)
				}
			}
			return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get prstatus %s: %w", prStatusName, err)
	}

	// The spec patch in handlePromoting is best-effort; until it lands the
	// PRStatusReconciler has no PR to poll and the step would wait forever
	// (C03-promotionstep-07). A spec that names another PR, from before the
	// step was recreated, is patched too, and the status the PRStatus
	// reconciler wrote for that PR is not read: it polled a closed PR (B72).
	if !prStatusOfStepPR(&prs, ps.Status.Outputs) {
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
		// PR duration: from the PR opening to the merge seen here. Not from
		// the PRStatus creationTimestamp: the Graph creates the PRStatus with
		// the Bundle, before the upstream environments and the gates. Read
		// before the transition, which closes the step statuses, and observed
		// only once the transition is patched: a failed patch is retried by
		// a later reconcile, and a deleted step does not transition.
		opened, openedKnown := prOpenedAt(ps)
		moved, err := r.transitionClosing(ctx, base, ps, StateHealthChecking,
			fmt.Sprintf("PR #%d merged", prs.Spec.PRNumber), "", nil)
		if err != nil {
			return ctrl.Result{}, err
		}
		if moved && openedKnown {
			observability.PRDurationSeconds.Observe(max(time.Since(opened), 0).Seconds())
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

	// An open PR follows its base branch: when the base moved, the PR
	// branch is rebuilt on the new head (refreshPRBranch). Not while the
	// Pipeline is paused: that holds every git write.
	if prs.Status.Open && !pipeline.Spec.Paused {
		wrote, err := r.refreshPRBranch(ctx, log, base, ps, pipeline, env)
		if err != nil {
			return ctrl.Result{}, err
		}
		if wrote {
			return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
		}
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
		msg = withLabelsError(fmt.Sprintf("PR #%d is open, waiting for merge", prs.Spec.PRNumber), ps.Status.Outputs)
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
// that opened a PR is Progressing until the merge commit is known (#1307),
// and an argocd one until it is known or the PRStatus records that it will
// not be (status.mergeCommitUnavailable, B80).
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
	if msg, err := r.repositoryNotAllowed(ctx, pipeline); err != nil {
		return ctrl.Result{}, err
	} else if msg != "" {
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, msg)
	}

	if r.HealthDetector == nil {
		// Both binaries always set HealthDetector; only unit tests of the
		// earlier phases run without one.
		return ctrl.Result{}, r.passHealth(ctx, base, ps, "Verified", "health check skipped: no health adapter configured")
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
		if deadline, ok := bakeDeadline(ps, env, timeout); ok && !time.Now().Before(deadline) {
			msg = bakeDeadlineMessage(ps, env, timeout)
		}
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
	var mergeCommitPending bool
	opts.ExpectedRevision, mergeCommitPending = r.expectedRevision(ctx, log, ps)
	recordMergeCommit(ps, opts.ExpectedRevision)
	for _, img := range bundle.Spec.Images {
		opts.ExpectedImages = append(opts.ExpectedImages,
			health.ImageExpectation{Repository: img.Repository, Tag: img.Tag, Digest: img.Digest})
	}
	opts.ImagesOnly = bundle.Spec.Type == "image"
	opts.Since = healthCheckStart(ps)
	opts.ChangedAt = changeReachedGitAfter(ps)
	if at := ps.Status.TargetUpdatedAt; at != nil {
		opts.TargetUpdatedAt = at.Time
	}

	detector, remote, unreachable, err := r.healthDetector(ctx, log, ps, env)
	if err != nil {
		// A refused kubeconfig: waiting does not fix it.
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, err.Error())
	}
	if unreachable != "" {
		return r.clusterUnreachable(ctx, log, base, ps, env, health.EffectiveType(env), unreachable, timeout)
	}
	adapter, err := detector.Select(ctx, opts.Type)
	if err != nil {
		// Only an unknown type reaches here; admission rejects it.
		return ctrl.Result{}, r.transition(ctx, base, ps, StateFailed, err.Error())
	}

	// With no changes there is no PR and no merge commit, and the previous
	// commit already is the target. Whether there is a PR follows from the
	// recorded sequence, not the live approval: a step that pushed straight
	// to the base branch has no merge commit to wait for.
	noMergeCommit := opts.ExpectedRevision == "" && opensPR(ps) && ps.Status.Outputs["noChanges"] != "true"
	var result health.HealthStatus
	var checkErr error
	switch {
	case noMergeCommit && adapter.Name() == "flux":
		// The flux adapter has no image check to fall back on: without the
		// merge commit, a Kustomization Ready on the previous commit would
		// pass. Wait for it; health.timeout ends the wait (#1307).
		result = health.HealthStatus{Progressing: true,
			Reason: "merge commit of the PR not known yet (needed to check lastAppliedRevision)"}
	case noMergeCommit && mergeCommitPending && adapter.Name() == "argocd":
		// The argocd adapter falls back to status.summary.images without a
		// commit, which does not show that Argo CD synced the merge. A
		// webhook can mark the PR merged before the merge commit is known
		// (B80), so wait while the PRStatus can still record it; it sets
		// status.mergeCommitUnavailable when it stops trying, and the images
		// decide from then on. health.timeout ends the wait. The resource,
		// argoRollouts and flagger adapters check images only and do not wait.
		result = health.HealthStatus{Progressing: true,
			Reason: "merge commit of the PR not known yet (needed to check the synced revision)"}
	default:
		result, checkErr = adapter.Check(ctx, opts)
	}
	if checkErr != nil && remote {
		// An unreachable cluster is not an unhealthy workload: no failure is
		// counted, health.timeout still applies.
		log.Warn().Err(checkErr).Str("adapter", adapter.Name()).Msg("remote cluster health check error")
		return r.clusterUnreachable(ctx, log, base, ps, env, adapter.Name(), health.ClassifyRemoteError(checkErr), timeout)
	}
	if checkErr != nil {
		log.Error().Err(checkErr).Str("adapter", adapter.Name()).Msg("health adapter check error")
		return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil
	}
	checkedAt := metav1.NewTime(time.Now())
	ps.Status.LastHealthCheckAt = &checkedAt
	// The first check that finds the target on the Bundle images dates the
	// update: the flagger check counts a Failed phase only when set later, and
	// so does the resource check a ProgressDeadlineExceeded when the
	// ReplicaSet it names does not decide. Every path below patches the
	// status.
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
		return ctrl.Result{}, r.passHealth(ctx, base, ps, "Verified",
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
//
// pending reports that the revision is not known yet but may still be: the
// PRStatus is merged with neither status.mergeCommitSHA nor
// status.mergeCommitUnavailable set, so the PRStatusReconciler is still
// asking the SCM provider. A PRStatus that cannot be read (other than not
// found) counts as pending too, so a cache error never turns into an image
// fallback. A deleted PRStatus records nothing more, and neither does one
// that does not describe the step's PR (B72): the step leaves WaitingForMerge
// only once its PRStatus does, and only Promoting and WaitingForMerge point
// the spec at the step's PR, so such a PRStatus was deleted and recreated
// (kro recreates it as a placeholder) or edited by hand.
func (r *Reconciler) expectedRevision(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (rev string, pending bool) {
	if sha := ps.Status.Outputs["commitSHA"]; sha != "" {
		return sha, false
	}
	if sha := ps.Status.Outputs["mergeCommitSHA"]; sha != "" {
		return sha, false
	}
	if ps.Spec.PRStatusRef == "" {
		return "", false
	}
	// The PRStatusReconciler may record the merge commit shortly after the merge.
	var prs v1alpha1.PRStatus
	if err := r.Get(ctx, types.NamespacedName{Name: ps.Spec.PRStatusRef, Namespace: ps.Namespace}, &prs); err != nil {
		if apierrors.IsNotFound(err) {
			return "", false
		}
		log.Warn().Err(err).Str("prStatusRef", ps.Spec.PRStatusRef).Msg("could not read the PRStatus for the merge commit")
		return "", true
	}
	if !prStatusOfStepPR(&prs, ps.Status.Outputs) || !prs.Status.Merged {
		return "", false
	}
	if prs.Status.MergeCommitSHA != "" {
		return prs.Status.MergeCommitSHA, false
	}
	return "", !prs.Status.MergeCommitUnavailable
}

// recordMergeCommit sets status.outputs.mergeCommitSHA to rev, the revision
// expectedRevision returned, when the outputs have neither a pushed commit
// nor a merge commit: rev then came from the PRStatus. The step copies the
// merge commit when it leaves WaitingForMerge, but a webhook can mark the PR
// merged before the merge commit is known (the Bitbucket and Azure DevOps
// events and a GitLab fast-forward merge have none), and the PRStatus
// reconciler records it later. The health-check paths that
// follow patch the status, except an adapter error, which requeues without a
// patch: the merge commit is then recorded by the next check that reaches a
// result.
func recordMergeCommit(ps *v1alpha1.PromotionStep, rev string) {
	if rev == "" || ps.Status.Outputs["commitSHA"] != "" || ps.Status.Outputs["mergeCommitSHA"] != "" {
		return
	}
	if ps.Status.Outputs == nil {
		ps.Status.Outputs = map[string]string{}
	}
	ps.Status.Outputs["mergeCommitSHA"] = rev
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

// changeReachedGitAfter is the earliest time the promoted change can have
// reached the environment's branch: when the promotion PR was opened (the
// merge is later; the SCM's merge time is not recorded), else when git-push
// started. Zero when the step statuses record neither, or when the
// promotion changed nothing in git.
func changeReachedGitAfter(ps *v1alpha1.PromotionStep) time.Time {
	if ps.Status.Outputs["noChanges"] == "true" {
		return time.Time{}
	}
	if opened, ok := prOpenedAt(ps); ok {
		return opened
	}
	for _, s := range ps.Status.Steps {
		if s.Name == "git-push" && s.StartedAt != nil {
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
// A release that keeps flapping (healthy, then an alarm, within every
// health.timeout) would re-arm that timeout forever. So the step must
// complete one full window by a deadline: status.bakeFirstStartedAt (the
// first window's start, never reset) + bake.maxDuration (default
// bake.minutes + health.timeout). A window that stops at or after the
// deadline, on an alarm or on a Waiting result such as a paused canary,
// applies onHealthFailure, and a stopped window's HealthCheckExpiry never
// passes the deadline (#1423). A window running at the deadline may still
// complete.
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
	if ps.Status.BakeFirstStartedAt == nil && ps.Status.BakeStartedAt != nil {
		// A window that started before the field existed.
		first := *ps.Status.BakeStartedAt
		ps.Status.BakeFirstStartedAt = &first
	}
	// stopWindow stops a running window; the time to the next Healthy result
	// is bounded by health.timeout again.
	stopWindow := func() {
		expiry := metav1.NewTime(now.Add(timeout))
		if deadline, ok := bakeDeadline(ps, env, timeout); ok && deadline.Before(expiry.Time) {
			expiry = metav1.NewTime(deadline)
		}
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
		if pastBakeDeadline(ps, env, timeout, now.Time) {
			ps.Status.ConsecutiveHealthFailures++
			return r.applyHealthFailurePolicy(ctx, log, base, ps, env, adapterName,
				bakeDeadlineMessage(ps, env, timeout)+"; last result: "+result.Reason)
		}
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
		if pastBakeDeadline(ps, env, timeout, now.Time) {
			// A release that keeps flapping never completes a window: the
			// deadline from the first window ends it (#1423).
			return r.applyHealthFailurePolicy(ctx, log, base, ps, env, adapterName,
				bakeDeadlineMessage(ps, env, timeout)+"; last result: "+result.Reason)
		}
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
			if ps.Status.BakeFirstStartedAt == nil {
				ps.Status.BakeFirstStartedAt = &now
			}
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
			msg := fmt.Sprintf("bake complete: %dm contiguous healthy via %s (resets=%d)",
				env.Bake.Minutes, adapterName, ps.Status.BakeResets)
			if verifies(ps) {
				return ctrl.Result{}, r.passHealth(ctx, base, ps, "BakeComplete", msg)
			}
			ps.Status.Conditions = appendCondition(ps.Status.Conditions,
				"Verified", metav1.ConditionTrue, "BakeComplete",
				fmt.Sprintf("contiguous soak %dm complete", env.Bake.Minutes), now.Time)
			return ctrl.Result{}, r.transition(ctx, base, ps, StateVerified, msg)
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

// bakeDeadline is the time by which a step with env.bake must complete one
// full window: the first window's start + bakeMaxDuration. ok is false
// before the first window started or without a bake. A step whose window
// started before status.bakeFirstStartedAt existed takes the running
// window's start (handleBake records it).
func bakeDeadline(ps *v1alpha1.PromotionStep, env v1alpha1.EnvironmentSpec, timeout time.Duration) (time.Time, bool) {
	if env.Bake == nil {
		return time.Time{}, false
	}
	first := ps.Status.BakeFirstStartedAt
	if first == nil {
		first = ps.Status.BakeStartedAt
	}
	if first == nil {
		return time.Time{}, false
	}
	return first.Add(bakeMaxDuration(env, timeout)), true
}

// bakeMaxDuration is bake.maxDuration, or bake.minutes + health.timeout
// when it is unset or not a duration (admission checks the format). A value
// shorter than one window counts as one window.
func bakeMaxDuration(env v1alpha1.EnvironmentSpec, timeout time.Duration) time.Duration {
	window := time.Duration(env.Bake.Minutes) * time.Minute
	if d, err := time.ParseDuration(env.Bake.MaxDuration); env.Bake.MaxDuration != "" && err == nil && d > 0 {
		return max(d, window)
	}
	return window + timeout
}

// pastBakeDeadline reports that now is at or after the bake deadline.
func pastBakeDeadline(ps *v1alpha1.PromotionStep, env v1alpha1.EnvironmentSpec, timeout time.Duration, now time.Time) bool {
	deadline, ok := bakeDeadline(ps, env, timeout)
	return ok && !now.Before(deadline)
}

// bakeDeadlineMessage says that the bake deadline passed.
func bakeDeadlineMessage(ps *v1alpha1.PromotionStep, env v1alpha1.EnvironmentSpec, timeout time.Duration) string {
	source := "bake.minutes + health.timeout"
	if env.Bake.MaxDuration != "" {
		source = "bake.maxDuration"
	}
	return fmt.Sprintf("bake: no %dm contiguous healthy window within %s of the first healthy check (%s; resets=%d)",
		env.Bake.Minutes, bakeMaxDuration(env, timeout), source, ps.Status.BakeResets)
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
	b := ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.Workers}).
		For(&v1alpha1.PromotionStep{}, builderutil.WithPredicates(
			predicate.Or(predicate.GenerationChangedPredicate{},
				eventfilter.LabelChangedExceptKro, predicate.AnnotationChangedPredicate{}),
		)).
		Watches(&v1alpha1.PRStatus{}, handler.EnqueueRequestsFromMapFunc(r.prStatusMapper)).
		Watches(&v1alpha1.PolicyGate{}, handler.EnqueueRequestsFromMapFunc(r.policyGateMapper)).
		Watches(&v1alpha1.Bundle{}, handler.EnqueueRequestsFromMapFunc(r.bundleMapper),
			builderutil.WithPredicates(bundleWakesSteps)).
		// A hold added or released (spec.holds) takes effect on the held
		// environment's steps at once (holdIfEnvironmentHeld).
		Watches(&v1alpha1.Pipeline{}, handler.EnqueueRequestsFromMapFunc(r.pipelineHoldMapper),
			builderutil.WithPredicates(holdsChanged))
	return shard.Active().Complete(b, tracing.WrapReconciler("promotionstep", r), &v1alpha1.PromotionStepList{})
}

// holdsChanged passes Pipeline updates that change spec.holds.
var holdsChanged = predicate.Funcs{
	CreateFunc:  func(event.CreateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
	UpdateFunc: func(e event.UpdateEvent) bool {
		o, ok1 := e.ObjectOld.(*v1alpha1.Pipeline)
		n, ok2 := e.ObjectNew.(*v1alpha1.Pipeline)
		return ok1 && ok2 && !equality.Semantic.DeepEqual(o.Spec.Holds, n.Spec.Holds)
	},
}

// pipelineHoldMapper wakes the unfinished steps of a Pipeline whose holds
// changed.
func (r *Reconciler) pipelineHoldMapper(ctx context.Context, obj client.Object) []reconcile.Request {
	var stepList v1alpha1.PromotionStepList
	if err := r.List(ctx, &stepList, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range stepList.Items {
		s := &stepList.Items[i]
		if s.Spec.PipelineName != obj.GetName() {
			continue
		}
		switch s.Status.State {
		case StatePending, "Pending", StatePromoting, StateWaitingForMerge:
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(s)})
		}
	}
	return reqs
}

// isHalted passes Bundle events of superseded and rejected Bundles.
func isHalted(obj client.Object) bool {
	b, ok := obj.(*v1alpha1.Bundle)
	return ok && haltOf(b) != nil
}

// bundleMapper wakes the unfinished PromotionSteps of a superseded or
// rejected Bundle so the supersession guard closes their PRs at once, and the
// steps of a Bundle whose maxConcurrentPromotions hold was set or lifted
// (holdForSlot). Without it a step in WaitingForMerge saw the new phase only
// at its next poll
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

// patchPRStatusSpec points the spec of the companion PRStatus at the PR in the
// open-pr step outputs. It patches only when the spec names another PR (or
// none): a step recreated after its PR was closed opens a new PR, and a spec
// left on the old one polled that closed PR, failed the step after the grace
// window and left the new PR open with nothing tracking it (B72). The PRStatus
// reconciler then clears the old PR's status (prstatus.DescribesSpec).
func (r *Reconciler) patchPRStatusSpec(ctx context.Context, ps *v1alpha1.PromotionStep, outputs map[string]string) error {
	var prs v1alpha1.PRStatus
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ps.Spec.PRStatusRef,
		Namespace: ps.Namespace,
	}, &prs); err != nil {
		return fmt.Errorf("get prstatus %s: %w", ps.Spec.PRStatusRef, err)
	}
	want, ok := prSpecFromOutputs(outputs)
	if !ok || specNamesPR(prs.Spec, want) {
		return nil
	}
	patch := client.MergeFrom(prs.DeepCopy())
	prs.Spec = want
	if err := r.Patch(ctx, &prs, patch); err != nil {
		return fmt.Errorf("patch prstatus spec %s: %w", ps.Spec.PRStatusRef, err)
	}
	return nil
}

// prSpecFromOutputs is the PRStatus spec for the PR in the open-pr outputs,
// and false when they have no PR.
func prSpecFromOutputs(outputs map[string]string) (v1alpha1.PRStatusSpec, bool) {
	prURL := outputs["prURL"]
	if prURL == "" {
		return v1alpha1.PRStatusSpec{}, false
	}
	prNum, err := strconv.Atoi(outputs["prNumber"])
	if err != nil || prNum == 0 {
		prNum = extractPRNumber(prURL)
	}
	return v1alpha1.PRStatusSpec{PRURL: prURL, PRNumber: prNum, Repo: extractRepo(prURL)}, true
}

// specNamesPR reports whether spec names the PR of want: the same number and
// URL. A spec without a URL, which patchPRStatusSpec never writes, is matched
// on the number.
func specNamesPR(spec, want v1alpha1.PRStatusSpec) bool {
	return spec.PRNumber == want.PRNumber && (spec.PRURL == "" || spec.PRURL == want.PRURL)
}

// prStatusOfStepPR reports whether the PRStatus spec names the PR the step
// opened (the open-pr outputs), and its status describes that spec. Before
// patchPRStatusSpec lands after a recreated step opened a new PR, and until
// the PRStatus reconciler cleared the old PR's status, the PRStatus still
// reports the old PR. A step whose outputs have no PR takes the PRStatus as
// it is.
func prStatusOfStepPR(prs *v1alpha1.PRStatus, outputs map[string]string) bool {
	want, ok := prSpecFromOutputs(outputs)
	return (!ok || specNamesPR(prs.Spec, want)) && prstatus.DescribesSpec(prs)
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

// stepSequence is the step list a step of env runs for bundle, recorded in
// status.steps when the step starts.
func stepSequence(env v1alpha1.EnvironmentSpec, bundle *v1alpha1.Bundle) []string {
	return steps.DefaultSequenceForBundle(env.Approval, bundle.Spec.Type, env.Update.Strategy, env.Layout)
}

// recordedSequence returns the step names in status.steps: the sequence
// handlePending recorded on entering Promoting, which handlePromoting runs.
// Only the controller writes the status.
func recordedSequence(ps *v1alpha1.PromotionStep) []string {
	names := make([]string, 0, len(ps.Status.Steps))
	for _, s := range ps.Status.Steps {
		names = append(names, s.Name)
	}
	return names
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
// Completed or Failed sets durationMs and is returned, to be observed once in
// kardinal_step_duration_seconds after the status patch, except the engine's
// placeholder health-check step (see closeStepStatuses).
func updateStepStatuses(ps *v1alpha1.PromotionStep, stepNames []string, currentIdx int, failed bool,
	failMessage string, timings map[int]steps.StepTiming) stepObservations {
	if len(ps.Status.Steps) == 0 {
		// Steps not yet initialized (e.g. crash before initStepStatuses ran).
		// Reconstruct the Pending slice so updates have something to apply to.
		ps.Status.Steps = initStepStatuses(stepNames)
	}

	now := time.Now()
	var closed stepObservations
	// finish closes step i in state.
	finish := func(i int, step *v1alpha1.StepStatus, state v1alpha1.StepExecutionState) {
		t, ran := timings[i]
		finished := t.Finished
		if !ran {
			finished = now
			if step.CompletedAt != nil {
				finished = step.CompletedAt.Time
			}
		}
		// The engine's placeholder health-check step: closeStepStatuses
		// reopens it when the health check starts and records that duration.
		closed = append(closed, closeStep(step, state, t.Started, finished, step.Name != healthCheckStep)...)
	}
	for i := range ps.Status.Steps {
		step := &ps.Status.Steps[i]
		switch {
		case i < currentIdx:
			// Steps before the current index have completed, in this
			// reconcile or a previous one. Only update a step not already
			// marked Completed (idempotent), so each is observed once.
			if step.State != v1alpha1.StepExecutionCompleted {
				finish(i, step, v1alpha1.StepExecutionCompleted)
			}
		case i == currentIdx && failed:
			if step.State != v1alpha1.StepExecutionFailed {
				finish(i, step, v1alpha1.StepExecutionFailed)
				step.Message = failMessage
			}
		case i == currentIdx:
			// This step is in progress (StepPending result means "still running").
			if step.State == v1alpha1.StepExecutionPending {
				step.State = v1alpha1.StepExecutionInProgress
				started := timings[i].Started
				if started.IsZero() {
					started = now
				}
				mt := metav1.NewTime(started)
				step.StartedAt = &mt
			}
		default:
			// Future steps: leave as Pending. When currentIdx ==
			// len(stepNames) every step is handled by the first case.
		}
	}
	return closed
}

// healthCheckStep is the last step of every sequence. The engine runs a
// placeholder of it; the health check itself is the HealthChecking state.
const healthCheckStep = "health-check"

// closeStep moves a step to Completed or Failed at finished and sets its
// durationMs. started is the start to use when the step has no startedAt; a
// zero started means the start is unknown (the step never ran, or ran in a
// reconcile whose status was lost), so the step starts at finished. Unless
// observe is false or the start is unknown, it returns the duration to
// observe in kardinal_step_duration_seconds once the status patch succeeds.
// Callers close a step once, so each step is observed once.
func closeStep(s *v1alpha1.StepStatus, state v1alpha1.StepExecutionState, started, finished time.Time, observe bool) stepObservations {
	s.State = state
	known := s.StartedAt != nil || !started.IsZero()
	if s.StartedAt == nil {
		if started.IsZero() {
			started = finished
		}
		st := metav1.NewTime(started)
		s.StartedAt = &st
	}
	ct := metav1.NewTime(finished)
	s.CompletedAt = &ct
	d := max(finished.Sub(s.StartedAt.Time), 0)
	s.DurationMs = d.Milliseconds()
	if observe && known {
		return stepObservations{{step: s.Name, duration: d}}
	}
	return nil
}

// stepObservations are kardinal_step_duration_seconds samples of the steps a
// reconcile closed in memory. They are recorded only after the status patch
// that closes the steps succeeds: a failed patch is retried by a later
// reconcile, which closes the same steps again, so observing them before the
// patch counted them twice.
type stepObservations []stepObservation

type stepObservation struct {
	step     string
	duration time.Duration
}

// record observes each sample.
func (o stepObservations) record() {
	for _, s := range o {
		observability.StepDurationSeconds.WithLabelValues(s.step).Observe(s.duration.Seconds())
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
		// Prefer the template's name and namespace over the generated
		// instance's: an org gate's instance lives in the Pipeline namespace,
		// but a reviewer looks for the template (#1581).
		if tmpl := g.Labels["kardinal.io/gate-name"]; tmpl != "" {
			gr.GateName = tmpl
		}
		if tmplNS := g.Labels[graph.LabelGateTemplateNamespace]; tmplNS != "" {
			gr.GateNamespace = tmplNS
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
	state.CreatedBy = bundle.Annotations[lifecycle.AnnotationCreatedBy]
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

// labelReferenceable must be "true" on a kubeconfig Secret that
// health.kubeconfigSecretRef names (program-wide rule for Secrets kardinal
// sends to an address someone else chose).
const labelReferenceable = "kardinal.io/referenceable"

// healthDetector returns the adapters for env's health check: the
// controller's own cluster, or with health.kubeconfigSecretRef the cluster of
// that kubeconfig (remote true). unreachable is set, with a nil error, when
// the remote cluster cannot be used yet (its Secret or key is missing, or the
// Secret is not labelled referenceable), so the step waits until
// health.timeout. err is a kubeconfig that is refused.
func (r *Reconciler) healthDetector(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep,
	env v1alpha1.EnvironmentSpec) (d *health.AutoDetector, remote bool, unreachable string, err error) {
	ref := env.Health.KubeconfigSecretRef
	if ref == nil {
		return r.HealthDetector, false, "", nil
	}
	if r.RemoteClusters == nil {
		return nil, true, "", fmt.Errorf("health.kubeconfigSecretRef is not supported by this controller")
	}
	// The Secret is always read from the step's (the Pipeline's) namespace:
	// a Pipeline cannot use another namespace's credentials.
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ps.Namespace, Name: ref.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			r.RemoteClusters.Forget(ps.Namespace, ref.Name)
			return nil, true, fmt.Sprintf("kubeconfig Secret %q not found", ref.Name), nil
		}
		log.Warn().Err(err).Str("secret", ref.Name).Msg("read kubeconfig Secret")
		return nil, true, fmt.Sprintf("kubeconfig Secret %q could not be read", ref.Name), nil
	}
	if secret.Labels[labelReferenceable] != "true" {
		r.RemoteClusters.Forget(ps.Namespace, ref.Name)
		return nil, true, fmt.Sprintf("SecretNotReferenceable: kubeconfig Secret %q does not have the label %s: \"true\"",
			ref.Name, labelReferenceable), nil
	}
	d, err = r.RemoteClusters.Detector(&secret, ref.Key)
	switch {
	case errors.Is(err, health.ErrKubeconfigNotAllowed):
		return nil, true, "", fmt.Errorf("health.kubeconfigSecretRef %q: %w", ref.Name, err)
	case err != nil:
		return nil, true, err.Error(), nil
	}
	return d, true, "", nil
}

// clusterUnreachable records that the remote cluster could not be checked,
// without counting a health failure, and checks again later. reason is a
// classified, short text (health.ClassifyRemoteError): the full error is
// logged, never written to status. The step still fails at health.timeout.
// During a bake the check counts as waiting: the window stops (the time the
// cluster was unreachable is not healthy time) and health.timeout bounds the
// wait for the next healthy check again.
func (r *Reconciler) clusterUnreachable(ctx context.Context, log zerolog.Logger, base, ps *v1alpha1.PromotionStep,
	env v1alpha1.EnvironmentSpec, adapter, reason string, timeout time.Duration) (ctrl.Result, error) {
	checkedAt := metav1.NewTime(time.Now())
	ps.Status.LastHealthCheckAt = &checkedAt
	if env.Bake != nil {
		return r.handleBake(ctx, log, base, ps, env,
			health.HealthStatus{Progressing: true, Reason: "ClusterUnreachable: " + reason}, adapter, timeout)
	}
	ps.Status.Message = fmt.Sprintf("waiting for %s: ClusterUnreachable: %s", adapter, reason)
	if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch health result: %w", err)
	}
	return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil
}
