# 04: PolicyGate Reconciler

> Status: Comprehensive
> Depends on: 01-graph-integration
> Blocks: nothing (leaf node)

## Purpose

The PolicyGate reconciler evaluates CEL policy expressions on PolicyGate CRDs created by the Graph controller. It determines whether a promotion is permitted to proceed through the Graph by writing `status.ready` and `status.lastEvaluatedAt` on each PolicyGate instance.

PolicyGate instances are per-Bundle copies of PolicyGate templates. The templates are authored by platform teams in the `platform-policies` namespace (org-level) or team namespaces (team-level). The translator creates instances as Graph nodes (see 02-pipeline-to-graph-translator). The reconciler evaluates instances, not templates.

## Go Package Structure

```
pkg/
  reconciler/
    policygate/
      reconciler.go       # Main reconciliation loop
      context.go          # CEL context building
      evaluator.go        # CEL environment setup and expression evaluation
      recheck.go          # Timer-based re-evaluation loop
      reconciler_test.go
  cel/
    environment.go        # Shared CEL environment configuration
    types.go              # CEL context type definitions
```

## CEL Environment

The CEL environment is configured once at controller startup using `google/cel-go`. It registers the context attributes as typed variables.

```go
func NewCELEnvironment() (*cel.Env, error) {
    return cel.NewEnv(
        cel.Variable("bundle", cel.ObjectType("Bundle")),
        cel.Variable("schedule", cel.ObjectType("Schedule")),
        cel.Variable("environment", cel.ObjectType("Environment")),
        cel.Variable("metrics", cel.MapType(cel.StringType, cel.DoubleType)),
        cel.Variable("previousBundle", cel.ObjectType("PreviousBundle")),
        cel.Variable("delegation", cel.ObjectType("Delegation")),
        cel.Variable("externalApproval", cel.MapType(cel.StringType, cel.AnyType)),
        cel.Variable("contracts", cel.MapType(cel.StringType, cel.AnyType)),
        cel.Variable("targetDrift", cel.ObjectType("TargetDrift")),
    )
}
```

Not all variables are populated in every phase. Referencing an unregistered or unpopulated variable causes a CEL evaluation error, which the reconciler treats as a gate failure (fail-closed).

## CEL Context Building

For each PolicyGate evaluation, the reconciler builds a context struct from cluster data:

### Phase 1 Attributes

| Attribute | Type | Source |
|---|---|---|
| `bundle.version` | string | `Bundle.spec.images[0].tag` (image bundles) or `Bundle.spec.configRef.commitSHA[:8]` prefix (config bundles) |
| `bundle.type` | string | `Bundle.spec.type` ("image" or "config") |
| `bundle.labels.*` | map[string]string | `Bundle.metadata.labels` (filtered to user labels, excluding `kardinal.io/*`) |
| `bundle.provenance.author` | string | `Bundle.spec.provenance.author` |
| `bundle.provenance.commitSHA` | string | `Bundle.spec.provenance.commitSHA` |
| `bundle.provenance.ciRunURL` | string | `Bundle.spec.provenance.ciRunURL` |
| `bundle.intent.targetEnvironment` | string | `Bundle.spec.intent.targetEnvironment` |
| `schedule.isWeekend` | bool | Computed from system clock: `time.Now().Weekday() == Saturday || Sunday` |
| `schedule.hour` | int | `time.Now().UTC().Hour()` |
| `schedule.dayOfWeek` | string | `time.Now().UTC().Weekday().String()` |
| `environment.name` | string | `PolicyGate.labels["kardinal.io/environment"]` |
| `environment.approval` | string | Read from the PromotionStep sibling in the same Graph (via label selector) |

### Phase 2 Attributes (additional)

| Attribute | Type | Source |
|---|---|---|
| `metrics.*` | map[string]float64 | Query MetricCheck CRDs referenced by the PolicyGate, inject results by name |
| `bundle.upstreamSoakMinutes` | int | `(now - upstreamVerifiedAt).Minutes()` where upstreamVerifiedAt is from the upstream PromotionStep |
| `previousBundle.version` | string | Previous Verified Bundle for this Pipeline + environment from Bundle history |

### Phase 3+ Attributes (additional)

| Attribute | Type | Source |
|---|---|---|
| `delegation.status` | string | From Argo Rollouts Rollout or Flagger Canary status |
| `externalApproval.*` | map | From webhook gate HTTP response |
| `contracts.*` | map | Cross-pipeline PolicyGate status for Promotion Contracts |
| `targetDrift.unreconciled` | bool | Whether the target environment has unreconciled changes |

### Context Assembly

```go
func (r *PolicyGateReconciler) buildContext(ctx context.Context, gate *v1alpha1.PolicyGate) (map[string]interface{}, error) {
    bundle := &v1alpha1.Bundle{}
    bundleName := gate.Labels["kardinal.io/bundle"]
    if err := r.Get(ctx, types.NamespacedName{Name: bundleName, Namespace: gate.Namespace}, bundle); err != nil {
        return nil, fmt.Errorf("loading bundle: %w", err)
    }

    now := time.Now().UTC()
    context := map[string]interface{}{
        "bundle": map[string]interface{}{
            "version":    extractVersion(bundle),
            "type":       bundle.Spec.Type,
            "labels":     filterUserLabels(bundle.Labels),
            "provenance": bundle.Spec.Provenance,
            "intent":     bundle.Spec.Intent,
        },
        "schedule": map[string]interface{}{
            "isWeekend": now.Weekday() == time.Saturday || now.Weekday() == time.Sunday,
            "hour":      now.Hour(),
            "dayOfWeek": now.Weekday().String(),
        },
        "environment": map[string]interface{}{
            "name":     gate.Labels["kardinal.io/environment"],
            "approval": r.resolveApprovalMode(ctx, gate),
        },
    }

    // Phase 2: add metrics and soak time if available
    if r.phase >= 2 {
        context["metrics"] = r.queryMetrics(ctx, gate)
        context["bundle"].(map[string]interface{})["upstreamSoakMinutes"] = r.computeSoakMinutes(ctx, gate)
        context["previousBundle"] = r.getPreviousBundle(ctx, gate)
    }

    return context, nil
}
```

## Evaluation

```go
func (r *PolicyGateReconciler) evaluate(gate *v1alpha1.PolicyGate, context map[string]interface{}) (bool, string, error) {
    ast, issues := r.celEnv.Compile(gate.Spec.Expression)
    if issues != nil && issues.Err() != nil {
        return false, fmt.Sprintf("CEL compile error: %s", issues.Err()), issues.Err()
    }
    prg, err := r.celEnv.Program(ast)
    if err != nil {
        return false, fmt.Sprintf("CEL program error: %s", err), err
    }
    out, _, err := prg.Eval(context)
    if err != nil {
        return false, fmt.Sprintf("CEL evaluation error: %s", err), err
    }
    result, ok := out.Value().(bool)
    if !ok {
        return false, "CEL expression did not return a boolean", fmt.Errorf("non-boolean result: %v", out.Value())
    }
    return result, fmt.Sprintf("%s = %v", gate.Spec.Expression, result), nil
}
```

The CEL expression is compiled and cached (keyed by expression string) to avoid recompilation on every evaluation cycle. Cache invalidation: on controller restart.

## Reconciliation Loop

```go
func (r *PolicyGateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    gate := &v1alpha1.PolicyGate{}
    if err := r.Get(ctx, req.NamespacedName, gate); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Only reconcile instances (created by Graph), not templates
    if gate.Labels["kardinal.io/bundle"] == "" {
        return ctrl.Result{}, nil // template, not instance
    }

    // Build context
    context, err := r.buildContext(ctx, gate)
    if err != nil {
        gate.Status.Ready = false
        gate.Status.Reason = fmt.Sprintf("Context build error: %s", err)
        gate.Status.LastEvaluatedAt = metav1.Now()
        r.Status().Update(ctx, gate)
        return ctrl.Result{RequeueAfter: parseDuration(gate.Spec.RecheckInterval)}, nil
    }

    // Evaluate
    result, reason, err := r.evaluate(gate, context)
    if err != nil {
        // CEL error: fail closed
        gate.Status.Ready = false
        gate.Status.Reason = reason
        gate.Status.LastEvaluatedAt = metav1.Now()
        r.Status().Update(ctx, gate)
        return ctrl.Result{RequeueAfter: parseDuration(gate.Spec.RecheckInterval)}, nil
    }

    gate.Status.Ready = result
    gate.Status.Reason = reason
    gate.Status.LastEvaluatedAt = metav1.Now()
    r.Status().Update(ctx, gate)

    // Requeue for periodic re-evaluation
    return ctrl.Result{RequeueAfter: parseDuration(gate.Spec.RecheckInterval)}, nil
}
```

The `RequeueAfter` ensures the gate is re-evaluated at `recheckInterval` even if no cluster state changes. This is the workaround for Graph not having native timer-based re-evaluation (see Section 3.5 of design-v2.1.md). The status write triggers a Graph watch event, causing Graph to re-check the `readyWhen` expression.

## Timer-Based Re-evaluation

The `RequeueAfter` mechanism in controller-runtime handles this natively. Each PolicyGate instance reconciliation returns `ctrl.Result{RequeueAfter: recheckInterval}`. The controller-runtime work queue schedules the next reconcile.

At scale: 100 PolicyGate instances with `recheckInterval: 5m` produces 20 reconciliations per minute. Each reconciliation reads one Bundle + evaluates one CEL expression + writes one status update. This is lightweight and well within Kubernetes API server capacity.

## Template vs Instance Distinction

PolicyGate CRDs serve dual purposes:

- **Templates** are authored by platform teams. They live in `platform-policies` or team namespaces. They do NOT have a `kardinal.io/bundle` label. The reconciler ignores them.
- **Instances** are created by the Graph controller as Graph nodes. They have `kardinal.io/bundle`, `kardinal.io/pipeline`, and `kardinal.io/environment` labels. The reconciler evaluates them.

The reconciler distinguishes by checking for the `kardinal.io/bundle` label. If absent, skip.

## Error Handling

| Error | Behavior |
|---|---|
| Bundle not found | `status.ready = false`, reason: "Bundle not found". Requeue after recheckInterval. |
| CEL compile error (syntax) | `status.ready = false`, reason: "CEL compile error: ...". Fail-closed. Requeue. |
| CEL evaluation error (unknown attribute) | `status.ready = false`, reason: "CEL evaluation error: ...". Fail-closed. Requeue. |
| CEL returns non-boolean | `status.ready = false`, reason: "Expression did not return boolean". Fail-closed. |
| MetricCheck query fails (Phase 2) | `metrics.*` set to nil in context. If the expression references `metrics.*`, CEL error triggers fail-closed. If the expression doesn't reference metrics, evaluation proceeds normally. |
| Context build timeout | `status.ready = false`, reason: "Context build timeout". Requeue. |

All errors are fail-closed. A PolicyGate that cannot be evaluated blocks the promotion.

## Unit Tests

1. Weekend gate: evaluate `!schedule.isWeekend` with mocked time (weekday and weekend).
2. Soak time gate: evaluate `bundle.upstreamSoakMinutes >= 30` with 15m and 45m values.
3. Author gate: evaluate `bundle.provenance.author != "dependabot[bot]"` with bot and human authors.
4. Composite gate: evaluate `!schedule.isWeekend && bundle.upstreamSoakMinutes >= 30`.
5. CEL syntax error: verify fail-closed with reason message.
6. Unknown attribute: verify fail-closed (e.g., referencing `metrics.successRate` in Phase 1).
7. Non-boolean expression: verify fail-closed (e.g., `bundle.version`).
8. Bundle not found: verify fail-closed.
9. Template ignored: verify reconciler skips PolicyGates without `kardinal.io/bundle` label.
10. Requeue after recheckInterval: verify `ctrl.Result.RequeueAfter` matches the gate's `recheckInterval`.
11. Config Bundle: verify `bundle.type == "config"` is available in context.
