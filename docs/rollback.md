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

Automatic rollback is configured per environment with `onHealthFailure`:

```yaml
spec:
  environments:
    - name: prod
      bake:
        minutes: 30
        policy: fail-on-alarm
      onHealthFailure: rollback   # rollback | abort | none (default)
```

| `onHealthFailure` | What happens to the PromotionStep |
|---|---|
| `none` (default) | `Failed`; downstream environments stop |
| `abort` | `AbortedByAlarm`; a human must intervene |
| `rollback` | A rollback Bundle is created (`spec.provenance.rollbackOf`, label `kardinal.io/rollback: "true"`) and the step moves to `RollingBack` |

**When it applies.** The controller applies `onHealthFailure` when a health check fails during a bake window with `bake.policy: fail-on-alarm`. Other failures only mark the step `Failed`; they do not create a rollback Bundle:

- **Health timeout.** If a PromotionStep does not pass its health check within `health.timeout` (default: 10m), the step is marked `Failed`.
- **Delegated rollout failure.** A `Degraded` Argo Rollouts or Flagger rollout counts as an unhealthy check, so the step is marked `Failed` at `health.timeout`, or sooner under `fail-on-alarm`.

In all cases, the Graph stops all downstream nodes automatically (Graph does not advance past a Failed node). Use `kardinal rollback` to roll back manually.

### `autoRollback` is not implemented

`spec.environments[].autoRollback.failureThreshold` (roll back after N consecutive failed health checks) is reserved. The API server rejects a Pipeline that sets it, with the message `environments[].autoRollback is not implemented`. Remove the field and use `onHealthFailure`.

The `RollbackPolicy` CRD is the building block for that feature. The controller reconciles RollbackPolicy objects that you create yourself, but nothing creates them automatically. A RollbackPolicy reads only the PromotionSteps of its `spec.bundleRef` in `spec.environment`. When the highest `status.consecutiveHealthFailures` among them reaches `spec.failureThreshold` (default: 3), it creates one rollback Bundle.

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
