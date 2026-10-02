# Flagger Demo — kardinal-promoter with Flagger Canary health adapter

This example demonstrates using kardinal-promoter with [Flagger](https://flagger.app/) for progressive delivery in production. kardinal opens a PR with the new image; you merge it. Flagger automatically runs a canary analysis; kardinal's Flagger health adapter waits for `Canary.status.phase == Succeeded` before marking the promotion as Verified.

## Architecture

```
CI creates Bundle
    ↓
kardinal-controller
    ↓ kustomize-set-image → git-commit → open-pr
Human merges prod PR
    ↓
Argo CD syncs the new image into the Deployment
    ↓
Flagger detects image change → starts canary analysis
    ↓ routes 5%→10%→...→50% of traffic to canary
    ↓ checks metrics (error rate, latency) each minute
    ↓ if metrics OK: promotes canary (Canary.status.phase = Succeeded)
    ↓ if metrics fail: rolls back (Canary.status.phase = Failed)
kardinal checks Canary.status.phase
    ↓ Succeeded, primary on the Bundle image → promotion Verified
    ↓ Failed → onHealthFailure: rollback at once → opens rollback PR
```

## Prerequisites

- A fork of pnz1990/kardinal-demo. Point `spec.git.url` in pipeline.yaml (and any Argo CD or Flux source) at the fork. The token needs write access to it.
- [Argo CD](https://argo-cd.readthedocs.io/en/stable/getting_started/) installed (it syncs test, uat and prod from the fork)
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
  --namespace default \
  --from-literal=token=$GITHUB_TOKEN

# 2. Deploy test and uat. The quickstart's ApplicationSet syncs environments/test
#    and environments/uat into kardinal-test-app-test and kardinal-test-app-uat,
#    the namespaces pipeline.yaml checks. (It also syncs environments/prod into
#    kardinal-test-app-prod, which this example does not use.) Set its repoURL
#    to your fork.
kubectl apply -f examples/quickstart/argocd-applications.yaml

# 3. Sync environments/prod into namespace prod, so the merged PR changes the
#    image Flagger watches. Flagger scales the target Deployment to zero, so
#    Argo CD must ignore its replica count.
kubectl apply -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: flagger-demo-prod
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/<you>/kardinal-demo
    targetRevision: main
    path: environments/prod
    kustomize:
      namespace: prod
  destination:
    server: https://kubernetes.default.svc
    namespace: prod
  ignoreDifferences:
    - group: apps
      kind: Deployment
      jsonPointers:
        - /spec/replicas
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - RespectIgnoreDifferences=true
EOF

# 4. Apply the Flagger Canary
kubectl apply -f examples/flagger-demo/canary.yaml

# Wait for Flagger to initialize the canary
kubectl get canary -n prod -w
# NAME           READY   STATUS       WEIGHT   LASTTRANSITIONTIME
# flagger-demo   True    Initialized  0        2026-04-18T...

# 5. Apply the Pipeline
kubectl apply -f examples/flagger-demo/pipeline.yaml
```

## Trigger a Promotion

The digest is pinned because the kardinal-demo overlays already run
`sha-9349a3f`: a Bundle with only the tag changes nothing and opens no PR.

```bash
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0"

kardinal create bundle flagger-demo --image $TEST_IMAGE
kardinal get pipelines
# After test+uat auto-promote, a PR opens for prod.
# Merge the PR — Flagger starts the canary.

# Watch the canary
kubectl describe canary flagger-demo -n prod
# Events:
#   Normal  Synced  Starting canary analysis for ...
#   Normal  Synced  Advance flagger-demo.prod canary weight 5
#   ...
#   Normal  Synced  Copying kardinal-test-app.prod template spec ...
#   Normal  Synced  Promotion completed! Scaling down ...

# kardinal sees Canary.status.phase=Succeeded → marks prod as Verified
kardinal get pipelines
```

## How the Flagger Health Adapter Works

The `flagger` health adapter maps Flagger's `Canary.status.phase` to kardinal health states.
Flagger keeps the phase of its last analysis until it notices a new revision,
so the adapter first checks that the Canary's target Deployment runs the
Bundle's image (until then it waits), and a `Succeeded` or `Failed` phase from
an earlier release is a wait too:

| Canary phase | kardinal verdict | Meaning |
|---|---|---|
| `Initializing` | Wait | Flagger setting up traffic split |
| `Initialized` | Wait | Ready for promotion trigger |
| `Waiting` | Wait | Waiting for new image |
| `Progressing` | Wait | Canary analysis running |
| `Promoting` | Wait | Promoting canary to primary |
| `Finalising` | Wait | Cleaning up canary |
| `Succeeded` | **Healthy** | Canary promoted: the primary Deployment runs the Bundle's image — promotion Verified |
| `Failed` | **Failed at once** | Canary rolled back — `onHealthFailure` applies without waiting for `health.timeout` |

This example sets `onHealthFailure: rollback` on prod: on a failed analysis
kardinal creates a rollback Bundle for the previous release, the step shows
`RollingBack`, and because prod is `pr-review` the rollback opens a PR for you
to merge. Without `onHealthFailure` (the default is `none`) the step is just
marked `Failed`.

**Configuration reference:**

```yaml
health:
  type: flagger
  timeout: 30m          # must exceed Flagger's canary analysis duration
```

The adapter looks for a Canary named after the Pipeline (`flagger-demo`) in a
namespace named after the environment (`prod`). To check a different one, set
`health.flagger.name` and `health.flagger.namespace` on the environment. The
Canary's `targetRef` is the Deployment `kardinal-test-app`.

## Without a Service Mesh (simplified metrics)

If you don't have Prometheus/service mesh metrics, remove the `metrics:` block from `canary.yaml`. Flagger will promote after `threshold` successful iterations with no failed metric checks.

## Validation

```bash
# Unit tests for the Flagger adapter
go test ./pkg/health/... -run TestFlagger -v

# Build and run the health adapter unit tests
bash scripts/demo-validate.sh
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `Canary not found` | Canary CR not applied | `kubectl apply -f examples/flagger-demo/canary.yaml` |
| Canary stuck in `Progressing` | Metric check failing | `kubectl describe canary flagger-demo -n prod` → check events |
| `Failed` immediately | Deployment not responding | Check pod logs in `prod` namespace |
| Promotion never starts | PR not merged | Merge the prod PR to trigger Flagger |
