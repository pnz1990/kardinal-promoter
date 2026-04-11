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
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

	// Shard filtering: skip if labels don't match our shard.
	if r.Shard != "" {
		stepShard := ps.Labels["kardinal.io/shard"]
		if stepShard != r.Shard {
			log.Debug().
				Str("step_shard", stepShard).
				Str("our_shard", r.Shard).
				Msg("shard mismatch, skipping")
			return ctrl.Result{}, nil
		}
	}

	// Pause check: if the Pipeline is paused, hold all non-terminal states.
	// This allows in-flight PRs to remain open without advancing further.
	if ps.Status.State != StateVerified && ps.Status.State != StateFailed {
		pipeline, pipelineErr := r.loadPipeline(ctx, &ps)
		if pipelineErr == nil && pipeline.Spec.Paused {
			log.Info().
				Str("pipeline", ps.Spec.PipelineName).
				Str("state", ps.Status.State).
				Msg("pipeline is paused — holding PromotionStep")
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
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
		// Terminal states — nothing to do.
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

	workDir := r.workDir(ps.Spec.PipelineName, ps.Spec.BundleName)

	token := ""
	// Token resolution from Pipeline.spec.git.secretRef is handled in production
	// by the controller's credential manager. The SecretRef field is read here
	// to allow future token injection without refactoring this function.
	_ = pipeline.Spec.Git.SecretRef

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
			return ctrl.Result{}, fmt.Errorf("patch failed state: %w", patchErr)
		}
		_ = r.copyEvidenceToBundle(ctx, ps, bundle)
		return ctrl.Result{}, nil
	}

	switch result.Status {
	case steps.StepPending:
		// A step is pending (e.g., wait-for-merge). Persist index and transition to WaitingForMerge.
		ps.Status.State = StateWaitingForMerge
		if prURL, ok := state.Outputs["prURL"]; ok && prURL != "" {
			ps.Status.PRURL = prURL
		}
		ps.Status.Message = result.Message
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch waiting-for-merge: %w", patchErr)
		}
		return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil

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
		_ = r.copyEvidenceToBundle(ctx, ps, bundle)
		return ctrl.Result{}, nil
	}
}

// handleWaitingForMerge checks PR status and advances to HealthChecking or Failed.
func (r *Reconciler) handleWaitingForMerge(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	if r.SCM == nil {
		log.Warn().Msg("no SCM configured, cannot check PR status")
		return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
	}

	prNumStr := ps.Status.Outputs["prNumber"]
	if prNumStr == "" {
		// Fall back to extracting from PRURL if available.
		prNumStr = extractPRNumber(ps.Status.PRURL)
	}

	if prNumStr == "" {
		log.Warn().Msg("no prNumber in outputs, cannot check merge status")
		return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
	}

	prNum, err := strconv.Atoi(prNumStr)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid prNumber %q: %w", prNumStr, err)
	}

	repo := extractRepo(ps.Status.PRURL)
	merged, open, err := r.SCM.GetPRStatus(ctx, repo, prNum)
	if err != nil {
		log.Error().Err(err).Msg("get PR status failed")
		return ctrl.Result{RequeueAfter: requeueWaitForMerge}, nil
	}

	if merged {
		log.Info().Int("pr", prNum).Msg("PR merged, advancing to HealthChecking")
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.State = StateHealthChecking
		ps.Status.Message = fmt.Sprintf("PR #%d merged", prNum)
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch health-checking: %w", patchErr)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if !open {
		log.Info().Int("pr", prNum).Msg("PR closed without merge, failing")
		bundle, _ := r.loadBundle(ctx, ps)
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.State = StateFailed
		ps.Status.Message = fmt.Sprintf("PR #%d was closed without merging", prNum)
		if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch failed on closed PR: %w", patchErr)
		}
		if bundle != nil {
			_ = r.copyEvidenceToBundle(ctx, ps, bundle)
		}
		return ctrl.Result{}, nil
	}

	// Still open — requeue.
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

		// Parse timeout from environment config; default 10m.
		timeout := 10 * time.Minute
		if env.Health.Timeout != "" {
			if d, err := time.ParseDuration(env.Health.Timeout); err == nil && d > 0 {
				timeout = d
			}
		}

		// Check if we've exceeded the timeout (recorded in step start time via conditions).
		// If startedAt is set and elapsed > timeout, fail.
		if ps.Status.Conditions != nil {
			for _, cond := range ps.Status.Conditions {
				if cond.Type == "HealthCheckStarted" {
					elapsed := time.Since(cond.LastTransitionTime.Time)
					if elapsed > timeout {
						log.Warn().Dur("elapsed", elapsed).Dur("timeout", timeout).Msg("health check timeout")
						patch := client.MergeFrom(ps.DeepCopy())
						ps.Status.State = StateFailed
						ps.Status.Message = fmt.Sprintf("health check timeout after %s", timeout)
						if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
							return ctrl.Result{}, fmt.Errorf("patch failed (health timeout): %w", patchErr)
						}
						if bundle != nil {
							_ = r.copyEvidenceToBundle(ctx, ps, bundle)
						}
						return ctrl.Result{}, nil
					}
				}
			}
		}

		// Record health check start time (idempotent — only add if not already there).
		hasStarted := false
		for _, c := range ps.Status.Conditions {
			if c.Type == "HealthCheckStarted" {
				hasStarted = true
				break
			}
		}
		if !hasStarted {
			patch := client.MergeFrom(ps.DeepCopy())
			ps.Status.Conditions = appendCondition(ps.Status.Conditions,
				"HealthCheckStarted", metav1.ConditionTrue, "Started", "health check in progress", time.Now())
			if patchErr := r.Status().Patch(ctx, ps, patch); patchErr != nil {
				return ctrl.Result{}, fmt.Errorf("patch health check start: %w", patchErr)
			}
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
		}

		result, checkErr := adapter.Check(ctx, opts)
		if checkErr != nil {
			log.Error().Err(checkErr).Str("adapter", adapter.Name()).Msg("health adapter check error")
			return ctrl.Result{RequeueAfter: requeueHealthCheck}, nil
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
			if bundle != nil {
				_ = r.copyEvidenceToBundle(ctx, ps, bundle)
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

		// Check auto-rollback threshold.
		env = findEnv(pipeline, ps.Spec.Environment) // re-evaluate (env was already declared above)
		if env.AutoRollback != nil && bundle != nil {
			threshold := env.AutoRollback.FailureThreshold
			if threshold <= 0 {
				threshold = 3 // default
			}
			if ps.Status.ConsecutiveHealthFailures >= threshold {
				if rbErr := r.maybeCreateAutoRollback(ctx, log, ps, bundle); rbErr != nil {
					log.Error().Err(rbErr).Msg("auto-rollback: failed to create rollback bundle")
				}
			}
		}

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
		if bundle != nil {
			_ = r.copyEvidenceToBundle(ctx, ps, bundle)
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
		if bundle != nil {
			_ = r.copyEvidenceToBundle(ctx, ps, bundle)
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
		if bundle != nil {
			_ = r.copyEvidenceToBundle(ctx, ps, bundle)
		}
		return ctrl.Result{}, nil
	}
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

// copyEvidenceToBundle writes per-environment evidence into Bundle.status.environments.
// This persists audit data beyond the PromotionStep's lifetime.
func (r *Reconciler) copyEvidenceToBundle(ctx context.Context, ps *v1alpha1.PromotionStep, bundle *v1alpha1.Bundle) error {
	envStatus := v1alpha1.EnvironmentStatus{
		Name:  ps.Spec.Environment,
		Phase: ps.Status.State,
		PRURL: ps.Status.PRURL,
	}
	if prURL, ok := ps.Status.Outputs["prURL"]; ok && prURL != "" {
		envStatus.PRURL = prURL
	}
	if ps.Status.State == StateVerified {
		now := metav1.NewTime(time.Now().UTC())
		envStatus.HealthCheckedAt = &now
	}

	patch := client.MergeFrom(bundle.DeepCopy())
	updated := false
	for i, env := range bundle.Status.Environments {
		if env.Name == ps.Spec.Environment {
			bundle.Status.Environments[i] = envStatus
			updated = true
			break
		}
	}
	if !updated {
		bundle.Status.Environments = append(bundle.Status.Environments, envStatus)
	}

	if err := r.Status().Patch(ctx, bundle, patch); err != nil {
		return fmt.Errorf("patch bundle evidence for env %s: %w", ps.Spec.Environment, err)
	}
	return nil
}

// SetupWithManager registers the PromotionStep reconciler with controller-runtime.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PromotionStep{}).
		Complete(r)
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

// extractPRNumber parses the PR number from a GitHub PR URL.
// e.g. "https://github.com/owner/repo/pull/42" → "42"
func extractPRNumber(prURL string) string {
	if prURL == "" {
		return ""
	}
	parts := strings.Split(prURL, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// maybeCreateAutoRollback creates a rollback Bundle if one doesn't already exist
// for this PromotionStep. It is idempotent: checks for an existing rollback Bundle
// before creating a new one.
func (r *Reconciler) maybeCreateAutoRollback(ctx context.Context, log zerolog.Logger,
	ps *v1alpha1.PromotionStep, bundle *v1alpha1.Bundle) error {
	// Check if a rollback Bundle already exists for this original bundle.
	var existingBundles v1alpha1.BundleList
	if err := r.List(ctx, &existingBundles, client.InNamespace(ps.Namespace)); err != nil {
		return fmt.Errorf("list bundles for rollback check: %w", err)
	}
	for _, b := range existingBundles.Items {
		if b.Labels["kardinal.io/rollback"] == "true" &&
			b.Spec.Provenance != nil &&
			b.Spec.Provenance.RollbackOf == bundle.Name {
			log.Debug().Str("existing_rollback", b.Name).Msg("auto-rollback: rollback bundle already exists, skipping")
			return nil
		}
	}

	// Create rollback Bundle with the same images as the current bundle.
	now := metav1.NewTime(time.Now().UTC())
	rollbackName := fmt.Sprintf("%s-rollback-%d", bundle.Spec.Pipeline, now.Unix()%100000)
	rollbackBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rollbackName,
			Namespace: ps.Namespace,
			Labels: map[string]string{
				"kardinal.io/pipeline": bundle.Spec.Pipeline,
				"kardinal.io/rollback": "true",
			},
		},
		Spec: v1alpha1.BundleSpec{
			Type:     bundle.Spec.Type,
			Pipeline: bundle.Spec.Pipeline,
			Images:   bundle.Spec.Images,
			Provenance: &v1alpha1.BundleProvenance{
				RollbackOf: bundle.Name,
				Timestamp:  now,
				Author:     "kardinal-controller (auto-rollback)",
			},
		},
	}

	if err := r.Create(ctx, rollbackBundle); err != nil {
		return fmt.Errorf("create rollback bundle: %w", err)
	}

	log.Info().
		Str("rollback_bundle", rollbackName).
		Str("original_bundle", bundle.Name).
		Int("failures", ps.Status.ConsecutiveHealthFailures).
		Msg("auto-rollback: created rollback bundle")
	return nil
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
