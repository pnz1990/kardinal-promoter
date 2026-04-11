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

### Health timeout

If a PromotionStep does not reach `Verified` within the configured `health.timeout` (default: 10m), the step is marked as `Failed`. The controller creates a rollback Bundle for the affected environment.

### Delegation failure

If a delegated rollout (Argo Rollouts or Flagger) reports a `Degraded` or `Failed` status, the PromotionStep is marked as `Failed`. The controller creates a rollback Bundle.

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

For environments where rollback should bypass time-based gates, the platform team can create a SkipPermission PolicyGate that permits rollback Bundles:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: allow-rollback-on-weekends
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/type: skip-permission
    kardinal.io/applies-to: prod
spec:
  expression: "bundle.labels.rollback == true"
  message: "Rollback bundles are permitted on weekends"
```

The rollback CLI sets `bundle.labels.rollback: "true"` on the generated Bundle.

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
