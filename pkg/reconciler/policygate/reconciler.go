// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package policygate implements the PolicyGateReconciler which evaluates
// CEL policy expressions on PolicyGate instances created by the Graph controller.
package policygate

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

const (
	// labelBundle is the label that identifies a PolicyGate instance (vs template).
	labelBundle = "kardinal.io/bundle"
	// labelGateTemplate names the template a gate instance was made from.
	labelGateTemplate = "kardinal.io/gate-template"
	// labelEnvironment is the environment the gate is evaluated for.
	labelEnvironment = "kardinal.io/environment"
	// labelPipeline is the pipeline the gate is associated with.
	labelPipeline = "kardinal.io/pipeline"
	// conditionReady is the gate condition whose lastTransitionTime marks when
	// the gate last flipped between allowed and blocked.
	conditionReady = "Ready"
	// condBundleGraphReady is the Bundle condition that mirrors its Graph's
	// Ready condition (written by the Bundle reconciler).
	condBundleGraphReady = "GraphReady"
	// metricResultStale replaces metrics.<name>.result when the MetricCheck's
	// status.validUntil is unset or has passed (#1302).
	metricResultStale = "Stale"
	// defaultRecheckInterval is used when gate.Spec.RecheckInterval is empty or invalid.
	defaultRecheckInterval = 5 * time.Minute
	// minRecheckInterval is the shortest re-evaluation interval, the same as
	// MetricCheck's. A smaller recheckInterval ("1ms") would re-evaluate and
	// patch the gate in a hot loop.
	minRecheckInterval = 10 * time.Second
	// historyLimit is the number of recent Bundles to include in history stats.
	historyLimit = 10
)

// Reconciler evaluates PolicyGate CEL expressions and patches status.
// It only processes instances (gates with kardinal.io/bundle label).
// Template PolicyGates (no bundle label) are ignored.
type Reconciler struct {
	client.Client
	// eval is the CEL evaluator, created once at construction time.
	// Unexported: callers should use NewReconciler to get a properly initialized Reconciler.
	eval *evaluator
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
	// MetricsNowFn, when set, is the time MetricCheck results are checked for
	// staleness against, instead of the evaluation time. `kardinal policy
	// simulate` sets it to the real current time, so a simulated --time keeps
	// the cluster's metric results as they are now. The controller leaves it
	// nil: staleness is judged at the evaluation time.
	MetricsNowFn func() time.Time
	// Recorder emits Kubernetes Events when a PolicyGate first blocks.
	// When nil, event emission is skipped (backward-compatible).
	Recorder events.EventRecorder
}

// NewReconciler creates a Reconciler with an initialized CEL evaluator.
// Use this instead of struct literal construction.
func NewReconciler(c client.Client) (*Reconciler, error) {
	ev, err := newEvaluator()
	if err != nil {
		return nil, fmt.Errorf("new policygate evaluator: %w", err)
	}
	return &Reconciler{
		Client: c,
		eval:   ev,
	}, nil
}

// Reconcile evaluates the CEL expression on a PolicyGate instance.
//
// State machine:
//   - No kardinal.io/bundle label → template, skip (no-op)
//   - Gate not found → deleted, skip
//   - Bundle settled (Superseded, or Verified with GraphReady True) → status
//     kept as it was, skip (no requeue)
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

	if writable, err := r.markGenerated(ctx, &gate); err != nil {
		return ctrl.Result{}, err
	} else if !writable {
		log.Warn().Msgf("PolicyGate name is longer than %d characters and kardinal did not create it, "+
			"so its status cannot be written and no Graph uses it; recreate it with a shorter name",
			validation.LabelValueMaxLength)
		return ctrl.Result{}, nil
	}

	// Template PolicyGates (no bundle label) are platform/team-defined gate specs.
	// They are not evaluated for a specific bundle, but we validate their CEL
	// expression and surface compilation errors in status.reason (Issue #315).
	bundleName := gate.Labels[labelBundle]
	if bundleName == "" {
		log.Debug().Msg("policygate has no bundle label, validating CEL syntax (template)")
		return r.reconcileTemplate(ctx, &gate)
	}

	// A settled Bundle never promotes again, so its gate instances are left
	// as they were: no evaluation, status write, audit record or requeue.
	// Superseded is terminal (E2E-R20). A Verified Bundle whose GraphReady is
	// True is finished: its Graph is not read again, so a gate write would
	// only wake the Graph and NotificationHook watchers for nothing (#1301).
	// A Failed Bundle can recover, so its gates keep being evaluated.
	if r.bundleSettled(ctx, gate.Namespace, bundleName) {
		log.Debug().Str("bundle", bundleName).Msg("bundle settled, gate not evaluated")
		return ctrl.Result{}, nil
	}

	recheckInterval := parseRecheckInterval(gate.Spec.RecheckInterval)

	// Check for active override (K-09): if any non-expired override exists for
	// this gate (matching stage or stage=""), the gate passes immediately.
	// This check is done before building the CEL context for performance.
	now := r.now()
	if activeOverride := findActiveOverride(gate.Spec.Overrides, gate.Labels[labelEnvironment], now); activeOverride != nil {
		overrideReason := fmt.Sprintf("OVERRIDDEN by %s: %s (expires %s)",
			activeOverride.CreatedBy,
			activeOverride.Reason,
			activeOverride.ExpiresAt.UTC().Format("2006-01-02T15:04Z"))
		log.Info().Str("reason", overrideReason).Msg("policygate override active, passing")
		if patchErr := r.patchStatus(ctx, &gate, true, overrideReason); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch gate status (override): %w", patchErr)
		}
		// Re-evaluate just after the override expires, so the gate does not stay
		// force-passed for up to a whole recheck interval (C04-gates-21).
		return ctrl.Result{RequeueAfter: min(recheckInterval, activeOverride.ExpiresAt.Sub(now)+time.Second)}, nil
	}

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
	pass, reason, evalErr := r.eval.evaluate(ctx, gate.Spec.Expression, celCtx)
	// Name the stale metrics the expression uses when it blocks: a stale
	// value is empty, so a double(...) comparison fails with an evaluation
	// error rather than false.
	if !pass {
		if notes := staleMetricNotes(gate.Spec.Expression, celCtx); notes != "" {
			reason = reason + "; " + notes
		}
	}
	// Prefix reason with bundle version so it is CRD-observable without a
	// separate status field (PG-6 partial fix — full fix requires api/v1alpha1
	// field addition for a dedicated status.bundleVersion field). An
	// evaluation error carries it too.
	if bundleVersion != "" {
		reason = fmt.Sprintf("bundle.version=%s: %s", bundleVersion, reason)
	}
	if evalErr != nil {
		// Fail-closed on evaluation error. spec.message explains a false
		// expression, not an error, so the reason does not lead with it.
		log.Warn().Err(evalErr).Str("expr", gate.Spec.Expression).
			Msg("CEL evaluation error, setting gate to blocked")
		if patchErr := r.patchStatus(ctx, &gate, false, reason); patchErr != nil {
			return ctrl.Result{}, fmt.Errorf("patch gate status on eval error: %w", patchErr)
		}
		return ctrl.Result{RequeueAfter: recheckInterval}, nil
	}

	// A false expression is what spec.message explains: lead with it, so the
	// Ready condition, kardinal explain/status, the UI, PR evidence and
	// notifications show it (GATE-MESSAGE-01).
	if !pass {
		reason = graph.BlockedReason(gate.Spec.Message, reason)
	}

	log.Info().
		Bool("ready", pass).
		Str("reason", reason).
		Str("expr", gate.Spec.Expression).
		Msg("policygate evaluated")

	if patchErr := r.patchStatus(ctx, &gate, pass, reason); patchErr != nil {
		return ctrl.Result{}, fmt.Errorf("patch gate status: %w", patchErr)
	}

	// Emit Prometheus metric. This counts every evaluation, including rechecks
	// with an unchanged result; it is a rate of evaluations, not of transitions.
	gateResult := "blocked"
	if pass {
		gateResult = "allowed"
	}
	observability.GateEvaluationsTotal.WithLabelValues(gateResult).Inc()

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
	celErr := r.eval.validate(gate.Spec.Expression)

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
	// (only instances are evaluated against a real bundle context and may become
	// ready=true). A template is not evaluated, so lastEvaluatedAt stays unset and
	// no Ready condition, audit record or Blocked event is written: a template is
	// not blocking anything (C04-gates-11).
	patch := client.MergeFrom(gate.DeepCopy())
	gate.Status.Ready = false
	gate.Status.Reason = reason
	gate.Status.LastEvaluatedAt = nil
	if patchErr := r.Status().Patch(ctx, gate, patch); patchErr != nil {
		return ctrl.Result{}, fmt.Errorf("patch template gate status: %w", patchErr)
	}

	return ctrl.Result{}, nil
}

// markGenerated sets spec.generated on a PolicyGate kardinal created before
// the field existed whose name is longer than 63 characters: a gate instance
// (kardinal.io/bundle or kardinal.io/gate-template label) or a freeze gate
// (lifecycle.IsFreezeGate). The CRD refuses every write to such a gate,
// status writes included, until it has the field. kro keeps the field when it
// re-applies an instance from a Graph rendered before the field existed,
// because kro does not own it.
//
// It reports whether gate can be written: false for a long-named gate
// kardinal did not create, which only the older, laxer name rule let in. No
// Graph uses such a gate (graph validateGateNames refuses it), and setting
// the field would only hide it.
func (r *Reconciler) markGenerated(ctx context.Context, gate *kardinalv1alpha1.PolicyGate) (bool, error) {
	if gate.Spec.Generated || len(gate.Name) <= validation.LabelValueMaxLength {
		return true, nil
	}
	_, created := gate.Labels[labelBundle]
	if _, fromTemplate := gate.Labels[labelGateTemplate]; fromTemplate {
		created = true
	}
	if pipeline, freeze := strings.CutPrefix(gate.Name, lifecycle.FreezeGateName("")); freeze {
		created = created || lifecycle.IsFreezeGate(gate, pipeline)
	}
	if !created {
		return false, nil
	}
	patch := client.MergeFrom(gate.DeepCopy())
	gate.Spec.Generated = true
	if err := r.Patch(ctx, gate, patch); err != nil {
		return false, fmt.Errorf("set spec.generated on policygate %s: %w", gate.Name, err)
	}
	return true, nil
}

// bundleSettled reports whether the gate's Bundle exists and is either
// Superseded, or Verified with its GraphReady condition True. The Bundle
// reconciler keeps a Verified Bundle's GraphReady up to date until it is True
// and does not read the Graph after that. A read error is not treated as
// settled: buildContext reports it and the gate fails closed as before.
func (r *Reconciler) bundleSettled(ctx context.Context, namespace, bundleName string) bool {
	var bundle kardinalv1alpha1.Bundle
	if err := r.Get(ctx, types.NamespacedName{Name: bundleName, Namespace: namespace}, &bundle); err != nil {
		return false
	}
	switch bundle.Status.Phase {
	case "Superseded":
		return true
	case "Verified":
		return meta.IsStatusConditionTrue(bundle.Status.Conditions, condBundleGraphReady)
	}
	return false
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

	// bundle.labels is always a map (empty when the Bundle has no labels), so
	// has(bundle.labels.hotfix) and "kardinal.io/rollback" in bundle.labels work.
	labelsCtx := make(map[string]interface{}, len(bundle.Labels))
	for k, v := range bundle.Labels {
		labelsCtx[k] = v
	}

	bundleCtx := map[string]interface{}{
		"type":    bundle.Spec.Type,
		"version": version,
		"labels":  labelsCtx,
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
	// and expose them as metrics.<name>.value, .result and .stale.
	metricsNow := now
	if r.MetricsNowFn != nil {
		metricsNow = r.MetricsNowFn()
	}
	metricsCtx, err := r.buildMetricsContext(ctx, gate.Namespace, metricsNow)
	if err != nil {
		// Non-fatal: log and continue with empty metrics context so the gate
		// evaluates with whatever data is available (fail-closed if expr references metrics).
		zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to build metrics context, using empty")
		metricsCtx = map[string]interface{}{}
	}

	// Build upstream soak context from bundle environment statuses,
	// enriched with cross-stage history from all Bundles in the pipeline (K-10).
	// SoakMinutes is read from Bundle.status.environments[*].soakMinutes (PG-3 fix).
	pipelineName := gate.Labels[labelPipeline]
	upstreamCtx, upstreamErr := r.buildUpstreamContextWithHistory(ctx, gate.Namespace, pipelineName, &bundle)
	if upstreamErr != nil {
		zerolog.Ctx(ctx).Warn().Err(upstreamErr).Msg("failed to build upstream history context, using basic soak only")
		upstreamCtx = buildUpstreamContext(&bundle)
	}

	// bundle.upstreamSoakMinutes is the soak time of the environment(s) directly
	// upstream of the gated environment in the Pipeline DAG, so expressions can write
	//   bundle.upstreamSoakMinutes >= 30
	// without naming the upstream. It is only computed when the expression uses it,
	// because it needs the Pipeline; a lookup failure fails the gate closed.
	// Documented in docs/policy-gates.md and docs/reference/cel-context.md.
	if strings.Contains(gate.Spec.Expression, "upstreamSoakMinutes") {
		soak, soakErr := r.directUpstreamSoakMinutes(ctx, gate, &bundle)
		if soakErr != nil {
			return nil, version, fmt.Errorf("bundle.upstreamSoakMinutes: %w", soakErr)
		}
		bundleCtx["upstreamSoakMinutes"] = soak
	}

	// Build PR review context (K-08): bundle.pr["<envName>"].isApproved / .approvalCount
	// Reads PRStatus CRDs labelled with this bundle. Non-fatal on error.
	prCtx, prErr := r.buildPRContext(ctx, gate.Namespace, bundleName)
	if prErr != nil {
		zerolog.Ctx(ctx).Warn().Err(prErr).Msg("failed to build PR context, using empty")
		prCtx = map[string]interface{}{}
	}
	bundleCtx["pr"] = prCtx

	// ChangeWindows are only listed when the expression references them, so a
	// List failure blocks exactly the gates that depend on a window (fail closed).
	cwCtx := map[string]interface{}{}
	if strings.Contains(gate.Spec.Expression, "changewindow") {
		var cwErr error
		cwCtx, cwErr = r.buildChangeWindowContext(ctx, now)
		if cwErr != nil {
			return nil, version, fmt.Errorf("changewindow: %w", cwErr)
		}
	}

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
		"metrics":      metricsCtx,
		"upstream":     upstreamCtx,
		"changewindow": cwCtx,
	}, version, nil
}

// directUpstreamSoakMinutes returns the soak minutes of the environments that
// the gate's environment directly depends on for this bundle (Pipeline dependsOn
// or list order, with skipped environments bridged, exactly as the Graph is
// built). With several upstreams (fan-in) it returns the minimum, so every
// upstream must have soaked long enough. An upstream that is not Verified counts
// as 0. A root environment (no upstream) returns 0.
//
// It reads only CRD state: the Pipeline spec and Bundle.status.environments[].soakMinutes,
// which the BundleReconciler writes.
func (r *Reconciler) directUpstreamSoakMinutes(ctx context.Context, gate *kardinalv1alpha1.PolicyGate,
	bundle *kardinalv1alpha1.Bundle) (int64, error) {
	envName := gate.Labels[labelEnvironment]
	if envName == "" {
		return 0, fmt.Errorf("gate has no %s label", labelEnvironment)
	}
	pipelineName := bundle.Spec.Pipeline
	if pipelineName == "" {
		pipelineName = gate.Labels[labelPipeline]
	}
	var pipeline kardinalv1alpha1.Pipeline
	if err := r.Get(ctx, types.NamespacedName{Name: pipelineName, Namespace: gate.Namespace}, &pipeline); err != nil {
		return 0, fmt.Errorf("load pipeline %s: %w", pipelineName, err)
	}
	upstreams, err := graph.DirectUpstreams(&pipeline, bundle, envName)
	if err != nil {
		return 0, err
	}
	if len(upstreams) == 0 {
		return 0, nil
	}
	byName := make(map[string]kardinalv1alpha1.EnvironmentStatus, len(bundle.Status.Environments))
	for _, env := range bundle.Status.Environments {
		byName[env.Name] = env
	}
	var minSoak int64 = -1
	for _, up := range upstreams {
		var soak int64
		if env, ok := byName[up]; ok && env.Phase == "Verified" {
			soak = env.SoakMinutes
		}
		if minSoak < 0 || soak < minSoak {
			minSoak = soak
		}
	}
	return minSoak, nil
}

// buildMetricsContext lists all MetricCheck objects in the given namespace and
// returns a map suitable for CEL:
// {"<name>": {"value": <string>, "result": <string>, "stale": <bool>}}.
// Only MetricCheck objects in the gate's own namespace are included.
//
// A result whose status.validUntil is unset or before now is stale (#1302):
// nothing has refreshed it for three MetricCheck intervals, for example after
// an outage or while its status patch keeps failing. It is exposed as result
// "Stale" with an empty value, so both metrics.x.result == "Pass" and
// double(metrics.x.value) < ... fail closed, and stale is true.
func (r *Reconciler) buildMetricsContext(ctx context.Context, ns string, now time.Time) (map[string]interface{}, error) {
	var list kardinalv1alpha1.MetricCheckList
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list metricchecks: %w", err)
	}

	result := make(map[string]interface{}, len(list.Items))
	for _, mc := range list.Items {
		entry := map[string]interface{}{
			"value":  mc.Status.LastValue,
			"result": mc.Status.Result,
			"stale":  false,
		}
		if mc.Status.ValidUntil == nil || mc.Status.ValidUntil.Time.Before(now) {
			entry["value"], entry["result"], entry["stale"] = "", metricResultStale, true
		}
		result[mc.Name] = entry
	}
	return result, nil
}

// staleMetricNotes returns `metric "x" result is stale` for each stale metric
// in the evaluation context that expr refers to by name, sorted, joined with
// "; ". It only explains a blocked gate in status.reason; it does not change
// the result.
func staleMetricNotes(expr string, celCtx map[string]interface{}) string {
	metrics, _ := celCtx["metrics"].(map[string]interface{})
	var notes []string
	for name, v := range metrics {
		entry, _ := v.(map[string]interface{})
		if stale, _ := entry["stale"].(bool); stale && exprRefersToMetric(expr, name) {
			notes = append(notes, fmt.Sprintf("metric %q result is stale", name))
		}
	}
	sort.Strings(notes)
	return strings.Join(notes, "; ")
}

// exprRefersToMetric reports whether expr names the metric: metrics["name"],
// metrics['name'] or metrics.name.
func exprRefersToMetric(expr, name string) bool {
	if strings.Contains(expr, `"`+name+`"`) || strings.Contains(expr, `'`+name+`'`) {
		return true
	}
	for rest := expr; ; {
		i := strings.Index(rest, "metrics."+name)
		if i < 0 {
			return false
		}
		rest = rest[i+len("metrics."+name):]
		if rest == "" || !isIdentChar(rest[0]) {
			return true
		}
	}
}

// isIdentChar reports whether c can continue a CEL identifier.
func isIdentChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
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

// buildUpstreamContextWithHistory extends buildUpstreamContext with cross-stage
// promotion history (K-10). It lists the last historyLimit Bundles for the same
// pipeline and computes per-environment history stats:
//
//   - soakMinutes         — minutes since this Bundle was Verified in the environment
//   - recentSuccessCount  — number of Verified bundles in last historyLimit
//   - recentFailureCount  — number of Failed bundles in last historyLimit
//   - lastPromotedAt      — RFC3339 timestamp of last verified promotion (or "")
//
// CEL usage:
//
//	upstream.staging.recentSuccessCount >= 3    # last 3 staging promotions succeeded
//	upstream.staging.lastPromotedAt != ""       # staging was ever promoted
//
// Graph-purity: reads Bundle CRD status only. No external API calls.
// Non-fatal: on List error, falls back to basic soakMinutes-only context.
func (r *Reconciler) buildUpstreamContextWithHistory(
	ctx context.Context, ns, pipelineName string,
	currentBundle *kardinalv1alpha1.Bundle,
) (map[string]interface{}, error) {
	// Start with current bundle's soak data (always available).
	result := buildUpstreamContext(currentBundle)

	if pipelineName == "" {
		// No pipeline label — cannot query history. Return soak-only context.
		return result, nil
	}

	// List all Bundles for this pipeline in the same namespace.
	var list kardinalv1alpha1.BundleList
	if err := r.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list bundles in namespace %s: %w", ns, err)
	}

	// Filter to this pipeline only (in-memory filter — no field indexer required).
	var pipelineBundles []kardinalv1alpha1.Bundle
	for _, b := range list.Items {
		if b.Spec.Pipeline == pipelineName {
			pipelineBundles = append(pipelineBundles, b)
		}
	}

	// Sort by creation time (newest first) to take the last historyLimit.
	sorted := sortBundlesByCreationDesc(pipelineBundles)
	if len(sorted) > historyLimit {
		sorted = sorted[:historyLimit]
	}

	// Per-environment stats across the last historyLimit bundles.
	type envStats struct {
		successCount int64
		failureCount int64
		lastAt       string // RFC3339 or ""
	}
	stats := make(map[string]*envStats)

	for _, b := range sorted {
		for _, env := range b.Status.Environments {
			if _, ok := stats[env.Name]; !ok {
				stats[env.Name] = &envStats{}
			}
			st := stats[env.Name]
			switch env.Phase {
			case "Verified":
				st.successCount++
				// Track latest verified timestamp.
				if env.HealthCheckedAt != nil {
					ts := env.HealthCheckedAt.UTC().Format(time.RFC3339)
					if st.lastAt == "" || ts > st.lastAt {
						st.lastAt = ts
					}
				}
			case "Failed":
				st.failureCount++
			}
		}
	}

	// Merge history stats into the upstream context.
	for envName, st := range stats {
		existing, ok := result[envName].(map[string]interface{})
		if !ok {
			existing = map[string]interface{}{"soakMinutes": int64(0)}
		}
		existing["recentSuccessCount"] = st.successCount
		existing["recentFailureCount"] = st.failureCount
		existing["lastPromotedAt"] = st.lastAt
		result[envName] = existing
	}

	// Ensure current bundle's env entries have history fields (zero-valued).
	for _, env := range currentBundle.Status.Environments {
		entry, ok := result[env.Name].(map[string]interface{})
		if !ok {
			continue
		}
		if _, ok := entry["recentSuccessCount"]; !ok {
			entry["recentSuccessCount"] = int64(0)
			entry["recentFailureCount"] = int64(0)
			entry["lastPromotedAt"] = ""
		}
		result[env.Name] = entry
	}

	return result, nil
}

// sortBundlesByCreationDesc sorts bundles newest-first by CreationTimestamp.
// Returns a new slice; does not modify the original.
func sortBundlesByCreationDesc(bundles []kardinalv1alpha1.Bundle) []kardinalv1alpha1.Bundle {
	out := make([]kardinalv1alpha1.Bundle, len(bundles))
	copy(out, bundles)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreationTimestamp.After(out[j-1].CreationTimestamp.Time); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// buildChangeWindowContext lists all ChangeWindow objects (cluster-scoped) and
// returns a map: {"<name>": bool} where the boolean is true if the window
// is currently active (blocking). CEL expressions use:
//
//	changewindow["holiday-freeze"]               → true when the window is active
//	!changewindow.isBlocked("holiday-freeze")    → passes when the window is inactive
//	changewindow.isAllowed("business-hours")     → passes inside a recurring allowed window
//
// Each window is evaluated at the gate's own evaluation time with
// changewindow.Evaluate, the same function the ChangeWindow reconciler uses to
// write status.active. Reading only status.active would let a gate pass for the
// moment between a window boundary and the ChangeWindow reconciler's next write;
// the ChangeWindow status write is what triggers the re-evaluation (see the
// ChangeWindow Watch in SetupWithManager).
//
// A List error is returned to the caller so the gate fails closed with a
// "context error" reason: an unreadable freeze must never allow a promotion
// (C04-gates-02). An invalid ChangeWindow spec evaluates as active (blocking).
func (r *Reconciler) buildChangeWindowContext(ctx context.Context, now time.Time) (map[string]interface{}, error) {
	var list kardinalv1alpha1.ChangeWindowList
	if err := r.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list ChangeWindows: %w", err)
	}

	result := make(map[string]interface{}, len(list.Items))
	for _, cw := range list.Items {
		res := changewindow.Evaluate(cw.Spec, now)
		if res.Err != nil {
			zerolog.Ctx(ctx).Warn().Err(res.Err).Str("changewindow", cw.Name).
				Msg("invalid ChangeWindow spec, treating it as active (blocking)")
		}
		result[cw.Name] = res.Active
	}
	return result, nil
}

// buildPRContext lists PRStatus CRDs for this bundle and returns a map
// keyed by environment name:
//
//	{"staging": {"isApproved": true, "approvalCount": 2}, "prod": {...}}
//
// CEL usage: bundle.pr["staging"].isApproved  — true/false
//
//	bundle.pr["staging"].approvalCount >= 2
//
// K-08: the review state is written by PRStatusReconciler to CRD status,
// so this is a pure CRD status read — no external API calls in the hot path.
func (r *Reconciler) buildPRContext(ctx context.Context, ns, bundleName string) (map[string]interface{}, error) {
	var list kardinalv1alpha1.PRStatusList
	if err := r.List(ctx, &list,
		client.InNamespace(ns),
		client.MatchingLabels{"kardinal.io/bundle": bundleName},
	); err != nil {
		return nil, fmt.Errorf("list prstatus for bundle %s: %w", bundleName, err)
	}

	result := make(map[string]interface{}, len(list.Items))
	for _, prs := range list.Items {
		envName := prs.Labels["kardinal.io/environment"]
		if envName == "" {
			continue
		}
		result[envName] = map[string]interface{}{
			"isApproved":    prs.Status.Approved,
			"approvalCount": int64(prs.Status.ApprovalCount),
		}
	}
	return result, nil
}

// patchStatus patches the PolicyGate's status fields.
func (r *Reconciler) patchStatus(ctx context.Context, gate *kardinalv1alpha1.PolicyGate,
	ready bool, reason string) error {
	prevReady := gate.Status.Ready
	isFirstEval := gate.Status.LastEvaluatedAt == nil
	// blockedSince is when the current blocking episode started, read before
	// the condition is updated.
	var blockedSince time.Time
	if c := meta.FindStatusCondition(gate.Status.Conditions, conditionReady); c != nil && c.Status == metav1.ConditionFalse {
		blockedSince = c.LastTransitionTime.Time
	}
	patch := client.MergeFrom(gate.DeepCopy())
	now := metav1.NewTime(r.now())
	gate.Status.Ready = ready
	gate.Status.Reason = reason
	gate.Status.LastEvaluatedAt = &now
	// The Ready condition's lastTransitionTime moves only when the gate flips,
	// unlike lastEvaluatedAt, so it identifies one blocking episode. The
	// NotificationHook reconciler keys PolicyGate.Blocked on it.
	cond := metav1.Condition{
		Type:               conditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Blocked",
		Message:            truncateMessage(reason),
		ObservedGeneration: gate.Generation,
		LastTransitionTime: now,
	}
	if ready {
		cond.Status, cond.Reason = metav1.ConditionTrue, "Allowed"
	}
	meta.SetStatusCondition(&gate.Status.Conditions, cond)
	if err := r.Status().Patch(ctx, gate, patch); err != nil {
		return fmt.Errorf("status patch: %w", err)
	}
	// Audit the first evaluation and every ready flip, not every recheck.
	if ready != prevReady || isFirstEval {
		outcome := "Success"
		if !ready {
			outcome = "Failure"
		}
		writeGateAuditEvent(ctx, r.Client, gate, outcome, reason, now)
	}
	// Emit Kubernetes Event on gate state change OR on first evaluation that blocks.
	// First block: isFirstEval && !ready (gate immediately blocks on creation).
	// State flip: ready != prevReady (gate transitions between allowed/blocked).
	if r.Recorder != nil {
		envName := gate.Labels[labelEnvironment]
		pipeline := gate.Labels[labelPipeline]
		if (!ready && isFirstEval) || (!ready && ready != prevReady) {
			// Gate is blocking (first eval or newly blocked).
			kubeevent.Emit(r.Recorder, gate, corev1.EventTypeWarning, "Blocked", "Evaluate",
				fmt.Sprintf("env %s pipeline %s: gate %s blocking promotion: %s",
					envName, pipeline, gate.Name, reason))
		} else if ready && ready != prevReady {
			// Gate just allowed (was blocked before).
			kubeevent.Emit(r.Recorder, gate, corev1.EventTypeNormal, "Allowed", "Evaluate",
				fmt.Sprintf("env %s pipeline %s: gate %s now allowing promotion",
					envName, pipeline, gate.Name))
		}
	}
	// Emit gate blocking duration when gate transitions from blocked to allowed:
	// the time since the Ready condition turned False. A gate that passes on
	// its first evaluation was never blocked and is not observed. Gates
	// evaluated before the condition existed fall back to CreationTimestamp
	// (C04-gates-32).
	if ready && !prevReady && !isFirstEval {
		if blockedSince.IsZero() {
			blockedSince = gate.CreationTimestamp.Time
		}
		blockingDuration := now.Sub(blockedSince)
		if blockingDuration > 0 {
			observability.GateBlockingDurationSeconds.Observe(blockingDuration.Seconds())
		}
	}
	return nil
}

// maxConditionMessage bounds the Ready condition message; a CEL error can be long.
const maxConditionMessage = 1024

// truncateMessage shortens msg to maxConditionMessage bytes on a rune boundary.
func truncateMessage(msg string) string {
	if len(msg) <= maxConditionMessage {
		return msg
	}
	cut := maxConditionMessage
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut]
}

// now returns the current time, using NowFn if set (for testing).
func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}

// metricCheckRequests enqueues the PolicyGate instances in the MetricCheck's
// namespace whose expression reads metrics, so they are re-evaluated as soon
// as a MetricCheck result changes.
func (r *Reconciler) metricCheckRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.instanceGateRequests(ctx, "MetricCheck", "metrics", client.InNamespace(obj.GetNamespace()))
}

// metricCheckResultChanged passes MetricCheck updates that change what a gate
// can read (metrics.<name>.value, .result and .stale) or the spec. The
// MetricCheck reconciler writes lastEvaluatedAt on every query; re-evaluating
// every gate in the namespace on each of those writes was wasted work
// (C04-gates-36). An evaluation that makes a stale result fresh again passes
// too (#1302): the old validUntil is unset or before the new lastEvaluatedAt,
// so a gate held on the stale result re-evaluates at once even when the
// result and value did not change. Create and delete events pass.
var metricCheckResultChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldMC, okOld := e.ObjectOld.(*kardinalv1alpha1.MetricCheck)
		newMC, okNew := e.ObjectNew.(*kardinalv1alpha1.MetricCheck)
		if !okOld || !okNew {
			return true
		}
		return oldMC.Generation != newMC.Generation ||
			oldMC.Status.Result != newMC.Status.Result ||
			oldMC.Status.LastValue != newMC.Status.LastValue ||
			staleToFresh(oldMC.Status, newMC.Status)
	},
}

// staleToFresh reports whether the update is an evaluation of a MetricCheck
// whose previous result had gone stale, or had no validUntil.
func staleToFresh(oldStatus, newStatus kardinalv1alpha1.MetricCheckStatus) bool {
	if newStatus.LastEvaluatedAt == nil {
		return false
	}
	return oldStatus.ValidUntil == nil || oldStatus.ValidUntil.Before(newStatus.LastEvaluatedAt)
}

// stepRequiredGateRequests enqueues the PolicyGates a PromotionStep requires
// (spec.requiredGates, in the step's namespace). A new step starts only on
// gate results evaluated at or after it was created (checkRequiredGates in
// the PromotionStep reconciler, #1300), so its gates are re-evaluated as soon
// as it exists rather than at their next recheck.
func stepRequiredGateRequests(_ context.Context, obj client.Object) []reconcile.Request {
	ps, ok := obj.(*kardinalv1alpha1.PromotionStep)
	if !ok {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(ps.Spec.RequiredGates))
	for _, name := range ps.Spec.RequiredGates {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: ps.Namespace},
		})
	}
	return reqs
}

// unstartedStepCreated passes only the create event of a PromotionStep that
// has not started (state "" or Pending) and requires a gate. Updates, deletes
// and generic events do not pass. At controller start the informer's initial
// list sends a create event for every existing step; the state filter skips
// the finished ones.
var unstartedStepCreated = predicate.Funcs{
	CreateFunc: func(e event.CreateEvent) bool {
		ps, ok := e.Object.(*kardinalv1alpha1.PromotionStep)
		if !ok {
			return false
		}
		return len(ps.Spec.RequiredGates) > 0 &&
			(ps.Status.State == "" || ps.Status.State == "Pending")
	},
	UpdateFunc:  func(event.UpdateEvent) bool { return false },
	DeleteFunc:  func(event.DeleteEvent) bool { return false },
	GenericFunc: func(event.GenericEvent) bool { return false },
}

// SetupWithManager registers the PolicyGateReconciler with the controller-runtime Manager.
// It adds a Watch on MetricCheck objects so that when any MetricCheck in a namespace
// changes (status updated by the MetricCheckReconciler), all PolicyGates in that
// same namespace are queued for re-evaluation. This is the controller-runtime
// equivalent of a "Watch node" — the PolicyGate reconciler reacts to MetricCheck
// status changes rather than waiting for recheckInterval alone.
//
// It also adds a Watch on ScheduleClock objects: when a ScheduleClock's status.tick
// changes (updated on each interval by the ScheduleClockReconciler), all PolicyGate
// instances in ALL namespaces are re-evaluated, so schedule.* expressions follow
// the clock interval. The per-gate RequeueAfter: recheckInterval still runs as
// well; it is the only periodic re-evaluation when no ScheduleClock exists.
//
// The gate's own status writes do not re-trigger it (eventfilter.SpecOrAnnotationChanged;
// a spec edit or an annotation such as kardinal.io/force-recheck does):
// every evaluation writes lastEvaluatedAt, and re-evaluating on that write
// doubled the evaluations and the status writes (C04-gates-36).
//
// It also watches ChangeWindow objects: the ChangeWindow reconciler writes
// status.active at every window boundary, and that write re-evaluates every
// PolicyGate instance whose expression references changewindow, so a freeze
// starts blocking at its boundary rather than at the next recheckInterval.
//
// It also watches PromotionStep creates: a new step's required gates are
// re-evaluated at once, because the step starts only on gate results
// evaluated at or after it was created (#1300). The evaluation's status write
// wakes the step (the PromotionStep reconciler watches PolicyGates).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	// scheduleClockMapper enqueues all PolicyGate instances across ALL namespaces
	// when any ScheduleClock ticks, so schedule.* expressions are re-evaluated
	// on every clock interval rather than only at their recheckInterval.
	scheduleClockMapper := func(ctx context.Context, _ client.Object) []reconcile.Request {
		return r.instanceGateRequests(ctx, "ScheduleClock", "")
	}

	// changeWindowMapper enqueues the PolicyGate instances that reference a
	// ChangeWindow. ChangeWindows are cluster-scoped, so gates in every
	// namespace the controller can see are considered.
	changeWindowMapper := func(ctx context.Context, _ client.Object) []reconcile.Request {
		return r.instanceGateRequests(ctx, "ChangeWindow", "changewindow")
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.PolicyGate{}, builder.WithPredicates(eventfilter.SpecOrAnnotationChanged)).
		// Watch MetricCheck objects: when a MetricCheck's result or value changes,
		// the gates in the same namespace that read metrics are re-evaluated
		// immediately.
		Watches(&kardinalv1alpha1.MetricCheck{}, handler.EnqueueRequestsFromMapFunc(r.metricCheckRequests),
			builder.WithPredicates(metricCheckResultChanged)).
		// Watch ScheduleClock objects: when status.tick changes, all PolicyGate instances
		// are re-evaluated cluster-wide.
		Watches(&kardinalv1alpha1.ScheduleClock{}, handler.EnqueueRequestsFromMapFunc(scheduleClockMapper)).
		// Watch ChangeWindow objects: a window boundary (status.active write) or a
		// spec edit re-evaluates the gates that reference changewindow.
		Watches(&kardinalv1alpha1.ChangeWindow{}, handler.EnqueueRequestsFromMapFunc(changeWindowMapper)).
		// Watch PromotionStep creates: a new unstarted step's required gates
		// are re-evaluated at once (#1300).
		Watches(&kardinalv1alpha1.PromotionStep{}, handler.EnqueueRequestsFromMapFunc(stepRequiredGateRequests),
			builder.WithPredicates(unstartedStepCreated)).
		Complete(r)
}

// instanceGateRequests lists PolicyGates and returns a request for every instance
// gate (one with a bundle label). When exprContains is non-empty, only gates whose
// expression contains it are returned. A List error is logged (the watch event is
// then lost, and the gate is re-evaluated at its next recheckInterval) rather than
// dropped silently (C04-gates-36).
func (r *Reconciler) instanceGateRequests(ctx context.Context, source, exprContains string,
	opts ...client.ListOption) []reconcile.Request {
	var gateList kardinalv1alpha1.PolicyGateList
	if err := r.List(ctx, &gateList, opts...); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("source", source).
			Msg("failed to list PolicyGates for watch event; gates re-evaluate at their recheckInterval")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(gateList.Items))
	for _, gate := range gateList.Items {
		// Only enqueue instance gates (those with a bundle label) — templates
		// are only validated, never evaluated against a context.
		if gate.Labels[labelBundle] == "" {
			continue
		}
		if exprContains != "" && !strings.Contains(gate.Spec.Expression, exprContains) {
			continue
		}
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace},
		})
	}
	return reqs
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

// parseRecheckInterval parses a Go duration string, returning
// defaultRecheckInterval on error and raising values below minRecheckInterval
// to minRecheckInterval.
func parseRecheckInterval(s string) time.Duration {
	if s == "" {
		return defaultRecheckInterval
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return defaultRecheckInterval
	}
	if d < minRecheckInterval {
		return minRecheckInterval
	}
	return d
}

// findActiveOverride returns the first non-expired override matching the given
// environment name (K-09). An override with Stage="" matches any environment.
// Returns nil if no active override is found.
func findActiveOverride(overrides []kardinalv1alpha1.PolicyGateOverride, envName string, now time.Time) *kardinalv1alpha1.PolicyGateOverride {
	for i := range overrides {
		o := &overrides[i]
		if o.ExpiresAt.Time.IsZero() || now.After(o.ExpiresAt.Time) {
			continue // expired or zero
		}
		if o.Stage == "" || o.Stage == envName {
			return o
		}
	}
	return nil
}
