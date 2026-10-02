# Argo Rollouts Demo — standalone single-cluster example

This example demonstrates kardinal-promoter with [Argo Rollouts](https://argoproj.github.io/rollouts/) for canary delivery in a single cluster. Unlike the multi-cluster-fleet example (an Argo CD hub with workload clusters), this example runs entirely in one cluster with three environments: `test` (Deployment), `uat` (ArgoCD Application), `prod` (Argo Rollouts canary).

## Architecture

```
CI creates Bundle
    ↓
kardinal-controller
    ↓ test:    kustomize-set-image → ArgoCD syncs the Deployment → resource adapter → Ready
    ↓ uat:     kustomize-set-image → ArgoCD syncs → argocd adapter → Healthy+Synced
    ↓ prod:    kustomize-set-image → open-pr → human merges
                ↓
           ArgoCD syncs the new image into the Rollout → starts canary steps
           10% → pause 5m → 30% → pause 5m → 60% → pause 5m → 100%
                ↓
           kardinal checks the Rollout runs the Bundle image, then Rollout.status.phase
           phase=Progressing/Paused → Wait
           phase=Healthy → Verified ✅
           phase=Degraded → Unhealthy; still Degraded at health.timeout
                            → onHealthFailure: rollback → rollback PR opened
```

## How this differs from multi-cluster-fleet

| | argo-rollouts-demo | multi-cluster-fleet |
|---|---|---|
| Clusters | Single | Hub plus workload clusters |
| Strategy | Steps (setWeight + pause) | Steps (setWeight + pause) |
| Focus | Learning Argo Rollouts integration | Multi-cluster fan-out |
| ArgoCD needed | Required (syncs all three environments) | Required (prod environments) |

## Prerequisites

- A fork of pnz1990/kardinal-demo. Point `spec.git.url` in pipeline.yaml (and any Argo CD or Flux source) at the fork. The token needs write access to it.
- In the fork, replace `environments/prod/deployment.yaml` with the contents of
  `rollout.yaml` (the Rollout and its Services). kardinal changes the image in
  git; Argo CD applies it to the Rollout.
- [Argo Rollouts](https://argoproj.github.io/rollouts/installation/) installed
  ```bash
  kubectl create namespace argo-rollouts
  kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/download/v1.7.1/install.yaml
  ```
- [ArgoCD](https://argo-cd.readthedocs.io/en/stable/getting_started/) installed (it syncs test, uat and prod from the fork)
- `kubectl` connected to your cluster

## Setup

```bash
# 1. Create namespaces
kubectl create namespace test
kubectl create namespace uat
kubectl create namespace prod

# 2. Create GitHub token secret
kubectl create secret generic github-token \
  --namespace default \
  --from-literal=token=$GITHUB_TOKEN

# 3. Create one ArgoCD Application per environment. Set repoURL to your fork.
#    The kardinal-demo overlays set namespace "default"; kustomize.namespace
#    moves each copy into the environment's namespace. Argo CD syncs the
#    test Deployment, the uat Deployment and the prod Rollout from git.
for env in test uat prod; do
kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: argo-rollouts-demo-${env}
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/<you>/kardinal-demo
    targetRevision: main
    path: environments/${env}
    kustomize:
      namespace: ${env}
  destination:
    server: https://kubernetes.default.svc
    namespace: ${env}
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
EOF
done

# 4. Apply the Pipeline
kubectl apply -f examples/argo-rollouts-demo/pipeline.yaml

# Verify Rollout is initialized
kubectl argo rollouts get rollout argo-rollouts-demo -n prod
# Status:          ✔ Healthy
```

## Trigger a Promotion

The digest is pinned because the kardinal-demo overlays already run
`sha-9349a3f`: a Bundle with only the tag changes nothing and opens no PR.

```bash
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0"

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

The `argoRollouts` health adapter first checks that the Rollout runs the
promoted revision: its pod template has the Bundle's image, and Argo Rollouts
observed that spec (`status.observedGeneration`). Until then it waits, whatever
the phase says: right after the merge, the phase still describes the previous
release. Then it reads `Rollout.status.phase`:

| Rollout phase | kardinal verdict | Description |
|---|---|---|
| `Progressing` | Wait | Steps running / image rollout in progress |
| `Paused` | Wait | Manual pause or step pause — waiting |
| `Healthy` | **Healthy** | All replicas running new image (stable ReplicaSet is the current one), analysis passed |
| `Degraded` | **Unhealthy** | Rollout aborted or analysis failed |

A `Degraded` phase is unhealthy but not final: Argo Rollouts can still roll
forward, so kardinal keeps checking until `health.timeout` (30m here) and only
then applies `onHealthFailure`. This example sets `onHealthFailure: rollback`
on prod: kardinal creates a rollback Bundle for the previous release, the step
shows `RollingBack`, and because prod is `pr-review` the rollback opens a PR
for you to merge. Downstream environments are blocked. Without
`onHealthFailure` (the default is `none`) the step is just marked `Failed`.

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

# Abort (Rollout.status.phase → Degraded; at health.timeout kardinal opens a rollback PR)
kubectl argo rollouts abort argo-rollouts-demo -n prod
```

## Validation

```bash
# Unit tests for the Argo Rollouts adapter
go test ./pkg/health/... -run TestArgoRollouts -v

# Build and run the health adapter unit tests
bash scripts/demo-validate.sh
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Promotion stuck at `HealthChecking` | Rollout phase is `Paused` | Check step durations; use `kubectl argo rollouts resume` if manual pause |
| `Rollout not found` | Argo CD has not synced the Rollout | Check that `environments/prod/deployment.yaml` in your fork holds the Rollout, and that `argo-rollouts-demo-prod` is synced |
| Rollout immediately `Degraded` | readinessProbe failing | Check pod logs; verify `/health` endpoint |
| kardinal shows `RollingBack` | Rollout stayed `Degraded` until `health.timeout` | Merge the rollback PR; `kubectl argo rollouts get rollout argo-rollouts-demo -n prod` shows why it aborted |
