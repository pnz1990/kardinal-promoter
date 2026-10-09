// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package metriccheck implements the MetricCheckReconciler which queries a
// metrics backend (Prometheus, Datadog, CloudWatch, New Relic, or any JSON
// HTTP API) and patches MetricCheck.status with the result.
// PolicyGate CEL expressions reference these results via metrics.<name>.value,
// metrics.<name>.result and metrics.<name>.stale (status.validUntil has
// passed). The reconciler never evaluates CEL itself — it only writes CRD
// status fields for the PolicyGate reconciler to read.
package metriccheck

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
)

const (
	// defaultInterval is used when spec.interval is empty or invalid.
	defaultInterval = 1 * time.Minute
	// minInterval is the shortest re-evaluation interval: a smaller
	// spec.interval would poll Prometheus in a hot loop (C04-gates-17).
	minInterval = 10 * time.Second
	// maxConcurrentReconciles is the reconcile worker count. Queries take at
	// most DefaultGlobalSlots of them (Limiter), so the rest keep serving
	// templates, suspended checks and checks waiting for a slot.
	maxConcurrentReconciles = 8
	// staleAfterIntervals and minValidFor set status.validUntil: a result is
	// valid for three intervals (two missed evaluations of margin), and at
	// least minValidFor. PolicyGates treat a result past validUntil as stale.
	staleAfterIntervals = 3
	minValidFor         = 30 * time.Second
	// firstWriteRetry caps the delay before the first retry of a failed
	// status write: a write that failed once, for example on a conflict or
	// a brief API server outage, is likely to work at once.
	firstWriteRetry = 5 * time.Second
	// DefaultGlobalSlots and DefaultNamespaceSlots are the Limiter caps the
	// controller uses: one query per namespace at a time, six in the cluster.
	DefaultGlobalSlots    = 6
	DefaultNamespaceSlots = 1
	// busyRetry is how soon a check that found no free query slot asks again.
	busyRetry = 2 * time.Second
)

// MetricsProvider queries a Prometheus-compatible backend and returns a
// scalar value for the given query.
type MetricsProvider interface {
	// QueryScalar evaluates a PromQL query and returns the scalar result.
	// Returns an error if the query fails or returns no data.
	QueryScalar(ctx context.Context, prometheusURL, query string) (float64, error)
}

// Reconciler queries the MetricCheck's backend, evaluates the threshold, and
// patches MetricCheck.status. It is idempotent and safe to re-run after a
// crash. It reads only the MetricCheck and the Secrets its spec names.
type Reconciler struct {
	client.Client
	// Backends evaluates each provider kind, keyed by spec.provider
	// (DefaultBackends).
	Backends map[string]Backend
	// Provider, when set and Backends has no "prometheus" entry, evaluates
	// prometheus MetricChecks that need no Authorization header.
	Provider MetricsProvider
	// SecretReader reads the Secrets that *SecretRef fields name, in the
	// MetricCheck's namespace. Nil means Client.
	SecretReader client.Reader
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
	// Limiter shares the query slots fairly between namespaces (every
	// provider dials a user-chosen address). Nil means no limit (tests).
	Limiter *Limiter
}

// Status reasons of MetricChecks that are not queried.
const (
	// ReasonTemplate is the reason of a per-promotion MetricCheck.
	ReasonTemplate = "Template: spec.perPromotion is set, so this MetricCheck is not queried itself; " +
		"each Bundle's Graph creates an instance per environment whose gates read it"
	// ReasonWaitingForSlot is the reason of a MetricCheck waiting for a
	// query slot (Limiter).
	ReasonWaitingForSlot = "WaitingForSlot: other MetricChecks of this namespace or the cluster are querying; " +
		"this one queries when a slot is free"
	// ReasonSuspended is the reason of a suspended MetricCheck.
	ReasonSuspended = "Suspended: spec.suspend is set; the last result goes stale at validUntil"
)

// Reconcile processes a single MetricCheck object.
//
// State machine:
//  1. Not found → deleted, skip.
//  2. spec.perPromotion → a template: write reason ReasonTemplate (and clear
//     any result), no query, no requeue. Its instances are evaluated instead.
//  3. spec.suspend → write reason ReasonSuspended, no query, no requeue; the
//     last result goes stale at validUntil.
//  4. A placeholder left in a templated field → Fail without a query.
//  5. Query the provider's backend → a number, or text for web.
//  6. Evaluate threshold → Pass or Fail.
//  7. Patch status.lastValue, status.result, status.lastEvaluatedAt, status.reason
//     and status.validUntil.
//  8. Requeue after spec.interval (default 1m, minimum 10s). When the status
//     patch fails, requeue after min(interval, 5s) the first time (the
//     evaluation due one interval after the last write that worked) and
//     after the interval otherwise.
//
// A MetricCheck deleted while it is reconciled ends the reconcile (objectgone).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, metricChecksResource, r.reconcile)
}

// metricChecksResource is the resource objectgone matches a NotFound against.
var metricChecksResource = kardinalv1alpha1.GroupVersion.WithResource("metricchecks").GroupResource()

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	if mc.Spec.PerPromotion {
		return ctrl.Result{}, r.markNotQueried(ctx, &mc, templateReason(&mc.Spec), true)
	}
	if mc.Spec.Suspend {
		return ctrl.Result{}, r.markNotQueried(ctx, &mc, ReasonSuspended, false)
	}

	interval := parseInterval(mc.Spec.Interval)

	// A placeholder left in a query means the Graph could not render it (an
	// unknown name, or a value with characters a query must not get): never
	// send it.
	if ph := unrenderedPlaceholders(&mc.Spec); len(ph) > 0 {
		reason := fmt.Sprintf("unrendered placeholder {{ %s }}: only a per-promotion MetricCheck's instances "+
			"have their placeholders replaced, with values made of [A-Za-z0-9._+-] or a digest", strings.Join(ph, " }}, {{ "))
		return r.record(ctx, log, &mc, interval, "", "Fail", reason)
	}

	if r.Limiter != nil {
		release, ok := r.Limiter.TryAcquire(req.NamespacedName)
		if !ok {
			// No free slot: wait in the queue without a result. The last
			// result still goes stale at its validUntil (fail closed).
			return ctrl.Result{RequeueAfter: busyRetry}, r.markNotQueried(ctx, &mc, ReasonWaitingForSlot, false)
		}
		defer release()
	}

	value, queryErr := r.evaluate(ctx, &mc)
	if queryErr != nil {
		log.Warn().Err(queryErr).Str("provider", mc.Spec.Provider).Msg("metric query failed")
		return r.record(ctx, log, &mc, interval, "", "Fail", fmt.Sprintf("%s query error: %s", providerName(&mc.Spec), queryErr))
	}

	// Evaluate threshold.
	result, reason := evaluateThreshold(value, mc.Spec.Threshold)

	log.Info().
		Str("value", value.Text).
		Str("result", result).
		Str("reason", reason).
		Msg("metriccheck evaluated")

	return r.record(ctx, log, &mc, interval, value.Text, result, reason)
}

// providerName is spec.provider, prometheus when empty.
func providerName(spec *kardinalv1alpha1.MetricCheckSpec) string {
	if spec.Provider == "" {
		return "prometheus"
	}
	return spec.Provider
}

// evaluate runs the MetricCheck's query with its provider's backend.
func (r *Reconciler) evaluate(ctx context.Context, mc *kardinalv1alpha1.MetricCheck) (Value, error) {
	provider := providerName(&mc.Spec)
	secrets := r.SecretReader
	if secrets == nil {
		secrets = r.Client
	}
	q := Query{Spec: &mc.Spec, Secret: secretReader(secrets, mc.Namespace), Now: r.now()}
	if b, ok := r.Backends[provider]; ok {
		return b.Evaluate(ctx, q)
	}
	if provider == "prometheus" && r.Provider != nil {
		if mc.Spec.Prometheus != nil && mc.Spec.Prometheus.AuthorizationSecretRef != nil {
			return Value{}, errors.New("prometheus authorization is not supported by this controller")
		}
		v, err := r.Provider.QueryScalar(ctx, mc.Spec.PrometheusURL, mc.Spec.Query)
		if err != nil {
			return Value{}, err
		}
		return NumberValue(v), nil
	}
	return Value{}, fmt.Errorf("provider %q is not available", provider)
}

// templateReason is the status reason of a per-promotion MetricCheck: it
// names the placeholders the Graph cannot replace.
func templateReason(spec *kardinalv1alpha1.MetricCheckSpec) string {
	var unknown []string
	for _, text := range templatedTexts(spec) {
		for _, name := range graph.MetricPlaceholders(text) {
			if !graph.KnownMetricPlaceholder(name) {
				unknown = append(unknown, name)
			}
		}
	}
	if len(unknown) == 0 {
		return ReasonTemplate
	}
	return fmt.Sprintf("%s. Unknown placeholder {{ %s }}: instances will fail", ReasonTemplate,
		strings.Join(unknown, " }}, {{ "))
}

// templatedTexts are the spec fields whose placeholders the Graph replaces.
func templatedTexts(spec *kardinalv1alpha1.MetricCheckSpec) []string {
	texts := []string{spec.Query}
	if spec.Web != nil {
		texts = append(texts, spec.Web.URL, spec.Web.Body)
		for _, h := range spec.Web.Headers {
			if h.Value != nil {
				texts = append(texts, *h.Value)
			}
		}
	}
	return texts
}

// unrenderedPlaceholders returns the placeholders left in the templated
// fields of a MetricCheck that is queried.
func unrenderedPlaceholders(spec *kardinalv1alpha1.MetricCheckSpec) []string {
	var names []string
	for _, text := range templatedTexts(spec) {
		names = append(names, graph.MetricPlaceholders(text)...)
	}
	return names
}

// markNotQueried writes the status of a MetricCheck that is not queried: its
// reason and, for a template (clear), no result, value or validity. It writes
// only when something changes, so it is idempotent and does not requeue.
func (r *Reconciler) markNotQueried(ctx context.Context, mc *kardinalv1alpha1.MetricCheck, reason string, clear bool) error {
	want := mc.Status
	want.Reason = reason
	if clear {
		want = kardinalv1alpha1.MetricCheckStatus{Reason: reason}
	}
	if equality.Semantic.DeepEqual(want, mc.Status) {
		return nil
	}
	patch := client.MergeFrom(mc.DeepCopy())
	mc.Status = want
	if err := r.Status().Patch(ctx, mc, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("status patch: %w", err)
	}
	return nil
}

// record writes an evaluation to the status and requeues after interval.
// A failed write is retried after writeRetry: soon the first time, then at
// interval. It requeues, not with an error: the error backoff would retry at
// once and then ever later (up to 1000s), querying the backend on every
// retry, and the next evaluation would come long after the write works
// again. Until a write succeeds, the result PolicyGates read goes stale at
// its status.validUntil, so they fail closed.
func (r *Reconciler) record(ctx context.Context, log zerolog.Logger, mc *kardinalv1alpha1.MetricCheck,
	interval time.Duration, lastValue, result, reason string) (ctrl.Result, error) {
	lastWrite := mc.Status.LastEvaluatedAt.DeepCopy()
	if err := r.patchStatus(ctx, mc, lastValue, result, reason); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		// patchStatus set lastEvaluatedAt to the time of the write that failed.
		retryIn := writeRetry(lastWrite, mc.CreationTimestamp.Time, mc.Status.LastEvaluatedAt.Time, interval)
		log.Error().Err(err).Dur("retryIn", retryIn).Msg("metriccheck status write failed")
		return ctrl.Result{RequeueAfter: retryIn}, nil
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// writeRetry is how long to wait before retrying a status write that failed
// at attempted. The first failure after a write that worked is retried after
// min(interval, 5s), since a conflict or a brief API server outage is likely
// gone by then; while the write keeps failing it is retried at interval.
//
// Which failure this is follows from the stored object, not from memory
// (nothing is kept between reconciles): lastWrite is status.lastEvaluatedAt
// as read, the time of the last write that worked. The first failure is the
// evaluation due one interval after it, and the retry of that failure comes
// min(interval, 5s) later, past the window. A failure less than an interval
// after it (an edit or a controller restart started the reconcile, or this
// replica's clock is behind) is retried at interval: retrying it early
// would stay in the window, every 5s, until an interval had passed. A
// MetricCheck never written is retried early only on its first attempt,
// right after it is created.
func writeRetry(lastWrite *metav1.Time, created, attempted time.Time, interval time.Duration) time.Duration {
	early := min(interval, firstWriteRetry)
	since, from := attempted.Sub(created), time.Duration(0)
	if lastWrite != nil {
		since, from = attempted.Sub(lastWrite.Time), interval
	}
	if since >= from && since < from+early {
		return early
	}
	return interval
}

// patchStatus patches MetricCheck.status with the latest evaluation result,
// and with validUntil, after which PolicyGates treat the result as stale.
func (r *Reconciler) patchStatus(ctx context.Context, mc *kardinalv1alpha1.MetricCheck,
	lastValue, result, reason string) error {
	patch := client.MergeFrom(mc.DeepCopy())
	evaluatedAt := r.now()
	now := metav1.NewTime(evaluatedAt)
	validUntil := metav1.NewTime(evaluatedAt.Add(validFor(parseInterval(mc.Spec.Interval))))
	mc.Status.LastValue = lastValue
	mc.Status.Result = result
	mc.Status.Reason = reason
	mc.Status.LastEvaluatedAt = &now
	mc.Status.ValidUntil = &validUntil
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
// Only spec or annotation changes trigger a reconcile: the reconciler's own status patch
// would otherwise cause a second query right after each one. A spec change
// includes the Graph flipping spec.suspend on a per-promotion instance.
// Re-evaluation is driven by RequeueAfter.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.MetricCheck{}, builder.WithPredicates(eventfilter.SpecOrAnnotationChanged)).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		Complete(r)
}

// evaluateThreshold compares value against threshold and returns "Pass" or "Fail" with reason.
// With threshold.text the value is compared as a string (eq, ne); otherwise
// it must be a number.
func evaluateThreshold(value Value, t kardinalv1alpha1.MetricThreshold) (string, string) {
	if t.Text != nil {
		var pass bool
		switch t.Operator {
		case "eq":
			pass = value.Text == *t.Text
		case "ne":
			pass = value.Text != *t.Text
		default:
			return "Fail", fmt.Sprintf("operator %q cannot compare text: use eq or ne", t.Operator)
		}
		return passFail(pass), fmt.Sprintf("%q %s %q = %t", value.Text, t.Operator, *t.Text, pass)
	}
	if !value.Numeric {
		return "Fail", fmt.Sprintf("value %q is not a number: set threshold.text to compare text", value.Text)
	}
	v := value.Number
	if math.IsNaN(v) || math.IsInf(v, 0) {
		// NaN compares false with everything and true with ne: a division by
		// zero must not pass a gate.
		return "Fail", fmt.Sprintf("value %s is not a finite number", value.Text)
	}
	var pass bool
	switch t.Operator {
	case "lt":
		pass = v < t.Value
	case "gt":
		pass = v > t.Value
	case "lte":
		pass = v <= t.Value
	case "gte":
		pass = v >= t.Value
	case "eq":
		pass = v == t.Value
	case "ne":
		pass = v != t.Value
	default:
		return "Fail", fmt.Sprintf("unknown operator %q", t.Operator)
	}
	return passFail(pass), fmt.Sprintf("%g %s %g = %t", v, t.Operator, t.Value, pass)
}

// passFail is "Pass" when pass, otherwise "Fail".
func passFail(pass bool) string {
	if pass {
		return "Pass"
	}
	return "Fail"
}

// validFor is how long a result evaluated every interval stays valid:
// staleAfterIntervals intervals, and at least minValidFor.
func validFor(interval time.Duration) time.Duration {
	return max(staleAfterIntervals*interval, minValidFor)
}

// parseInterval parses a Go duration string, returning defaultInterval on
// error and raising values below minInterval to minInterval.
func parseInterval(s string) time.Duration {
	if s == "" {
		return defaultInterval
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return defaultInterval
	}
	if d < minInterval {
		return minInterval
	}
	return d
}
