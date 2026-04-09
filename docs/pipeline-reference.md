# Pipeline Reference

The Pipeline CRD is the primary user-facing resource in kardinal-promoter. It defines the promotion path for one application: which Git repo contains the manifests, which environments exist, what order they promote in, and how each environment is configured.

## Full Spec

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: <string>                        # Pipeline name, typically matches the application
  namespace: <string>                   # Kubernetes namespace
spec:
  git:                                  # Git repository configuration
    url: <string>                       # GitOps repo URL (HTTPS)
    branch: <string>                    # Base branch (default: "main")
    layout: <string>                    # "directory" (default) or "branch"
    provider: <string>                  # "github" (Phase 1), "gitlab" (Phase 2)
    secretRef:
      name: <string>                    # Secret containing the Git token

  environments:                         # Ordered list of environments
    - name: <string>                    # Environment name (must be unique within the Pipeline)
      path: <string>                    # Path in the GitOps repo (default: "environments/<name>")
      dependsOn: [<string>, ...]        # Environments this one depends on (default: previous in list)
      update:
        strategy: <string>              # "kustomize" (default), "helm" (Phase 2), "replace" (Phase 3)
      approval: <string>               # "auto" (default) or "pr-review"
        # pr: <bool>                    # For approval: auto, set pr: true to create audit PRs
      health:
        type: <string>                  # "resource" (default), "argocd", "flux", "argoRollouts", "flagger"
        resource:                       # When type: resource
          kind: <string>                # Default: "Deployment"
          name: <string>                # Default: Pipeline metadata.name
          namespace: <string>           # Default: environment name
          condition: <string>           # Default: "Available"
        argocd:                         # When type: argocd
          name: <string>                # Argo CD Application name
          namespace: <string>           # Default: "argocd"
        flux:                           # When type: flux
          name: <string>                # Flux Kustomization name
          namespace: <string>           # Default: "flux-system"
        argoRollouts:                   # When type: argoRollouts (Phase 2)
          name: <string>                # Rollout name
          namespace: <string>           # Rollout namespace
        flagger:                        # When type: flagger (Phase 2)
          name: <string>                # Canary name
          namespace: <string>           # Canary namespace
        cluster: <string>              # kubeconfig Secret name for remote clusters
        timeout: <duration>             # Health check timeout (default: "10m")
      delivery:
        delegate: <string>              # "none" (default), "argoRollouts" (Phase 2), "flagger" (Phase 2)
      shard: <string>                   # Agent shard name for distributed mode (optional)
      steps:                            # Custom promotion step sequence (optional, overrides defaults)
        - uses: <string>                # Step name (built-in or custom)
          config: <map>                 # Step-specific configuration (for custom steps: url, timeout)

  historyLimit: <int>                   # Number of Bundles to retain (default: 20)
```

## Field Details

### spec.git

| Field | Required | Default | Description |
|---|---|---|---|
| `url` | Yes | | HTTPS URL of the GitOps repository |
| `branch` | No | `main` | Base branch for manifest reads |
| `layout` | No | `directory` | `directory`: environments as directories on one branch. `branch`: environments as separate branches. |
| `provider` | Yes | | `github` (Phase 1) or `gitlab` (Phase 2). Selects the SCM provider for PR creation. |
| `secretRef.name` | Yes | | Name of a Kubernetes Secret in the Pipeline's namespace containing a `token` field with a GitHub PAT or GitLab token. |

### spec.environments[]

| Field | Required | Default | Description |
|---|---|---|---|
| `name` | Yes | | Environment name. Must be unique within the Pipeline. Used in PolicyGate matching (`kardinal.io/applies-to` label). |
| `path` | No | `environments/<name>` | Directory (layout: directory) or branch (layout: branch) in the GitOps repo containing the environment's manifests. |
| `dependsOn` | No | Previous environment | List of environment names that must be Verified before this one starts. Default: sequential ordering (each depends on the previous). Specifying `dependsOn` enables parallel fan-out. |
| `update.strategy` | No | `kustomize` | How to update image references in manifests. `kustomize`: runs `kustomize edit set-image`. `helm`: patches a configurable path in `values.yaml` (Phase 2). |
| `approval` | No | `auto` | `auto`: push directly to the target branch, no PR. `pr-review`: open a PR with promotion evidence, wait for human merge. |
| `health.type` | No | auto-detected | Health verification adapter. Auto-detected on startup if omitted: checks for Argo CD Application CRD, then Flux Kustomization CRD, then falls back to Deployment condition. |
| `health.timeout` | No | `10m` | How long to wait for health verification before marking the step as Failed. |
| `health.cluster` | No | (local cluster) | Name of a Kubernetes Secret containing a kubeconfig for a remote cluster. Used for multi-cluster health verification. |
| `delivery.delegate` | No | `none` | Progressive delivery delegation. `argoRollouts`: watch Rollout status after promotion. `flagger`: watch Canary status. `none`: instant deploy (rolling update). |
| `shard` | No | (none) | Agent shard name for distributed mode. When set, only a kardinal-agent started with `--shard=<value>` will reconcile this environment's PromotionSteps. When omitted, the control plane controller handles the step. |
| `steps` | No | (inferred) | Custom promotion step sequence. When omitted, the default sequence is inferred from `update.strategy` and `approval`. When specified, overrides the default entirely. See [Promotion Steps](#promotion-steps). |

### spec.historyLimit

Number of Bundles (and their associated Graph objects) to retain per Pipeline. Older Bundles are garbage-collected. The Git PR history is permanent regardless of this setting.

Default: 20.

## Health Check Defaults

When the `health` field is omitted or partially specified, the controller applies defaults:

| Field | Default |
|---|---|
| `type` | Auto-detected (argocd if Application CRD exists, flux if Kustomization CRD exists, resource otherwise) |
| `resource.kind` | `Deployment` |
| `resource.name` | `Pipeline.metadata.name` |
| `resource.namespace` | Environment name |
| `resource.condition` | `Available` |
| `timeout` | `10m` |

This means a minimal environment definition with no `health` field works for the common case where the Deployment name matches the Pipeline name and the namespace matches the environment name.

## Environment Ordering

Environments promote sequentially by default. Each environment depends on the one above it in the list.

```yaml
environments:
  - name: dev         # depends on nothing (first)
  - name: staging     # depends on dev
  - name: prod        # depends on staging
```

For non-linear topologies, use `dependsOn` to express parallel fan-out:

```yaml
environments:
  - name: dev
  - name: staging
  - name: prod-us
    dependsOn: [staging]     # depends on staging, parallel with prod-eu
  - name: prod-eu
    dependsOn: [staging]     # depends on staging, parallel with prod-us
```

Both `prod-us` and `prod-eu` start after staging is Verified. They run concurrently.

For converging topologies (all regions must pass before a final step):

```yaml
environments:
  - name: staging
  - name: prod-us
    dependsOn: [staging]
  - name: prod-eu
    dependsOn: [staging]
  - name: post-deploy-validation
    dependsOn: [prod-us, prod-eu]    # waits for both regions
```

## Git Layout: Directory vs Branch

**Directory layout** (default, recommended): all environments share one branch. Each environment is a directory.

```
main branch:
  environments/
    dev/
      kustomization.yaml
    staging/
      kustomization.yaml
    prod/
      kustomization.yaml
```

Promotion updates the image tag in the target directory and pushes (auto) or opens a PR (pr-review) against the base branch.

**Branch layout**: each environment is a separate branch.

```
branches:
  env/dev
  env/staging
  env/prod
```

Promotion pushes to the target branch (auto) or opens a PR from a feature branch to the target branch (pr-review).

## Examples

### Minimal (3 lines per environment, all defaults)

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
    provider: github
    secretRef: { name: github-token }
  environments:
    - name: dev
    - name: staging
    - name: prod
      approval: pr-review
```

### Multi-cluster with Argo Rollouts

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
    provider: github
    secretRef: { name: github-token }
  environments:
    - name: test
      approval: auto
      health:
        type: argocd
        argocd: { name: my-app-test }
    - name: pre-prod
      approval: pr-review
      health:
        type: argocd
        argocd: { name: my-app-pre-prod }
    - name: prod-us
      dependsOn: [pre-prod]
      approval: pr-review
      health:
        type: argoRollouts
        argoRollouts: { name: my-app, namespace: prod }
      delivery:
        delegate: argoRollouts
    - name: prod-eu
      dependsOn: [pre-prod]
      approval: pr-review
      health:
        type: argoRollouts
        argoRollouts: { name: my-app, namespace: prod }
      delivery:
        delegate: argoRollouts
```

### Flux-based with remote clusters

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
    provider: github
    secretRef: { name: github-token }
  environments:
    - name: dev
      health:
        type: flux
        flux: { name: my-app-dev, namespace: flux-system }
    - name: prod
      approval: pr-review
      health:
        type: flux
        flux: { name: my-app-prod, namespace: flux-system }
        cluster: prod-cluster
```
