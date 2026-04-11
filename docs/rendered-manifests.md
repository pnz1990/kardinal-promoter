# Rendered Manifests

The rendered manifests pattern is an advanced GitOps workflow in which Kustomize (or Helm)
templates are executed at promotion time and the rendered YAML is committed directly to Git.
Argo CD and Flux sync from the rendered output, not from the source templates.

This pattern is the standard for large Argo CD deployments (50+ applications) and is
used in production at organizations that need PR reviewers to see exact YAML diffs
rather than template changes.

## Why Render Manifests at Promotion Time

### Benefit 1: Visible PR diffs

In a standard GitOps setup, a PR that changes `image.tag` in `values.yaml` from
`v1.28.0` to `v1.29.0` is a 1-line diff. The reviewer has no idea what the rendered
output looks like without running `helm template` locally.

With rendered manifests, the same promotion PR shows the exact YAML change:

```diff
-        image: ghcr.io/myorg/my-app:1.28.0
+        image: ghcr.io/myorg/my-app:1.29.0
```

Every change — including environment variables, resource limits, Ingress rules, security
contexts — is visible as a plain diff.

### Benefit 2: GitOps agent performance

When an Argo CD Application tracks plain YAML, the agent only applies it. When it tracks
a Helm chart or Kustomize base, the agent runs `helm template` or `kustomize build` on
every reconciliation loop, on every cluster. At 200+ applications, this causes CPU spikes
and reconciliation lag. Pre-rendering eliminates this entirely.

### Benefit 3: CODEOWNERS enforcement on rendered output

CODEOWNERS rules can be placed on the rendered environment branches. A change to
a production Ingress requires review from the security team — even if the source
template change looked innocuous.

### Benefit 4: Auditability

The Git history of `env/prod` is the exact sequence of manifests that ran in production.
No reconstruction needed.

## Repository Structure

```
main branch (DRY source):
  base/
    deployment.yaml
    service.yaml
    kustomization.yaml
  environments/
    dev/
      kustomization.yaml     # patches over base
    staging/
      kustomization.yaml
    prod/
      kustomization.yaml

env/dev branch (rendered):
  deployment.yaml            # plain YAML, no Kustomize references
  service.yaml

env/staging branch (rendered):
  deployment.yaml
  service.yaml

env/prod branch (rendered):
  deployment.yaml
  service.yaml
```

kardinal-promoter reads from the source branch, runs `kustomize-set-image` then
`kustomize-build`, and commits the result to the environment branch via a PR (for
`pr-review` environments) or a direct push (for `auto` environments).

## Pipeline Configuration

Configure `layout: branch` on each environment that should use the rendered manifests pattern:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
    branch: source       # DRY source branch where overlays live
    provider: github
    secretRef: { name: github-token }

  environments:
    - name: dev
      path: overlays/dev          # overlay path within the source branch
      approval: auto
      layout: branch              # use rendered manifests branch layout
      health:
        type: resource

    - name: staging
      path: overlays/staging
      approval: auto
      layout: branch
      health:
        type: argocd

    - name: prod
      path: overlays/prod
      approval: pr-review
      layout: branch
      health:
        type: argocd
```

When `layout: branch` is set, the default step sequence becomes:

```
git-clone → kustomize-set-image → kustomize-build → git-commit → git-push
         → [open-pr → wait-for-merge]  # for pr-review environments
         → health-check
```

The `kustomize-build` step:
1. Runs `kustomize build <env.path>` in the cloned working directory
2. Writes the rendered YAML to `rendered-<env>.yaml` in the work directory
3. Stores the path in `Outputs["renderedManifestPath"]` for subsequent steps

The `git-commit` step picks up the rendered manifest file and commits it to the
environment branch (`env/<name>` by convention).

See `examples/rendered-manifests/` for a complete working example.

When the `branchPrefix` field is set, the default step sequence auto-infers the branch
names. Manual `steps` configuration is only needed for non-standard naming schemes.

## Argo CD Configuration

Argo CD Applications must track the rendered branch, not the source branch:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: my-app-prod
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/myorg/gitops-repo
    targetRevision: env/prod    # rendered branch, not main
    path: .                     # root of the rendered branch (all files are plain YAML)
  destination:
    server: https://kubernetes.default.svc
    namespace: my-app-prod
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
```

Because the rendered branch contains only plain YAML (no Kustomize base references or
Helm chart), Argo CD uses the `Directory` source type automatically. This is significantly
faster than Kustomize or Helm source types on large clusters.

## Branch Protection

The `env/prod` rendered branch should be protected:

1. Require pull request reviews before merging (CODEOWNERS or required reviewers)
2. Require status checks (Argo CD Application health, custom validation scripts)
3. Disallow direct pushes — only kardinal-promoter's PR branch merges are permitted

```yaml
# GitHub branch protection (in Terraform or GitHub UI)
branch_protection:
  pattern: "env/*"
  required_pull_request_reviews:
    required_approving_review_count: 1
  require_status_checks:
    strict: true
    contexts:
      - "argocd-health/my-app-prod"
```

CODEOWNERS on rendered branches is enforced by GitHub without any kardinal-promoter
configuration. The controller does not bypass CODEOWNERS.

## CODEOWNERS Integration

Place a `CODEOWNERS` file in the rendered branch:

```
# env/prod CODEOWNERS
/deployment.yaml    @myorg/security-reviewers
/ingress.yaml       @myorg/platform-team @myorg/security-reviewers
*                   @myorg/platform-team
```

Any promotion PR to `env/prod` that touches `deployment.yaml` will require approval from
`@myorg/security-reviewers`. kardinal-promoter waits for the PR to be approved and merged
before advancing the PromotionStep.

## Comparison: Directory Layout vs Branch Layout

| Aspect | Directory layout (default) | Branch layout (rendered) |
|---|---|---|
| PR diff | Template source diff (values.yaml) | Rendered YAML diff |
| Argo CD source type | Kustomize or Helm | Directory (plain YAML) |
| Argo CD performance | Template runs on every reconcile | No template run |
| CODEOWNERS granularity | Overlay directory | Individual YAML files |
| Git history | All envs on one branch | Each env has its own history |
| Complexity | Lower | Higher |
| Best for | Small teams, simple apps | Enterprise, security requirements |

## Anti-Pattern: Do Not Use `targetRevision` Updates

A common shortcut in some GitOps tools is to update the Argo CD Application's
`spec.source.targetRevision` field to point to a new commit SHA, bypassing Git entirely.
This is sometimes called "Pseudo-GitOps."

**Do not use this pattern.** It breaks the core GitOps guarantee that Git is the source
of truth. You cannot recover the cluster state from Git if `targetRevision` was never
committed. The Argo CD Application object becomes your source of truth instead of Git,
which defeats auditability and disaster recovery.

kardinal-promoter never mutates Argo CD Application objects directly. All promotions
write to Git first.

## Examples

See `examples/rendered-manifests/` for a complete working example including:
- GitOps repo structure with source and env branches
- Pipeline CRD with `renderManifests: true`
- Argo CD ApplicationSet targeting rendered branches
- CODEOWNERS file for the `env/prod` branch
