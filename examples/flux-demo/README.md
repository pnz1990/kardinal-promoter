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
# NAME             READY   STATUS
# flux-demo-test   True    Applied revision: main/...
# flux-demo-uat    True    Applied revision: main/...
# flux-demo-prod   True    Applied revision: main/...
```

## Trigger a Promotion

```bash
# Get the latest test app image
LATEST_SHA=$(gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]')
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}"

# Create a bundle
kardinal create bundle flux-demo --image $TEST_IMAGE

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
  timeout: 20m               # default: 10m
```

The adapter looks for a Kustomization named `<pipeline>-<env>` (`flux-demo-prod`)
in the `flux-system` namespace. To check a different one, set `health.flux.name`
and `health.flux.namespace` on the environment.

## Validation

```bash
# Run the unit tests for the Flux adapter
go test ./pkg/health/... -run TestFlux -v

# Run the full demo validation
bash scripts/demo-validate.sh
```

## Expected Output

```
test  | Flux Kustomization flux-demo-test  | Ready=True, generation=2 matches, lastAppliedRevision=1f0c2a9b7d3e
uat   | Flux Kustomization flux-demo-uat   | Ready=True, generation=2 matches, lastAppliedRevision=8b41d7e05c2a
prod  | PR #42 open — waiting for merge
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Promotion stuck at HealthChecking | Flux Kustomization `Ready=False` | `kubectl describe kustomization flux-demo-test -n flux-system` |
| `observedGeneration` lag | Flux reconcile interval | Default 1m; reduce to `interval: 30s` for faster iteration |
| Kustomization not found | Flux CRD not installed | `flux install` or apply `flux-kustomizations.yaml` |
