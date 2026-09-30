# Rollback

In kardinal-promoter, rollback is not a special operation. It is a forward promotion of a previous Bundle version through the same pipeline, same PolicyGate evaluation, and same PR flow.

## How Rollback Works

1. `kardinal rollback <pipeline> --env <environment>` picks the target: the most recent Bundle, other than the one deployed in that environment now, that was Verified there and deploys different artifacts. A Bundle that an earlier rollback in that environment rolled back from is skipped, so after `v2` was rolled back to `v1`, the next rollback does not return to `v2`. The UI Rollback button, `onHealthFailure: rollback` and RollbackPolicy use the same selection.
2. It creates a new Bundle that copies the target's `spec.images` and `spec.configRef`, with `spec.provenance.rollbackOf` set to the target, `spec.intent.targetEnvironment` set to the environment, the label `kardinal.io/rollback: "true"` and the annotation `kardinal.io/rollback-from: <bundle deployed now>`.
3. This Bundle runs through the normal promotion flow: Graph generation, PolicyGate evaluation, Git write, PR creation (for pr-review environments), health verification. Like any Bundle with `intent.targetEnvironment`, it is promoted through every environment upstream of the target first (see [Multi-Environment Rollback](#multi-environment-rollback)).
4. The PR is labeled with `kardinal/rollback` instead of `kardinal/promotion` for visibility.

If there is nothing safe to roll back to (no earlier Verified Bundle, or only ones with the same artifacts as the failing Bundle), the command fails and creates nothing. The failing image is never promoted again.

`onHealthFailure: rollback` and RollbackPolicy never roll back a Bundle that is itself a rollback. If a rollback fails its health check, the step stops at `AbortedByAlarm` for a human, instead of starting another rollback.

There is no separate rollback subsystem. The same code path handles promotions and rollbacks.

## CLI

### Roll back to the previous version

```bash
kardinal rollback my-app --env prod
```

Output:
```
Rolling back my-app in prod from my-app-v1-29-0 to my-app-v1-28-0 (ghcr.io/myorg/my-app:v1.28.0)
Bundle my-app-rollback-x7k2p created (rollbackOf=my-app-v1-28-0)
Track with: kardinal explain my-app --env prod
```

### Roll back to a specific version

```bash
kardinal rollback my-app --env prod --to my-app-v1-27-0
```

The `--to` flag names the Bundle to roll back to. It must exist in the Bundle history (within `historyLimit`), belong to the pipeline, carry images or a config ref, differ from what is deployed now, and have been Verified in the environment; otherwise the command fails and creates nothing. `--to` can name a Bundle that an earlier rollback rolled back from.

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

The `historyLimit` field on the Pipeline (default: 50) determines how many Bundles are retained. `kardinal rollback --to <bundle>` can target any Bundle within the history. Bundles beyond the limit are garbage-collected, but the Git PRs remain as the permanent audit trail.

## Multi-Environment Rollback

When a PromotionStep fails, the Graph stops the environments downstream of it. A rollback targets one environment (`spec.intent.targetEnvironment`), but its Graph keeps every environment upstream of the target, the same as any Bundle with a target environment. So the rollback writes the old artifacts to each upstream environment first, opens PRs there for pr-review environments, and waits on their PolicyGates, soak and health checks, before it reaches the target. Environments that are not upstream of the target are not touched.

For example, in a pipeline `dev -> staging -> [prod-us, prod-eu]`, if `prod-us` fails:
- The rollback promotes the old version to `dev`, then `staging`, then `prod-us`. `dev` and `staging` run the old version afterwards too.
- `prod-eu` is not upstream of `prod-us`. It is not rolled back.
- If both prod environments fail, each gets its own rollback Bundle, and each of them goes through `dev` and `staging`.

## Comparison with Other Tools

| Tool | Rollback mechanism |
|---|---|
| kardinal-promoter | Forward promotion of prior Bundle through same pipeline, same gates, same PR flow |
| Kargo | Re-promote prior Freight to the Stage (AnalysisTemplate verification) |
| GitOps Promoter | Manual: create a revert PR |
| Argo Rollouts | Automatic in-cluster rollback on AnalysisRun failure (no cross-env awareness) |

---

## Pause and Resume

During an incident, you may want to stop promotions without rolling back.

```bash
# Pause: no new promotion step starts; in-flight steps hold at the next safe point
kardinal pause my-app

# Resume: held steps continue where they stopped
kardinal resume my-app
```

`kardinal pause` (and the UI Pause button) sets `spec.paused: true` on the Pipeline, and the controller keeps a freeze PolicyGate named `freeze-<pipeline>` in the Pipeline's namespace while it is set. While the Pipeline is paused:
- No PromotionStep leaves `Pending`, so no new environment starts promoting.
- A step in `Promoting` (clone, update manifests, commit, open PR) holds before its next git step. Its status message says the pipeline is paused.
- A step in `WaitingForMerge` or `HealthChecking` finishes. Stopping it would leave a merged change unverified. Open PRs stay open; merging one during a pause still deploys it.
- New Bundles are still accepted, but their steps wait in `Pending`.

After resume, held steps continue from where they stopped. No re-trigger is required.

While the Pipeline is paused it has a `Paused` condition. `True` (reason `FreezeGateActive`) means the freeze gate holds new promotions. Do not name your own PolicyGate `freeze-<pipeline>`: kardinal does not treat a gate it did not create (no `kardinal.io/freeze=true` label and not owned by the Pipeline) as a pause, and does not delete it. While such a gate exists, `kardinal pause` fails with an error naming it, and the condition is `False` with reason `FreezeGateNameConflict`, so the pipeline keeps running. Rename or delete that gate and the pause takes effect.
