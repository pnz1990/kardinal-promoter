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
    layout: <string>                    # "directory" (default); "branch" is not implemented (promotions fail)
    provider: <string>                  # Not read; the controller's --scm-provider flag selects the SCM
    secretRef:
      name: <string>                    # Secret containing the Git token

  environments:                         # Ordered list of environments
    - name: <string>                    # Environment name (must be unique within the Pipeline)
      path: <string>                    # Path in the GitOps repo (default: "environments/<name>")
      dependsOn: [<string>, ...]        # Environments this one depends on (default: previous in list)
      update:
        strategy: <string>              # "kustomize" (default), "helm" or "argocd"
      approval: <string>                # "auto" (default) or "pr-review"
      health:
        type: <string>                  # "resource" (default), "argocd", "flux", "argoRollouts", "flagger"
        resource:                       # When type: resource
          kind: <string>                # Only "Deployment" is supported
          name: <string>                # Default: Pipeline metadata.name
          namespace: <string>           # Default: environment name
          condition: <string>           # Default: "Available"
        argocd:                         # When type: argocd
          name: <string>                # Default: "<pipeline>-<environment>"
          namespace: <string>           # Default: "argocd"
        flux:                           # When type: flux
          name: <string>                # Default: "<pipeline>-<environment>"
          namespace: <string>           # Default: "flux-system"
        argoRollouts:                   # When type: argoRollouts
          name: <string>                # Default: Pipeline metadata.name
          namespace: <string>           # Default: environment name
        flagger:                        # When type: flagger
          name: <string>                # Default: Pipeline metadata.name
          namespace: <string>           # Default: environment name
        cluster: <string>               # Not supported: must be empty (see Health Adapters)
        timeout: <duration>             # Health check timeout (default: "10m")
      delivery:
        delegate: <string>              # "none" (default), "argoRollouts" (implemented), "flagger" (implemented)
      shard: <string>                   # Agent shard name for distributed mode (optional)
      steps:                            # Reserved, not implemented yet: a Pipeline that sets it is rejected
        - uses: <string>                #   (see docs/custom-steps.md)
      promotionTemplate:                # Reserved, not implemented yet: a Pipeline that sets it is rejected
        name: <string>
      waitForMergeTimeout: <duration>   # pr-review only: fail the step and close the PR after this (default: wait forever)
      stepTimeoutSeconds: <int>         # Per built-in step timeout in seconds, minimum 1 (default: none)

  historyLimit: <int>                   # Number of Bundles to retain (default: 50)
```

## Field Details

### spec.git

| Field | Required | Default | Description |
|---|---|---|---|
| `url` | Yes | | HTTPS URL of the GitOps repository |
| `branch` | No | `main` | Base branch for manifest reads |
| `layout` | No | `directory` | `directory`: environments as directories on one branch. `branch` (rendered manifests on per-environment branches) is **not implemented**: the `git-clone` step fails every promotion that uses it. See [Rendered Manifests](rendered-manifests.md). |
| `provider` | No | `github` | **Not read by the controller.** The SCM provider is chosen once per controller by `--scm-provider` (`github`, `gitlab`, `forgejo`, `gitea`, `bitbucket` or `azuredevops`); see [SCM Providers](scm-providers.md). The CRD accepts only `github` or `gitlab` here. Leave it unset. |
| `secretRef.name` | Yes | | Name of a Kubernetes Secret in the Pipeline's namespace containing a `token` field with a GitHub PAT or GitLab token. |
| `secretRef.namespace` | No | Pipeline's namespace | Must be empty or the Pipeline's own namespace. Any other namespace fails the PromotionStep without reading the Secret, so a Pipeline cannot use another namespace's credentials. The Pipeline's `Ready` condition is `False` with reason `ValidationFailed`, `kardinal validate` reports it when the file sets `metadata.namespace`, and the optional admission webhook rejects the Pipeline. |

### spec.environments[]

A Pipeline has 1 to 100 environments. The CRD rejects, at `kubectl apply` time:

- a name that is not a DNS label: lowercase letters, digits and `-`, starting and ending
  with a letter or digit, at most 63 characters. The name is used as a namespace
  (`resource.namespace` default, below) and in object names;
- two environments with the same name;
- a name that is reserved in kro Graph node IDs or CEL: `bundle`, `api-version`, `kind`,
  `metadata`, `namespace`, `spec`, `status`, `graph`, `graphengine`, `kro`, `each`,
  `item`, `items`, `object`, `self`, `this`, `context`, and CEL keywords such as `true`,
  `false`, `null`, `in`, `if`, `for`, `let`, `var`, `while`.

Duration fields (`health.timeout`, `waitForMergeTimeout`) must be Go durations such as
`30s`, `10m` or `1h30m`; a value like `15 minutes` is rejected.

| Field | Required | Default | Description |
|---|---|---|---|
| `name` | Yes | | Environment name. Must be unique within the Pipeline. Used in PolicyGate matching (`kardinal.io/applies-to` label). |
| `path` | No | `environments/<name>` | Directory in the GitOps repo containing the environment's manifests. It must be relative and stay inside the repository: absolute paths, `..` segments and symlinks that point outside the checkout fail the step. |
| `dependsOn` | No | Previous environment | List of environment names that must be Verified before this one starts. Default: sequential ordering (each depends on the previous). Specifying `dependsOn` enables parallel fan-out. |
| `wave` | No | 0 (sequential) | Assigns this environment to a numbered deployment wave (K-06). Environments with the same wave number are promoted in parallel. A wave depends on every environment of the next lower wave, and on the environment without a wave listed before it. Gaps in the numbers are allowed. Composable with `dependsOn`. See [Wave Topology](#wave-topology-k-06). |
| `update.strategy` | No | `kustomize` | How to update image references in manifests. `kustomize`: edits the `images:` list of the environment's `kustomization.yaml` the way `kustomize edit set image` does. `helm`: patches a configurable path in `values.yaml`. `argocd`: patches the Argo CD Application's `spec.source.helm.valuesObject` directly, with no Git commit or PR (`approval: pr-review` fails the step); see [Argo CD native promotion](argocd-native-promotion.md). |
| `approval` | No | `auto` | `auto`: push directly to the target branch, no PR. `pr-review`: open a PR with promotion evidence, wait for human merge. |
| `health.type` | No | `resource` | Health verification adapter: `resource`, `argocd`, `flux`, `argoRollouts` or `flagger`. `delivery.delegate`, when set, takes precedence. There is no auto-detection. The step is Verified only when the adapter sees the promoted revision (commit or Bundle images) healthy. See [Health Adapters](health-adapters.md). |
| `health.resource`, `health.argocd`, `health.flux`, `health.argoRollouts`, `health.flagger` | No | see [Health Check Defaults](#health-check-defaults) | Name and namespace of the object the adapter checks. `health.resource.kind` must be `Deployment`. |
| `health.timeout` | No | `10m` | Maximum time from the start of health checking to the first healthy check. When it expires, it counts as a health failure and applies `onHealthFailure`. It does not cut a running `bake` window short. |
| `health.cluster` | No | (must be empty) | **Not supported.** Remote-cluster health checks are not implemented, and a non-empty value fails the PromotionStep. For a workload in another cluster, check its Argo CD Application in the controller's cluster (`type: argocd`). |
| `health.labelSelector` | No | (none) | `health.type=resource` only. When set, **every** Deployment in the namespace that matches these labels must pass the health check. No match is unhealthy. Example: `{"app": "nginx", "kardinal.io/pipeline": "nginx-demo"}`. When unset, a single Deployment named after the Pipeline is checked. Ignored for `argocd`, `flux`, `argoRollouts`, and `flagger`. |
| `delivery.delegate` | No | `none` | Progressive delivery delegation. `argoRollouts`: watch Argo Rollouts Rollout status after promotion. `flagger`: watch Flagger Canary status. `none`: instant deploy (rolling update). |
| `shard` | No | (none) | Agent shard name for distributed mode. When set, only a kardinal-agent started with `--shard=<value>` reconciles this environment's PromotionSteps, and the control plane controller skips them. When omitted, the control plane controller handles the step. |
| `steps` | No | (inferred) | **Not implemented yet.** Reserved for a custom step sequence. The controller always runs the default sequence, which it infers from `update.strategy` and `approval`. A Pipeline that sets `steps` is rejected: `kardinal validate` reports it, and its Bundles fail with a message naming the environment. See [Custom Steps](custom-steps.md). |
| `promotionTemplate` | No | (none) | **Not implemented yet.** Reserved for a shared step sequence. It is rejected the same way as `steps`. |
| `waitForMergeTimeout` | No | (none) | `pr-review` only. How long the step may wait for its PR to merge, as a Go duration (`24h`, `72h`). When it expires, the step is marked `Failed` and the controller closes the PR, so a late merge cannot deliver the change. Unset or `0` waits forever. |
| `stepTimeoutSeconds` | No | (none) | Maximum seconds one built-in step (`git-clone`, `kustomize-set-image`, `open-pr`, ...) may run. The step is cancelled and the error is handled like any other step error: a retryable error is retried with backoff, then the PromotionStep is marked `Failed`. Minimum 1. Unset means no per-step timeout. |
| `bake.minutes` | No | (none) | Contiguous-healthy soak window in minutes (K-01). When set, the step must observe healthy deployment status *continuously* for this many minutes before transitioning to Verified. A health alarm resets the timer. |
| `bake.policy` | No | `reset-on-alarm` | What to do when health fails during the bake window. `reset-on-alarm`: reset the elapsed timer to 0, stay in HealthChecking. `fail-on-alarm`: immediately apply `onHealthFailure` policy. |
| `onHealthFailure` | No | `none` | What to do when `health.timeout` expires without a Healthy result, when the adapter reports a terminal failure (Deployment `ProgressDeadlineExceeded`, Flagger `Failed`), or when health fails during bake with `policy: fail-on-alarm` (K-03). `none`: step → Failed (default behavior). `abort`: step → AbortedByAlarm; requires human intervention. `rollback`: create a rollback Bundle with the artifacts of the Bundle verified before the failing one in this environment; step → RollingBack, or AbortedByAlarm when there is nothing safe to roll back to (a step of a rollback Bundle → AbortedByAlarm instead, so rollbacks do not chain). See [Automatic Rollback](rollback.md#automatic-rollback). |
| `regions` | No | (none) | **Not implemented** (#612). With two or more regions the translator stamps out one PromotionStep per region, but every region would edit the same path and push the same branch, so those PromotionSteps fail with `environments[].regions fan-out is not implemented`. Declare one environment per region instead (for example `prod-us` and `prod-eu`, with `dependsOn` or `wave`). With zero or one region the field has no effect. |

**Reserved fields.** `steps`, `promotionTemplate`, `autoRollback`, `regions` with two or
more entries, `layout: branch` (on `spec.git` or an environment), `health.cluster` and a
`health.resource.kind` other than `Deployment` are not implemented. A Bundle fails when it
reaches an environment that uses one (`steps` and `promotionTemplate` fail it when its Graph
is built; a `health.resource.kind` fails the step after the change merged, during the
health check). `kardinal validate` reports each of them, and the controller sets the
Pipeline's `Ready` condition to `False` with reason `NotImplemented` and the same messages
(`kubectl get pipeline <name> -o jsonpath='{.status.conditions}'`). The optional admission
webhook admits the Pipeline with a warning per field. The API server rejects
`autoRollback` outright.

### spec.historyLimit

Number of finished Bundles (Verified, Failed or Superseded) to retain per Pipeline. Older ones are garbage-collected, oldest first, when a new Bundle is created. `kardinal rollback` can only target a retained Bundle. The Git PR history is permanent regardless of this setting.

Default: 50.

### spec.paused

When `true`, no PromotionStep of the Pipeline leaves `Pending`, and a step in `Promoting` holds before its next git step. Steps waiting for a PR merge or running a health check finish. The controller keeps a freeze PolicyGate named `freeze-<pipeline>` while the Pipeline is paused. `kardinal pause` / `kardinal resume` and the UI set this field. See [Pause and Resume](rollback.md#pause-and-resume).

Default: `false`.

### spec.maxConcurrentPromotions

Maximum number of this Pipeline's Bundles in the `Promoting` phase at once. A Bundle over the cap stays `Available` with the `Ready` condition reason `WaitingForSlot`, and starts when a promoting Bundle becomes Verified, Failed or Superseded. `0` means no cap.

Default: `0`.

## Health Check Defaults

When the `health` field is omitted or partially specified, the controller applies defaults:

| Field | Default |
|---|---|
| `type` | `resource` (or `delivery.delegate` when set) |
| `resource.kind` | `Deployment` (the only supported kind) |
| `resource.name` | `Pipeline.metadata.name` |
| `resource.namespace` | Environment name |
| `resource.condition` | `Available` |
| `argocd.name` / `argocd.namespace` | `<pipeline>-<environment>` / `argocd` |
| `flux.name` / `flux.namespace` | `<pipeline>-<environment>` / `flux-system` |
| `argoRollouts.name` / `argoRollouts.namespace` | `Pipeline.metadata.name` / environment name |
| `flagger.name` / `flagger.namespace` | `Pipeline.metadata.name` / environment name |
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

### Wave Topology (K-06)

For multi-region deployments with many parallel environments, `wave:` is syntactic sugar for `dependsOn`. Environments with the same wave number are promoted in parallel. Each wave environment automatically depends on **all** environments of the next lower wave.

```yaml
environments:
  - name: test        # no wave — sequential (depends on nothing)
  - name: staging     # no wave — sequential (depends on test)

  # Wave 1: prod-eu and prod-us start simultaneously after staging
  - name: prod-eu
    wave: 1
    approval: pr-review
    bake:
      minutes: 720

  - name: prod-us
    wave: 1
    approval: pr-review
    bake:
      minutes: 720

  # Wave 2: prod-ap starts after BOTH prod-eu AND prod-us reach Verified
  - name: prod-ap
    wave: 2
    approval: pr-review
    bake:
      minutes: 480
```

The rules, in full:

- **Previous wave.** A wave environment depends on every environment of the next lower wave that exists. Numbers need not be consecutive: with waves 10, 20 and 30, wave 20 follows wave 10. A gap never makes a wave start on its own.
- **Environments before a wave.** Without `dependsOn`, the environments of a wave also depend on the last environment without a wave listed before the wave's first environment. In the example, that is why wave 1 waits for `staging`.
- **Environments after a wave.** Without `dependsOn`, an environment without a wave depends on the environment listed before it. If that environment is in a wave, it depends on every environment of that wave.
- **`dependsOn`.** Explicit `dependsOn` entries are added to the previous-wave edges; they replace only the list-order edges.

Only the first environment in the list is a root, unless `dependsOn` says otherwise.

List the waves in ascending order. If a higher wave comes before a lower one with an environment without a wave between them (`test`, `a` in wave 2, `staging`, `b` in wave 1), the list-order edges and the wave edges form a cycle. The Pipeline is then rejected, and the error names each edge in the cycle.

See `examples/wave-topology/pipeline.yaml` for a complete example.

## Git Layout: Directory vs Branch

**Directory layout** (default, recommended for small teams): all environments share one branch. Each environment is a directory.

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

**Branch layout** (`layout: branch`) is **not implemented**. It is meant for the rendered
manifests pattern, where DRY Kustomize source lives on one branch and rendered plain YAML
lives on per-environment branches (`env/<name>`). Today the `git-clone` step fails every
promotion whose Pipeline or environment sets `layout: branch`, before it changes anything.
`kardinal validate` reports it, and the Pipeline is `Ready=False` with reason `NotImplemented`.
`sourceBranch`, `branchPrefix` and `renderManifests` are not Pipeline fields.

See [Rendered Manifests](rendered-manifests.md) for the planned design.

## Integration Test Step (K-07)

> **Not reachable yet.** A step outside the default sequence runs only when it is listed in `spec.environments[].steps`, and `steps` is not implemented yet. A Pipeline that sets it is rejected (see [Custom Steps](custom-steps.md)). This section describes the step for when `steps` ships.

The `integration-test` built-in step creates a Kubernetes Job in the target environment namespace, waits for it to complete, and writes the result to the step output accumulator. This is the most powerful quality gate after bake time — running real tests against the actual deployed service.

### Configuration

```yaml
steps:
  - uses: integration-test
```

The step reads its config from `PromotionStep.spec.inputs`:

| Input key | Required | Default | Description |
|---|---|---|---|
| `integration_test.image` | Yes | | Container image to run (e.g., `ghcr.io/myorg/integration-tests:latest`) |
| `integration_test.command` | No | container default | Space-separated command and args (e.g., `./run-tests.sh --env staging`) |
| `integration_test.timeout` | No | `30m` | Maximum time to wait for Job completion (Go duration, e.g., `10m`, `1h`) |

### Behavior

1. **First call**: creates a `batch/v1 Job` in the environment namespace and returns `Pending` (reconciler requeues in 15s).
2. **Subsequent calls**: re-checks Job status. Returns `Pending` while running, `Success` when Job succeeds, `Failed` when Job fails.
3. **Timeout**: if `integration_test.timeout` elapses, the Job is deleted and the step returns `Failed`.
4. **Idempotent**: multiple reconcile iterations never create duplicate Jobs (deterministic Job name from bundle+env).
5. **Cleanup**: Jobs use `ttlSecondsAfterFinished: 3600` so they self-delete after 1 hour.
6. **RBAC**: the controller needs `create` and `delete` on `batch` Jobs in the environment namespace. The Helm chart grants this with `--set rbac.integrationTestJobs=true`.

### Outputs

On success, the step populates:
- `integration_test.result`: `"passed"`
- `integration_test.job`: the Job name
- `integration_test.elapsed`: time taken (e.g., `"2m34s"`)

On failure:
- `integration_test.result`: `"failed"`
- `integration_test.job`: the Job name

### Example

```yaml
environments:
  - name: test
    steps:
      - uses: git-clone
      - uses: kustomize-set-image
      - uses: git-commit
      - uses: git-push
      - uses: health-check
      - uses: integration-test   # runs after health check passes
```

`examples/integration-test/pipeline.yaml` shows the environment layout; its `steps` block is commented out until the field is implemented.

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

The `argocd` checks work for any destination cluster because the Applications live in the controller's cluster. The `argoRollouts` checks read the Rollout in the controller's cluster, so this layout suits Rollouts that the controller's cluster runs (see [Remote Clusters](health-adapters.md#remote-clusters)).

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

### Remote prod cluster through an Argo CD hub

Health adapters read objects in the controller's own cluster, and `health.cluster` is not supported. To verify a workload in another cluster, let an Argo CD instance in the controller's cluster manage it and check its Application there: the adapter confirms the Application synced the promoted commit and is Healthy. A Flux Kustomization, Rollout, Canary or Deployment in another cluster cannot be checked. See [Health Adapters](health-adapters.md#remote-clusters).

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
        type: argocd
        argocd: { name: my-app-prod, namespace: argocd }   # Application in the hub, destination: the prod cluster
```

### Rendered manifests (branch layout with kustomize-build)

Not implemented yet: `layout: branch` fails the promotion. See
[Rendered Manifests](rendered-manifests.md) for the planned design.
