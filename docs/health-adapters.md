# Health Adapters

After a promotion is applied (manifests written to Git), kardinal-promoter verifies that the target environment is healthy before marking the PromotionStep as Verified. Health verification uses pluggable adapters that check the appropriate Kubernetes resource status.

## Auto-Detection

On startup, the controller checks which CRDs are installed in the cluster:

1. If `Application` CRD (`argoproj.io/v1alpha1`) exists, the `argocd` adapter is available.
2. If `Kustomization` CRD (`kustomize.toolkit.fluxcd.io/v1`) exists, the `flux` adapter is available.
3. If `Rollout` CRD (`argoproj.io/v1alpha1`) exists, the `argoRollouts` adapter is available (Phase 2).
4. If `Canary` CRD (`flagger.app/v1beta1`) exists, the `flagger` adapter is available (Phase 2).

When a Pipeline environment does not specify `health.type`, the controller uses the first available adapter in priority order: `argocd`, `flux`, `resource`.

Auto-detection results are cached and re-checked every 5 minutes (CRDs can be installed at runtime).

## Adapter: resource (default)

Watches a Kubernetes Deployment's status conditions.

```yaml
health:
  type: resource
  resource:
    kind: Deployment            # default
    name: my-app                # default: Pipeline.metadata.name
    namespace: prod             # default: environment name
    condition: Available        # default
  timeout: 10m
```

**Healthy when:** `Available=True` and `Progressing` is not stalled (either `Progressing=True` with reason `NewReplicaSetAvailable`, or the Progressing condition is absent).

**When to use:** Clusters without Argo CD or Flux. Also used as a fallback when auto-detection finds no GitOps tool.

**Limitations:** Only checks that the Deployment itself is healthy. Does not verify that the image was actually updated (the old Deployment may still report Available if the new image fails to pull). For reliable verification, use the `argocd` or `flux` adapter.

## Adapter: argocd

Watches an Argo CD Application's health, sync, and operation status.

```yaml
health:
  type: argocd
  argocd:
    name: my-app-prod           # Argo CD Application name
    namespace: argocd           # default: "argocd"
  timeout: 15m
```

**Healthy when:** all three conditions are met:
- `status.health.status` = `Healthy`
- `status.sync.status` = `Synced`
- `status.operationState.phase` = `Succeeded`

**When to use:** Any cluster managed by Argo CD. This is the recommended adapter for Argo CD users because it verifies that Argo CD successfully synced the promoted manifests, not just that the Deployment is running.

**Multi-cluster:** In the Argo CD hub-spoke model, all Applications live in the hub cluster. The controller reads Application status from the hub. No cross-cluster API calls needed.

**Edge cases:**
| Application state | Adapter behavior |
|---|---|
| `health.status = Progressing` | Wait (sync in progress) |
| `health.status = Degraded` | Fail after timeout |
| `health.status = Missing` | Fail immediately |
| `health.status = Suspended` | Fail after timeout |
| `sync.status = OutOfSync` | Wait (may be mid-sync-wave) |
| Application not found | Retry for 60s, then fail |

## Adapter: flux

Watches a Flux Kustomization's reconciliation status.

```yaml
health:
  type: flux
  flux:
    name: my-app-prod           # Kustomization name
    namespace: flux-system       # default: "flux-system"
  timeout: 10m
```

**Healthy when:** both conditions are met:
- `Ready=True` in `status.conditions`
- `status.observedGeneration` equals `metadata.generation` (ensures the controller has reconciled the latest spec)

**When to use:** Any cluster managed by Flux.

**Multi-cluster:** Flux runs per-cluster. For remote clusters, add a `cluster` field referencing a kubeconfig Secret:

```yaml
health:
  type: flux
  flux:
    name: my-app-prod
    namespace: flux-system
  cluster: prod-cluster         # kubeconfig Secret name
```

**Edge cases:**
| Kustomization state | Adapter behavior |
|---|---|
| `Ready=True`, generation matches | Healthy |
| `Ready=False`, reconciling | Wait |
| `Ready=False`, stalled | Fail after timeout |
| Suspended | Fail after timeout |
| Not found | Retry for 60s, then fail |

## Adapter: argoRollouts

Watches an Argo Rollouts Rollout's phase after promotion.

```yaml
health:
  type: argoRollouts
  argoRollouts:
    name: my-app                # Rollout name
    namespace: prod             # Rollout namespace
  timeout: 30m
```

**Healthy when:** `status.phase` = `Healthy`

This adapter is used when `delivery.delegate: argoRollouts` is set on the environment. After kardinal-promoter writes the new image tag to Git and the GitOps tool syncs, Argo Rollouts detects the image change and executes the canary or blue-green strategy. The adapter watches the Rollout until it completes.

| Rollout phase | Adapter behavior |
|---|---|
| `Progressing` | Wait (canary in progress) |
| `Paused` | Wait (manual promotion step in Argo Rollouts) |
| `Healthy` | Healthy (canary completed successfully) |
| `Degraded` | Fail (canary failed, Argo Rollouts rolled back) |

## Adapter: flagger (Phase 2)

Watches a Flagger Canary's phase.

```yaml
health:
  type: flagger
  flagger:
    name: my-app
    namespace: prod
  timeout: 30m
```

**Healthy when:** `status.phase` = `Succeeded`

| Canary phase | Adapter behavior |
|---|---|
| `Initializing` | Wait |
| `Progressing` | Wait |
| `Promoting` | Wait |
| `Finalising` | Wait |
| `Succeeded` | Healthy |
| `Failed` | Fail |

## Remote Clusters

For multi-cluster deployments where the workload is in a different cluster from the controller, add a `cluster` field to the health config. This field references a Kubernetes Secret containing a kubeconfig for the remote cluster.

```yaml
health:
  type: argocd
  argocd:
    name: my-app-prod-us-east
  cluster: prod-us-east
```

The Secret must exist in the controller's namespace:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: prod-us-east
  namespace: kardinal-system
type: Opaque
data:
  kubeconfig: <base64-encoded kubeconfig>
```

The controller creates a Kubernetes client from the kubeconfig and uses it for health checks. One client is created per remote cluster and reused across health checks.

**For Argo CD hub-spoke users:** You typically do not need the `cluster` field. Argo CD Applications for all clusters live in the hub. The controller reads Application health from the hub cluster, which reflects the state of workloads in remote clusters.

## Timeout

The `timeout` field on the health config determines how long the controller waits for the environment to become healthy after promotion. If the timeout expires, the PromotionStep is marked as `Failed`.

Default: `10m`. Recommended for Argo Rollouts canary environments: `30m` (canary steps take time).

## Health Check Defaults

When the `health` field is omitted from an environment, the controller applies these defaults:

| Default | Value |
|---|---|
| type | Auto-detected (argocd > flux > resource) |
| resource.kind | Deployment |
| resource.name | Pipeline.metadata.name |
| resource.namespace | Environment name |
| resource.condition | Available |
| timeout | 10m |
