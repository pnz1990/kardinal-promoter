# Argo Rollouts Demo — standalone single-cluster example

This example demonstrates kardinal-promoter with [Argo Rollouts](https://argoproj.github.io/rollouts/) for canary delivery in a single cluster. Unlike the multi-cluster-fleet example (which uses shards), this example runs entirely in one cluster with three environments: `test` (Deployment), `uat` (ArgoCD Application), `prod` (Argo Rollouts canary).

## Architecture

```
CI creates Bundle
    ↓
kardinal-controller
    ↓ test:    kustomize-set-image → Deployment → resource adapter → Ready
    ↓ uat:     kustomize-set-image → ArgoCD syncs → argocd adapter → Healthy+Synced
    ↓ prod:    kustomize-set-image → open-pr → human merges
                ↓
           Rollout detects new image → starts canary steps
           10% → pause 5m → 30% → pause 5m → 60% → pause 5m → 100%
                ↓
           kardinal checks Rollout.status.phase
           phase=Progressing/Paused → Wait
           phase=Healthy → Verified ✅
           phase=Degraded → Failed → rollback PR opened
```

## How this differs from multi-cluster-fleet

| | argo-rollouts-demo | multi-cluster-fleet |
|---|---|---|
| Clusters | Single | Multiple (shards) |
| Strategy | Steps (setWeight + pause) | Steps (setWeight + pause) |
| Focus | Learning Argo Rollouts integration | Multi-cluster fan-out |
| ArgoCD needed | Optional (uat only) | Required (prod environments) |

## Prerequisites

- [Argo Rollouts](https://argoproj.github.io/rollouts/installation/) installed
  ```bash
  kubectl create namespace argo-rollouts
  kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/download/v1.7.1/install.yaml
  ```
- [ArgoCD](https://argo-cd.readthedocs.io/en/stable/getting_started/) installed (for the uat env)
- `kubectl` connected to your cluster

## Setup

```bash
# 1. Create namespaces
kubectl create namespace prod
kubectl create namespace uat

# 2. Apply Rollout and Services
kubectl apply -f examples/argo-rollouts-demo/rollout.yaml

# 3. Create GitHub token secret
kubectl create secret generic github-token \
  --from-literal=token=$GITHUB_TOKEN

# 4. Create ArgoCD Application for uat (optional — remove the uat env if not using ArgoCD)
#    The kardinal-demo overlays set namespace "default"; kustomize.namespace
#    moves the uat copy into the uat namespace.
kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argo-rollouts-demo-uat
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/pnz1990/kardinal-demo
    targetRevision: main
    path: environments/uat
    kustomize:
      namespace: uat
  destination:
    server: https://kubernetes.default.svc
    namespace: uat
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
EOF

# 5. Apply the Pipeline
kubectl apply -f examples/argo-rollouts-demo/pipeline.yaml

# Verify Rollout is initialized
kubectl argo rollouts get rollout argo-rollouts-demo -n prod
# NAME                   KIND     IMAGE                                    TAG       HASH      REPLICAS
# argo-rollouts-demo     Rollout  ghcr.io/pnz1990/kardinal-test-app  sha-...  xxxxx     3/3
```

## Trigger a Promotion

```bash
LATEST_SHA=$(gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]')
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}"

# Create bundle — starts promotion through test → uat → prod
kardinal create bundle argo-rollouts-demo --image $TEST_IMAGE

# Watch test and uat auto-promote
kardinal get pipelines

# Merge the prod PR when it opens, then watch the rollout
kubectl argo rollouts get rollout argo-rollouts-demo -n prod --watch
# Shows canary progression: 10% → 30% → 60% → 100%

# kardinal detects Rollout.status.phase=Healthy → marks prod Verified
kardinal get pipelines
```

## How the Argo Rollouts Health Adapter Works

The `argoRollouts` health adapter reads `Rollout.status.phase`:

| Rollout phase | kardinal verdict | Description |
|---|---|---|
| `Progressing` | Wait | Steps running / image rollout in progress |
| `Paused` | Wait | Manual pause or step pause — waiting |
| `Healthy` | **Healthy** | All replicas running new image, analysis passed |
| `Degraded` | **Unhealthy** | Rollout failed or analysis failed |

A `Degraded` phase triggers kardinal's failure path: the PromotionStep is marked failed, downstream environments are blocked, and a rollback PR is opened (if `onHealthFailure: rollback` is set on the environment).

**Configuration reference:**

```yaml
health:
  type: argoRollouts
  timeout: 30m        # must exceed total canary step duration (default: 10m)
```

The adapter looks for a Rollout named after the Pipeline (`argo-rollouts-demo`)
in a namespace named after the environment (`prod`). To check a different one,
set `health.argoRollouts.name` and `health.argoRollouts.namespace` on the
environment.

## Manual Canary Control

```bash
# Pause the canary manually (override kardinal schedule)
kubectl argo rollouts pause argo-rollouts-demo -n prod

# Resume
kubectl argo rollouts resume argo-rollouts-demo -n prod

# Promote immediately (skip remaining steps)
kubectl argo rollouts promote argo-rollouts-demo -n prod

# Abort (Rollout.status.phase → Degraded → kardinal opens rollback PR)
kubectl argo rollouts abort argo-rollouts-demo -n prod
```

## Validation

```bash
# Unit tests for the Argo Rollouts adapter
go test ./pkg/health/... -run TestArgoRollouts -v

# Full demo validation
bash scripts/demo-validate.sh
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Promotion stuck at `HealthChecking` | Rollout phase is `Paused` | Check step durations; use `kubectl argo rollouts resume` if manual pause |
| `Rollout not found` | Rollout CR not applied | `kubectl apply -f examples/argo-rollouts-demo/rollout.yaml` |
| Rollout immediately `Degraded` | readinessProbe failing | Check pod logs; verify `/health` endpoint |
| kardinal shows `Failed` | Rollout reached `Degraded` | `kubectl argo rollouts get rollout argo-rollouts-demo -n prod` |
