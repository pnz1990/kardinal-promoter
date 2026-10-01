# Health Adapters

After a promotion is applied (manifests written to Git), kardinal-promoter verifies that the target environment runs **the promoted revision** and is healthy before marking the PromotionStep as Verified. A healthy environment that still runs the previous version is not Verified. Health verification uses pluggable adapters that check the appropriate Kubernetes resource status.

## Selecting an Adapter

```yaml
environments:
  - name: prod
    health:
      type: argocd   # one of resource | argocd | flux | argoRollouts | flagger (default: resource)
```

The adapter is chosen in this order:

1. `delivery.delegate` (`argoRollouts` or `flagger`), when set and not `none`;
2. `health.type`;
3. `resource` when neither is set. There is no probing of the cluster for installed CRDs.

An unknown `health.type` is rejected by the CRD schema when the Pipeline is applied.

## What "the promoted revision" means

The PromotionStep records the git commit its promotion delivered:

- `status.outputs.commitSHA` — the commit pushed straight to the environment branch (`approval: auto`);
- `status.outputs.mergeCommitSHA` — the merge commit of the promotion PR (`approval: pr-review`), copied from `PRStatus.status.mergeCommitSHA` when the step sees the merge, or at the next health check when the PRStatus records it later (a webhook can report the merge before the merge commit is known).

The `argocd` and `flux` adapters require that commit. On a branch shared with other environments, both also accept another commit when the Bundle images run: for `argocd` in the Application, for `flux` in the Kustomization's Deployments (see below). They compare images, not git history, so they do not check that the other commit is later than the promoted one. The `resource` adapter requires the Bundle images in the Deployment's pod template, `argoRollouts` in the Rollout's, and `flagger` in the Canary's target Deployment and, for `Succeeded`, its primary Deployment. `kubectl get promotionstep <name> -o yaml` shows the recorded commit in `status.outputs` and the last health result in `status.message`.

## Adapter: resource (default)

Checks a Kubernetes Deployment the way `kubectl rollout status` does.

```yaml
health:
  type: resource
  resource:
    kind: Deployment            # the only supported kind
    name: my-app                # default: Pipeline.metadata.name
    namespace: prod             # default: environment name
    condition: Available        # default
  timeout: 10m
```

**Healthy when**, in order:

1. every container that runs one of the Bundle's image repositories runs the Bundle's tag or digest (otherwise: waiting, "not updated yet");
2. `status.observedGeneration` is at least `metadata.generation` (otherwise: waiting);
3. the `Progressing` condition does not have reason `ProgressDeadlineExceeded` (otherwise: **failed**, see [Timings and failures](#timings-and-failures));
4. all replicas are updated, no old replicas remain, and every updated replica is available (otherwise: waiting while the rollout runs; unhealthy if replicas become unavailable after the rollout finished);
5. the configured condition (default `Available`) is `True`.

If the Deployment runs none of the Bundle's image repositories (for example, kustomize `newName` renamed the image), the image cannot be verified. The check then passes with "(image not verified)" in the message.

`resource.kind` other than `Deployment` fails the PromotionStep with `health.resource.kind "StatefulSet" is not supported: only Deployment is checked`. For other workloads, use the `argocd` or `flux` adapter.

**When to use:** Clusters without Argo CD or Flux, or as a baseline when only Kubernetes Deployments are available.

### WatchKind mode (label selector)

By default, the `resource` adapter checks a single named Deployment (`Pipeline.metadata.name`). When `health.labelSelector` is set, it checks **every** Deployment in the namespace (`resource.namespace`, default: the environment name) that matches the selector, and `resource.name` is ignored.

```yaml
health:
  type: resource
  labelSelector:
    app: my-app
    kardinal.io/pipeline: nginx-demo
  timeout: 10m
```

**When to use WatchKind mode:**
- When your Deployment name does not match the Pipeline name
- When you deploy multiple Deployments per environment and need all of them healthy before advancing
- When you want to match Deployments by label rather than by exact name

**How it works:** Each matched Deployment must pass all five checks above. The step reports the worst result: failed, then unhealthy, then waiting. No matching Deployment is unhealthy, not vacuously healthy.

> **Note:** `labelSelector` is only supported for `health.type: resource`. For `argocd`, `flux`, `argoRollouts`, and `flagger`, the adapter always watches a single named resource and `labelSelector` is ignored.

## Adapter: argocd

Watches an Argo CD Application's health, sync, operation status and synced revision.

```yaml
health:
  type: argocd
  argocd:
    name: my-app-prod           # default: <pipeline>-<environment>
    namespace: argocd           # default: "argocd"
  timeout: 15m
```

**Healthy when:** all of these are met:
- `status.health.status` = `Healthy`
- `status.sync.status` = `Synced`
- `status.operationState.phase` = `Succeeded` (or no operation recorded, or a finished operation on another commit)
- the Application synced the promoted commit: it appears in `status.sync.revision(s)`, `status.operationState.syncResult.revision(s)` or `status.history`. `Synced` alone only means the cluster matches whatever commit Argo CD last fetched, which can be the previous one.

On a branch shared with other environments, a later commit can reach Argo CD before ours does. The adapter accepts a revision other than the promoted one only when `status.summary.images` shows the Bundle images; it does not check that the revision is later. With `update.strategy: argocd` there is no commit to compare, so `status.summary.images` must show the Bundle images.

**Unhealthy only once the promoted change is deployed.** Until Argo CD has deployed the promoted commit, `Degraded` health and a failed operation describe the version before it, so the check waits instead of counting a failure. The commit is deployed once the Application is `Synced` on it, an operation ran on it, or it is in `status.history`. `status.sync.revision` alone does not count: Argo CD sets it to the newest commit it fetched, also while the Application is `OutOfSync` with auto-sync off. An operation counts only when it ran on the promoted commit: `status.operationState.syncResult.revision(s)`, or the requested `operation.sync.revision(s)` before Argo CD records a result. The message then names the operation it ignores, for example `waiting for argocd: health=Healthy, sync=OutOfSync, opPhase=Failed, revision=<new> not synced yet, ignoring the operation on <old>`. With `update.strategy: argocd`, the change is deployed once `status.summary.images` shows the Bundle images.

**When to use:** Any cluster managed by Argo CD. This is the recommended adapter for Argo CD users because it verifies that Argo CD successfully synced the promoted manifests, not just that the Deployment is running.

**Multi-cluster:** In the Argo CD hub-spoke model, all Applications live in the hub cluster. The controller reads Application status from the hub. No cross-cluster API calls needed. See [Remote Clusters](#remote-clusters).

**Edge cases:**
| Application state | Adapter behavior |
|---|---|
| `health.status = Progressing`, `Missing` or `Suspended` | Wait |
| `sync.status = OutOfSync` | Wait (may be mid-sync-wave) |
| Synced to an older revision | Wait (`revision=<old>, waiting for <new>`) |
| The promoted commit fetched but not synced (auto-sync off) | Wait (`revision=<new> not synced yet`) |
| `health.status = Degraded` once the promoted change is deployed | Unhealthy (counts as a health failure) |
| `health.status = Degraded` before that | Wait: it is the previous version's health |
| `operationState.phase = Failed` or `Error` on the promoted commit | Unhealthy (counts as a health failure) |
| `operationState.phase = Failed` or `Error` on another commit | Wait until the promoted commit is synced (`ignoring the operation on <old>`); does not hold a Healthy, Synced promoted commit |
| An operation running on any commit | Wait |
| Application not found | Unhealthy (counts as a health failure) |

Unhealthy results count toward `status.consecutiveHealthFailures`; waiting results do not. When `health.timeout` expires without a Healthy result, whichever of the two the last check returned, the timeout counts as one more health failure and applies `onHealthFailure`. See [Timings and failures](#timings-and-failures).

## Adapter: flux

Watches a Flux Kustomization's reconciliation status.

```yaml
health:
  type: flux
  flux:
    name: my-app-prod           # default: <pipeline>-<environment>
    namespace: flux-system       # default: "flux-system"
  timeout: 10m
```

**Healthy when:** all of these are met:
- `Ready=True` in `status.conditions`
- `status.observedGeneration` equals `metadata.generation` (the controller has reconciled the latest spec). A Kustomization missing either field waits.
- `status.lastAppliedRevision` is the promoted commit (Flux reports `<branch>@sha1:<commit>`, or `<branch>/<commit>` before Flux 2.0), or another commit while the Kustomization's Deployments run the Bundle images (see the table below). A revision that is not a git commit (an OCI or Helm source) cannot be compared: the check passes with "(revision not verified)" in the message.

**When to use:** Any cluster managed by Flux.

**Multi-cluster:** Use a Kustomization in the hub that targets the remote cluster with `spec.kubeConfig.secretRef`; see [Remote Clusters](#remote-clusters).

**Edge cases:**
| Kustomization state | Adapter behavior |
|---|---|
| `Ready=True`, generation matches, promoted commit applied | Healthy |
| `Ready=True`, older commit applied | Wait |
| `Ready=True`, another commit applied (a sibling environment pushed to the same branch before Flux fetched ours) | Healthy when the Kustomization's Deployments (in `status.inventory` or `spec.healthChecks`) run the Bundle images and are rolled out; the message says `(not <commit>, but the Kustomization's Deployments run the Bundle images)`. Otherwise Wait, then `onHealthFailure` at `health.timeout`. A Kustomization with `spec.kubeConfig` (another cluster) has no Deployments kardinal can read, so it waits |
| `Ready=Unknown` while Flux reconciles again the commit it applied (generation observed, `lastAttemptedRevision` equals `lastAppliedRevision`) | Flux sets this at the start of every reconcile, including its interval and a `reconcile.fluxcd.io/requestedAt` request. The result is the result of the Kustomization's Deployments (in `status.inventory` or `spec.healthChecks`) that run a Bundle image repository, with the revision rules above: Healthy keeps a running bake going, and one of them that lost its replicas or stalled is a health failure. A Deployment that runs none of the Bundle's repositories (a cache, say) and is not healthy makes the check wait, never fail; Flux's own result decides once the reconcile ends. Without Deployments that run a Bundle repository it waits |
| `Ready=Unknown` while Flux applies another commit, or generation not observed yet | Wait |
| `spec.suspend: true` and the promoted commit not applied | Wait; the message starts with `Kustomization <namespace>/<name> is suspended; Flux applies nothing until it is resumed`. `onHealthFailure` at `health.timeout`. A suspended Kustomization that already applied the commit is Healthy |
| `approval: pr-review` and the merge commit is not known yet | Wait, then `onHealthFailure` at `health.timeout` (see below) |
| `Ready=False` because Flux gave up on the promoted commit: its resources stalled (`HealthCheckFailed`, "failed early due to stalled resources", for example a Deployment past its `progressDeadlineSeconds`) | **Failed at once**: `onHealthFailure` applies without waiting for the timeout |
| The same stall on another commit (a sibling environment pushed to the same branch before Flux fetched ours) when a Deployment that runs the Bundle images in its pod template is itself past its progress deadline | **Failed at once**; the message says `(lastAttemptedRevision=<other>, not <commit>, but Deployment <namespace>/<name>, which runs the Bundle images, stalled)` |
| `Ready=False` otherwise (build or apply failed, a health check timed out, or a stall of another commit where no Deployment that runs the Bundle images stalled, for example a cache Deployment stalled while ours is rolled out) | Unhealthy (counts as a health failure) |
| Not found | Unhealthy (counts as a health failure) |

**The promoted commit must be known.** For a direct push it is the pushed commit. For a PR (`approval: pr-review`) it is the merge commit: the SCM webhook records it with the merge for GitHub and GitLab, and otherwise the controller asks the SCM provider for it after the merge. Until it is known, the check waits with `merge commit of the PR not known yet`, because a Kustomization that is `Ready=True` on the **previous** commit would otherwise pass: there is no image check to fall back on, unlike `argocd` and `resource`. If the commit never becomes known, `health.timeout` applies `onHealthFailure` with that reason. That happens when:

- the provider does not return one (for example an Azure DevOps PR without `lastMergeCommit`, or a Bitbucket PR whose `merge_commit` is empty);
- the lookup keeps failing for 10 minutes after the merge, after which the controller stops asking.

For those providers, use `argocd` or `resource`.

## Adapter: argoRollouts

Watches an Argo Rollouts Rollout after promotion until it has rolled out the promoted revision.

```yaml
health:
  type: argoRollouts
  argoRollouts:
    name: my-app                # default: Pipeline.metadata.name
    namespace: prod             # default: environment name
  timeout: 30m
```

**Healthy when:** the Rollout's pod template (or the Deployment its `spec.workloadRef` names) runs the Bundle images, Argo Rollouts observed that spec (`status.observedGeneration`), `status.phase` = `Healthy`, and the stable ReplicaSet is the current pod template (`status.stableRS` = `status.currentPodHash`).

This adapter is used for `health.type: argoRollouts` and when `delivery.delegate: argoRollouts` is set on the environment. After kardinal-promoter writes the new image tag to Git and the GitOps tool syncs, Argo Rollouts detects the image change and executes the canary or blue-green strategy. The adapter watches the Rollout until it completes.

Until the GitOps tool applied the change and Argo Rollouts observed it, the check waits (`Rollout <ns>/<name> not updated yet: ...`) whatever the phase says: the phase still describes the previous release, so neither a `Healthy` nor a `Degraded` phase from it counts. Then:

| Rollout phase | Adapter behavior |
|---|---|
| `Progressing` | Wait (canary in progress) |
| `Paused` | Wait (a pause step, or paused by hand) |
| `Healthy` | Healthy (canary completed successfully) |
| `Degraded` | Unhealthy (canary aborted or analysis failed, Argo Rollouts serves the stable revision; counts as a health failure). Not final: Argo Rollouts can roll forward again, so without a `bake` window the step fails at `health.timeout`. |

## Adapter: flagger

Watches a Flagger Canary until Flagger has analyzed and promoted the promoted revision. Used for `health.type: flagger` and for `delivery.delegate: flagger`.

```yaml
health:
  type: flagger
  flagger:
    name: my-app                # default: Pipeline.metadata.name
    namespace: prod             # default: environment name
  timeout: 30m
```

**Healthy when:** `status.phase` = `Succeeded` and the primary Deployment (`<target>-primary`, to which Flagger copies a revision it promotes) runs the Bundle images and is Available.

Flagger keeps the phase of its last analysis until an analysis tick notices that the target changed, so right after the GitOps tool applies a change the phase describes the previous release. The adapter therefore checks the revision first:

- Until the Canary's target Deployment (`spec.targetRef`) runs the Bundle images, the check waits (`Canary <ns>/<name>: target Deployment <ns>/<name> not updated yet: ...`).
- `Succeeded` with a primary on other images is from an earlier release: Wait.
- `Failed` counts only when Flagger set it after the target ran the Bundle images; an earlier `Failed` is from an earlier release: Wait. The previous release's analysis can fail after this health check started but before the GitOps tool applied the Bundle, so the start of the health check is not enough. The first check that finds the target on the Bundle images records the time in the PromotionStep's `status.targetUpdatedAt`; that check, and every later one, waits on a `Failed` set at or before it (in the same second counts as before). Flagger notices a new target before it rolls back, so a `Failed` set later is about the Bundle.
- A `Failed` Canary whose primary runs the Bundle images (the Bundle is the revision Flagger last promoted) is checked like `Succeeded`.
- When the images cannot be compared (a Bundle without images, a target that is not a Deployment), `Succeeded` and `Failed` count only when Flagger set them at or after the start of this health check.

When Flagger set the phase is the `lastUpdateTime` of the Canary's `Promoted` condition, whose reason is the phase. `status.lastTransitionTime` is not used for this when that condition is there: Flagger rewrites it at every analysis tick of a `Failed` Canary.

`status.targetUpdatedAt` is when kardinal first saw the target updated, not when the GitOps tool updated it. Checks run every 10 seconds, and an analysis takes at least one Flagger interval, so kardinal sees the update first. If it does not (the controller was down from before the GitOps tool applied the Bundle until after Flagger failed it), the `Failed` looks like the previous release's: the step waits and fails at `health.timeout` instead of at once.

| Canary phase | Adapter behavior |
|---|---|
| `Initializing`, `Initialized`, `Waiting` | Wait |
| `Progressing`, `WaitingPromotion`, `Promoting`, `Finalising` | Wait |
| `Succeeded` (this release) | Healthy |
| `Failed` (this release) | **Failed at once**: Flagger rolled the canary back, so `onHealthFailure` applies without waiting for the timeout |

The step message carries the message of the Canary's `Promoted` condition, for example why Flagger rolled it back.

## Remote Clusters

kardinal checks health only in the cluster it runs in. It holds no credentials for other clusters and makes no calls to their API servers. To verify a workload in another cluster, run kardinal next to the GitOps hub that manages that cluster and read the hub's object:

- **Argo CD hub:** use `type: argocd`. The Applications for every cluster live in the hub, and their status (health, sync and synced revision) covers the workload in the destination cluster.

    ```yaml
    health:
      type: argocd
      argocd:
        name: my-app-prod-eu        # an Application in the hub whose destination is the spoke
    ```

- **Flux hub:** use `type: flux` on a Kustomization in the hub that sets `spec.kubeConfig.secretRef`. Flux applies that Kustomization to the remote cluster, and the flux adapter reads the Kustomization's status in the hub. Set `spec.wait: true` (or `spec.healthChecks`) on the Kustomization so its `Ready` condition covers the remote workloads, not only the apply. This follows the Flux documentation; it is not tested in this repository.

    ```yaml
    health:
      type: flux
      flux:
        name: my-app-prod-eu        # a Kustomization in the hub with spec.kubeConfig.secretRef
        namespace: flux-system
    ```

**Limit: a spoke the hub cannot reach has no kardinal health check.** kardinal sees only what the hub reports. A cluster that runs its own Argo CD or Flux and is not managed from the hub (for example a pull-only or disconnected cluster) cannot be checked. While a managed spoke is unreachable, the hub's status is what Argo CD or Flux last recorded or an error, not a fresh reading of the workload.

`health.cluster` (a kubeconfig Secret for a remote cluster) is **not supported** and is deprecated. The Pipeline is `Ready=False` (reason `NotImplemented`), `kardinal validate` fails, and a PromotionStep whose environment sets it fails with:

```
health.cluster is not supported: kardinal checks health only in the cluster it runs in; for a workload in another cluster, check its Argo CD Application or Flux Kustomization in this cluster (health.type: argocd or flux, see docs/health-adapters.md#remote-clusters)
```

Distributed mode (`kardinal-agent`, `shard`) was removed; see [Multi-Cluster](distributed-mode.md).

## Timings and failures

| Setting | Value | Effect |
|---|---|---|
| `health.timeout` | default `10m` | Maximum time from entering HealthChecking to the **first** healthy check, and from the moment a `bake` window stops to the next healthy check. When it expires it counts as a health failure (`status.consecutiveHealthFailures`) and applies `onHealthFailure`, with the message `health alarm via <adapter> (onHealthFailure=<action>): health check timeout after <timeout>; last result: ...`. It does not apply while a bake window runs, so a bake longer than the timeout completes. |
| Check interval | 10s | A step is checked at most once every 10s, however often it is reconciled. |
| `bake.minutes` | — | The environment must stay healthy for this long, contiguously, after the first healthy check. A Waiting check stops the window without an alarm (a canary paused at a step, for example). An Unhealthy check is an alarm: with `policy: reset-on-alarm` (default) it stops the window and increments `status.bakeResets`; with `fail-on-alarm` it applies `onHealthFailure`. A stopped window starts again at the next healthy check, and `health.timeout` bounds the wait for it, so a release that stays unhealthy fails at the timeout under either policy. A release that keeps flapping between healthy and unhealthy keeps restarting the window under `reset-on-alarm` and never fails (`status.bakeResets` counts the restarts); use `fail-on-alarm` to fail on the first unhealthy check. |

Each health check has one of four results:

- **Healthy** — Verified (or the bake window starts or advances).
- **Waiting** — the promoted revision is still rolling out or syncing. It does not count as a failure.
- **Unhealthy** — for example Degraded, `Ready=False`, not found, or replicas unavailable after the rollout finished. Each check increments `status.consecutiveHealthFailures`, which a `RollbackPolicy` you create reads (see [Rollback](rollback.md)).
- **Failed** — Deployment `ProgressDeadlineExceeded`, Flagger canary `Failed` or a Flux Kustomization whose resources stalled, on the promoted revision (for Flux, also on another commit when the stalled Deployment runs the Bundle images). `onHealthFailure` (`none` → Failed, `abort` → AbortedByAlarm, `rollback` → RollingBack) applies at once.

Reaching `health.timeout` without a Healthy result is treated like a Failed result: it is counted and applies `onHealthFailure`. A new image that crash-loops is **Waiting**, not Unhealthy: Kubernetes reports the rollout as still progressing (`Progressing=True`, reason `ReplicaSetUpdated`) until the Deployment's `progressDeadlineSeconds` (default 600s) passes. Set `progressDeadlineSeconds` below `health.timeout` to fail such a rollout sooner; otherwise the timeout fails it.

An error reading the target from the API server (not a "not found") is retried at the next interval and does not count as a failure.

## Health Check Defaults

When sub-fields are omitted, the controller applies these defaults:

| Default | Value |
|---|---|
| type | `resource` (or `delivery.delegate` when set) |
| resource.kind | Deployment (the only supported kind) |
| resource.name | Pipeline.metadata.name |
| resource.namespace | Environment name |
| resource.condition | Available |
| argocd.name / argocd.namespace | `<pipeline>-<environment>` / `argocd` |
| flux.name / flux.namespace | `<pipeline>-<environment>` / `flux-system` |
| argoRollouts.name / argoRollouts.namespace | Pipeline.metadata.name / environment name |
| flagger.name / flagger.namespace | Pipeline.metadata.name / environment name |
| timeout | 10m |
| cluster | not supported (must be empty) |
