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
//   - PRStatus CRs are created by the open-pr step (or by the webhook on
//     the first merged-PR event) with spec.prURL, spec.prNumber, spec.repo.
//   - This reconciler polls GitHub via SCM.GetPRStatus and writes
//     status.merged, status.open, status.lastCheckedAt.
//   - The Graph Watch node reads status.merged to advance the Graph DAG,
//     replacing the old polling loop in handleWaitingForMerge.
//   - Idempotent: reconciling a PRStatus with status.merged=true is a no-op.
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
	// requeuePollInterval is how often to re-check an open PR.
	requeuePollInterval = 30 * time.Second
)

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

	// Terminal: already merged — nothing to poll.
	if prs.Status.Merged {
		log.Debug().Str("prURL", prs.Spec.PRURL).Msg("PR already merged, no-op")
		return ctrl.Result{}, nil
	}

	// Terminal: PR is closed (not open, not merged) — nothing to poll.
	if prs.Status.LastCheckedAt != nil && !prs.Status.Open && !prs.Status.Merged {
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

	merged, open, err := r.SCM.GetPRStatus(ctx, prs.Spec.Repo, prs.Spec.PRNumber)
	if err != nil {
		log.Error().Err(err).
			Str("prURL", prs.Spec.PRURL).
			Int("prNumber", prs.Spec.PRNumber).
			Msg("GetPRStatus failed, will retry")
		// Non-fatal: requeue to retry.
		return ctrl.Result{RequeueAfter: requeuePollInterval}, nil
	}

	// Write results to status — this is the only CRD status this reconciler writes.
	now := metav1.NewTime(time.Now().UTC())
	patch := client.MergeFrom(prs.DeepCopy())
	prs.Status.Merged = merged
	prs.Status.Open = open
	prs.Status.LastCheckedAt = &now

	if err := r.Status().Patch(ctx, &prs, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch prstatus %s: %w", req.Name, err)
	}

	if merged {
		log.Info().
			Str("prURL", prs.Spec.PRURL).
			Int("prNumber", prs.Spec.PRNumber).
			Msg("PR merged — status updated")
		return ctrl.Result{}, nil
	}

	if !open {
		log.Info().
			Str("prURL", prs.Spec.PRURL).
			Int("prNumber", prs.Spec.PRNumber).
			Msg("PR closed without merge — status updated")
		return ctrl.Result{}, nil
	}

	// Still open — requeue to poll again.
	log.Debug().
		Str("prURL", prs.Spec.PRURL).
		Int("prNumber", prs.Spec.PRNumber).
		Dur("requeue", requeuePollInterval).
		Msg("PR still open, requeueing")
	return ctrl.Result{RequeueAfter: requeuePollInterval}, nil
}

// SetupWithManager registers the PRStatus reconciler with controller-runtime.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PRStatus{}).
		Complete(r)
}
