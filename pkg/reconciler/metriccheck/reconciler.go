// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package metriccheck implements the MetricCheckReconciler which queries a
// Prometheus-compatible backend and patches MetricCheck.status with the result.
// PolicyGate CEL expressions reference these results via metrics.<name>.value and
// metrics.<name>.result. The reconciler never evaluates CEL itself — it only
// writes CRD status fields for the PolicyGate reconciler to read.
package metriccheck

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// defaultInterval is used when spec.interval is empty or invalid.
const defaultInterval = 1 * time.Minute

// MetricsProvider queries a metrics backend and returns a scalar value for the given query.
type MetricsProvider interface {
	// QueryScalar evaluates a PromQL query and returns the scalar result.
	// Returns an error if the query fails or returns no data.
	QueryScalar(ctx context.Context, prometheusURL, query string) (float64, error)
}

// Reconciler queries Prometheus, evaluates the threshold, and patches
// MetricCheck.status. It is idempotent and safe to re-run after a crash.
type Reconciler struct {
	client.Client
	// Provider is the metrics query backend (Prometheus-compatible).
	Provider MetricsProvider
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
}

// Reconcile processes a single MetricCheck object.
//
// State machine (all paths write status, then requeue):
//  1. Not found → deleted, skip.
//  2. Query Prometheus → get scalar value.
//  3. Evaluate threshold → Pass or Fail.
//  4. Patch status.lastValue, status.result, status.lastEvaluatedAt, status.reason.
//  5. Requeue after spec.interval (default 1m).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("metriccheck", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var mc kardinalv1alpha1.MetricCheck
	if err := r.Get(ctx, req.NamespacedName, &mc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get metriccheck: %w", err)
	}

	interval := parseInterval(mc.Spec.Interval)

	// Query Prometheus for the metric value.
	value, queryErr := r.Provider.QueryScalar(ctx, mc.Spec.PrometheusURL, mc.Spec.Query)
	if queryErr != nil {
		log.Warn().Err(queryErr).Str("query", mc.Spec.Query).Msg("prometheus query failed")
		if patchErr := r.patchStatus(ctx, &mc, "", "Fail",
			fmt.Sprintf("prometheus query error: %s", queryErr)); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch metriccheck status: %w", patchErr)
		}
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	// Evaluate threshold.
	result, reason := evaluateThreshold(value, mc.Spec.Threshold)

	log.Info().
		Float64("value", value).
		Str("result", result).
		Str("reason", reason).
		Msg("metriccheck evaluated")

	if patchErr := r.patchStatus(ctx, &mc, fmt.Sprintf("%g", value), result, reason); patchErr != nil {
		return ctrl.Result{}, fmt.Errorf("patch metriccheck status: %w", patchErr)
	}

	return ctrl.Result{RequeueAfter: interval}, nil
}

// patchStatus patches MetricCheck.status with the latest evaluation result.
func (r *Reconciler) patchStatus(ctx context.Context, mc *kardinalv1alpha1.MetricCheck,
	lastValue, result, reason string) error {
	patch := client.MergeFrom(mc.DeepCopy())
	now := metav1.NewTime(r.now())
	mc.Status.LastValue = lastValue
	mc.Status.Result = result
	mc.Status.Reason = reason
	mc.Status.LastEvaluatedAt = &now
	if err := r.Status().Patch(ctx, mc, patch); err != nil {
		return fmt.Errorf("status patch: %w", err)
	}
	return nil
}

// now returns the current time via NowFn if set (for testing), otherwise time.Now().UTC().
func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}

// SetupWithManager registers the MetricCheckReconciler with the controller-runtime Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.MetricCheck{}).
		Complete(r)
}

// evaluateThreshold compares value against threshold and returns "Pass" or "Fail" with reason.
func evaluateThreshold(value float64, t kardinalv1alpha1.MetricThreshold) (string, string) {
	var pass bool
	switch t.Operator {
	case "lt":
		pass = value < t.Value
	case "gt":
		pass = value > t.Value
	case "lte":
		pass = value <= t.Value
	case "gte":
		pass = value >= t.Value
	case "eq":
		pass = value == t.Value
	default:
		return "Fail", fmt.Sprintf("unknown operator %q", t.Operator)
	}

	if pass {
		return "Pass", fmt.Sprintf("%g %s %g = true", value, t.Operator, t.Value)
	}
	return "Fail", fmt.Sprintf("%g %s %g = false", value, t.Operator, t.Value)
}

// parseInterval parses a Go duration string, returning defaultInterval on error.
func parseInterval(s string) time.Duration {
	if s == "" {
		return defaultInterval
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return defaultInterval
	}
	return d
}
