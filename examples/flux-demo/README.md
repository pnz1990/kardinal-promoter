# Flux Demo — kardinal-promoter with Flux health adapter

This example demonstrates using kardinal-promoter with [Flux](https://fluxcd.io/) as the GitOps engine. kardinal writes kustomize patches to the GitOps repo; Flux reconciles them; kardinal's Flux health adapter waits for the Kustomization's `Ready=True` condition before advancing the promotion.

## Architecture

```
CI creates Bundle
    ↓
kardinal-controller
    ↓ kustomize-set-image
    ↓ git-commit → git push to kardinal-demo repo
    ↓ open-pr (for prod)
Flux watches kardinal-demo repo
    ↓ reconciles Kustomization → applies to cluster
kardinal checks Kustomization.status.conditions[Ready]
    ↓ advances promotion when Ready=True and observedGeneration==generation
```

## Prerequisites

- [Flux](https://fluxcd.io/flux/installation/) installed in your cluster (`flux install`)
- `kubectl` connected to your cluster
- GitHub token with repo write access

## Setup

```bash
# 1. Create the GitHub token secret
kubectl create secret generic github-token \
  --from-literal=token=$GITHUB_TOKEN

# 2. Apply Flux GitRepository and Kustomizations
kubectl apply -f examples/flux-demo/flux-kustomizations.yaml

# 3. Apply the Pipeline
kubectl apply -f examples/flux-demo/pipeline.yaml

# 4. Verify Flux is reconciling
kubectl get kustomizations -n flux-system
# NAME                          READY   STATUS
# kardinal-test-app-test        True    Applied revision: main/...
# kardinal-test-app-uat         True    Applied revision: main/...
# kardinal-test-app-prod        True    Applied revision: main/...
```

## Trigger a Promotion

```bash
# Get the latest test app image
LATEST_SHA=$(gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]')
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}"

# Create a bundle
kardinal create bundle kardinal-test-app --image $TEST_IMAGE

# Watch the promotion
kardinal get pipelines
```

## How the Flux Health Adapter Works

The `flux` health adapter checks:

1. **`Ready` condition is `True`** on the Kustomization resource
2. **`observedGeneration == metadata.generation`** — the controller has reconciled the *current* spec, not a previous version

This two-part check prevents a false positive where Flux reconciled the previous manifest successfully but hasn't yet picked up the new commit kardinal pushed.

**Unhealthy states that cause the adapter to wait:**
- `Ready=False` (reconciliation failed or in progress)
- `Ready=True` but `observedGeneration` lags `generation` (Flux hasn't reconciled the new commit yet)
- Kustomization not found (Flux hasn't created it yet)

**Configuration reference:**

```yaml
health:
  type: flux
  flux:
    name: my-app-prod        # Kustomization name (required)
    namespace: flux-system   # default: "flux-system"
  timeout: 20m               # default: 10m
```

## Validation

```bash
# Run the unit tests for the Flux adapter
go test ./pkg/health/... -run TestFlux -v

# Run the full demo validation
bash scripts/demo-validate.sh
```

## Expected Output

```
test  | Flux Kustomization kardinal-test-app-test  | Ready=True, generation=2 matches
uat   | Flux Kustomization kardinal-test-app-uat   | Ready=True, generation=2 matches
prod  | PR #42 open — waiting for merge
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Promotion stuck at HealthChecking | Flux Kustomization `Ready=False` | `kubectl describe kustomization kardinal-test-app-test -n flux-system` |
| `observedGeneration` lag | Flux reconcile interval | Default 1m; reduce to `interval: 30s` for faster iteration |
| Kustomization not found | Flux CRD not installed | `flux install` or apply `flux-kustomizations.yaml` |
