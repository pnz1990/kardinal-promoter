# Example: Multi-Cluster Fleet Promotion

A multi-cluster promotion pipeline with parallel production fan-out and Argo Rollouts canary.
Equivalent to the AWS workshops:
- [Platform Engineering on EKS: Production Deploy with Kargo](https://catalog.workshops.aws/platform-engineering-on-eks/en-US/30-progressiveapplicationdelivery/40-production-deploy-kargo)
- [Fleet Management on Amazon EKS: Promote App to Prod Clusters](https://github.com/aws-samples/fleet-management-on-amazon-eks-workshop/tree/mainline/patterns/kro-eks-cluster-mgmt#promote-the-application-to-prod-clusters)

## What this example does

1. CI builds the `rollouts-demo` image and creates a Bundle.
2. kardinal-promoter promotes through: test (auto) --> pre-prod (PR) --> [prod-eu, prod-us] (PR, parallel).
3. Test and pre-prod use instant deploy. Prod regions use Argo Rollouts canary with ALB traffic routing.
4. PolicyGates enforce: no weekend deploys, staging soak time, and require pre-prod verification.
5. Argo CD hub-spoke manages Applications across 4 clusters.

## Pipeline topology

```
test (auto) --> pre-prod (pr-review) --> [no-weekend-deploys] --> prod-eu (pr-review, canary)
                                         [pre-prod-soak]      --> prod-us (pr-review, canary)
```

prod-eu and prod-us run in parallel after pre-prod is verified and all policy gates pass.

## Cluster layout

| Cluster | Role | What runs there |
|---|---|---|
| hub | Management | Argo CD, kardinal-promoter, Graph controller |
| test | Workload | rollouts-demo (test env) |
| pre-prod | Workload | rollouts-demo (pre-prod env) |
| prod-eu | Workload | rollouts-demo (prod EU, Argo Rollouts canary) |
| prod-us | Workload | rollouts-demo (prod US, Argo Rollouts canary) |

Argo CD in the hub cluster manages Applications for all 4 workload clusters.
kardinal-promoter reads Application health from the hub. No cross-cluster API calls.

## Prerequisites

- Hub cluster with kardinal-promoter, Graph controller, and Argo CD
- 4 workload clusters registered as Argo CD cluster targets
- Argo Rollouts installed in prod-eu and prod-us clusters
- AWS ALB Ingress Controller in prod clusters (for canary traffic routing)
- GitOps repo with Kustomize overlays:
  ```
  base/
    rollout.yaml
    service.yaml
    kustomization.yaml
  env/
    test/
      kustomization.yaml
    pre-prod/
      kustomization.yaml
    prod-eu/
      kustomization.yaml
    prod-us/
      kustomization.yaml
  ```

## Setup

### 1. Create Git credentials

```bash
kubectl create secret generic github-token \
  --namespace=default \
  --from-literal=token=$GITHUB_PAT
```

### 2. Create Argo CD Applications for each cluster

```bash
kubectl apply -f argocd-applications.yaml
```

### 3. Create org-level PolicyGates

```bash
kubectl apply -f policy-gates.yaml
```

### 4. Create the Pipeline

```bash
kubectl apply -f pipeline.yaml
```

### 5. Create a Bundle (from CI or manually)

```bash
kardinal create bundle rollouts-demo \
  --image ghcr.io/myorg/rollouts-demo:v2.0.0 \
  --commit def456 \
  --ci-run https://github.com/myorg/rollouts-demo/actions/runs/67890
```

## What happens next

1. Graph created with topology: test --> pre-prod --> [gates] --> [prod-eu, prod-us]
2. Test: image tag updated in `env/test/`, pushed directly. Argo CD syncs. Health verified.
3. Pre-prod: PR opened with promotion evidence. Human reviews, merges. Argo CD syncs. Health verified.
4. PolicyGates evaluated: no-weekend-deploys, pre-prod-soak (30m minimum).
5. When gates pass: prod-eu and prod-us PromotionSteps created in parallel.
6. For each prod region:
   - PR opened with promotion evidence (including pre-prod metrics).
   - Human reviews and merges.
   - Argo CD syncs the Rollout manifest.
   - Argo Rollouts executes canary: 20% --> 40% --> 60% --> 80% --> 100% with ALB.
   - kardinal-promoter watches Rollout.status.phase via argoRollouts health adapter.
   - When Rollout is Healthy, Bundle is Verified for that region.
7. Both regions verified: Bundle fully promoted.

## Observing the promotion

```bash
# Pipeline overview
kardinal get pipelines

# See all steps including parallel prod regions
kardinal get steps rollouts-demo

# Why is prod-eu waiting?
kardinal explain rollouts-demo --env prod-eu

# Watch canary progress (Phase 2 feature)
kardinal status rollouts-demo --env prod-eu

# See the full promotion history
kardinal history rollouts-demo
```

## Comparison: Kargo vs kardinal-promoter for this scenario

| Aspect | Kargo (workshop) | kardinal-promoter (this example) |
|---|---|---|
| CRDs to write | Project + Warehouse + PromotionTask + 4 Stages = 6 resources | Pipeline + 3 PolicyGates = 4 resources |
| Parallel prod regions | Separate Stage per region, manual ordering | `dependsOn: [pre-prod]` on both regions, Graph handles parallel execution |
| Promotion mechanism | PromotionTask steps (git-clone, kustomize-set-image, git-push, argocd-update) | Built-in: controller handles git + kustomize + PR automatically |
| Approval | Kargo UI or manual `kargo promote` | GitHub PR with promotion evidence (metrics, provenance, policy gates) |
| Policy governance | None (autoPromotionEnabled: true/false) | PolicyGate DAG nodes with CEL expressions |
| Canary | Argo Rollouts (same) | Argo Rollouts via delegation (same) |

## Files in this example

| File | What it is |
|---|---|
| `pipeline.yaml` | Pipeline CRD (4 environments, parallel prod fan-out, Argo Rollouts delegation) |
| `policy-gates.yaml` | Org-level PolicyGates (no-weekend-deploys, pre-prod-soak) |
| `bundle.yaml` | Sample Bundle for manual creation |
| `argocd-applications.yaml` | Argo CD ApplicationSet for 4 environments across 4 clusters |
