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
- `status.outputs.mergeCommitSHA` — the merge commit of the promotion PR (`approval: pr-review`), copied from `PRStatus.status.mergeCommitSHA`.

The `argocd` and `flux` adapters require that commit. The `argocd` adapter also accepts a later commit on a shared branch when the Application runs the Bundle images; the `flux` adapter does not (see below). The `resource` adapter requires the Bundle images in the Deployment's pod template. `kubectl get promotionstep <name> -o yaml` shows the recorded commit in `status.outputs` and the last health result in `status.message`.

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
- `status.operationState.phase` = `Succeeded` (or no operation recorded)
- the Application synced the promoted commit: it appears in `status.sync.revision(s)`, `status.operationState.syncResult.revision(s)` or `status.history`. `Synced` alone only means the cluster matches whatever commit Argo CD last fetched, which can be the previous one.

On a branch shared with other environments, a later commit can reach Argo CD before ours does. The adapter accepts that later revision only when `status.summary.images` shows the Bundle images. With `update.strategy: argocd-set-image` there is no commit to compare, so `status.summary.images` must show the Bundle images.

**When to use:** Any cluster managed by Argo CD. This is the recommended adapter for Argo CD users because it verifies that Argo CD successfully synced the promoted manifests, not just that the Deployment is running.

**Multi-cluster:** In the Argo CD hub-spoke model, all Applications live in the hub cluster. The controller reads Application status from the hub. No cross-cluster API calls needed.

**Edge cases:**
| Application state | Adapter behavior |
|---|---|
| `health.status = Progressing`, `Missing` or `Suspended` | Wait |
| `sync.status = OutOfSync` | Wait (may be mid-sync-wave) |
| Synced to an older revision | Wait (`revision=<old>, waiting for <new>`) |
| `health.status = Degraded` | Unhealthy (counts as a health failure) |
| `operationState.phase = Failed` or `Error` | Unhealthy (counts as a health failure) |
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
- `status.lastAppliedRevision` is the promoted commit (Flux reports `<branch>@sha1:<commit>`, or `<branch>/<commit>` before Flux 2.0). A revision that is not a git commit (an OCI or Helm source) cannot be compared: the check passes with "(revision not verified)" in the message.

**When to use:** Any cluster managed by Flux.

**Multi-cluster:** Flux runs per-cluster. `health.cluster` is not supported; see [Remote Clusters](#remote-clusters).

**Edge cases:**
| Kustomization state | Adapter behavior |
|---|---|
| `Ready=True`, generation matches, promoted commit applied | Healthy |
| `Ready=True`, older commit applied | Wait |
| `Ready=True`, a later commit applied (another push to the same branch reached Flux before it fetched ours) | Wait, then `onHealthFailure` at `health.timeout`: unlike `argocd`, this adapter has no image check to fall back on. Give each environment its own branch, or use the `resource` adapter |
| `Ready=Unknown` (reconciling) or generation not observed yet | Wait |
| `approval: pr-review` and the merge commit is not known yet | Wait, then `onHealthFailure` at `health.timeout` (see below) |
| `Ready=False` (reconciliation failed or stalled) | Unhealthy (counts as a health failure) |
| Not found | Unhealthy (counts as a health failure) |

**The promoted commit must be known.** For a direct push it is the pushed commit. For a PR (`approval: pr-review`) it is the merge commit: the SCM webhook records it with the merge for GitHub and GitLab, and otherwise the controller asks the SCM provider for it after the merge. Until it is known, the check waits with `merge commit of the PR not known yet`, because a Kustomization that is `Ready=True` on the **previous** commit would otherwise pass: there is no image check to fall back on, unlike `argocd` and `resource`. If the commit never becomes known, `health.timeout` applies `onHealthFailure` with that reason. That happens when:

- the provider does not return one (for example an Azure DevOps PR without `lastMergeCommit`, or a Bitbucket PR whose `merge_commit` is empty);
- the lookup keeps failing for 10 minutes after the merge, after which the controller stops asking.

For those providers, use `argocd` or `resource`.

## Adapter: argoRollouts

Watches an Argo Rollouts Rollout's phase after promotion.

```yaml
health:
  type: argoRollouts
  argoRollouts:
    name: my-app                # default: Pipeline.metadata.name
    namespace: prod             # default: environment name
  timeout: 30m
```

**Healthy when:** `status.phase` = `Healthy`

This adapter is used when `delivery.delegate: argoRollouts` is set on the environment. After kardinal-promoter writes the new image tag to Git and the GitOps tool syncs, Argo Rollouts detects the image change and executes the canary or blue-green strategy. The adapter watches the Rollout until it completes.

| Rollout phase | Adapter behavior |
|---|---|
| `Progressing` | Wait (canary in progress) |
| `Paused` | Wait (manual promotion step in Argo Rollouts) |
| `Healthy` | Healthy (canary completed successfully) |
| `Degraded` | Unhealthy (canary failed, Argo Rollouts rolled back; counts as a health failure) |

**Limitation:** the adapter checks the Rollout phase only, not which revision it rolled out. A Rollout that is `Healthy` on the previous version before the GitOps tool applies the change can report Verified early. Use a `bake` window, or the `argocd`/`flux` adapter, when that matters.

## Adapter: flagger

Watches a Flagger Canary's phase. Used for `health.type: flagger` and for `delivery.delegate: flagger`.

```yaml
health:
  type: flagger
  flagger:
    name: my-app                # default: Pipeline.metadata.name
    namespace: prod             # default: environment name
  timeout: 30m
```

**Healthy when:** `status.phase` = `Succeeded`

| Canary phase | Adapter behavior |
|---|---|
| `Initializing`, `Initialized`, `Waiting` | Wait |
| `Progressing`, `WaitingPromotion`, `Promoting`, `Finalising` | Wait |
| `Succeeded` | Healthy |
| `Failed` | **Failed at once**: Flagger rolled the canary back, so `onHealthFailure` applies without waiting for the timeout |

The step message carries the message of the Canary's `Promoted` condition, for example why Flagger rolled it back.

**Limitation:** like `argoRollouts`, the adapter checks the phase, not the revision. A Canary that is still `Succeeded` from the previous release can report Verified before Flagger detects the change.

## Remote Clusters

`health.cluster` (a kubeconfig Secret for a remote cluster) is **not supported**. A PromotionStep whose environment sets it fails with:

```
health.cluster is not supported: remote-cluster health checks are not implemented; for a workload in another cluster, check its Argo CD Application in this cluster (health.type: argocd)
```

This replaces the earlier behaviour, where the field was accepted and the health check silently ran against the controller's own cluster.

Every adapter reads objects through the reconciler's own Kubernetes client, so it only sees the cluster that holds the PromotionSteps. To verify a workload in another cluster:

- **Argo CD hub-spoke:** use `type: argocd`. Applications for all clusters live in the hub, and their status (sync revision and health) reflects the remote workloads. No `cluster` field is needed.
- **Distributed mode does not help yet:** a `kardinal-agent` uses one client configuration for both the PromotionSteps and the health checks (see [Distributed Mode](distributed-mode.md)), so it checks the API server that holds the PromotionSteps, not the cluster it runs in.

## Timings and failures

| Setting | Value | Effect |
|---|---|---|
| `health.timeout` | default `10m` | Maximum time from entering HealthChecking to the **first** healthy check. When it expires it counts as a health failure (`status.consecutiveHealthFailures`) and applies `onHealthFailure`, with the message `health alarm via <adapter> (onHealthFailure=<action>): health check timeout after <timeout>; last result: ...`. It stops applying once a `bake` window has started, so a bake longer than the timeout completes. |
| Check interval | 10s | A step is checked at most once every 10s, however often it is reconciled. |
| `bake.minutes` | — | The environment must stay healthy for this long, contiguously, after the first healthy check. With `policy: reset-on-alarm` (default) an unhealthy check restarts the window. With `fail-on-alarm` it applies `onHealthFailure`. |

Each health check has one of four results:

- **Healthy** — Verified (or the bake window starts or advances).
- **Waiting** — the promoted revision is still rolling out or syncing. It does not count as a failure.
- **Unhealthy** — for example Degraded, `Ready=False`, not found, or replicas unavailable after the rollout finished. Each check increments `status.consecutiveHealthFailures`, which a `RollbackPolicy` you create reads (see [Rollback](rollback.md)).
- **Failed** — Deployment `ProgressDeadlineExceeded` or Flagger canary `Failed`. `onHealthFailure` (`none` → Failed, `abort` → AbortedByAlarm, `rollback` → RollingBack) applies at once.

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
