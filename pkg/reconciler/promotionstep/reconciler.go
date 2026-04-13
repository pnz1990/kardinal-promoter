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
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	builderutil "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
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

	// requeueWaitForMerge is how often to requeue while waiting for a PR merge.
	requeueWaitForMerge = 30 * time.Second
	// requeueHealthCheck is how often to requeue during health checking.
	requeueHealthCheck = 10 * time.Second
)

// Reconciler drives the PromotionStep state machine.
//
// State transitions:
//
//	"" / "Pending" → "Promoting": initialize step sequence, set currentStepIndex=0
//	"Promoting"    → "WaitingForMerge": open-pr step completed (prURL in outputs)
//	"WaitingForMerge" → "HealthChecking": SCM reports PR merged
//	"WaitingForMerge" → "Failed": PR closed without merge
//	"HealthChecking" → "Verified": health adapter returns Healthy
//	any → "Failed": any step returns Failed
//
// The reconciler persists currentStepIndex to etcd on every step completion so that
// a crash-restart resumes from the correct step (idempotent re-execution).
type Reconciler struct {
	client.Client

	// SCM is the SCM provider for PR operations.
	SCM scm.SCMProvider

	// GitClient is the Git operations client.
	GitClient scm.GitClient

	// HealthDetector selects the health adapter for health checking.
	// If nil, the health-check step stub (always-success) is used.
	HealthDetector *health.AutoDetector

	// Shard, when set, causes the reconciler to skip PromotionSteps whose
	// kardinal.io/shard label does not match this value (distributed mode).
	Shard string

	// WorkDirFn returns the working directory for a given pipeline+bundle pair.
	// Defaults to os.MkdirTemp if nil.
	WorkDirFn func(pipelineName, bundleName string) string
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

	// Shard filtering: in sharded mode, SetupWithManager already uses a predicate
	// to prevent non-matching steps from being enqueued. This secondary guard
	// handles the edge case where events arrive before the predicate is fully active
	// (e.g. at startup) or when Shard is set but WithPredicates is not in effect.
	// Graph-purity: the primary guard is now the controller-runtime predicate.
	// See SetupWithManager and PS-3 in docs/design/11-graph-purity-tech-debt.md.
	if r.Shard != "" {
		stepShard := ps.Labels["kardinal.io/shard"]
		if stepShard != r.Shard {
			log.Debug().
				Str("step_shard", stepShard).
				Str("our_shard", r.Shard).
				Msg("shard mismatch: secondary guard — should not normally fire")
			return ctrl.Result{}, nil
		}
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

		// Supersession guard: if parent bundle is Superseded, close any open PR and
		// move active steps to Failed so they don't linger (#310).
		// Graph-first: we write only to our OWN status (PromotionStep), and close
		// the PR via the SCM provider (external I/O scoped to this reconciler's step).
		isActiveState := ps.Status.State == StateWaitingForMerge || ps.Status.State == StatePromoting
		if parentBundle.Status.Phase == "Superseded" && isActiveState {
			log.Info().
				Str("bundle", ps.Spec.BundleName).
				Str("env", ps.Spec.Environment).
				Str("state", ps.Status.State).
				Msg("parent bundle superseded — closing open PR and marking step Failed")

			// Best-effort PR close via PRStatus CRD (preferred path).
			if r.SCM != nil && ps.Spec.PRStatusRef != "" {
				var prs v1alpha1.PRStatus
				if prErr := r.Get(ctx, types.NamespacedName{
					Name:      ps.Spec.PRStatusRef,
					Namespace: ps.Namespace,
				}, &prs); prErr == nil && prs.Spec.PRNumber > 0 {
					if closeErr := r.SCM.ClosePR(ctx, prs.Spec.Repo, prs.Spec.PRNumber); closeErr != nil {
						log.Warn().Err(closeErr).
							Int("pr", prs.Spec.PRNumber).
							Msg("failed to close superseded PR (non-fatal)")
					} else {
						log.Info().Int("pr", prs.Spec.PRNumber).Msg("closed superseded PR via PRStatus")
					}
				}
			} else if r.SCM != nil {
				// Fallback: close via PR URL from outputs (covers Promoting steps that
				// opened a PR but haven't yet transitioned to WaitingForMerge).
				if prURL, ok := ps.Status.Outputs["prURL"]; ok && prURL != "" {
					repo := extractRepo(prURL)
					prNum := extractPRNumber(prURL)
					if repo != "" && prNum > 0 {
						if closeErr := r.SCM.ClosePR(ctx, repo, prNum); closeErr != nil {
							log.Warn().Err(closeErr).
								Int("pr", prNum).
								Msg("failed to close superseded PR via outputs (non-fatal)")
						} else {
							log.Info().Int("pr", prNum).Msg("closed superseded PR via outputs")
						}
					}
				}
			}

			return r.patchState(ctx, &ps, StateFailed,
				fmt.Sprintf("bundle %s was superseded — promotion cancelled", ps.Spec.BundleName))
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
	default:
		log.Warn().Str("state", ps.Status.State).Msg("unknown state, resetting to Pending")
		return r.patchState(ctx, &ps, StatePendingExplicit, "")
	}
}

// handlePending initializes the step sequence and transitions to Promoting.
func (r *Reconciler) handlePending(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	pipeline, err := r.loadPipeline(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load pipeline: %w", err)
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

	patch := client.MergeFrom(ps.DeepCopy())
	ps.Status.State = StatePromoting
	ps.Status.CurrentStepIndex = 0
	ps.Status.Message = fmt.Sprintf("initialized with %d steps", len(seq))
	// Persist workDir to status (ST-7/ST-8/ST-9 short-term mitigation):
	// a restarted controller reads this field instead of recomputing the path,
	// enabling crash-recovery without re-cloning.
	ps.Status.WorkDir = r.workDir(ps.Spec.PipelineName, ps.Spec.BundleName)
	if err := r.Status().Patch(ctx, ps, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch pending→promoting: %w", err)
	}

	return ctrl.Result{Requeue: true}, nil
}

// handlePromoting runs the step engine from the current index.
func (r *Reconciler) handlePromoting(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	pipeline, err := r.loadPipeline(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load pipeline: %w", err)
	}
	bundle, err := r.loadBundle(ctx, ps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load bundle: %w", err)
	}
	env := findEnv(pipeline, ps.Spec.Environment)
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

	// Use persisted workDir if available (crash recovery — ST-7/ST-8 mitigation).
	// On a fresh run status.WorkDir will be empty; use the computed default.
	workDir := ps.Status.WorkDir
	if workDir == "" {
		workDir = r.workDir(ps.Spec.PipelineName, ps.Spec.BundleName)
	}

	token := ""
	// Resolve git token from Pipeline.spec.git.secretRef if configured.
	if secretRef := pipeline.Spec.Git.SecretRef; secretRef != nil && secretRef.Name != "" {
		ns := secretRef.Namespace
		if ns == "" {
			ns = ps.Namespace
		}
		var secret corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Name: secretRef.Name, Namespace: ns}, &secret); err != nil {
			log.Warn().Err(err).Str("secret", secretRef.Name).Msg("failed to read git secret — git operations may fail")
		} else {
			if t, ok := secret.Data["token"]; ok && len(t) > 0 {
				token = string(t)
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
		SCM:       r.SCM,
		GitClient: r.GitClient,
	}

	nextIdx, result, execErr := eng.ExecuteFrom(ctx, state, ps.Status.CurrentStepIndex)

	// Persist outputs regardless of result.
	patch := client.MergeFrom(ps.DeepCopy())
	ps.Status.Outputs = state.Outputs
	ps.Status.CurrentStepIndex = nextIdx

	if execErr != nil {
		log.Error().Err(execErr).Str("env", ps.Spec.Environment).Msg("step engine failed")
		ps.Status.State = StateFailed
		ps.Status.Message = execErr.Error()
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			if apierrors.IsNotFound(patchErr) {
				log.Debug().Msg("promotionstep deleted before failed-state patch — ignoring")
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, fmt.Errorf("patch failed state: %w", patchErr)
		}
		return ctrl.Result{}, nil
	}

	switch result.Status {
	case steps.StepPending:
		prURL, hasPRURL := state.Outputs["prURL"]
		if hasPRURL && prURL != "" {
			// The open-pr step (or similar) has opened a PR and is waiting for merge.
			// Transition to WaitingForMerge so the PRStatusReconciler can take over.
			ps.Status.State = StateWaitingForMerge
			ps.Status.PRURL = prURL
			ps.Status.Message = result.Message
			if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch waiting-for-merge: %w", patchErr)
			}
			// Patch the PRStatus CRD spec so PRStatusReconciler can poll it.
			// This is idempotent — if prStatusRef is not set, skip gracefully.
			if ps.Spec.PRStatusRef != "" {
				if prErr := r.patchPRStatusSpec(ctx, ps, state.Outputs); prErr != nil {
					log.Warn().Err(prErr).Msg("failed to patch PRStatus spec (non-fatal)")
				}
			}
			return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
		}

		// No prURL — this is a non-blocking retry (e.g. custom webhook 5xx backoff).
		// Stay in Promoting state; use the step's requested RequeueAfter duration if set.
		ps.Status.Message = result.Message
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch promoting retry: %w", patchErr)
		}
		requeue := result.RequeueAfter
		if requeue == 0 {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{RequeueAfter: requeue}, nil

	case steps.StepSuccess:
		if nextIdx >= len(seq) {
			// All steps completed — move to HealthChecking.
			ps.Status.State = StateHealthChecking
			ps.Status.Message = "all steps complete, running health check"
			if prURL, ok := state.Outputs["prURL"]; ok {
				ps.Status.PRURL = prURL
			}
			if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch health-checking: %w", patchErr)
			}
			return ctrl.Result{Requeue: true}, nil
		}
		// More steps remain — persist index and requeue immediately.
		ps.Status.Message = fmt.Sprintf("completed step %d/%d", nextIdx, len(seq))
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch step progress: %w", patchErr)
		}
		return ctrl.Result{Requeue: true}, nil

	default:
		ps.Status.State = StateFailed
		ps.Status.Message = result.Message
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch failed: %w", patchErr)
		}
		return ctrl.Result{}, nil
	}
}

// handleWaitingForMerge checks the PRStatus CRD (written by PRStatusReconciler)
// instead of polling GitHub directly. This eliminates the PS-4 logic leak.
//
// Architecture: the open-pr step created a PRStatus CR and set spec.prURL/prNumber/repo.
// The PRStatusReconciler polls GitHub and writes status.merged/open.
// This reconciler simply reads the CRD status — no GitHub API call here.
func (r *Reconciler) handleWaitingForMerge(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	prStatusName := ps.Spec.PRStatusRef
	if prStatusName == "" {
		// PRStatusRef not set — this is a pre-PRStatus PromotionStep (schema migration).
		// Fall back to direct SCM check using the PR number from outputs (#367).
		return r.handleWaitingForMergeViaDirectSCM(ctx, log, ps)
	}

	var prs v1alpha1.PRStatus
	if err := r.Get(ctx, types.NamespacedName{Name: prStatusName, Namespace: ps.Namespace}, &prs); err != nil {
		if apierrors.IsNotFound(err) {
			// PRStatus not yet created by open-pr step — requeue.
			log.Debug().Str("prStatusRef", prStatusName).Msg("PRStatus not found yet, requeueing")
			return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get prstatus %s: %w", prStatusName, err)
	}

	if prs.Status.Merged {
		log.Info().
			Str("prStatusRef", prStatusName).
			Int("prNumber", prs.Spec.PRNumber).
			Msg("PRStatus reports merged — advancing to HealthChecking")
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.State = StateHealthChecking
		ps.Status.Message = fmt.Sprintf("PR #%d merged (via PRStatus CRD)", prs.Spec.PRNumber)
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch health-checking: %w", patchErr)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if prs.Status.LastCheckedAt != nil && !prs.Status.Open && !prs.Status.Merged {
		// PR closed without merge
		log.Info().
			Str("prStatusRef", prStatusName).
			Int("prNumber", prs.Spec.PRNumber).
			Msg("PRStatus reports PR closed without merge — failing")
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.State = StateFailed
		ps.Status.Message = fmt.Sprintf("PR #%d was closed without merging", prs.Spec.PRNumber)
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch failed on closed PR: %w", patchErr)
		}
		return ctrl.Result{}, nil
	}

	// PR is still open or PRStatus reconciler hasn't polled yet — requeue.
	return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
}

// handleWaitingForMergeViaDirectSCM handles WaitingForMerge for legacy PromotionSteps
// that have no prStatusRef (created before the PRStatus CRD was introduced).
// Falls back to calling the SCM provider directly (#367 migration fix).
func (r *Reconciler) handleWaitingForMergeViaDirectSCM(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	// Extract PR number from step outputs.
	prNumStr := ps.Status.Outputs["prNumber"]
	prURL := ps.Status.Outputs["prURL"]
	if prNumStr == "" || prURL == "" {
		log.Warn().Msg("no prStatusRef and no prNumber/prURL outputs — requeueing")
		return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
	}

	if r.SCM == nil {
		log.Warn().Msg("SCM provider not configured — cannot check PR status")
		return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
	}

	// Derive repo from pipeline Git URL.
	pipeline, err := r.loadPipeline(ctx, ps)
	if err != nil {
		log.Warn().Err(err).Msg("failed to load pipeline for SCM check (non-fatal)")
		return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
	}
	repo := extractRepo(pipeline.Spec.Git.URL)

	prNum := 0
	if _, err := fmt.Sscanf(prNumStr, "%d", &prNum); err != nil || prNum <= 0 {
		log.Warn().Str("prNumber", prNumStr).Msg("invalid prNumber — requeueing")
		return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
	}

	merged, open, err := r.SCM.GetPRStatus(ctx, repo, prNum)
	if err != nil {
		log.Warn().Err(err).Int("prNumber", prNum).Msg("SCM GetPRStatus failed — requeueing")
		return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
	}

	if merged {
		log.Info().Int("prNumber", prNum).Msg("SCM reports PR merged (migration fallback) — advancing to HealthChecking")
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.State = StateHealthChecking
		ps.Status.Message = fmt.Sprintf("PR #%d merged (via SCM direct check — migration)", prNum)
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch health-checking: %w", patchErr)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if !open {
		log.Info().Int("prNumber", prNum).Msg("SCM reports PR closed without merge")
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.State = StateFailed
		ps.Status.Message = fmt.Sprintf("PR #%d was closed without merging", prNum)
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch failed: %w", patchErr)
		}
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
}

// handleHealthChecking verifies the deployment health using the configured adapter.
// Uses the real health adapter when HealthDetector is configured; falls back to
// the stub health-check step when it is nil (for backward compatibility in tests
// written before Stage 7).
func (r *Reconciler) handleHealthChecking(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	pipeline, pipelineErr := r.loadPipeline(ctx, ps)
	bundle, bundleErr := r.loadBundle(ctx, ps)
	if pipelineErr != nil || bundleErr != nil {
		log.Warn().Err(pipelineErr).Err(bundleErr).Msg("failed to load pipeline/bundle for health check")
	}

	// Use real health adapter if HealthDetector is available.
	if r.HealthDetector != nil && pipeline != nil {
		env := findEnv(pipeline, ps.Spec.Environment)
		healthType := env.Health.Type

		// Delivery delegation: when env.Delivery.Delegate is set, override the health
		// adapter type with the delegate type. This allows using ArgoRollouts or Flagger
		// for progressive delivery without requiring a separate health.type setting.
		// The delegate value maps directly to a health adapter type.
		if env.Delivery.Delegate != "" && env.Delivery.Delegate != "none" {
			delegateType := env.Delivery.Delegate
			log.Info().
				Str("env", ps.Spec.Environment).
				Str("delegate", delegateType).
				Msg("delivery.delegate is set — overriding health adapter type for delegation")
			healthType = delegateType
			// Update the step message immediately to show delegation is active.
			patch := client.MergeFrom(ps.DeepCopy())
			ps.Status.Message = fmt.Sprintf("delegated to %s", delegateType)
			if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
				log.Warn().Err(patchErr).Msg("failed to patch delegation message (non-fatal)")
			}
		}

		// Parse timeout from environment config; default 10m.
		timeout := 10 * time.Minute
		if env.Health.Timeout != "" {
			if d, err := time.ParseDuration(env.Health.Timeout); err == nil && d > 0 {
				timeout = d
			}
		}

		// Set status.healthCheckExpiry on first entry (idempotent — only set once).
		// This writes time-based state to the CRD so the Graph can observe it.
		// Graph-purity: eliminates PS-5 (time.Since() in reconciler hot path).
		if ps.Status.HealthCheckExpiry == nil {
			expiry := metav1.NewTime(time.Now().Add(timeout))
			patch := client.MergeFrom(ps.DeepCopy())
			ps.Status.HealthCheckExpiry = &expiry
			if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch health check expiry: %w", patchErr)
			}
		}

		// Check if we've exceeded the timeout by comparing to the stored expiry field.
		// time.Now() is used only to write a CRD status field — Graph-first compliant.
		if time.Now().After(ps.Status.HealthCheckExpiry.Time) {
			log.Warn().
				Time("expiry", ps.Status.HealthCheckExpiry.Time).
				Dur("timeout", timeout).
				Msg("health check timeout")
			patch := client.MergeFrom(ps.DeepCopy())
			ps.Status.State = StateFailed
			ps.Status.Message = fmt.Sprintf("health check timeout after %s", timeout)
			if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch failed (health timeout): %w", patchErr)
			}
			return ctrl.Result{}, nil
		}

		adapter, err := r.HealthDetector.Select(ctx, healthType)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("select health adapter: %w", err)
		}

		// Build CheckOptions from pipeline environment config.
		opts := health.CheckOptions{
			Type:    healthType,
			Timeout: timeout,
			Resource: health.ResourceConfig{
				Name:      pipeline.Name,
				Namespace: ps.Spec.Environment,
				Condition: "Available",
			},
			ArgoCD: health.ArgoCDConfig{
				Name:      pipeline.Name + "-" + ps.Spec.Environment,
				Namespace: "argocd",
			},
			Flux: health.FluxConfig{
				Name:      pipeline.Name + "-" + ps.Spec.Environment,
				Namespace: "flux-system",
			},
			ArgoRollouts: health.ArgoRolloutsConfig{
				Name:      pipeline.Name,
				Namespace: ps.Spec.Environment,
			},
		}

		result, checkErr := adapter.Check(ctx, opts)
		if checkErr != nil {
			log.Error().Err(checkErr).Str("adapter", adapter.Name()).Msg("health adapter check error")
			return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil
		}

		// K-01: Contiguous soak / bake tracking.
		// When env.Bake is configured, health must be healthy for Bake.Minutes
		// contiguously before transitioning to Verified.
		if env.Bake != nil {
			return r.handleBake(ctx, log, ps, pipeline, env, result.Healthy, adapter.Name(), result.Reason)
		}

		if result.Healthy {
			log.Info().Str("env", ps.Spec.Environment).Str("adapter", adapter.Name()).Msg("health check passed, Verified")
			now := metav1.NewTime(time.Now().UTC())
			patch := client.MergeFrom(ps.DeepCopy())
			ps.Status.State = StateVerified
			ps.Status.Message = fmt.Sprintf("health check passed via %s: %s", adapter.Name(), result.Reason)
			ps.Status.ConsecutiveHealthFailures = 0 // reset on success
			ps.Status.Conditions = appendCondition(ps.Status.Conditions, "Verified", metav1.ConditionTrue, "Verified", "promotion complete", now.Time)
			if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch verified: %w", patchErr)
			}
			return ctrl.Result{}, nil
		}

		// Not yet healthy — increment failure counter and check auto-rollback threshold.
		log.Debug().Str("reason", result.Reason).Str("adapter", adapter.Name()).Msg("health check not yet passed, requeueing")
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.ConsecutiveHealthFailures++
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch consecutive failures: %w", patchErr)
		}

		// Note: the auto-rollback threshold decision is handled by the RollbackPolicyReconciler,
		// which watches PromotionStep.status.consecutiveHealthFailures and creates a rollback Bundle
		// when the threshold is exceeded. This eliminates PS-6 and PS-7 (logic leaks).

		return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil
	}

	// Fallback: use the health-check step stub (Stage 6 behavior / tests without HealthDetector).
	healthStep, err := steps.Lookup("health-check")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("lookup health-check step: %w", err)
	}

	var pipelineSpec v1alpha1.PipelineSpec
	var bundleSpec v1alpha1.BundleSpec
	var envSpec v1alpha1.EnvironmentSpec
	if pipeline != nil {
		pipelineSpec = pipeline.Spec
		envSpec = findEnv(pipeline, ps.Spec.Environment)
	}
	if bundle != nil {
		bundleSpec = bundle.Spec
	}

	state := &steps.StepState{
		Pipeline:    pipelineSpec,
		Environment: envSpec,
		Bundle:      bundleSpec,
		Outputs:     cloneMap(ps.Status.Outputs),
		SCM:         r.SCM,
		GitClient:   r.GitClient,
	}

	result, execErr := healthStep.Execute(ctx, state)
	if execErr != nil {
		log.Error().Err(execErr).Msg("health check error")
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.State = StateFailed
		ps.Status.Message = execErr.Error()
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch failed (health): %w", patchErr)
		}
		return ctrl.Result{}, nil
	}

	switch result.Status {
	case steps.StepSuccess:
		log.Info().Str("env", ps.Spec.Environment).Msg("health check passed, Verified")
		now := metav1.NewTime(time.Now().UTC())
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.State = StateVerified
		ps.Status.Message = "health check passed"
		ps.Status.Conditions = appendCondition(ps.Status.Conditions, "Verified", metav1.ConditionTrue, "Verified", "promotion complete", now.Time)
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch verified: %w", patchErr)
		}
		return ctrl.Result{}, nil

	case steps.StepPending:
		return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil

	default:
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.State = StateFailed
		ps.Status.Message = result.Message
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch failed (health): %w", patchErr)
		}
		return ctrl.Result{}, nil
	}
}

// handleBake implements the K-01 contiguous-healthy soak window.
//
// When env.Bake is configured, the step must be healthy for Bake.Minutes
// contiguously before transitioning to Verified. A health failure with
// policy=reset-on-alarm resets the elapsed timer to 0 and increments BakeResets.
//
// All time values are written to CRD status fields — Graph-first compliant.
// time.Now() is called only to write status fields, never in a conditional.
func (r *Reconciler) handleBake(
	ctx context.Context,
	log zerolog.Logger,
	ps *v1alpha1.PromotionStep,
	pipeline *v1alpha1.Pipeline,
	env v1alpha1.EnvironmentSpec,
	healthy bool,
	adapterName, reason string,
) (ctrl.Result, error) {
	now := metav1.NewTime(time.Now().UTC())
	patch := client.MergeFrom(ps.DeepCopy())

	if !healthy {
		policy := env.Bake.Policy
		if policy == "" {
			policy = "reset-on-alarm"
		}
		if policy == "reset-on-alarm" {
			// Reset the contiguous timer.
			ps.Status.BakeElapsedMinutes = 0
			ps.Status.BakeStartedAt = &now // restart the window
			ps.Status.BakeResets++
			ps.Status.Message = fmt.Sprintf(
				"bake: health alarm via %s — timer reset (resets=%d, need %dm contiguous)",
				adapterName, ps.Status.BakeResets, env.Bake.Minutes)
			log.Info().
				Str("env", ps.Spec.Environment).
				Int("bakeResets", ps.Status.BakeResets).
				Int("bakeMinutes", env.Bake.Minutes).
				Msg("bake: health alarm, timer reset")
		} else {
			// fail-on-alarm: transition to Failed immediately.
			ps.Status.State = StateFailed
			ps.Status.Message = fmt.Sprintf(
				"bake: health alarm via %s (policy=fail-on-alarm): %s", adapterName, reason)
			log.Info().Str("env", ps.Spec.Environment).Msg("bake: health alarm, fail-on-alarm")
		}
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch bake alarm: %w", patchErr)
		}
		return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil
	}

	// Healthy. Start or advance the bake window.
	if ps.Status.BakeStartedAt == nil {
		// First healthy check — start the window.
		ps.Status.BakeStartedAt = &now
		ps.Status.BakeElapsedMinutes = 0
	} else {
		// Advance elapsed time: minutes since BakeStartedAt minus any resets.
		// We compute elapsed as time since the current window started,
		// minus the time spent in unhealthy periods. Since we reset BakeStartedAt
		// on alarm, the simple calculation is: now - BakeStartedAt in minutes.
		elapsed := time.Since(ps.Status.BakeStartedAt.Time)
		ps.Status.BakeElapsedMinutes = int64(elapsed.Minutes())
	}

	ps.Status.ConsecutiveHealthFailures = 0

	if ps.Status.BakeElapsedMinutes >= int64(env.Bake.Minutes) {
		// Bake complete — transition to Verified.
		ps.Status.State = StateVerified
		ps.Status.Message = fmt.Sprintf(
			"bake complete: %dm contiguous healthy via %s (resets=%d)",
			env.Bake.Minutes, adapterName, ps.Status.BakeResets)
		ps.Status.Conditions = appendCondition(ps.Status.Conditions,
			"Verified", metav1.ConditionTrue, "BakeComplete",
			fmt.Sprintf("contiguous soak %dm complete", env.Bake.Minutes),
			now.Time)
		log.Info().
			Str("env", ps.Spec.Environment).
			Int64("elapsedMinutes", ps.Status.BakeElapsedMinutes).
			Int("requiredMinutes", env.Bake.Minutes).
			Msg("bake: complete, Verified")
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch bake verified: %w", patchErr)
		}
		return ctrl.Result{}, nil
	}

	// Still baking — update progress and requeue.
	remaining := int64(env.Bake.Minutes) - ps.Status.BakeElapsedMinutes
	ps.Status.Message = fmt.Sprintf(
		"bake: %dm/%dm contiguous healthy via %s (~%dm remaining, resets=%d)",
		ps.Status.BakeElapsedMinutes, env.Bake.Minutes, adapterName,
		remaining, ps.Status.BakeResets)
	log.Debug().
		Str("env", ps.Spec.Environment).
		Int64("elapsed", ps.Status.BakeElapsedMinutes).
		Int64("remaining", remaining).
		Msg("bake: in progress")
	if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
		return ctrl.Result{}, fmt.Errorf("patch bake progress: %w", patchErr)
	}
	return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil
}

// patchState is a helper to patch state + message atomically.
func (r *Reconciler) patchState(ctx context.Context, ps *v1alpha1.PromotionStep, state, message string) (ctrl.Result, error) {
	patch := client.MergeFrom(ps.DeepCopy())
	ps.Status.State = state
	ps.Status.Message = message
	if err := r.Status().Patch(ctx, ps, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch state %s: %w", state, err)
	}
	return ctrl.Result{Requeue: true}, nil
}

// SetupWithManager registers the PromotionStep reconciler with controller-runtime.
// In distributed (sharded) mode, it adds a label-selector predicate so that only
// PromotionSteps matching the agent's shard label are enqueued. This replaces the
// silent Go-level skip (PS-3 in docs/design/11-graph-purity-tech-debt.md) with a
// declarative controller-level filter that is observable via label selectors.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr)
	if r.Shard != "" {
		// In sharded mode, filter to only watch PromotionSteps for our shard.
		// Steps without a shard label are NOT processed by any sharded agent —
		// they are intended for standalone (non-sharded) controllers.
		shard := r.Shard
		b = b.For(&v1alpha1.PromotionStep{}, builderutil.WithPredicates(shardMatchPredicate{shard: shard}))
	} else {
		b = b.For(&v1alpha1.PromotionStep{})
	}
	return b.Complete(r)
}

// shardMatchPredicate implements sigs.k8s.io/controller-runtime/pkg/predicate.Predicate
// for shard-based filtering. Eliminates PS-3: shard filtering is now at the watch
// layer (controller-runtime predicate) rather than inside the reconcile function.
type shardMatchPredicate struct {
	shard string
}

func (p shardMatchPredicate) Create(e event.CreateEvent) bool {
	return e.Object.GetLabels()["kardinal.io/shard"] == p.shard
}

func (p shardMatchPredicate) Delete(e event.DeleteEvent) bool {
	return e.Object.GetLabels()["kardinal.io/shard"] == p.shard
}

func (p shardMatchPredicate) Update(e event.UpdateEvent) bool {
	return e.ObjectNew.GetLabels()["kardinal.io/shard"] == p.shard
}

func (p shardMatchPredicate) Generic(e event.GenericEvent) bool {
	return e.Object.GetLabels()["kardinal.io/shard"] == p.shard
}

// patchPRStatusSpec updates the spec of the companion PRStatus CRD with PR data
// from the open-pr step outputs. This is idempotent: if the PRStatus already has
// a prURL set, it is a no-op.
func (r *Reconciler) patchPRStatusSpec(ctx context.Context, ps *v1alpha1.PromotionStep, outputs map[string]string) error {
	var prs v1alpha1.PRStatus
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ps.Spec.PRStatusRef,
		Namespace: ps.Namespace,
	}, &prs); err != nil {
		return fmt.Errorf("get prstatus %s: %w", ps.Spec.PRStatusRef, err)
	}

	// Idempotent: already has PR data — skip.
	if prs.Spec.PRURL != "" {
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

func (r *Reconciler) workDir(pipelineName, bundleName string) string {
	if r.WorkDirFn != nil {
		return r.WorkDirFn(pipelineName, bundleName)
	}
	// Default: use a temp directory per pipeline+bundle combination.
	return "/tmp/kardinal/" + pipelineName + "/" + bundleName
}

// cleanWorkDir removes the working directory on disk when a PromotionStep reaches
// a terminal state (Verified or Failed). This ensures host-local git state does not
// accumulate across promotions (ST-7/ST-8 short-term mitigation).
// The cleanup is best-effort — failure to remove the directory is logged but not fatal.
func (r *Reconciler) cleanWorkDir(log zerolog.Logger, ps *v1alpha1.PromotionStep) {
	dir := ps.Status.WorkDir
	if dir == "" {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Warn().Err(err).Str("workDir", dir).Msg("cleanWorkDir: failed to remove working directory")
	} else {
		log.Debug().Str("workDir", dir).Msg("cleanWorkDir: removed working directory")
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

// extractPRNumber extracts the PR number from a GitHub PR URL.
// e.g. "https://github.com/owner/repo/pull/42" → 42
func extractPRNumber(prURL string) int {
	idx := strings.LastIndex(prURL, "/pull/")
	if idx < 0 {
		return 0
	}
	numStr := prURL[idx+len("/pull/"):]
	// Trim any trailing path segments
	if end := strings.Index(numStr, "/"); end >= 0 {
		numStr = numStr[:end]
	}
	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return 0
	}
	return n
}

// extractRepo extracts "owner/repo" from a GitHub PR URL.
// e.g. "https://github.com/owner/repo/pull/42" → "owner/repo"
func extractRepo(prURL string) string {
	// Remove scheme
	s := strings.TrimPrefix(prURL, "https://")
	s = strings.TrimPrefix(s, "http://")
	// Remove host
	idx := strings.Index(s, "/")
	if idx < 0 {
		return ""
	}
	s = s[idx+1:]
	// Take first two path segments: owner/repo
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
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
