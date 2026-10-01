# Rollback

In kardinal-promoter, rollback is not a special operation. It is a forward promotion of a previous Bundle version through the same pipeline, same PolicyGate evaluation, and same PR flow.

## How Rollback Works

1. `kardinal rollback <pipeline> --env <environment>` picks the target: the most recent Bundle, other than the one deployed in that environment now, that was Verified there and deploys different artifacts. A Bundle that an earlier rollback in that environment rolled back from is skipped, so after `v2` was rolled back to `v1`, the next rollback does not return to `v2`. The UI Rollback button, `onHealthFailure: rollback` and RollbackPolicy use the same selection.
2. It creates a new Bundle that copies the target's `spec.images` and `spec.configRef`, with `spec.provenance.rollbackOf` set to the target, `spec.intent.targetEnvironment` set to the environment, the label `kardinal.io/rollback: "true"` and the annotations `kardinal.io/rollback-from: <bundle deployed now>` and `kardinal.io/requested-by: <who ran the rollback>`. The rest of `spec.provenance` is the target's, so its author is the author of the restored build, not the person who rolled back. It also puts back what the deployed Bundle changed that the target does not name (see [Images the target does not name](#images-the-target-does-not-name)).
3. This Bundle runs through the normal promotion flow: Graph generation, PolicyGate evaluation, Git write, PR creation (for pr-review environments), health verification. Like any Bundle with `intent.targetEnvironment`, it is promoted through every environment upstream of the target first (see [Multi-Environment Rollback](#multi-environment-rollback)).
4. The PR title is `[kardinal] Rollback <environment> to <rollback bundle> (restores <target>)`, where `<target>` is the version the rollback deploys: the image tag (for example `(restores 1.28.0)`), `<image>:<tag>` for each of several images, or `config <commit>` for a config Bundle. It is the target Bundle's name when the Bundle has no images or config commit. The PR body says which Bundle and version it replaces, which it restores, and who rolled back (see [PR Evidence](pr-evidence.md)). The PR is labeled with `kardinal/rollback` in addition to `kardinal` and `kardinal/promotion`, so filter on `kardinal/rollback` to find rollback PRs. Bitbucket Cloud has no PR labels: there, find rollback PRs by their `[kardinal] Rollback` title (see [SCM providers](scm-providers.md)).

If there is nothing safe to roll back to (no earlier Verified Bundle, or only ones with the same artifacts as the failing Bundle), the command fails and creates nothing. The failing image is never promoted again.

### Images the target does not name

A Bundle does not have to name every image of the application. If the deployed Bundle `v2` names only `b:2`, and the target `v1` names only `a:1`, copying `v1` would leave `b:2`, the failing version, in place. So for each image repository the deployed Bundle names and the target does not, the rollback Bundle also carries the version from the newest Bundle that was Verified in the environment, other than the deployed one and the ones an earlier rollback rolled back from.

For example, with `v0 = {a:0, b:0}`, `v1 = {a:1}` and `v2 = {b:2}` all promoted to prod, rolling back `v2` gives `{a:1, b:0}`.

If no such Bundle names the image, the rollback is refused, and the error names the image. `onHealthFailure: rollback` then stops the step at `AbortedByAlarm` for a human. Roll back with `--to` a Bundle that names the image, or promote a fixed version.

The same rule applies to config commits. A config Bundle deploys its config commit, and a `mixed` Bundle deploys its config commit and then its images (see [config-only promotions](design/09-config-only-promotions.md)). When the deployed Bundle is a config or mixed Bundle, the rollback carries the target's config commit or, if the target has none, the newest earlier Verified one, from a config or mixed Bundle. So rolling back a mixed Bundle restores both its images and its config commit.

The rollback Bundle has the target's type and deploys what that type deploys: images for an image Bundle, the config commit for a config Bundle, both for a mixed Bundle. Without `--to`, only a Bundle of the deployed Bundle's type is a target. A `--to` target that cannot deploy what the deployed Bundle changed is refused, and the error names what it cannot restore:

| Deployed | `--to` an image Bundle | `--to` a config Bundle | `--to` a mixed Bundle |
|---|---|---|---|
| image | restores the images | refused | restores the images, and deploys the target's config commit |
| config | refused | restores the config commit | restores the config commit, and deploys the target's images |
| mixed | restores the images; refused if the mixed Bundle changed the config commit (its commit is not the newest earlier Verified one) | restores the config commit; refused if the mixed Bundle changed an image (one of its images is not the newest earlier Verified version) | restores both |

A target that, with the added images or config commit, deploys nothing the deployed Bundle did not is skipped, and `--to` such a target fails.

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

The `--to` flag names the Bundle to roll back to. It must exist in the Bundle history (within `historyLimit`), belong to the pipeline, carry images or a config ref, differ from what is deployed now, and have been Verified in the environment; otherwise the command fails and creates nothing. `--to` can name a Bundle that an earlier rollback rolled back from. Images the deployed Bundle changed and the `--to` Bundle does not name are filled in the same way as without `--to`.

`--emergency` is deprecated and has no effect. It never bypassed a gate. It prints a warning, the rollback runs as without it, and the flag will be removed in the next minor release. To let a rollback through a blocking gate, use [`kardinal override`](#rollback-and-policygates).

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
| `rollback` | A rollback Bundle is created with the artifacts of the Bundle verified before the failing one in this environment (the selection in [How Rollback Works](#how-rollback-works)), and the step moves to `RollingBack`. With nothing safe to roll back to, including an image of the failing Bundle that no earlier Bundle names, the step is `AbortedByAlarm` and nothing is created |

**When it applies.** The controller applies `onHealthFailure` when:

- the PromotionStep has no Healthy check within `health.timeout` (default: 10m) of entering `HealthChecking`, or of a bake window stopping;
- the health adapter reports a terminal result (Deployment `ProgressDeadlineExceeded`, Flagger canary `Failed`);
- a health check is Unhealthy during a bake window with `bake.policy: fail-on-alarm`.

A rollback Bundle (label `kardinal.io/rollback: "true"` or `spec.provenance.rollbackOf` set) is not rolled back again: when its own health check fails with `rollback` set, the step is `AbortedByAlarm` instead, so rollbacks do not chain.

### What counts as a failed health check

Each health check has one of four results (see [Timings and failures](health-adapters.md#timings-and-failures)):

| Result | Examples | Counts in `consecutiveHealthFailures` | Applies `onHealthFailure` |
|---|---|---|---|
| Healthy | The promoted revision runs and is available | No (resets it to 0) | No |
| Waiting | A rollout in progress, including new pods that are not available yet; Argo CD or Flux not synced to the promoted commit yet; a Rollout or Canary target not on the Bundle images yet, so a `Healthy`, `Degraded`, `Succeeded` or `Failed` phase left by the previous release; Argo Rollouts `Progressing` or `Paused`; Flagger `Progressing` | No | No |
| Unhealthy | A Deployment whose rollout finished but whose pods became unavailable; Argo CD `Degraded`; Flux `Ready=False`; Argo Rollouts `Degraded` on the promoted revision; target not found | Yes, once per check | Only during a bake window with `fail-on-alarm` |
| Failed | Deployment `ProgressDeadlineExceeded`; Flagger canary `Failed` on the promoted revision | Yes | Yes, at once |

### Health timeout

If a PromotionStep has no Healthy check within `health.timeout` (default: 10m) of entering `HealthChecking`, the timeout is a health failure: it increments `status.consecutiveHealthFailures` and applies `onHealthFailure`, as a Failed result does. The step message is `health alarm via <adapter> (onHealthFailure=<action>): health check timeout after <timeout>; last result: <last check>`. The timeout does not apply while a `bake` window runs. When the window stops (an Unhealthy check, or a Waiting one), the timeout starts again from that moment: a release that does not become healthy again fails at the timeout under either `bake.policy`.

A new image whose pods crash-loop is the common case. Kubernetes reports such a Deployment as still rolling out (`Progressing=True`, reason `ReplicaSetUpdated`) until its `progressDeadlineSeconds` (default: 600s) passes, so every check before that is Waiting. The step then fails at `health.timeout`, or earlier when `ProgressDeadlineExceeded` is reported first. To fail sooner, set the Deployment's `progressDeadlineSeconds` below `health.timeout`.

### Delegation failure

A Flagger canary `Failed` on the promoted revision is a Failed result: `onHealthFailure` applies at once. An Argo Rollouts `Degraded` phase on the promoted revision is Unhealthy: each check counts, and `onHealthFailure` applies at the health timeout, or sooner under `bake.policy: fail-on-alarm`. A phase the previous release left (Flagger keeps it until its next analysis tick notices the change) is Waiting: it neither fails nor verifies the Bundle. That includes a `Failed` of the previous release that lands after the health check started but before the GitOps tool applied the Bundle: a Flagger `Failed` fails the step only when Flagger set it after kardinal first saw the target on the Bundle images (`status.targetUpdatedAt`). If kardinal does not see the target updated before Flagger fails the Bundle (the controller was down meanwhile), the step fails at `health.timeout` instead of at once. See [argoRollouts](health-adapters.md#adapter-argorollouts) and [flagger](health-adapters.md#adapter-flagger).

In all cases, the Graph stops all downstream nodes automatically (Graph does not advance past a Failed node). Use `kardinal rollback` to roll back manually.

### `autoRollback` is not implemented

`spec.environments[].autoRollback.failureThreshold` (roll back after N consecutive failed health checks) is reserved. The API server rejects a Pipeline that sets it, with the message `environments[].autoRollback is not implemented`. Remove the field and use `onHealthFailure`.

The `RollbackPolicy` CRD is the building block for that feature. The controller reconciles RollbackPolicy objects that you create yourself, but nothing creates them automatically. A RollbackPolicy reads only the PromotionSteps of its `spec.bundleRef` in `spec.environment`. When the highest `status.consecutiveHealthFailures` among them reaches `spec.failureThreshold` (default: 3), it creates one rollback Bundle.

When the threshold is reached but there is nothing safe to roll back to (see [How Rollback Works](#how-rollback-works)), the RollbackPolicy creates nothing. It sets the `RollbackRefused` condition to `True`, and the message gives the reason. It also emits one `Warning` Event with reason `RollbackRefused`. The condition shows in the `REFUSED` column:

```bash
kubectl get rollbackpolicy
# NAME   SHOULDROLLBACK   FAILURES   THRESHOLD   REFUSED   AGE
# rp-1   true             3          3           True      2m
kubectl get rollbackpolicy rp-1 -o jsonpath='{.status.conditions[?(@.type=="RollbackRefused")].message}'
```

Roll back by hand (see [CLI](#cli)) or fix the policy. The RollbackPolicy is evaluated again when its PromotionSteps or its own spec change; when that evaluation creates the rollback Bundle, the condition becomes `False` with reason `RollbackCreated`.

`SHOULDROLLBACK` follows the failures until a rollback Bundle exists: when they drop below the threshold first (the health check passed again after a refusal), it is `false` again and nothing is created. Once a rollback Bundle exists it stays `true`, and the policy does nothing more. `THRESHOLD` shows `3` when `spec.failureThreshold` is not set.

## What Happens in Git

Rollback is a forward promotion. The controller writes the previous version's image tag to the environment's manifests, commits, and pushes (or opens a PR). The Git history shows:

```
commit abc123  [kardinal] Promote my-app-9tptr to prod
commit def456  [kardinal] Promote my-app-rollback-bkgwk to prod
```

Every commit, a rollback's included, is titled `[kardinal] Promote <bundle> to <environment>`, with
the Bundle and Pipeline names in the body. A rollback Bundle is usually named `<pipeline>-rollback-<suffix>`,
and its PR is labelled `kardinal/rollback` (on Bitbucket Cloud, which has no PR labels, it is titled `[kardinal] Rollback ...`).

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

Upgrading: before this release the UI Pause button, and editing `spec.paused` by hand, set the field without creating the freeze gate, so the Pipeline kept promoting. After upgrading, the controller creates the gate for every Pipeline with `spec.paused: true`, and those Pipelines stop. Resume any that should keep running.
