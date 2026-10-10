# Rendered Manifests Example

This example promotes with `layout: branch`: kardinal renders each environment's Kustomize
overlay at promotion time and commits the plain YAML to that environment's branch, which Argo CD
syncs. See [docs/rendered-manifests.md](../../docs/rendered-manifests.md).

## Repository structure

```
main              ← DRY source (spec.git.branch)
  base/
    deployment.yaml
    kustomization.yaml
  overlays/
    dev/kustomization.yaml
    staging/kustomization.yaml
    prod/kustomization.yaml

env/dev           ← rendered by kardinal: plain YAML, one file per object
env/staging
env/prod
```

## What happens during a promotion

```
git-clone           clones env/<env> (creating it the first time) and the DRY source
kustomize-set-image sets the Bundle's image in the DRY overlay (never committed)
render-manifests    renders the overlay in the controller, checks env/<env> for drift
git-commit          commits to env/<env> with Kardinal-Dry-Commit trailers
git-push            pushes env/<env> (dev, staging) or kardinal/<bundle>/prod
open-pr             prod: a PR into env/prod whose diff is the rendered YAML
wait-for-merge      prod: waits for the merge
health-check        the Argo CD Application that syncs env/<env>
```

## Usage

```bash
kubectl apply -f examples/rendered-manifests/application.yaml   # one Application per env branch
kubectl apply -f examples/rendered-manifests/pipeline.yaml

kardinal create bundle rendered-demo --image ghcr.io/myorg/app:v2.0.0
kardinal get steps rendered-demo
```

## Benefits

- PR reviewers see **rendered YAML diffs**, not Kustomize template changes
- Argo CD applies plain YAML (directory source) instead of running kustomize on every reconcile
- CODEOWNERS can name individual rendered files on `env/prod`
- `git log env/prod` is the history of what ran in production, with the DRY commit of each render
- A rollback re-renders the DRY commit of the release it restores
