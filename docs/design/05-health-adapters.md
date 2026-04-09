# 05: Health Adapters

> Status: Outline
> Depends on: 01-graph-integration (for CRD types)
> Blocks: nothing (leaf node, consumed by 03-promotionstep-reconciler)

Three adapters in Phase 1. Each has different semantics for "healthy."

## Scope

- Per-adapter state machine: what "healthy" means for each

- **Deployment adapter (resource)**
  - Watches: Deployment.status.conditions
  - Healthy: Available=True AND Progressing not stalled (Progressing=True with reason NewReplicaSetAvailable, or Progressing condition absent)
  - Edge cases: Deployment not found (retry), Deployment scaled to 0 (healthy or not?), HPA-managed Deployments

- **Argo CD adapter (argocd)**
  - Watches: Application.status.health.status + Application.status.sync.status + Application.status.operationState.phase
  - Healthy: health=Healthy AND sync=Synced AND operationState.phase=Succeeded
  - Edge cases: Application in Progressing state (wait), Application Missing (fail), Application Suspended (fail or wait?), sync=OutOfSync after promotion (wait for sync wave)
  - Namespace: default argocd, configurable

- **Flux adapter (flux)**
  - Watches: Kustomization.status.conditions
  - Healthy: Ready=True AND status.observedGeneration == metadata.generation
  - Edge cases: Kustomization suspended (fail), Kustomization not found (retry), stale observedGeneration (wait)
  - Namespace: default flux-system, configurable

- Auto-detection logic
  - On startup: check for Application CRD, Kustomization CRD existence via discovery API
  - Priority ordering: argocd > flux > resource
  - Caching: detection result cached, re-checked every 5 minutes (CRDs can be installed at runtime)

- Remote cluster support
  - cluster field on environment health config references a kubeconfig Secret
  - Secret loading: read from controller namespace
  - Client creation: rest.Config from kubeconfig, dynamic client per remote cluster
  - Connection pooling: one client per remote cluster, reused across health checks
  - Error handling: Secret not found, kubeconfig invalid, remote cluster unreachable

- Phase 2 adapter stubs (interface design only, not implemented)
  - Argo Rollouts: Rollout.status.phase (Healthy/Progressing/Degraded/Paused)
  - Flagger: Canary.status.phase (Initializing/Progressing/Promoting/Finalising/Succeeded/Failed)
