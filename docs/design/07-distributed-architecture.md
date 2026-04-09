# 07: Distributed Architecture

> Status: Comprehensive
> Depends on: 01-graph-integration, 03-promotionstep-reconciler
> Blocks: nothing (additive to standalone mode)

## Purpose

This spec defines how kardinal-promoter scales to multi-cluster enterprise environments by splitting the controller into a control plane component and per-shard agents. The agent runs behind firewalls in workload clusters, connecting outbound to the control plane. The control plane retains all state and provides a single pane of glass.

## Design Constraint

The PromotionStep reconciler code is identical in standalone and distributed modes. The difference is where it runs and what it watches. In standalone mode, it runs in the kardinal-controller binary and watches all PromotionSteps. In distributed mode, it runs in the kardinal-agent binary and watches only PromotionSteps matching its shard label.

No reconciler logic changes between modes. The distributed architecture is a deployment concern, not a code concern.

## Binaries

### kardinal-controller (control plane)

Runs in the control plane cluster. Contains:
- Pipeline reconciler: watches Pipeline + Bundle CRDs, generates Graphs, validates skip permissions
- PolicyGate reconciler: evaluates CEL expressions on PolicyGate instances
- Bundle lifecycle: detects new Bundles, manages superseding, garbage collection
- PromotionStep reconciler: handles PromotionSteps WITHOUT a shard label (local environments)
- kardinal-ui: embedded UI
- Webhook handlers: `/api/v1/bundles`, `/webhooks`
- Metrics: `/metrics`

In standalone mode, this is the only binary. It handles all PromotionSteps regardless of shard.

### kardinal-agent (per shard)

Runs in a workload cluster. Contains:
- PromotionStep reconciler: handles PromotionSteps WITH a shard label matching `--shard`
- Git cache: local to the agent cluster
- SCM credentials: stored in the agent cluster (not the control plane)
- Health check clients: local to the agent cluster

The agent does NOT contain: Pipeline reconciler, PolicyGate reconciler, Bundle lifecycle, UI, webhook handlers. These responsibilities stay in the control plane.

## Shard Routing

### Pipeline configuration

```yaml
environments:
  - name: prod-eu
    shard: eu-cluster
    path: env/prod-eu
    approval: pr-review
  - name: prod-us
    shard: us-cluster
    path: env/prod-us
    approval: pr-review
```

### Label propagation

During Pipeline-to-Graph translation (see 02), the translator sets `kardinal.io/shard` on each PromotionStep template:

```yaml
metadata:
  labels:
    kardinal.io/shard: eu-cluster
```

When the Graph controller creates the PromotionStep CR, this label is propagated.

### Agent filtering

```go
func (r *PromotionStepReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    step := &v1alpha1.PromotionStep{}
    if err := r.Get(ctx, req.NamespacedName, step); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    stepShard := step.Labels["kardinal.io/shard"]

    if r.shard == "" {
        // Standalone mode: handle all steps, OR
        // Control plane: handle steps without a shard label
        if stepShard != "" {
            return ctrl.Result{}, nil // has a shard, leave it for an agent
        }
    } else {
        // Agent mode: handle only matching shard
        if stepShard != r.shard {
            return ctrl.Result{}, nil
        }
    }

    // proceed with reconciliation...
}
```

An additional optimization: use a label selector on the informer to only watch PromotionSteps matching the shard. This reduces the number of events the agent processes:

```go
informer := cache.NewFilteredInformerForResource(
    dynamicClient, promotionstepGVR, namespace,
    cache.WithLabelSelector(fmt.Sprintf("kardinal.io/shard=%s", shard)),
)
```

## Connectivity

### Agent to control plane

The agent connects to the control plane's Kubernetes API server using a kubeconfig. This kubeconfig contains a ServiceAccount token scoped to the minimum required RBAC (see Credentials section below).

```bash
kardinal-agent \
  --shard=eu-cluster \
  --control-plane-kubeconfig=/etc/kardinal/control-plane-kubeconfig \
  --git-cache-dir=/var/cache/kardinal \
  --health-kubeconfig=/etc/kardinal/local-kubeconfig  # or in-cluster
```

The agent:
1. Watches PromotionStep CRs in the control plane (read via the control plane kubeconfig)
2. Updates PromotionStep status in the control plane (write via the control plane kubeconfig)
3. Reads Bundle CRs from the control plane (to build step state)
4. Executes Git operations using local Git credentials
5. Checks health using local cluster credentials or in-cluster access

The connection is outbound from the agent to the control plane. The control plane does not initiate connections to agents.

### Control plane to agents

There is no direct connection. The control plane reads PromotionStep status (which agents update) from its own API server. The UI shows all shards because all CRDs live in the control plane.

## Credentials

### Agent ServiceAccount in the control plane

Each agent needs a ServiceAccount in the control plane cluster with minimal RBAC:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kardinal-agent-eu-cluster
  namespace: kardinal-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kardinal-agent
  namespace: default                    # or the Pipeline's namespace
rules:
  - apiGroups: ["kardinal.io"]
    resources: ["promotionsteps"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["kardinal.io"]
    resources: ["promotionsteps/status"]
    verbs: ["update", "patch"]
  - apiGroups: ["kardinal.io"]
    resources: ["bundles"]
    verbs: ["get"]
  - apiGroups: ["kardinal.io"]
    resources: ["bundles/status"]
    verbs: ["update", "patch"]          # for evidence copy
```

The agent cannot: create or delete PromotionSteps, create or delete Bundles, access PolicyGates, access Pipelines, access Graphs, access Secrets in the control plane.

### Git and SCM credentials

Git tokens and SCM webhook secrets are stored in the agent cluster, not the control plane. Each agent manages its own Git credentials for the repositories relevant to its shard.

This means the control plane never holds workload cluster credentials or Git tokens for shard-specific repositories. Credential isolation is a security benefit.

### Health check credentials

The agent uses its local cluster credentials (in-cluster ServiceAccount or a provided kubeconfig) for health checks. For remote health checks within the agent's network, the agent stores kubeconfig Secrets locally.

## Observability

All CRDs live in the control plane. When an agent updates a PromotionStep's status, the update is written to the control plane's etcd. The UI and CLI read from the control plane and show status across all shards.

Agent Prometheus metrics: each agent exposes `/metrics` on its own Pod. To aggregate:
- Option A: each cluster's Prometheus scrapes its local agent. Central Grafana queries multiple Prometheus instances.
- Option B: agent pushes metrics to a central collector (e.g., Prometheus remote write).

Recommended: Option A (standard Kubernetes monitoring pattern). The controller's metrics (Pipeline reconciler, PolicyGate reconciler) are scraped from the control plane.

## Standalone to Distributed Upgrade Path

1. **Phase 1 (standalone):** Deploy `kardinal-controller` as a single binary. The `shard` field on environments is ignored. All PromotionSteps are reconciled locally.

2. **Add an agent:** Deploy `kardinal-agent` in a target cluster with `--shard=eu-cluster`. Create a ServiceAccount in the control plane with the required RBAC. Distribute the control plane kubeconfig to the agent.

3. **Configure the Pipeline:** Add `shard: eu-cluster` to the target environment. The translator adds the label to the PromotionStep template.

4. **Result:** The control plane controller skips PromotionSteps with `kardinal.io/shard=eu-cluster`. The agent picks them up. All other PromotionSteps continue to be reconciled by the control plane.

No downtime. No Pipeline CRD migration. The shard field is optional and additive.

## Edge Cases

| Case | Behavior |
|---|---|
| Agent goes down | PromotionSteps in its shard stay in current state. The Graph does not advance. When the agent restarts, it resumes from the last known step index (idempotent reconciliation). |
| Control plane goes down | Agents continue reconciling existing PromotionSteps (they have already been created). No new PromotionSteps are created (Graph controller is in the control plane). When the control plane restarts, Graph resumes and creates pending steps. |
| Shard reassignment (environment.shard changes) | The existing PromotionStep retains its original shard label. The old agent continues reconciling it. New Bundles will get PromotionSteps with the new shard. There is no "migration" of in-flight steps between agents. |
| Agent and controller both try to reconcile | If a PromotionStep has a shard label, the controller skips it. If it doesn't, the agent skips it. There is no overlap. In standalone mode, the controller's shard is empty, so it handles steps without a shard label and does not interfere with agents. |
| Agent can't reach control plane | Agent can't read or update PromotionSteps. It retries with exponential backoff. When connectivity restores, it catches up. |

## Unit Tests

1. Shard filtering: PromotionStep with `shard=eu` processed by agent with `--shard=eu`.
2. Shard filtering: PromotionStep with `shard=eu` skipped by agent with `--shard=us`.
3. Shard filtering: PromotionStep without shard processed by controller (standalone mode).
4. Shard filtering: PromotionStep with shard skipped by controller (standalone mode).
5. Label propagation: translator sets `kardinal.io/shard` on PromotionStep template.
6. Evidence copy: agent writes evidence to Bundle status in control plane.

## Integration Tests

7. Deploy controller + agent in separate namespaces (simulating separate clusters). Create a Pipeline with `shard: agent`. Verify the agent reconciles the sharded step and the controller reconciles unsharded steps.
