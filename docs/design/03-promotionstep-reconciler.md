# 03: PromotionStep Reconciler

> Status: Historical (checked against the code on 2026-09-29). Several parts differ from the
> code; `api/v1alpha1/promotionstep_types.go` and `pkg/reconciler/promotionstep/` are the
> reference. Known differences:
> - These status fields do not exist: `prMerged`, `prClosed`, `stepOutputs`, `failedAt`,
>   `verifiedAt`, `evidence`, `delegatedTo`.
> - Merge detection goes through the PRStatus CRD and its reconciler, not a webhook that sets
>   `prMerged`.
> - Evidence reaches the Bundle through the Bundle reconciler (`handleSyncEvidence`), not
>   `copyEvidenceToBundle`.
> - `propagateWhen` does not exist in kro (ledger G1).
> - The Pipeline reconciler (`pkg/reconciler/pipeline`) does not create rollbacks. Rollback
>   Bundles are built by `pkg/lifecycle` and created by `kardinal rollback` or the UI, by the
>   RollbackPolicy reconciler and, for `onHealthFailure: rollback`, by the PromotionStep
>   reconciler.
> - Health checks follow `onHealthFailure` (`none` → Failed, `abort` → AbortedByAlarm,
>   `rollback` → RollingBack) on timeout or a terminal result; see the Health Check section.
> - Distributed mode (the kardinal-agent and shard filtering) was removed (#1321; see
>   [07](07-distributed-architecture.md)). The shard filtering below is history.
> Depends on: 01-graph-integration, 02-pipeline-to-graph-translator, 08-promotion-steps-engine
> Blocks: nothing (leaf node, but the workhorse)

## Purpose

The PromotionStep reconciler watches PromotionStep CRDs created by the Graph controller and executes the promotion logic: running the step sequence (built-in or custom), managing the state machine, and writing evidence back to the Bundle.

This is the code that runs in both the standalone kardinal-controller and the distributed kardinal-agent. The logic is identical in both modes. In distributed mode, the reconciler only processes PromotionSteps whose `kardinal.io/shard` label matches the agent's `--shard` flag.

## Go Package Structure

```
pkg/
  reconciler/
    promotionstep/
      reconciler.go       # Main reconciliation loop
      state_machine.go    # State transition logic
      evidence.go         # Evidence collection + copy to Bundle status
      reconciler_test.go  # Unit tests
```

The reconciler imports:
- `pkg/steps/` for the step execution engine (see 08-promotion-steps-engine)
- `pkg/scm/` for Git and SCM operations
- `pkg/health/` for health verification adapters (see 05-health-adapters)

## State Machine

A PromotionStep transitions through these states:

```
Pending -> StepExecution -> WaitingForMerge -> HealthChecking -> Verified
                |                                     |
                v                                     v
              Failed                                Failed
```

**User-visible states** (in `status.state`): `Pending`, `Promoting`, `WaitingForMerge`, `HealthChecking`, `Verified`, `Failed`, `AbortedByAlarm`, `RollingBack`.

**Internal states** (not exposed): `StepExecution` maps to `Promoting` in the external state. The reconciler tracks the current step index internally in `status.currentStepIndex`.

### State: Pending

The PromotionStep has been created by the Graph controller but the reconciler has not yet started processing it.

Transition to StepExecution: immediately on first reconcile.

### State: StepExecution (external: Promoting)

The reconciler executes the step sequence. Each step is called in order via the Step interface (see 08-promotion-steps-engine).

The current step index is stored in `status.currentStepIndex`. On crash and restart, the reconciler resumes from this index. Each step checks if its work is already done (idempotency) before executing.

Steps that are long-running (e.g., `wait-for-merge`) return a "pending" result. The reconciler sets `status.state = "WaitingForMerge"` and requeues.

Transition to WaitingForMerge: when the `open-pr` step succeeds and the next step is `wait-for-merge`.
Transition to Failed: when any step returns an error or `success: false`.

### State: WaitingForMerge (only for pr-review environments)

The PR has been opened. The reconciler waits for the PR to be merged.

Detection: the webhook handler receives a `pull_request.closed` event with `merged: true` for a PR matching the `kardinal` label and the PromotionStep's PR URL. The handler updates the PromotionStep's `status.prMerged = true`.

On controller restart: the reconciler lists all open PRs with the `kardinal` label and reconciles any that were merged during downtime. This is a one-time catch-up, not polling.

Transition to HealthChecking: when `status.prMerged = true`.
Transition to Failed: when the PR is closed without merge (`status.prClosed = true, status.prMerged = false`). The promotion is cancelled.

> **Architecture note — why the PRStatus Graph node has no `readyWhen` or `propagateWhen`:**
>
> The PRStatus Watch node in the Graph has no `readyWhen` (B69): a `status.merged == true`
> readyWhen kept the Graph from being Ready when the step opened no PR, and the
> PromotionStep node is Verified only after the merge anyway. Using `propagateWhen`
> would create a circular dependency: the PromotionStep node includes
> `${prstatus.metadata.name}` in its template, creating a data-flow edge from
> PRStatus to PromotionStep. If PRStatus had `propagateWhen: false`, the
> PromotionStep could never start (it depends on PRStatus propagating).
>
> The `WaitingForMerge → HealthChecking` transition is enforced by the PromotionStep
> reconciler state machine, not by Graph `propagateWhen`. The PRStatus node drives the
> `prStatusRef` field in the PromotionStep template so the reconciler can find the
> PRStatus CRD by name without polling GitHub. This is not a design flaw — it is an
> intentional consequence of the flat DAG topology (see docs/design/11-graph-purity-tech-debt.md §Flat DAG
> Compilation — Why It Does Not Work).
>
> A future architecture change that splits HealthChecking into a separate CRD/reconciler
> would allow using `propagateWhen` on PRStatus to gate health checks at the Graph level.
> Until then, the state machine enforces sequencing correctly; the Graph provides
> observability.

### State: HealthChecking

The `health-check` step is executing. The health adapter (see 05-health-adapters) verifies the target environment is healthy.

The adapter is called repeatedly (every 10 seconds) until it returns Healthy, the timeout expires, or a delegate reports failure.

When delegation is configured (`delivery.delegate: argoRollouts`), the health adapter watches the Rollout or Canary status instead of the Deployment. The adapter returns Healthy only when the progressive delivery rollout completes successfully.

Transition to Verified: when the health adapter returns Healthy.
Transition by `onHealthFailure` (`none` → Failed, `abort` → AbortedByAlarm, `rollback` → RollingBack): when `health.timeout` expires without a Healthy result, or the adapter reports a terminal result (Deployment `ProgressDeadlineExceeded`, Flagger `Failed`). An Argo Rollouts `Degraded` phase is unhealthy, not terminal: it is counted and reaches `onHealthFailure` through the timeout.

### State: Verified

Terminal success state. The PromotionStep is complete.

On reaching Verified:
1. Record `status.verifiedAt` timestamp.
2. Collect evidence (see Evidence Collection below).
3. Copy evidence to Bundle `status.environments[<env>]` for durable storage.
4. Graph sees `readyWhen` satisfied and advances to dependent nodes.

### State: Failed

Terminal failure state.

On reaching Failed:
1. Record `status.failedAt` timestamp and `status.reason`.
2. Copy failure information to Bundle `status.environments[<env>]`.
3. Graph stops all downstream nodes.
4. The Pipeline reconciler detects the failure and opens rollback PRs for environments that received the failed Bundle.

## Reconciliation Loop

```go
func (r *PromotionStepReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    step := &v1alpha1.PromotionStep{}
    if err := r.Get(ctx, req.NamespacedName, step); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Shard filtering (distributed mode)
    if r.shard != "" && step.Labels["kardinal.io/shard"] != r.shard {
        return ctrl.Result{}, nil // not our shard, skip
    }

    switch step.Status.State {
    case "", "Pending":
        return r.handlePending(ctx, step)
    case "Promoting":
        return r.handleStepExecution(ctx, step)
    case "WaitingForMerge":
        return r.handleWaitingForMerge(ctx, step)
    case "HealthChecking":
        return r.handleHealthChecking(ctx, step)
    case "Verified", "Failed":
        return ctrl.Result{}, nil // terminal, nothing to do
    case "AbortedByAlarm":
        return ctrl.Result{}, nil // terminal human-intervention state — operator must resume or rollback
    case "RollingBack":
        return ctrl.Result{}, nil // managed by rollback Bundle — this reconciler takes no action
    }
    return ctrl.Result{}, nil
}
```

### handlePending

1. Load the Bundle CRD (from `spec.bundleRef`).
2. Resolve the step sequence:
   - If `spec.steps` is set, use the explicit step list.
   - If not, infer the default step sequence from `spec.update.strategy`, `spec.approval`, and the Bundle type (`image` or `config`).
3. Initialize `status.currentStepIndex = 0`.
4. Set `status.state = "Promoting"`.
5. Requeue immediately.

### handleStepExecution

1. Load the step at `status.currentStepIndex`.
2. Build the `StepState` (Pipeline spec, Environment spec, Bundle spec, work dir, outputs from previous steps stored in `status.stepOutputs`).
3. Execute the step via `step.Execute(ctx, state)`.
4. If result is success:
   - Merge result.Outputs into `status.stepOutputs`.
   - Increment `status.currentStepIndex`.
   - If more steps remain, requeue immediately.
   - If the next step is `wait-for-merge`, set `status.state = "WaitingForMerge"`.
   - If all steps are done, set `status.state = "HealthChecking"` (or Verified if no health check step).
5. If result is pending (long-running step):
   - Requeue after 30 seconds.
6. If result is failure:
   - Set `status.state = "Failed"` with `status.reason = result.Message`.

### handleWaitingForMerge

1. Check `status.prMerged`. If true, advance to HealthChecking.
2. Check `status.prClosed`. If true (closed without merge), set Failed.
3. Otherwise, do nothing. The webhook handler will update the status.
4. On controller restart: if `status.prURL` is set and `status.prMerged` is not set, check the PR status via the SCM provider. Update accordingly.

### handleHealthChecking

1. Resolve the health adapter from the environment config (`delivery.delegate`, then `health.type`, then `resource`; no auto-detection) and the target from the `health.*` overrides (`pkg/health/options.go`).
2. Skip the check if the last one (`status.lastHealthCheckAt`) is less than 10 seconds old.
3. Call `adapter.Check(ctx, opts)` with `opts.ExpectedRevision` (the pushed or merged commit) and the Bundle images, so a healthy environment still on the previous revision is not Verified. The first check that reports the target on the Bundle images (resource, flagger) sets `status.targetUpdatedAt`, which later checks pass back so that a Deployment `ProgressDeadlineExceeded` or a Flagger `Failed` from an earlier rollout is not taken for the Bundle's.
4. If Healthy: set `status.state = "Verified"` (or start/advance the bake window), record evidence, copy to Bundle.
5. If progressing: requeue after 10 seconds. If unhealthy: increment `status.consecutiveHealthFailures` and requeue. If terminal (Deployment `ProgressDeadlineExceeded` or Flagger `Failed`, set during this promotion): apply `onHealthFailure` at once.
6. If `health.timeout` expired before the first healthy check: increment `status.consecutiveHealthFailures` and apply `onHealthFailure` with reason "health check timeout after <timeout>; last result: ...". A crash-looping image counts as progressing until the Deployment's `progressDeadlineSeconds`, so the timeout is what fails it (or rolls it back) when that deadline is longer. The timeout does not apply while the bake window runs; when the window stops (an unhealthy check, or a waiting one, which is not an alarm) `status.healthCheckExpiry` moves to now + timeout, so the next healthy check must come within the timeout.

## Evidence Collection

At each significant state transition, the reconciler records evidence in `status.evidence`:

| Evidence field | When collected | Source |
|---|---|---|
| `prURL` | After `open-pr` step | SCM provider response |
| `mergedAt` | After merge detection | Webhook event timestamp |
| `verifiedAt` | After health check passes | System clock |
| `approvedBy` | After merge detection | PR merge author from webhook event |
| `gateDuration` | At Verified | `verifiedAt - promotedAt` |
| `metrics` | At Verified (Phase 2) | MetricCheck query results |
| `policyGates` | At Verified | Read from sibling PolicyGate CRs |

### Evidence Copy to Bundle

When a PromotionStep reaches Verified or Failed, the reconciler copies evidence into the Bundle's `status.environments[<env>]` field. This ensures the audit record survives PromotionStep garbage collection (when the Graph and its children are deleted during history limit cleanup).

```go
func (r *PromotionStepReconciler) copyEvidenceToBundle(ctx context.Context, step *v1alpha1.PromotionStep) error {
    bundle := &v1alpha1.Bundle{}
    if err := r.Get(ctx, types.NamespacedName{Name: step.Spec.BundleRef, Namespace: step.Namespace}, bundle); err != nil {
        return err
    }
    if bundle.Status.Environments == nil {
        bundle.Status.Environments = make(map[string]v1alpha1.EnvironmentStatus)
    }
    bundle.Status.Environments[step.Spec.Environment] = v1alpha1.EnvironmentStatus{
        State:      step.Status.State,
        PromotedAt: step.Status.StartedAt,
        VerifiedAt: step.Status.VerifiedAt,
        PRURL:      step.Status.PRURL,
        Evidence:   step.Status.Evidence,
    }
    return r.Status().Update(ctx, bundle)
}
```

## Idempotency

Every reconcile call must be safe to repeat. Specific concerns:

- `git-clone`: checks if the work dir already exists and is up to date.
- `kustomize-set-image`: checks if the image is already set to the target version.
- `git-commit`: checks if there are uncommitted changes. If the image was already set (idempotent kustomize), there are no changes and the commit is a no-op.
- `git-push`: checks if the remote branch already has the commit.
- `open-pr`: checks if a PR already exists for the branch (via SCM provider). If yes, returns the existing PR.
- `wait-for-merge`: checks `status.prMerged`. Purely status-driven.
- `health-check`: stateless query. Always safe to repeat.

## Rollback Trigger

The reconciler itself does not open rollback PRs. When a PromotionStep reaches Failed:
1. It writes `status.state = "Failed"` with a reason.
2. The Graph controller stops all downstream nodes.
3. The Pipeline reconciler (in the control plane) watches Bundle status. When it sees a Failed environment, it creates a rollback Bundle (with the previous verified version and `intent.targetEnvironment` set to the failed environment).
4. The rollback Bundle goes through the normal translation -> Graph -> PromotionStep flow.

## PromotionStep CRD Status Subresource

```go
type PromotionStepStatus struct {
    State            string                     `json:"state"`
    Reason           string                     `json:"reason,omitempty"`
    CurrentStepIndex int                        `json:"currentStepIndex"`
    StepOutputs      map[string]json.RawMessage `json:"stepOutputs,omitempty"`
    PRURL            string                     `json:"prURL,omitempty"`
    PRMerged         bool                       `json:"prMerged,omitempty"`
    PRClosed         bool                       `json:"prClosed,omitempty"`
    StartedAt        *metav1.Time               `json:"startedAt,omitempty"`
    VerifiedAt       *metav1.Time               `json:"verifiedAt,omitempty"`
    FailedAt         *metav1.Time               `json:"failedAt,omitempty"`
    Evidence         Evidence                   `json:"evidence,omitempty"`
    DelegatedTo      string                     `json:"delegatedTo,omitempty"`
    DelegatedStatus  string                     `json:"delegatedStatus,omitempty"`
}

type Evidence struct {
    Metrics      map[string]float64 `json:"metrics,omitempty"`
    GateDuration string             `json:"gateDuration,omitempty"`
    ApprovedBy   []string           `json:"approvedBy,omitempty"`
    PolicyGates  []PolicyGateResult `json:"policyGates,omitempty"`
}

type PolicyGateResult struct {
    Name   string `json:"name"`
    Result string `json:"result"` // "pass" or "fail"
}
```

## Unit Tests

1. Pending to Promoting: verify step sequence resolution (default and custom).
2. Step execution: mock each built-in step, verify correct ordering and output passing.
3. WaitingForMerge: verify transition on prMerged = true.
4. WaitingForMerge: verify transition to Failed on prClosed = true.
5. HealthChecking: verify transition to Verified on adapter returning Healthy.
6. HealthChecking: verify transition to Failed on timeout.
7. Evidence copy: verify Bundle status is updated with evidence at Verified.
8. Evidence copy: verify Bundle status is updated with failure at Failed.
9. Idempotency: run reconcile twice at each state, verify no side effects.
10. Shard filtering: verify PromotionSteps with non-matching shard are skipped.
11. Config Bundle: verify config-merge step is used instead of kustomize-set-image.
12. Custom steps: verify custom step webhook is called with correct payload.
