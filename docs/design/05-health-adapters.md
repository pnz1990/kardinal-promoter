# 05: Health Adapters

> Status: Comprehensive
> Depends on: 01-graph-integration (for CRD types)
> Blocks: nothing (consumed by 03-promotionstep-reconciler via the health-check step)

## Purpose

Health adapters verify that a GitOps tool (or Kubernetes itself) has successfully applied a promoted manifest change. The PromotionStep reconciler calls the health adapter during the HealthChecking state. Each adapter watches a different Kubernetes resource type and defines its own criteria for "healthy."

## Go Package Structure

```
pkg/
  health/
    adapter.go          # Interface definition
    registry.go         # Adapter registry + auto-detection
    resource.go         # Deployment condition adapter
    argocd.go           # Argo CD Application adapter
    flux.go             # Flux Kustomization adapter
    argorollouts.go     # Argo Rollouts adapter (Phase 2)
    flagger.go          # Flagger Canary adapter (Phase 2)
    remote.go           # Remote cluster client management
    health_test.go      # Unit tests
```

## Interface

```go
type Adapter interface {
    // Check returns the health status of the target workload.
    // Called repeatedly (every 10s) until it returns Healthy, timeout expires, or an error occurs.
    // Must not cache results across calls; each call must reflect current cluster state.
    Check(ctx context.Context, opts CheckOptions) (HealthStatus, error)

    // Name returns the adapter identifier (e.g., "resource", "argocd", "flux").
    Name() string

    // Available returns true if this adapter's CRD prerequisites exist in the cluster.
    // Must only check for CRD existence, not for specific resource instances.
    Available(ctx context.Context, client dynamic.Interface) (bool, error)
}

type CheckOptions struct {
    // From the PromotionStep health config
    Type       string            // "resource", "argocd", "flux", "argoRollouts", "flagger"
    Resource   ResourceConfig    // for type: resource
    ArgoCD     ArgoCDConfig      // for type: argocd
    Flux       FluxConfig        // for type: flux
    ArgoRollouts ArgoRolloutsConfig // for type: argoRollouts
    Flagger    FlaggerConfig     // for type: flagger
    Cluster    string            // kubeconfig Secret name for remote clusters
    Timeout    time.Duration     // health check timeout
}

type HealthStatus struct {
    Healthy   bool
    Reason    string
    Details   map[string]string   // adapter-specific metadata (e.g., sync status, rollout phase)
}
```

## Auto-Detection

On controller startup and every 5 minutes, the registry checks which adapter CRDs are installed:

```go
func (r *Registry) Detect(ctx context.Context, client dynamic.Interface) {
    r.argocdAvailable = crdExists(ctx, client, "applications.argoproj.io")
    r.fluxAvailable = crdExists(ctx, client, "kustomizations.kustomize.toolkit.fluxcd.io")
    r.argoRolloutsAvailable = crdExists(ctx, client, "rollouts.argoproj.io")
    r.flaggerAvailable = crdExists(ctx, client, "canaries.flagger.app")
}

func crdExists(ctx context.Context, client dynamic.Interface, crdName string) bool {
    _, err := client.Resource(crdGVR).Get(ctx, crdName, metav1.GetOptions{})
    return err == nil
}
```

When a PromotionStep's `health.type` is omitted, the registry selects in priority order: `argocd` (if available), `flux` (if available), `resource` (always available).

## Adapter: resource

Watches a Kubernetes Deployment's status conditions.

**Config:**
```go
type ResourceConfig struct {
    Kind      string // default: "Deployment"
    Name      string // default: Pipeline.metadata.name
    Namespace string // default: environment name
    Condition string // default: "Available"
}
```

**Health logic:**
```go
func (a *ResourceAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
    deploy := &appsv1.Deployment{}
    key := types.NamespacedName{
        Name:      opts.Resource.Name,
        Namespace: opts.Resource.Namespace,
    }
    if err := a.client.Get(ctx, key, deploy); err != nil {
        if apierrors.IsNotFound(err) {
            return HealthStatus{Healthy: false, Reason: "Deployment not found"}, nil
        }
        return HealthStatus{}, err
    }

    for _, cond := range deploy.Status.Conditions {
        if string(cond.Type) == opts.Resource.Condition {
            if cond.Status == corev1.ConditionTrue {
                return HealthStatus{Healthy: true, Reason: "Available=True"}, nil
            }
            return HealthStatus{
                Healthy: false,
                Reason:  fmt.Sprintf("%s=%s: %s", cond.Type, cond.Status, cond.Message),
            }, nil
        }
    }
    return HealthStatus{Healthy: false, Reason: "Condition not found"}, nil
}
```

**Edge cases:**
- Deployment not found: return unhealthy, retry (it may not exist yet if the GitOps tool hasn't synced).
- Deployment scaled to 0: `Available=True` if `spec.replicas == 0` and `status.availableReplicas == 0`. This is healthy by Kubernetes definition.
- HPA-managed: the adapter checks the condition, not the replica count. HPA changes don't affect the health check.

## Adapter: argocd

Watches an Argo CD Application's health, sync, and operation status.

**Config:**
```go
type ArgoCDConfig struct {
    Name      string // Application name
    Namespace string // default: "argocd"
}
```

**Health logic:**

The adapter reads the Application CR via the dynamic client (no compile-time Argo CD dependency):

```go
func (a *ArgoCDAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
    gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
    app, err := a.dynamic.Resource(gvr).Namespace(opts.ArgoCD.Namespace).Get(ctx, opts.ArgoCD.Name, metav1.GetOptions{})
    if err != nil {
        if apierrors.IsNotFound(err) {
            return HealthStatus{Healthy: false, Reason: "Application not found"}, nil
        }
        return HealthStatus{}, err
    }

    health, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
    sync, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
    opPhase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")

    details := map[string]string{"health": health, "sync": sync, "opPhase": opPhase}

    if health == "Healthy" && sync == "Synced" && (opPhase == "Succeeded" || opPhase == "") {
        return HealthStatus{Healthy: true, Reason: "Healthy+Synced", Details: details}, nil
    }

    return HealthStatus{
        Healthy: false,
        Reason:  fmt.Sprintf("health=%s, sync=%s, opPhase=%s", health, sync, opPhase),
        Details: details,
    }, nil
}
```

**State matrix:**

| health | sync | opPhase | Adapter result |
|---|---|---|---|
| Healthy | Synced | Succeeded or empty | Healthy |
| Healthy | Synced | Running | Wait (sync in progress) |
| Progressing | any | any | Wait |
| Degraded | any | any | Unhealthy (will fail after timeout) |
| Missing | any | any | Unhealthy (immediate fail) |
| Suspended | any | any | Unhealthy (will fail after timeout) |
| any | OutOfSync | any | Wait (may be mid-sync-wave) |

**Multi-cluster:** In the Argo CD hub-spoke model, Applications for remote clusters live in the hub. The adapter reads Application status from the hub cluster (where the controller runs). No remote kubeconfig needed.

## Adapter: flux

Watches a Flux Kustomization's reconciliation status.

**Config:**
```go
type FluxConfig struct {
    Name      string // Kustomization name
    Namespace string // default: "flux-system"
}
```

**Health logic:**
```go
func (a *FluxAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
    gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
    ks, err := a.dynamic.Resource(gvr).Namespace(opts.Flux.Namespace).Get(ctx, opts.Flux.Name, metav1.GetOptions{})
    if err != nil {
        if apierrors.IsNotFound(err) {
            return HealthStatus{Healthy: false, Reason: "Kustomization not found"}, nil
        }
        return HealthStatus{}, err
    }

    conditions, _, _ := unstructured.NestedSlice(ks.Object, "status", "conditions")
    observedGen, _, _ := unstructured.NestedInt64(ks.Object, "status", "observedGeneration")
    generation, _, _ := unstructured.NestedInt64(ks.Object, "metadata", "generation")

    readyCondition := findCondition(conditions, "Ready")
    if readyCondition == nil {
        return HealthStatus{Healthy: false, Reason: "Ready condition not found"}, nil
    }

    if readyCondition["status"] == "True" && observedGen == generation {
        return HealthStatus{Healthy: true, Reason: "Ready=True, generation match"}, nil
    }

    return HealthStatus{
        Healthy: false,
        Reason: fmt.Sprintf("Ready=%s, observedGen=%d, generation=%d",
            readyCondition["status"], observedGen, generation),
    }, nil
}
```

**Generation matching:** `observedGeneration == generation` ensures the controller has reconciled the latest spec. Without this check, the adapter could return Healthy based on the previous reconciliation.

**Multi-cluster:** Flux runs per-cluster. For remote clusters, the adapter creates a dynamic client from the kubeconfig Secret (see Remote Cluster section below).

## Adapter: argoRollouts (Phase 2)

Watches an Argo Rollouts Rollout phase.

**Config:**
```go
type ArgoRolloutsConfig struct {
    Name      string
    Namespace string
}
```

**State mapping:**

| Rollout phase | Adapter result |
|---|---|
| Progressing | Wait |
| Paused | Wait (manual step in Argo Rollouts) |
| Healthy | Healthy |
| Degraded | Unhealthy (Argo Rollouts rolled back) |

## Adapter: flagger (Phase 2)

Watches a Flagger Canary phase.

| Canary phase | Adapter result |
|---|---|
| Initializing, Progressing, Promoting, Finalising | Wait |
| Succeeded | Healthy |
| Failed | Unhealthy |

## Remote Cluster Client Management

When `health.cluster` is set on an environment, the adapter uses a dynamic client created from a kubeconfig Secret.

```go
type RemoteClientManager struct {
    mu      sync.RWMutex
    clients map[string]dynamic.Interface // keyed by Secret name
    local   client.Client                // for reading Secrets
}

func (m *RemoteClientManager) GetClient(ctx context.Context, secretName, namespace string) (dynamic.Interface, error) {
    m.mu.RLock()
    if c, ok := m.clients[secretName]; ok {
        m.mu.RUnlock()
        return c, nil
    }
    m.mu.RUnlock()

    // Load kubeconfig from Secret
    secret := &corev1.Secret{}
    if err := m.local.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err != nil {
        return nil, fmt.Errorf("loading kubeconfig secret %s: %w", secretName, err)
    }
    kubeconfig := secret.Data["kubeconfig"]
    config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
    if err != nil {
        return nil, fmt.Errorf("parsing kubeconfig: %w", err)
    }
    dynClient, err := dynamic.NewForConfig(config)
    if err != nil {
        return nil, fmt.Errorf("creating dynamic client: %w", err)
    }

    m.mu.Lock()
    m.clients[secretName] = dynClient
    m.mu.Unlock()
    return dynClient, nil
}
```

Clients are cached per Secret name. Cache invalidation: on Secret update (watch Secrets for changes) or on connection failure (remove from cache, recreate on next request).

## Adapter Selection in the PromotionStep Reconciler

The `health-check` step in the promotion sequence calls:

```go
func (s *HealthCheckStep) Execute(ctx context.Context, state *StepState) (StepResult, error) {
    adapter := s.registry.Get(state.Environment.Health.Type)
    if adapter == nil {
        adapter = s.registry.AutoDetect()
    }

    client := s.localClient
    if state.Environment.Health.Cluster != "" {
        var err error
        client, err = s.remoteClients.GetClient(ctx, state.Environment.Health.Cluster, state.Pipeline.Namespace)
        if err != nil {
            return StepResult{Success: false, Message: err.Error()}, nil
        }
    }

    status, err := adapter.Check(ctx, CheckOptions{...})
    if err != nil {
        return StepResult{}, err
    }
    if status.Healthy {
        return StepResult{Success: true, Message: status.Reason, Outputs: map[string]any{"healthDetails": status.Details}}, nil
    }
    // Not healthy yet: return pending (reconciler will requeue)
    return StepResult{Success: false, Message: status.Reason}, nil
}
```

The PromotionStep reconciler handles the "not healthy yet" case by requeueing after 10 seconds and retrying until timeout.

## Unit Tests

1. Resource adapter: Deployment with Available=True returns Healthy.
2. Resource adapter: Deployment not found returns unhealthy.
3. Resource adapter: Deployment with Available=False returns unhealthy with message.
4. Argo CD adapter: Healthy+Synced+Succeeded returns Healthy.
5. Argo CD adapter: Progressing returns unhealthy (wait).
6. Argo CD adapter: Degraded returns unhealthy.
7. Argo CD adapter: Application not found returns unhealthy.
8. Flux adapter: Ready=True with generation match returns Healthy.
9. Flux adapter: Ready=True with stale generation returns unhealthy.
10. Flux adapter: Kustomization not found returns unhealthy.
11. Auto-detection: Argo CD CRD present, selects argocd.
12. Auto-detection: Flux CRD present (no Argo CD), selects flux.
13. Auto-detection: no CRDs present, selects resource.
14. Remote client: valid kubeconfig Secret creates client.
15. Remote client: invalid kubeconfig returns error.
16. Remote client: cached client reused on second call.
