# Rendered Manifests Example

This example demonstrates the **rendered manifests** pattern with `layout: branch`.

## Repository Structure

```
source/           ← DRY source branch (Kustomize base + overlays)
  base/
    deployment.yaml
    kustomization.yaml
  overlays/
    dev/
      kustomization.yaml
    staging/
      kustomization.yaml
    prod/
      kustomization.yaml

env/dev           ← Rendered: plain YAML for dev (tracked by Argo CD)
env/staging       ← Rendered: plain YAML for staging
env/prod          ← Rendered: plain YAML for prod
```

## What Happens During Promotion

With `layout: branch`, the promotion sequence is:

```
git-clone         → checks out the source branch, creates the env branch
kustomize-set-image → updates image tag in the overlay kustomization.yaml
kustomize-build   → renders the overlay to plain YAML
git-commit        → commits rendered YAML to env/prod branch
git-push          → pushes the env branch
open-pr           → PR: env/prod-incoming → env/prod (for pr-review)
wait-for-merge    → waits for PR merge
health-check      → verifies Argo CD Application is Healthy+Synced
```

## Usage

```bash
kubectl apply -f examples/rendered-manifests/pipeline.yaml

kardinal create bundle rendered-demo --image ghcr.io/myorg/app:v2.0.0

# Watch the DAG
kardinal explain rendered-demo --env prod
# prod: kustomize-build Succeeded (rendered 1234 bytes)
# prod: WaitingForMerge PR #N (rendered branch diff visible in GitHub)
```

## Benefits

- PR reviewers see **rendered YAML diffs** — not Kustomize template changes
- Argo CD syncs from `env/<name>` branch — no template expansion at sync time
- `CODEOWNERS` can enforce review on rendered output
- Source branch is never modified by promotions — only `env/*` branches change
