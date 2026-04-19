# Flagger Demo — kardinal-promoter with Flagger Canary health adapter

This example demonstrates using kardinal-promoter with [Flagger](https://flagger.app/) for progressive delivery in production. kardinal promotes the new image (merges the PR); Flagger automatically runs a canary analysis; kardinal's Flagger health adapter waits for `Canary.status.phase == Succeeded` before marking the promotion as Verified.

## Architecture

```
CI creates Bundle
    ↓
kardinal-controller
    ↓ kustomize-set-image → git-commit → open-pr
Human merges prod PR
    ↓
Deployment updated (new image)
    ↓
Flagger detects image change → starts canary analysis
    ↓ routes 5%→10%→...→50% of traffic to canary
    ↓ checks metrics (error rate, latency) each minute
    ↓ if metrics OK: promotes canary (Canary.status.phase = Succeeded)
    ↓ if metrics fail: rolls back (Canary.status.phase = Failed)
kardinal checks Canary.status.phase
    ↓ Succeeded → promotion Verified
    ↓ Failed → kardinal marks promotion failed → opens rollback PR
```

## Prerequisites

- [Flagger](https://docs.flagger.app/install/flagger-install-on-kubernetes) installed
- A metrics provider (Prometheus recommended; required for `request-success-rate` metric)
- If using a service mesh: Istio, Linkerd, or Nginx ingress controller
- `kubectl` connected to your cluster

```bash
# Install Flagger (example with Prometheus)
helm repo add flagger https://flagger.app
helm upgrade -i flagger flagger/flagger \
  --namespace flagger-system \
  --set prometheus.install=true \
  --set meshProvider=kubernetes

# Verify Flagger is running
kubectl -n flagger-system get pods
```

## Setup

```bash
# 1. Create the namespace and GitHub token
kubectl create namespace prod
kubectl create secret generic github-token \
  --from-literal=token=$GITHUB_TOKEN

# 2. Deploy the initial version of the app (so Flagger can initialize)
kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kardinal-test-app
  namespace: prod
spec:
  replicas: 2
  selector:
    matchLabels:
      app: kardinal-test-app
  template:
    metadata:
      labels:
        app: kardinal-test-app
    spec:
      containers:
        - name: kardinal-test-app
          image: ghcr.io/pnz1990/kardinal-test-app:sha-abc1234
          ports:
            - containerPort: 8080
EOF

# 3. Apply the Flagger Canary
kubectl apply -f examples/flagger-demo/canary.yaml

# Wait for Flagger to initialize the canary
kubectl get canary -n prod -w
# NAME                READY   STATUS       WEIGHT   LASTTRANSITIONTIME
# kardinal-test-app   True    Initialized  0        2026-04-18T...

# 4. Apply the Pipeline
kubectl apply -f examples/flagger-demo/pipeline.yaml
```

## Trigger a Promotion

```bash
LATEST_SHA=$(gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]')
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}"

kardinal create bundle kardinal-test-app --image $TEST_IMAGE
kardinal get pipelines
# After test+uat auto-promote, a PR opens for prod.
# Merge the PR — Flagger starts the canary.

# Watch the canary
kubectl describe canary kardinal-test-app -n prod
# Events:
#   Normal  Synced  Starting canary analysis for ...
#   Normal  Synced  Advance kardinal-test-app.prod canary weight 5
#   ...
#   Normal  Synced  Copying kardinal-test-app.prod template spec ...
#   Normal  Synced  Promotion completed! Scaling down ...

# kardinal sees Canary.status.phase=Succeeded → marks prod as Verified
kardinal get pipelines
```

## How the Flagger Health Adapter Works

The `flagger` health adapter maps Flagger's `Canary.status.phase` to kardinal health states:

| Canary phase | kardinal verdict | Meaning |
|---|---|---|
| `Initializing` | Wait | Flagger setting up traffic split |
| `Initialized` | Wait | Ready for promotion trigger |
| `Waiting` | Wait | Waiting for new image |
| `Progressing` | Wait | Canary analysis running |
| `Promoting` | Wait | Promoting canary to primary |
| `Finalising` | Wait | Cleaning up canary |
| `Succeeded` | **Healthy** | Canary promoted — promotion Verified |
| `Failed` | **Unhealthy** | Canary rolled back — kardinal opens rollback PR |

**Configuration reference:**

```yaml
health:
  type: flagger
  flagger:
    name: my-app        # Canary CR name (default: pipeline name)
    namespace: prod     # namespace where Canary lives (default: environment name)
  timeout: 30m          # must exceed Flagger's canary analysis duration
```

## Without a Service Mesh (simplified metrics)

If you don't have Prometheus/service mesh metrics, remove the `metrics:` block from `canary.yaml`. Flagger will promote after `threshold` successful iterations with no failed metric checks.

## Validation

```bash
# Unit tests for the Flagger adapter
go test ./pkg/health/... -run TestFlagger -v

# Full demo validation
bash scripts/demo-validate.sh
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `Canary not found` | Canary CR not applied | `kubectl apply -f examples/flagger-demo/canary.yaml` |
| Canary stuck in `Progressing` | Metric check failing | `kubectl describe canary kardinal-test-app -n prod` → check events |
| `Failed` immediately | Deployment not responding | Check pod logs in `prod` namespace |
| Promotion never starts | PR not merged | Merge the prod PR to trigger Flagger |
