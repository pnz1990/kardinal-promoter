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

// Package prstatus implements the PRStatus reconciler.
//
// Architecture:
//   - The Graph creates each PRStatus as a placeholder with an empty spec,
//     next to the PromotionStep it belongs to. Once the open-pr step has
//     opened the PR, the PromotionStep reconciler fills in spec.prURL,
//     spec.prNumber and spec.repo (patchPRStatusSpec), and retries that until
//     it lands.
//   - This reconciler polls the SCM provider (GetPRStatus, GetPRReviewStatus)
//     and writes status.merged, status.open, status.approved,
//     status.approvalCount and status.lastCheckedAt. When the PR is merged it
//     also records status.mergeCommitSHA, which the health check requires the
//     GitOps tool to have deployed.
//   - A poll error that a retry cannot fix (401, 403 that is not a rate limit,
//     404, 410) is written to status.pollError and the PR is polled again
//     every 5 minutes; the PromotionStep waiting for the PR fails with it.
//     Transient errors (429, 5xx, network) are retried every 30 seconds.
//   - The PromotionStep reconciler watches PRStatus and advances from
//     WaitingForMerge when status.merged is true. The SCM webhook may set
//     status.merged first, with status.mergeCommitSHA for GitHub and GitLab;
//     this reconciler then only fills in a missing merge commit.
//   - PolicyGate CEL reads the approval state as bundle.pr["<env>"].isApproved
//     and bundle.pr["<env>"].approvalCount (K-08).
//   - Polling is throttled: at most one poll per requeuePollInterval, and the
//     status is patched only when a polled value changed or lastCheckedAt is
//     older than lastCheckedRefresh. Each patch re-enqueues the object, so
//     patching every poll made it poll in a tight loop (C03-promotionstep-20).
//   - A PR closed without merging is polled for ClosedGracePeriod after the
//     first poll that saw it closed, which records that time in
//     status.closedAt. A reopen clears closedAt. Once the window has passed
//     the reconciler comments on the PR once, sets status.closedFinal and
//     stops polling; only then does the PromotionStep fail (#1306).
//   - Idempotent: a merged PR whose merge commit is known is a no-op, and so
//     is a PR that is final-closed.
//
// Graph-purity: eliminates PS-4, SCM-2, ST-10, ST-11, BU-3, WH-1.
package prstatus

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const (
	// requeuePollInterval is how often to re-check an open PR. It is also the
	// minimum time between two polls of the same PR.
	requeuePollInterval = 30 * time.Second
	// lastCheckedRefresh is how stale status.lastCheckedAt may get before an
	// unchanged poll still patches it, so readers can tell polling is alive.
	lastCheckedRefresh = 5 * time.Minute
	// mergeCommitWindow bounds how long after the merge was recorded the
	// reconciler keeps asking the SCM for the merge commit.
	mergeCommitWindow = 10 * time.Minute
	// permanentErrorInterval is how often a PR whose last poll failed with a
	// permanent SCM error (status.pollError) is polled again.
	permanentErrorInterval = 5 * time.Minute
	// labelEnvironment names the environment of the PRStatus; the Graph sets it.
	labelEnvironment = "kardinal.io/environment"
	// maxPollErrorLen bounds status.pollError; SCM error bodies can be pages long.
	maxPollErrorLen = 512
	// ClosedGracePeriod is how long a PR closed without merging is still
	// polled, from status.closedAt, before it is final-closed. A reopen within
	// the window keeps the promotion going.
	ClosedGracePeriod = 5 * time.Minute
)

// IsClosed reports whether the PR is closed without merging: the reconciler
// polled it (lastCheckedAt is set) and found it neither open nor merged. The
// PR may still be in its grace window; see IsClosedFinal.
func IsClosed(s *v1alpha1.PRStatusStatus) bool {
	return !s.Merged && s.LastCheckedAt != nil && !s.Open
}

// IsClosedFinal reports whether the PR is closed without merging for good:
// it stayed closed for ClosedGracePeriod (status.closedFinal), or it is
// closed without a status.closedAt, which only a release before the grace
// window wrote. The PRStatus is then no longer polled and the PromotionStep
// waiting for it fails.
func IsClosedFinal(s *v1alpha1.PRStatusStatus) bool {
	if s.Merged {
		return false
	}
	return s.ClosedFinal || (IsClosed(s) && s.ClosedAt == nil)
}

// Reconciler watches PRStatus objects and polls the SCM provider to update
// status.merged / status.open.
type Reconciler struct {
	client.Client

	// SCM is the SCM provider for PR state queries.
	// If nil the reconciler is a no-op (useful in tests that only test CRD
	// plumbing without a live GitHub connection).
	SCM scm.SCMProvider
}

// Reconcile processes one PRStatus event.
// It is idempotent: safe to re-run after a crash at any point.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("prstatus", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var prs v1alpha1.PRStatus
	if err := r.Get(ctx, req.NamespacedName, &prs); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get prstatus %s: %w", req.Name, err)
	}

	// Merged: only the merge commit may still be missing, for example when
	// the webhook recorded the merge.
	if prs.Status.Merged {
		return r.recordMergeCommit(ctx, log, &prs)
	}

	// Terminal: the PR stayed closed without merging for the grace window
	// (or an older release recorded it closed) — nothing to poll.
	if IsClosedFinal(&prs.Status) {
		log.Debug().Str("prURL", prs.Spec.PRURL).Msg("PR is closed without merge, no-op")
		return ctrl.Result{}, nil
	}

	if r.SCM == nil {
		log.Warn().Msg("no SCM configured, cannot poll PR status")
		return ctrl.Result{RequeueAfter: requeuePollInterval}, nil
	}

	// Placeholder guard: PRNumber=0 means the open-pr step has not yet run.
	// The Graph creates a PRStatus Watch node as a placeholder before the PR exists.
	// Do not call SCM with prNumber=0 — that would cause a 404 from GitHub (#276).
	if prs.Spec.PRNumber == 0 {
		log.Debug().Msg("PRStatus placeholder (prNumber=0), waiting for open-pr step")
		return ctrl.Result{RequeueAfter: requeuePollInterval}, nil
	}

	// Throttle: a reconcile triggered by our own status patch (or any other
	// event) within the poll interval waits for the rest of it.
	if last := prs.Status.LastCheckedAt; last != nil {
		if wait := requeuePollInterval - time.Since(last.Time); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}

	merged, open, err := r.SCM.GetPRStatus(ctx, prs.Spec.Repo, prs.Spec.PRNumber)
	if err != nil {
		if scm.IsPermanentError(err) {
			return r.recordPollError(ctx, log, &prs, err)
		}
		log.Error().Err(err).
			Str("prURL", prs.Spec.PRURL).
			Int("prNumber", prs.Spec.PRNumber).
			Msg("GetPRStatus failed, will retry")
		// Transient (429, 5xx, network): requeue to retry.
		return ctrl.Result{RequeueAfter: requeuePollInterval}, nil
	}

	// Poll review approval state (K-08: PR review gate).
	// Non-fatal on error — keep the previous values.
	approved, approvalCount, reviewErr := r.SCM.GetPRReviewStatus(ctx, prs.Spec.Repo, prs.Spec.PRNumber)
	if reviewErr != nil {
		log.Warn().Err(reviewErr).
			Str("prURL", prs.Spec.PRURL).
			Int("prNumber", prs.Spec.PRNumber).
			Msg("GetPRReviewStatus failed (non-fatal), keeping the previous approval state")
		approved = prs.Status.Approved
		approvalCount = prs.Status.ApprovalCount
	}

	mergeSHA := prs.Status.MergeCommitSHA
	if merged && mergeSHA == "" {
		mergeSHA = r.fetchMergeCommit(ctx, log, &prs)
	}

	// now is written to status.lastCheckedAt whenever it decides something:
	// the grace window compares it with the stored status.closedAt.
	now := metav1.NewTime(time.Now().UTC())
	closedAt, closedFinal := closedState(&prs.Status, merged, open, now)
	if closedFinal {
		// Best-effort, before closedFinal is written: a crash in between
		// can post the comment twice, never zero times.
		r.commentStoppedTracking(ctx, log, &prs)
	}
	changed := merged != prs.Status.Merged || open != prs.Status.Open ||
		approved != prs.Status.Approved || approvalCount != prs.Status.ApprovalCount ||
		mergeSHA != prs.Status.MergeCommitSHA || prs.Status.PollError != "" ||
		!closedAt.Equal(prs.Status.ClosedAt) || closedFinal != prs.Status.ClosedFinal
	stale := prs.Status.LastCheckedAt == nil || now.Sub(prs.Status.LastCheckedAt.Time) >= lastCheckedRefresh
	if changed || stale {
		// This is the only CRD status this reconciler writes.
		patch := client.MergeFrom(prs.DeepCopy())
		prs.Status.Merged = merged
		prs.Status.Open = open
		prs.Status.Approved = approved
		prs.Status.ApprovalCount = approvalCount
		prs.Status.MergeCommitSHA = mergeSHA
		prs.Status.PollError = ""
		prs.Status.ClosedAt = closedAt
		prs.Status.ClosedFinal = closedFinal
		prs.Status.LastCheckedAt = &now
		if err := r.Status().Patch(ctx, &prs, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch prstatus %s: %w", req.Name, err)
		}
	}

	switch {
	case merged:
		log.Info().
			Str("prURL", prs.Spec.PRURL).
			Int("prNumber", prs.Spec.PRNumber).
			Str("mergeCommit", mergeSHA).
			Msg("PR merged — status updated")
		if mergeSHA == "" && r.canGetMergeCommit() {
			return ctrl.Result{RequeueAfter: requeuePollInterval}, nil
		}
		return ctrl.Result{}, nil
	case closedFinal:
		log.Info().
			Str("prURL", prs.Spec.PRURL).
			Int("prNumber", prs.Spec.PRNumber).
			Dur("grace", ClosedGracePeriod).
			Msg("PR stayed closed without merge for the grace window — no longer tracked")
		return ctrl.Result{}, nil
	case !open:
		remaining := closedAt.Add(ClosedGracePeriod).Sub(now.Time)
		log.Info().
			Str("prURL", prs.Spec.PRURL).
			Int("prNumber", prs.Spec.PRNumber).
			Dur("remaining", remaining).
			Msg("PR closed without merge — polling until the grace window ends")
		return ctrl.Result{RequeueAfter: min(requeuePollInterval, max(remaining, time.Second))}, nil
	}

	// Still open — requeue to poll again.
	log.Debug().
		Str("prURL", prs.Spec.PRURL).
		Int("prNumber", prs.Spec.PRNumber).
		Dur("requeue", requeuePollInterval).
		Msg("PR still open, requeueing")
	return ctrl.Result{RequeueAfter: requeuePollInterval}, nil
}

// closedState returns status.closedAt and status.closedFinal after a poll
// that found the PR merged or open or neither. The first poll that sees the
// PR closed records now as closedAt; a poll that sees it open or merged
// clears it; the PR is final-closed on the first poll at or after closedAt +
// ClosedGracePeriod. now is the time this poll writes to lastCheckedAt.
func closedState(s *v1alpha1.PRStatusStatus, merged, open bool, now metav1.Time) (*metav1.Time, bool) {
	switch {
	case merged || open:
		return nil, false
	case s.ClosedAt == nil:
		return &now, false
	default:
		return s.ClosedAt, !now.Time.Before(s.ClosedAt.Add(ClosedGracePeriod))
	}
}

// commentStoppedTracking tells the PR's readers that kardinal no longer
// tracks it. The comment is best-effort: a failure is logged and the PR is
// final-closed anyway.
func (r *Reconciler) commentStoppedTracking(ctx context.Context, log zerolog.Logger, prs *v1alpha1.PRStatus) {
	env := prs.Labels[labelEnvironment]
	if env == "" {
		env = "the target environment"
	}
	// The reconciler cannot tell who closed the PR (a person, or kardinal
	// when the step was superseded or timed out), so the text holds for both.
	body := fmt.Sprintf("kardinal stopped tracking this PR: it has been closed without merging for %s. "+
		"Merging it would change environment %s without a PromotionStep tracking it. "+
		"To promote again, create a new Bundle.", ClosedGracePeriod, env)
	if err := r.SCM.CommentOnPR(ctx, prs.Spec.Repo, prs.Spec.PRNumber, body); err != nil {
		log.Warn().Err(err).Int("pr", prs.Spec.PRNumber).
			Msg("could not comment on the closed PR (non-fatal)")
	}
}

// recordPollError records a GetPRStatus error that polling again cannot fix
// (scm.IsPermanentError: 401, 403 that is not a rate limit, 404, 410) in
// status.pollError, where the PromotionStep waiting for the PR reads it and
// fails. The PR is polled again every permanentErrorInterval, not every
// requeuePollInterval, so a rotated token is still picked up. Only the error
// is written: lastCheckedAt stays the time of the last successful poll, since
// a set lastCheckedAt with open=false means "closed without merging".
func (r *Reconciler) recordPollError(ctx context.Context, log zerolog.Logger, prs *v1alpha1.PRStatus, pollErr error) (ctrl.Result, error) {
	msg := pollErr.Error()
	if len(msg) > maxPollErrorLen {
		msg = msg[:maxPollErrorLen] + "..."
	}
	log.Error().Err(pollErr).
		Str("prURL", prs.Spec.PRURL).
		Int("prNumber", prs.Spec.PRNumber).
		Dur("retry", permanentErrorInterval).
		Msg("GetPRStatus failed and a retry will not fix it; recorded in status.pollError")
	if prs.Status.PollError != msg {
		// Written only when it changes: each patch re-enqueues the object.
		patch := client.MergeFrom(prs.DeepCopy())
		prs.Status.PollError = msg
		if err := r.Status().Patch(ctx, prs, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch prstatus %s poll error: %w", prs.Name, err)
		}
	}
	return ctrl.Result{RequeueAfter: permanentErrorInterval}, nil
}

// recordMergeCommit fills in status.mergeCommitSHA of a merged PR. It retries
// for mergeCommitWindow after the merge was recorded; after that, or when the
// SCM provider cannot report merge commits, the argocd and resource health
// checks fall back to checking the Bundle images; the flux check has no such
// fallback and waits until health.timeout (see docs/health-adapters.md).
func (r *Reconciler) recordMergeCommit(ctx context.Context, log zerolog.Logger, prs *v1alpha1.PRStatus) (ctrl.Result, error) {
	if prs.Status.MergeCommitSHA != "" || prs.Spec.PRNumber == 0 || !r.canGetMergeCommit() {
		log.Debug().Str("prURL", prs.Spec.PRURL).Msg("PR already merged, no-op")
		return ctrl.Result{}, nil
	}
	if last := prs.Status.LastCheckedAt; last != nil && time.Since(last.Time) > mergeCommitWindow {
		log.Debug().Str("prURL", prs.Spec.PRURL).Msg("merge commit not reported by the SCM, giving up")
		return ctrl.Result{}, nil
	}
	sha := r.fetchMergeCommit(ctx, log, prs)
	if sha == "" {
		return ctrl.Result{RequeueAfter: requeuePollInterval}, nil
	}
	patch := client.MergeFrom(prs.DeepCopy())
	prs.Status.MergeCommitSHA = sha
	if err := r.Status().Patch(ctx, prs, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch prstatus %s merge commit: %w", prs.Name, err)
	}
	return ctrl.Result{}, nil
}

// canGetMergeCommit reports whether the SCM provider can report merge commits.
func (r *Reconciler) canGetMergeCommit() bool {
	_, ok := r.SCM.(scm.MergeCommitGetter)
	return ok
}

// fetchMergeCommit asks the SCM provider for the merge commit of prs, or
// returns "" when it cannot tell.
func (r *Reconciler) fetchMergeCommit(ctx context.Context, log zerolog.Logger, prs *v1alpha1.PRStatus) string {
	getter, ok := r.SCM.(scm.MergeCommitGetter)
	if !ok {
		return ""
	}
	sha, err := getter.GetPRMergeCommit(ctx, prs.Spec.Repo, prs.Spec.PRNumber)
	if err != nil {
		log.Warn().Err(err).Int("prNumber", prs.Spec.PRNumber).Msg("could not read the merge commit, will retry")
		return ""
	}
	return sha
}

// SetupWithManager registers the PRStatus reconciler with controller-runtime.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PRStatus{}).
		Complete(r)
}
