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
    provider: <string>                  # Deprecated and ignored; the controller's --scm-provider flag selects the SCM
    secretRef:
      name: <string>                    # Secret containing the Git token
      namespace: <string>               # Must be empty or the Pipeline's namespace

  environments:                         # Ordered list of environments
    - name: <string>                    # Environment name (must be unique within the Pipeline)
      path: <string>                    # Path in the GitOps repo (default: "environments/<name>")
      dependsOn: [<string>, ...]        # Environments this one depends on (default: previous in list)
      wave: <int>                       # Deployment wave, minimum 1 (default: none)
      update:
        strategy: <string>              # "kustomize" (default), "helm" or "argocd"
        helm:                           # When strategy: helm
          imagePathTemplate: <string>   # Dot path of the image tag (default: ".image.tag")
          valuesFile: <string>          # Relative to path (default: "values.yaml")
        argocd:                         # When strategy: argocd
          application: <string>         # Argo CD Application to patch (required)
          namespace: <string>           # Default: "argocd"
          imageKey: <string>            # Key in spec.source.helm.valuesObject (default: "image.tag")
      approval: <string>                # "auto" (default) or "pr-review"
      health:
        type: <string>                  # "resource" (default), "argocd", "flux", "argoRollouts", "flagger"
        labelSelector: {<key>: <value>} # type: resource only: check every matching Deployment
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
        cluster: <string>               # Deprecated, not supported: must be empty (see Health Adapters)
        timeout: <duration>             # Health check timeout (default: "10m")
      bake:
        minutes: <int>                  # Contiguous healthy minutes before Verified, minimum 1
        policy: <string>                # "reset-on-alarm" (default) or "fail-on-alarm"
        maxDuration: <duration>         # Deadline for one full window (default: minutes + health.timeout)
      onHealthFailure: <string>         # "none" (default), "abort" or "rollback"
      delivery:
        delegate: <string>              # "none" (default), "argoRollouts" (implemented), "flagger" (implemented)
      layout: <string>                  # "directory" (default); "branch" is not implemented (promotions fail)
      shard: <string>                   # Deprecated, not supported: must be empty (distributed mode was removed)
      regions: [<string>, ...]          # Deprecated, not supported: declare one environment per region
      steps:                            # Deprecated, not supported: the API server rejects it
        - uses: <string>                #   (see Promotion Steps below)
      promotionTemplate:                # Deprecated, not supported: the API server rejects it
        name: <string>
      waitForMergeTimeout: <duration>   # pr-review only: fail the step and close the PR after this (default: wait forever)
      stepTimeoutSeconds: <int>         # Per built-in step timeout in seconds, minimum 1 (default: none)

  paused: <bool>                        # Hold every promotion of the Pipeline (default: false)
  maxConcurrentPromotions: <int>        # Bundles promoting at once; 0 is no cap (default: 0)
  policyNamespaces: [<string>, ...]     # Extra namespaces to read PolicyGates from (default: none)
  historyLimit: <int>                   # Number of Bundles to retain (default: 50)
```

## Field Details

### spec.git

| Field | Required | Default | Description |
|---|---|---|---|
| `url` | Yes | | HTTPS URL of the GitOps repository |
| `branch` | No | `main` | Base branch: `git-clone` checks it out, `approval: auto` pushes to it, and `pr-review` PRs target it. The API server sets `main` when the field is omitted, and the controller also reads an empty value as `main`. |
| `layout` | No | `directory` | `directory`: environments as directories on one branch. `branch` (rendered manifests on per-environment branches) is **not implemented**: the `git-clone` step fails every promotion that uses it. See [Rendered Manifests](rendered-manifests.md). |
| `provider` | No | (none) | **Not read by the controller.** The SCM provider is the controller's `--scm-provider` (`github`, `gitlab`, `forgejo`, `gitea`, `bitbucket` or `azuredevops`), or the one `providerRef` names; see [SCM Providers](scm-providers.md). The CRD accepts only `github` or `gitlab` here. Leave it unset. |
| `secretRef.name` | No | | `secretRef` is optional; when it is set, `name` must be too. Name of a Kubernetes Secret in the Pipeline's namespace containing a `token` field with a GitHub PAT or GitLab token. Needed when the HTTPS remote refuses git without a token (every push to a hosted provider, and the clone of a private repository); not needed for an ssh remote or a URL that carries its credentials. When it is not set, or the Secret does not exist, and the HTTPS remote refuses `git-clone` or `git-push` without a token, the step retries until the Secret exists; the step message says what is missing (see [Troubleshooting](troubleshooting.md#symptom-authentication-required-with-git-secret-not-found-or-specgitsecretref-is-not-set)). |
| `secretRef.namespace` | No | Pipeline's namespace | Must be empty or the Pipeline's own namespace. Any other namespace fails the PromotionStep without reading the Secret, so a Pipeline cannot use another namespace's credentials. The Pipeline's `Ready` condition is `False` with reason `ValidationFailed`, and `kardinal validate` reports it when the file sets `metadata.namespace`. |
| `providerRef.name` | No | | Name of the [ScmProvider or ClusterScmProvider](scm-providers.md#several-scm-providers-scmprovider-and-clusterscmprovider) whose API and token this Pipeline's PRs use. When `providerRef` is not set, the controller's `--scm-provider` is used. The provider is resolved when a Bundle's Graph is built. If it is missing, does not allow the Pipeline's namespace (`allowedNamespaces` of a ClusterScmProvider), or does not allow the repository (`allowedRepositories`), the Bundle waits with the reason. |
| `providerRef.kind` | No | `ScmProvider` | `ScmProvider`, in the Pipeline's namespace, or `ClusterScmProvider`. |

### spec.environments[]

A Pipeline has 1 to 100 environments. The CRD rejects, at `kubectl apply` time:

- a name that is not a DNS label: lowercase letters, digits and `-`, starting and ending
  with a letter or digit, at most 63 characters. The name is used as a namespace
  (`resource.namespace` default, below) and in object names;
- two environments with the same name;
- a name that is reserved in kro Graph node IDs or CEL: `bundle`, `time`, `api-version`, `kind`,
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
| `wave` | No | (none) | Assigns this environment to a numbered deployment wave (K-06). Minimum 1. Environments with the same wave number are promoted in parallel. A wave depends on every environment of the next lower wave, and on the environment without a wave listed before it. Gaps in the numbers are allowed. Composable with `dependsOn`. See [Wave Topology](#wave-topology-k-06). |
| `update.strategy` | No | `kustomize` | How to update image references in manifests. `kustomize`: edits the `images:` list of the environment's `kustomization.yaml` the way `kustomize edit set image` does. `helm`: patches the image tag at `update.helm.imagePathTemplate` in `update.helm.valuesFile`; one image per Bundle, so use one Bundle per chart image, or kustomize. `argocd`: patches the Argo CD Application's `spec.source.helm.valuesObject` directly, with no Git commit or PR. The API server rejects `argocd` with `approval: pr-review`, and a config or mixed Bundle fails before its first environment when any environment it promotes uses `argocd`; see [Argo CD native promotion](argocd-native-promotion.md). |
| `update.helm.imagePathTemplate` | No | `.image.tag` | `helm` only. Dot path of the image tag in the values file. |
| `update.helm.valuesFile` | No | `values.yaml` | `helm` only. Values file to patch, relative to the environment `path`. |
| `approval` | No | `auto` | `auto`: push directly to the target branch, no PR. `pr-review`: open a PR with promotion evidence, wait for human merge. The step list is fixed when an environment's step starts: an edit applies to steps that start after it, so an environment already promoting finishes with the approval it started with and uses the new one from the next Bundle. A step that started as `auto` still pushes straight to the target branch after an edit to `pr-review`. The Bundle in flight still finishes: its Graph turns Ready once its steps are Verified and its gates pass, whether or not they opened a PR. |
| `health.type` | No | `resource` | Health verification adapter: `resource`, `argocd`, `flux`, `argoRollouts` or `flagger`. `delivery.delegate`, when set, takes precedence. There is no auto-detection. The step is Verified only when the adapter sees the promoted revision (commit or Bundle images) healthy. See [Health Adapters](health-adapters.md). |
| `health.resource`, `health.argocd`, `health.flux`, `health.argoRollouts`, `health.flagger` | No | see [Health Check Defaults](#health-check-defaults) | Name and namespace of the object the adapter checks. `health.resource.kind` must be `Deployment`. |
| `health.timeout` | No | `10m` | Maximum time from the start of health checking to the first healthy check, and from the moment a `bake` window stops to the next healthy check. When it expires, it counts as a health failure and applies `onHealthFailure`. It does not cut a running `bake` window short. |
| `health.cluster` | No | (must be empty) | **Deprecated, not supported.** kardinal checks health only in the cluster it runs in. A non-empty value sets the Pipeline `Ready=False` and fails the PromotionStep. For a workload in another cluster, check its Argo CD Application (`type: argocd`) or a Flux Kustomization that targets the cluster (`type: flux`) in the hub; see [Remote Clusters](health-adapters.md#remote-clusters). |
| `health.labelSelector` | No | (none) | `health.type=resource` only. When set, **every** Deployment in the namespace that matches these labels must pass the health check. No match is unhealthy. Example: `{"app": "nginx", "kardinal.io/pipeline": "nginx-demo"}`. When unset, a single Deployment named after the Pipeline is checked. Ignored for `argocd`, `flux`, `argoRollouts`, and `flagger`. |
| `delivery.delegate` | No | `none` | Progressive delivery delegation. `argoRollouts`: watch Argo Rollouts Rollout status after promotion. `flagger`: watch Flagger Canary status. `none`: instant deploy (rolling update). |
| `shard` | No | (must be empty) | **Deprecated, not supported.** Distributed mode was removed. A non-empty value sets the Pipeline `Ready=False` (reason `NotImplemented`), `kardinal validate` fails, and every PromotionStep of the environment fails with `shard is not supported`. Remove it; the controller reconciles every environment. See [Multi-Cluster](distributed-mode.md). |
| `steps` | No | (none) | **Deprecated, not supported.** kardinal has no custom step engine: the controller always runs the sequence it infers from the Bundle type, `update.strategy`, `approval` and `layout`. The API server rejects a Pipeline that sets `steps` (an empty list is accepted). See [Promotion Steps](#promotion-steps). |
| `promotionTemplate` | No | (none) | **Deprecated, not supported.** The `PromotionTemplate` CRD was removed. The API server rejects a Pipeline that sets `promotionTemplate`. |
| `waitForMergeTimeout` | No | (none) | `pr-review` only. How long the step may wait for its PR to merge, as a Go duration (`24h`, `72h`). When it expires, the step is marked `Failed` and the controller closes the PR and deletes its head branch (`kardinal/<bundle>/<env>`), so a late merge cannot deliver the change: GitHub's API merges a closed PR whose branch is still there. Unset or `0` waits forever. |
| `stepTimeoutSeconds` | No | (none) | Maximum seconds one built-in step (`git-clone`, `kustomize-set-image`, `open-pr`, ...) may run. The step is cancelled and the error is handled like any other step error: a retryable error is retried with backoff, then the PromotionStep is marked `Failed`. Minimum 1. Unset means no per-step timeout. |
| `bake.minutes` | No | (none) | Contiguous-healthy soak window in minutes (K-01). When set, the step must observe healthy deployment status *continuously* for this many minutes before transitioning to Verified. A check that is not healthy stops the window; it starts again at the next healthy check, and `health.timeout` bounds the wait for it. A Waiting check (the workload is changing, such as a canary paused at a step) is not an alarm under either policy. |
| `bake.policy` | No | `reset-on-alarm` | What to do when a check is unhealthy during the bake window. `reset-on-alarm`: stop the window, increment `status.bakeResets`, stay in HealthChecking. `fail-on-alarm`: immediately apply `onHealthFailure` policy. A release that keeps flapping between healthy and unhealthy fails under `reset-on-alarm` when no full window completes by the first window's start + `bake.minutes` + `health.timeout` (see [Timings and failures](health-adapters.md#timings-and-failures)); `fail-on-alarm` fails it on the first unhealthy check. |
| `bake.maxDuration` | No | `bake.minutes` + `health.timeout` | Go duration (`36h`). The longest time from the first bake window's start (`status.bakeFirstStartedAt`) to a complete window. A window that stops after it, on an alarm or a Waiting check such as a paused canary, applies `onHealthFailure`. A value shorter than `bake.minutes` counts as `bake.minutes`. |
| `onHealthFailure` | No | `none` | What to do when `health.timeout` expires without a Healthy result, when the adapter reports a terminal failure (Deployment `ProgressDeadlineExceeded` from this promotion's rollout, Flagger `Failed`), or when health fails during bake with `policy: fail-on-alarm` (K-03). `none`: step → Failed (default behavior). `abort`: step → AbortedByAlarm; requires human intervention. `rollback`: create a rollback Bundle with the artifacts of the Bundle verified before the failing one in this environment; step → RollingBack, or AbortedByAlarm when there is nothing safe to roll back to (a step of a rollback Bundle → AbortedByAlarm instead, so rollbacks do not chain). See [Automatic Rollback](rollback.md#automatic-rollback). |
| `regions` | No | (none) | **Deprecated, not supported.** Declare one environment per region instead (for example `prod-us` and `prod-eu`) and promote them in parallel with `wave` or `dependsOn`; each gets its own path, PR, gates and health check. Two or more regions set the Pipeline `Ready=False`, `kardinal validate` fails, and every Bundle fails when its Graph is built with `regions is not supported; declare one environment per region (prod-us, prod-eu) and use wave`. A single region is accepted and ignored. |

**Reserved and unsupported fields.** `layout: branch` (on `spec.git` or an environment) and
a `health.resource.kind` other than `Deployment` are not implemented; `regions` with two or
more entries, `shard` and `health.cluster` are deprecated and not supported. A Bundle fails
when it reaches an environment that uses one (two or more `regions` fail it when its Graph is
built; the others fail the environment's step before it changes anything in git).
`kardinal validate` reports each of them, and the controller sets the Pipeline's `Ready` condition to `False` with reason `NotImplemented` and the same messages
(`kubectl get pipeline <name> -o jsonpath='{.status.conditions}'`). The API server rejects
`autoRollback`, a non-empty `spec.policyGates` (org gates use the `kardinal.io/applies-to` label) and the deprecated `steps` and `promotionTemplate` outright. A Pipeline stored
before those rules existed is still reported the same way, and its Bundles fail when their
Graph is built.

### spec.historyLimit

Number of finished Bundles (Verified, Failed or Superseded) to retain per Pipeline. Older ones are garbage-collected, oldest first, when a new Bundle is created. `kardinal rollback` can only target a retained Bundle. The Git PR history is permanent regardless of this setting.

Default: 50.

### spec.paused

When `true`, no PromotionStep of the Pipeline leaves `Pending`, and a step in `Promoting` holds before its next git step. Steps waiting for a PR merge or running a health check finish. The controller keeps a freeze PolicyGate named `freeze-<pipeline>` while the Pipeline is paused. `kardinal pause` / `kardinal resume` and the UI set this field. See [Pause and Resume](rollback.md#pause-and-resume).

Default: `false`.

### spec.maxConcurrentPromotions

Maximum number of this Pipeline's Bundles in the `Promoting` phase at once. A Bundle over the cap stays `Available` with the `Ready` condition reason `WaitingForSlot`, and starts when a promoting Bundle becomes Verified, Failed or Superseded. `0` means no cap.

Default: `0`.

### spec.policyNamespaces

Extra namespaces to read PolicyGates from. It only adds: the org policy namespaces (the controller's `--policy-namespaces`, default `platform-policies`) and the Pipeline's namespace are always read. A gate found only through it is a team gate unless it is labelled `kardinal.io/scope: org`, and it never grants a skip. See [Policy Gates](policy-gates.md).

Default: none.

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

List the waves in ascending order. If a higher wave comes before a lower one with an environment without a wave between them (`test`, `a` in wave 2, `staging`, `b` in wave 1), the list-order edges and the wave edges form a cycle. The Pipeline is then `Ready=False` (reason `ValidationFailed`), `kardinal validate` fails, and every Bundle fails; the message names each edge in the cycle.

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

## Promotion Steps

Every environment runs a fixed step sequence. The controller picks it from the Bundle type,
`update.strategy`, `approval` and `layout` (`pkg/steps/defaults.go`) when the environment's step
starts, records it in the step's `status.steps`, and runs that list to the end: an `approval`
edit made while the step runs applies from the next Bundle. The Bundle in flight still
finishes, and its Graph turns Ready once its steps are Verified and its gates pass, with or without a PR.

| Case | Steps |
|---|---|
| Image Bundle, `update.strategy: kustomize` (default) | `git-clone`, `kustomize-set-image`, `git-commit`, `git-push`, [`open-pr`, `wait-for-merge`,] `health-check` |
| Image Bundle, `update.strategy: helm` | `git-clone`, `helm-set-image`, `git-commit`, `git-push`, [`open-pr`, `wait-for-merge`,] `health-check` |
| Config Bundle | `git-clone`, `config-merge`, `git-commit`, `git-push`, [`open-pr`, `wait-for-merge`,] `health-check` |
| Mixed Bundle | `git-clone`, `config-merge`, then the image Bundle's update step (`kustomize-set-image` or `helm-set-image`), `git-commit`, `git-push`, [`open-pr`, `wait-for-merge`,] `health-check` |
| `update.strategy: argocd` | `argocd-set-image`, `health-check` |

`open-pr` and `wait-for-merge` run only with `approval: pr-review`. When the files in git
already have the Bundle's change, `git-commit` finds nothing to commit: `git-push`, `open-pr`
and `wait-for-merge` then do nothing, no PR is opened, and the step goes on to the health check.
`layout: branch` is not implemented and fails at `git-clone`. [Architecture: Steps Engine](architecture.md#steps-engine-pkgsteps)
describes each step.

kardinal has no custom step engine. `spec.environments[].steps` and
`spec.environments[].promotionTemplate` are deprecated and cannot change the sequence: the
API server rejects a Pipeline that sets either, and `kardinal validate` reports it.

### How `kustomize-set-image` matches images

kustomize matches an `images` entry on its `name` only, against the image name in the
manifests. So `kustomize-set-image` always writes, or updates, an entry whose `name` is the
full repository (for example `name: ghcr.io/org/app`), as `kustomize edit set image` does.
That entry rewrites manifests that use `image: ghcr.io/org/app`.

Entries that point at the repository under another name get the same tag or digest, so
manifests that use that name keep promoting:

- an entry whose `newName` is the repository (for example `name: app`,
  `newName: ghcr.io/org/app`, as older kardinal versions wrote it);
- an older short-name entry (`name: app`, no `newName`), when only one Bundle image has that
  short name. It also gets `newName: ghcr.io/org/app`.

A short-name entry whose `newName` points at another repository is left alone.

### Image signatures and tests

Checks that must hold for the running workload belong where it runs, not in the promoter:

- **Image signatures.** Enforce them at admission in the cluster that runs the pods, with
  [Sigstore policy-controller](https://docs.sigstore.dev/policy-controller/overview/) or
  [Kyverno `verifyImages`](https://kyverno.io/docs/policy-types/cluster-policy/verify-images/).
  A check in the promoter is bypassed by anyone who can push to the GitOps repository;
  admission is not.
- **Tests after a deploy.** Run them as an Argo CD
  [PostSync hook](https://argo-cd.readthedocs.io/en/stable/user-guide/sync-waves/) Job and
  set `health.type: argocd`. Argo CD keeps the sync operation open while the hook runs and
  marks it failed when a PostSync hook fails. The argocd adapter is healthy only when the
  Application is Healthy and Synced on the promoted revision and its last operation is
  `Succeeded` (or there is none), so the step waits for the tests; a `Failed` or `Error`
  operation on that revision is a health failure and applies `onHealthFailure`.
- **Metric checks.** Create a `MetricCheck` and read it from a PolicyGate on the next
  environment, for example `metrics["error-rate"].result == "Pass"`. See
  [Policy Gates: Metric-based](policy-gates.md#metric-based).

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
    secretRef: { name: github-token }
  environments:
    - name: dev
    - name: staging
    - name: prod
      approval: pr-review
```

### Multi-cluster with Argo Rollouts

The `argocd` checks work for any destination cluster because the Applications live in the controller's cluster. The `argoRollouts` checks read the Rollout in the controller's cluster, so this layout suits Rollouts that the controller's cluster runs: here each prod region has its own namespace there. Give each environment its own Rollout; two environments that name the same one check the same object. For Rollouts in other clusters, check their hub Applications with `type: argocd` instead (see [Remote Clusters](health-adapters.md#remote-clusters)).

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
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
        argoRollouts: { name: my-app, namespace: prod-us }
      delivery:
        delegate: argoRollouts
    - name: prod-eu
      dependsOn: [pre-prod]
      approval: pr-review
      health:
        type: argoRollouts
        argoRollouts: { name: my-app, namespace: prod-eu }
      delivery:
        delegate: argoRollouts
```

### Remote prod cluster through an Argo CD hub

Health adapters read objects in the controller's own cluster, and `health.cluster` is not supported. To verify a workload in another cluster, let an Argo CD instance in the controller's cluster manage it and check its Application there: the adapter confirms the Application synced the promoted commit and is Healthy. A Flux Kustomization in the hub that targets the cluster with `spec.kubeConfig.secretRef` works the same way with `type: flux`. A Rollout, Canary or Deployment in another cluster cannot be checked, and neither can a cluster the hub does not reach. See [Health Adapters](health-adapters.md#remote-clusters).

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
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
