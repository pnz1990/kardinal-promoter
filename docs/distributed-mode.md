# Distributed Mode

kardinal-promoter supports a distributed deployment model where multiple controller instances
run in separate clusters, each responsible for a named _shard_ of PromotionSteps.

## When to Use Distributed Mode

Distributed mode is useful when:

- You have multiple Kubernetes clusters (e.g. prod-eu and prod-us) and want credentials to stay in each cluster
- You want to isolate blast radius between environments
- The single controller cannot reach all target clusters (e.g. behind firewalls)

In standalone mode (the default), a single controller instance processes all PromotionSteps.

## How Sharding Works

Each PromotionStep carries a `kardinal.io/shard` label. When a controller starts with
`--shard <name>`, it processes **only** the steps whose shard label matches. Steps for
other shards are skipped silently.

The Graph controller (which creates PromotionSteps) assigns the shard label based on the
`shard` field in the Pipeline environment spec:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: rollouts-demo
spec:
  environments:
    - name: prod-eu
      shard: cluster-eu       # ← this agent handles prod-eu steps
      git:
        url: https://github.com/myorg/gitops
        branch: main
      approval: pr-review
    - name: prod-us
      shard: cluster-us       # ← this agent handles prod-us steps
      git:
        url: https://github.com/myorg/gitops
        branch: main
      approval: pr-review
```

## Deployment

### Control plane (main cluster)

The main controller runs without a shard flag. It handles Bundle reconciliation, Pipeline
reconciliation, PolicyGate evaluation, and Graph generation. It does NOT handle PromotionStep
reconciliation when a shard is configured.

```bash
helm install kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
  --namespace kardinal-system --create-namespace \
  --set controller.githubToken=$GITHUB_PAT
```

### Shard agent (per remote cluster)

Each remote cluster runs a controller configured with its shard name:

```bash
helm install kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
  --namespace kardinal-system --create-namespace \
  --set controller.githubToken=$GITHUB_PAT \
  --set controller.shard=cluster-eu
```

Or via environment variable:

```bash
export KARDINAL_SHARD=cluster-eu
kardinal-controller --leader-elect=false
```

### RBAC and credential isolation

Each shard agent only needs RBAC to read/write PromotionSteps in its own cluster.
Git credentials (GitHub PAT, SSH key) stay in the shard cluster — the control plane
never sees them.

## Integration with ArgoCD Hub-Spoke

In a hub-spoke setup, a single ArgoCD installation manages multiple downstream clusters.
kardinal-promoter uses the same hub: the Pipeline controller reads ArgoCD Application
health from the hub cluster, while the shard agent handles Git operations for the
downstream cluster.

See `examples/multi-cluster-fleet/` for a complete example.

## Observability

When a controller is running in sharded mode, it logs a startup message:

```
{"level":"info","shard":"cluster-eu","msg":"controller started in distributed mode"}
```

Steps skipped due to shard mismatch are logged at debug level:

```
{"level":"debug","step":"step-prod-eu","step_shard":"cluster-eu","our_shard":"cluster-us","msg":"skipping step — shard mismatch"}
```
