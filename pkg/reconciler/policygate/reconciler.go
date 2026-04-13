// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package policygate implements the PolicyGateReconciler which evaluates
// CEL policy expressions on PolicyGate instances created by the Graph controller.
package policygate

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel"
)

const (
	// labelBundle is the label that identifies a PolicyGate instance (vs template).
	labelBundle = "kardinal.io/bundle"
	// labelEnvironment is the environment the gate is evaluated for.
	labelEnvironment = "kardinal.io/environment"
	// defaultRecheckInterval is used when gate.Spec.RecheckInterval is empty or invalid.
	defaultRecheckInterval = 5 * time.Minute
)

// Reconciler evaluates PolicyGate CEL expressions and patches status.
// It only processes instances (gates with kardinal.io/bundle label).
// Template PolicyGates (no bundle label) are ignored.
type Reconciler struct {
	client.Client
	// Evaluator evaluates CEL expressions.
	Evaluator *cel.Evaluator
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
}

// Reconcile evaluates the CEL expression on a PolicyGate instance.
//
// State machine:
//   - No kardinal.io/bundle label → template, skip (no-op)
//   - Gate not found → deleted, skip
//   - Otherwise → build context, evaluate CEL, patch status, requeue
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("gate", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var gate kardinalv1alpha1.PolicyGate
	if err := r.Get(ctx, req.NamespacedName, &gate); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get policygate: %w", err)
	}

	// Template PolicyGates (no bundle label) are platform/team-defined gate specs.
	// They are not evaluated for a specific bundle, but we validate their CEL
	// expression and surface compilation errors in status.reason (Issue #315).
	bundleName := gate.Labels[labelBundle]
	if bundleName == "" {
		log.Debug().Msg("policygate has no bundle label, validating CEL syntax (template)")
		return r.reconcileTemplate(ctx, &gate)
	}

	recheckInterval := parseRecheckInterval(gate.Spec.RecheckInterval)

	// Build CEL context. bundleVersion is returned separately so it can be
	// written to status.reason, making the evaluated bundle version CRD-observable
	// (PG-6: extractVersion result was previously invisible to the Graph).
	celCtx, bundleVersion, err := r.buildContext(ctx, &gate, bundleName)
	if err != nil {
		log.Warn().Err(err).Msg("failed to build CEL context, setting gate to blocked")
		if patchErr := r.patchStatus(ctx, &gate, false,
			fmt.Sprintf("context error: %s", err)); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch gate status: %w", patchErr)
		}
		return ctrl.Result{RequeueAfter: recheckInterval}, nil
	}

	// Evaluate CEL expression
	pass, reason, evalErr := r.Evaluator.Evaluate(gate.Spec.Expression, celCtx)
	if evalErr != nil {
		// Fail-closed on evaluation error
		log.Warn().Err(evalErr).Str("expr", gate.Spec.Expression).
			Msg("CEL evaluation error, setting gate to blocked")
		if patchErr := r.patchStatus(ctx, &gate, false, reason); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch gate status on eval error: %w", patchErr)
		}
		return ctrl.Result{RequeueAfter: recheckInterval}, nil
	}

	// Prefix reason with bundle version so it is CRD-observable without a
	// separate status field (PG-6 partial fix — full fix requires api/v1alpha1
	// field addition for a dedicated status.bundleVersion field).
	if bundleVersion != "" {
		reason = fmt.Sprintf("bundle.version=%s: %s", bundleVersion, reason)
	}

	log.Info().
		Bool("ready", pass).
		Str("reason", reason).
		Str("expr", gate.Spec.Expression).
		Msg("policygate evaluated")

	if patchErr := r.patchStatus(ctx, &gate, pass, reason); patchErr != nil {
		return ctrl.Result{}, fmt.Errorf("patch gate status: %w", patchErr)
	}

	return ctrl.Result{RequeueAfter: recheckInterval}, nil
}

// reconcileTemplate validates the CEL expression on a template PolicyGate
// (one without a kardinal.io/bundle label). It attempts to compile the expression
// and writes the result to status so operators can see CEL errors without needing
// to wait for a bundle to fail (Issue #315 — minimum viable fix: Option B).
//
// On syntax error: status.ready=false, status.reason shows the CEL error message.
// On valid syntax: status.ready=false (not yet evaluated against a real context),
// status.reason shows "valid CEL syntax".
func (r *Reconciler) reconcileTemplate(ctx context.Context, gate *kardinalv1alpha1.PolicyGate) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("gate", gate.Name).
		Str("namespace", gate.Namespace).
		Logger()

	if gate.Spec.Expression == "" {
		// No expression to validate — leave status alone.
		return ctrl.Result{}, nil
	}

	// Validate CEL syntax using compile-only check (no evaluation).
	// Using Validate() instead of Evaluate() avoids false positives: a syntactically
	// valid expression like bundle.metadata.annotations["team"] would fail evaluation
	// with an empty bundle map, but that's a runtime concern, not a syntax error.
	celErr := r.Evaluator.Validate(gate.Spec.Expression)

	var reason string
	if celErr != nil {
		// CEL compilation error — surface it so operators can see it.
		reason = fmt.Sprintf("CEL syntax error: %s", celErr)
		log.Warn().Err(celErr).Str("expr", gate.Spec.Expression).
			Msg("template PolicyGate has invalid CEL expression")
	} else {
		reason = "valid CEL syntax (not yet evaluated — awaiting bundle promotion)"
	}

	// Patch status to reflect the validation result. ready=false for templates
	// (only instances are evaluated against a real bundle context and may become ready=true).
	if patchErr := r.patchStatus(ctx, gate, false, reason); patchErr != nil {
		return ctrl.Result{}, fmt.Errorf("patch template gate status: %w", patchErr)
	}

	return ctrl.Result{}, nil
}

// buildContext constructs the Phase 1 CEL context for gate evaluation.
// It also returns the bundle version string (second return value) so the caller
// can write it to status.reason — making the version CRD-observable (PG-6 partial fix).
func (r *Reconciler) buildContext(ctx context.Context, gate *kardinalv1alpha1.PolicyGate,
	bundleName string) (map[string]interface{}, string, error) {
	var bundle kardinalv1alpha1.Bundle
	if err := r.Get(ctx, types.NamespacedName{
		Name:      bundleName,
		Namespace: gate.Namespace,
	}, &bundle); err != nil {
		return nil, "", fmt.Errorf("load bundle %s: %w", bundleName, err)
	}

	now := r.now()
	version := extractVersion(&bundle)

	bundleCtx := map[string]interface{}{
		"type":    bundle.Spec.Type,
		"version": version,
		"provenance": map[string]interface{}{
			"author":    "",
			"commitSHA": "",
			"ciRunURL":  "",
		},
		"intent": map[string]interface{}{
			"targetEnvironment": "",
		},
	}
	if bundle.Spec.Provenance != nil {
		bundleCtx["provenance"] = map[string]interface{}{
			"author":    bundle.Spec.Provenance.Author,
			"commitSHA": bundle.Spec.Provenance.CommitSHA,
			"ciRunURL":  bundle.Spec.Provenance.CIRunURL,
		}
	}
	if bundle.Spec.Intent != nil {
		bundleCtx["intent"] = map[string]interface{}{
			"targetEnvironment": bundle.Spec.Intent.TargetEnvironment,
		}
	}

	// Build metrics context: list all MetricChecks in the gate's namespace
	// and expose them as metrics.<name>.value and metrics.<name>.result.
	metricsCtx, err := r.buildMetricsContext(ctx, gate.Namespace)
	if err != nil {
		// Non-fatal: log and continue with empty metrics context so the gate
		// evaluates with whatever data is available (fail-closed if expr references metrics).
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to build metrics context, using empty")
		metricsCtx = map[string]interface{}{}
	}

	// Build upstream soak context from bundle environment statuses.
	// SoakMinutes is read from Bundle.status.environments[*].soakMinutes (PG-3 fix).
	upstreamCtx := buildUpstreamContext(&bundle)

	// bundle.upstreamSoakMinutes is the maximum soak minutes across all verified
	// upstream environments. It is a convenience shorthand so expressions can write
	//   bundle.upstreamSoakMinutes >= 30
	// instead of requiring knowledge of the specific upstream environment name.
	// Documented in docs/policy-gates.md §CEL context variables.
	var maxSoakMinutes int64
	for _, env := range bundle.Status.Environments {
		if env.SoakMinutes > maxSoakMinutes {
			maxSoakMinutes = env.SoakMinutes
		}
	}
	bundleCtx["upstreamSoakMinutes"] = maxSoakMinutes

	return map[string]interface{}{
		"bundle": bundleCtx,
		"schedule": map[string]interface{}{
			"isWeekend": now.Weekday() == time.Saturday || now.Weekday() == time.Sunday,
			"hour":      now.Hour(),
			"dayOfWeek": now.Weekday().String(),
		},
		"environment": map[string]interface{}{
			"name": gate.Labels[labelEnvironment],
		},
		"metrics":  metricsCtx,
		"upstream": upstreamCtx,
	}, version, nil
}

// buildMetricsContext lists all MetricCheck objects in the given namespace and
// returns a map suitable for CEL: {"<name>": {"value": <string>, "result": <string>}}.
// Only MetricCheck objects in the gate's own namespace are included.
func (r *Reconciler) buildMetricsContext(ctx context.Context, ns string) (map[string]interface{}, error) {
	var list kardinalv1alpha1.MetricCheckList
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list metricchecks: %w", err)
	}

	result := make(map[string]interface{}, len(list.Items))
	for _, mc := range list.Items {
		result[mc.Name] = map[string]interface{}{
			"value":  mc.Status.LastValue,
			"result": mc.Status.Result,
		}
	}
	return result, nil
}

// buildUpstreamContext reads per-environment soak minutes from bundle status.
// Returns a map: {"<envName>": {"soakMinutes": <int64>}}.
// soakMinutes is read from Bundle.status.environments[*].soakMinutes which is
// written by the BundleReconciler as a CRD status field (PG-3 fix: eliminates
// time.Since() from PolicyGate reconciler hot path).
func buildUpstreamContext(bundle *kardinalv1alpha1.Bundle) map[string]interface{} {
	result := make(map[string]interface{}, len(bundle.Status.Environments))
	for _, env := range bundle.Status.Environments {
		result[env.Name] = map[string]interface{}{
			"soakMinutes": env.SoakMinutes,
		}
	}
	return result
}

// patchStatus patches the PolicyGate's status fields.
func (r *Reconciler) patchStatus(ctx context.Context, gate *kardinalv1alpha1.PolicyGate,
	ready bool, reason string) error {
	patch := client.MergeFrom(gate.DeepCopy())
	now := metav1.NewTime(r.now())
	gate.Status.Ready = ready
	gate.Status.Reason = reason
	gate.Status.LastEvaluatedAt = &now
	if err := r.Status().Patch(ctx, gate, patch); err != nil {
		return fmt.Errorf("status patch: %w", err)
	}
	return nil
}

// now returns the current time, using NowFn if set (for testing).
func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}

// SetupWithManager registers the PolicyGateReconciler with the controller-runtime Manager.
// It adds a Watch on MetricCheck objects so that when any MetricCheck in a namespace
// changes (status updated by the MetricCheckReconciler), all PolicyGates in that
// same namespace are queued for re-evaluation. This is the controller-runtime
// equivalent of a "Watch node" — the PolicyGate reconciler reacts to MetricCheck
// status changes rather than waiting for recheckInterval alone.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	// metricCheckMapper enqueues all PolicyGates in the same namespace as the
	// changed MetricCheck. This ensures PolicyGates with metrics.* expressions
	// are re-evaluated immediately when a MetricCheck result changes.
	metricCheckMapper := func(ctx context.Context, obj client.Object) []reconcile.Request {
		var gateList kardinalv1alpha1.PolicyGateList
		if err := r.List(ctx, &gateList, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(gateList.Items))
		for _, gate := range gateList.Items {
			// Only enqueue instance gates (those with a bundle label) —
			// templates have no bundle label and are always skipped by Reconcile.
			if gate.Labels[labelBundle] == "" {
				continue
			}
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      gate.Name,
					Namespace: gate.Namespace,
				},
			})
		}
		return reqs
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.PolicyGate{}).
		// Watch MetricCheck objects: when a MetricCheck status changes (Pass/Fail),
		// all PolicyGates in the same namespace are re-evaluated immediately.
		// This replaces the polling-only model with an event-driven one,
		// moving toward the Graph-first Watch node architecture.
		Watches(&kardinalv1alpha1.MetricCheck{}, handler.EnqueueRequestsFromMapFunc(metricCheckMapper)).
		Complete(r)
}

// --- helpers ---

// extractVersion returns the version string from a Bundle.
// For image bundles: first image tag. For config bundles: first 8 chars of commitSHA.
func extractVersion(bundle *kardinalv1alpha1.Bundle) string {
	if bundle.Spec.Type == "config" && bundle.Spec.ConfigRef != nil {
		sha := bundle.Spec.ConfigRef.CommitSHA
		if len(sha) > 8 {
			return sha[:8]
		}
		return sha
	}
	if len(bundle.Spec.Images) > 0 {
		return bundle.Spec.Images[0].Tag
	}
	return ""
}

// parseRecheckInterval parses a Go duration string, returning defaultRecheckInterval on error.
func parseRecheckInterval(s string) time.Duration {
	if s == "" {
		return defaultRecheckInterval
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return defaultRecheckInterval
	}
	return d
}
