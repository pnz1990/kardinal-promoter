# Rollback

In kardinal-promoter, rollback is not a special operation. It is a forward promotion of a previous Bundle version through the same pipeline, same PolicyGate evaluation, and same PR flow.

## How Rollback Works

1. `kardinal rollback <pipeline> --env <environment>` identifies the previous verified Bundle for that environment.
2. The controller creates a new Bundle whose `spec.artifacts` point to the previous version, with `spec.intent.target` set to the specified environment.
3. This Bundle runs through the normal promotion flow: Graph generation, PolicyGate evaluation, Git write, PR creation (for pr-review environments), health verification.
4. The PR is labeled with `kardinal/rollback` instead of `kardinal/promotion` for visibility.

There is no separate rollback subsystem. The same code path handles promotions and rollbacks.

## CLI

### Roll back to the previous version

```bash
kardinal rollback my-app --env prod
```

Output:
```
Rolling back my-app in prod: v1.29.0 to v1.28.0
  Previous verified Bundle: v1.28.0
  PR #145 opened: https://github.com/myorg/gitops-repo/pull/145
  Merge PR #145 to complete rollback (gate: pr-review)
```

### Roll back to a specific version

```bash
kardinal rollback my-app --env prod --to v1.27.0
```

The `--to` flag specifies which version to roll back to. The version must exist in the Bundle history (within `historyLimit`).

### Emergency rollback

```bash
kardinal rollback my-app --env prod --emergency
```

This adds the `kardinal/emergency` label to the PR, signaling to reviewers that this rollback requires priority review.

For environments where emergency rollbacks should not require PR review, configure `rollbackAutoMerge: true` on the environment. (This is currently a proposed feature. In Phase 1, all rollback PRs follow the same approval mode as forward promotions.)

## Automatic Rollback

kardinal-promoter triggers automatic rollback in two scenarios:

### Consecutive health-check failures (configurable threshold)

The most common automatic rollback scenario. Configure per environment:

```yaml
spec:
  environments:
    - name: prod
      autoRollback:
        failureThreshold: 3   # default: 3 consecutive failures
```

**How it works:**

1. The `PromotionStep` reconciler tracks `status.consecutiveHealthFailures` during `HealthChecking`.
2. On each failed health check the counter increments. On success it resets to 0.
3. When `consecutiveHealthFailures >= failureThreshold`, the controller automatically creates a rollback Bundle with:
   - `spec.provenance.rollbackOf: <original bundle name>`
   - Label `kardinal.io/rollback: "true"`
4. The rollback Bundle runs through the same promotion flow (same gates, same PR flow).
5. **Idempotent**: if a rollback Bundle already exists for the original Bundle, no duplicate is created.

Omit `spec.environments[].autoRollback` to disable automatic rollback for an environment.

### What counts as a failed health check

Each health check has one of four results (see [Timings and failures](health-adapters.md#timings-and-failures)):

| Result | Examples | Counts in `consecutiveHealthFailures` | Applies `onHealthFailure` |
|---|---|---|---|
| Healthy | The promoted revision runs and is available | No (resets it to 0) | No |
| Waiting | A rollout in progress, including new pods that are not available yet; Argo CD or Flux not synced to the promoted commit yet; Argo Rollouts `Progressing` or `Paused`; Flagger `Progressing` | No | No |
| Unhealthy | A Deployment whose rollout finished but whose pods became unavailable; Argo CD `Degraded`; Flux `Ready=False`; Argo Rollouts `Degraded`; target not found | Yes, once per check | No |
| Failed | Deployment `ProgressDeadlineExceeded`; Flagger canary `Failed` | Yes | Yes, at once |

`onHealthFailure` sets what happens to the PromotionStep: `none` (default) marks it `Failed`, `abort` marks it `AbortedByAlarm` (a human must intervene), and `rollback` creates a rollback Bundle (`spec.provenance.rollbackOf`, label `kardinal.io/rollback: "true"`) and moves the step to `RollingBack`. A rollback Bundle is not rolled back again: when its own health check fails with `rollback` set, the step is `AbortedByAlarm` instead, so rollbacks do not chain. With `bake.policy: fail-on-alarm`, an unhealthy check during the bake window also applies `onHealthFailure`.

### Health timeout

If a PromotionStep has no Healthy check within `health.timeout` (default: 10m) of entering `HealthChecking`, the timeout is a health failure: it increments `status.consecutiveHealthFailures` and applies `onHealthFailure`, as a Failed result does. The step message is `health alarm via <adapter> (onHealthFailure=<action>): health check timeout after <timeout>; last result: <last check>`. The timeout does not apply once a `bake` window has started.

A new image whose pods crash-loop is the common case. Kubernetes reports such a Deployment as still rolling out (`Progressing=True`, reason `ReplicaSetUpdated`) until its `progressDeadlineSeconds` (default: 600s) passes, so every check before that is Waiting. The step then fails at `health.timeout`, or earlier when `ProgressDeadlineExceeded` is reported first. To fail sooner, set the Deployment's `progressDeadlineSeconds` below `health.timeout`.

### Delegation failure

A Flagger canary `Failed` is a Failed result: `onHealthFailure` applies at once. An Argo Rollouts `Degraded` phase is Unhealthy: each check counts, and the step fails through the health timeout, or sooner under `bake.policy: fail-on-alarm`.

In all cases, the Graph stops all downstream nodes automatically (Graph does not advance past a Failed node).

## What Happens in Git

Rollback is a forward promotion. The controller writes the previous version's image tag to the environment's manifests, commits, and pushes (or opens a PR). The Git history shows:

```
commit abc123  [kardinal] Promote my-app to prod: v1.28.0 to v1.29.0
commit def456  [kardinal] Rollback my-app in prod: v1.29.0 to v1.28.0
```

The rollback commit is a new commit, not a `git revert`. The history is always append-only.

## Rollback and PolicyGates

Rollback Bundles go through the same PolicyGate evaluation as forward promotions. If the `no-weekend-deploys` gate is active and it is a weekend, the rollback PR will also be blocked.

Rollback Bundles have no gate exemption. A skip-permission gate does not help: it only lets a Bundle skip an environment through `intent.skipEnvironments`, and a rollback does not skip the environment it rolls back.

To let a rollback through a blocking gate, use a break-glass override. It is time-limited and recorded in the audit trail:

```bash
kardinal override my-app --stage prod --gate no-weekend-deploys \
  --reason "Rollback of v1.29.0 — incident #4521" --expires-in 1h
```

See [Emergency Overrides](policy-gates.md#emergency-overrides-k-09).

## Rollback History

The `kardinal history` command shows both promotions and rollbacks:

```bash
kardinal history my-app
```

```
BUNDLE    ACTION     ENV     PR     APPROVER   DURATION   TIMESTAMP
v1.29.0   promote    prod    #144   alice      15m        2026-04-09 10:20
v1.28.0   rollback   prod    #145   bob        5m         2026-04-09 11:00
v1.28.0   promote    prod    #138   alice      12m        2026-04-07 14:00
```

## How Far Back Can You Roll Back

The `historyLimit` field on the Pipeline (default: 20) determines how many Bundles are retained. `kardinal rollback --to <version>` can target any version within the history. Bundles beyond the limit are garbage-collected, but the Git PRs remain as the permanent audit trail.

## Multi-Environment Rollback

When a PromotionStep fails in a downstream environment, Graph stops all downstream nodes. The controller opens rollback PRs only for environments that actually received the failed Bundle. Environments that were not yet promoted are unaffected.

For example, in a pipeline `dev -> staging -> [prod-us, prod-eu]`, if `prod-us` fails:
- `prod-eu` may still be promoting or may have already succeeded. It is not rolled back.
- Only `prod-us` gets a rollback PR.
- If both prod environments fail, both get rollback PRs.

## Comparison with Other Tools

| Tool | Rollback mechanism |
|---|---|
| kardinal-promoter | Forward promotion of prior Bundle through same pipeline, same gates, same PR flow |
| Kargo | Re-promote prior Freight to the Stage (AnalysisTemplate verification) |
| GitOps Promoter | Manual: create a revert PR |
| Argo Rollouts | Automatic in-cluster rollback on AnalysisRun failure (no cross-env awareness) |

---

## Pause and Resume

During an incident, you may want to stop all in-flight promotions without rolling back.

```bash
# Pause: hold all promotions at their current state
kardinal pause my-app

# Resume: allow promotions to continue
kardinal resume my-app
```

When a Pipeline is paused (`spec.paused: true`):
- PromotionSteps already in progress are held at their current state (Promoting, WaitingForMerge, etc.)
- Open PRs remain open — no new commits are pushed
- No new Bundles will advance from Available to Promoting
- All states are preserved in etcd — resume picks up exactly where pause left

After resume, promotions continue automatically from where they paused. No re-trigger is required.
