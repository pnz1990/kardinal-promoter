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
    layout: <string>                    # "directory" (default) or "branch" (rendered manifests)
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
        strategy: <string>              # "kustomize" (default), "helm", "argocd" or "yaml"
        helm:                           # When strategy: helm
          imagePathTemplate: <string>   # Dot path of the image tag (default: ".image.tag")
          valuesFile: <string>          # Relative to path (default: "values.yaml")
          chartVersionFile: <string>    # chart Bundles: file with the chart version (default: "Chart.yaml")
          chartVersionPath: <string>    # chart Bundles: its dot path (default: ".dependencies[name=<chart>].version")
        argocd:                         # When strategy: argocd
          application: <string>         # Argo CD Application to patch (required)
          namespace: <string>           # Default: "argocd"
          imageKey: <string>            # Key in spec.source.helm.valuesObject (default: "image.tag")
        yaml:                           # When strategy: yaml
          updates:                      # One or more; all are applied in one commit
            - file: <string>            # YAML file relative to path
              path: <string>            # YAML path, e.g. "spec.template.spec.containers[name=app].image"
              image: <string>           # Bundle image repository (optional with one image)
              value: <string>           # tag (default), digest, tagWithDigest, image, imageWithDigest
      approval: <string>                # "auto" (default) or "pr-review"
      health:
        type: <string>                  # "resource" (default), "argocd", "flux", "argoRollouts", "flagger"
        labelSelector: {<key>: <value>} # type: resource only: check every matching Deployment,
                                        # in the namespace named after the environment unless
                                        # resource.namespace is set
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
      layout: <string>                  # "directory" (default) or "branch" (rendered manifests)
      render:                           # layout: branch
        branch: <string>                # Rendered branch (default: "env/<name>")
        onDrift: <string>               # "fail" (default) or "overwrite"
        helm: {releaseName: <string>, namespace: <string>, valuesFiles: [<string>]}
        allowNondeterministic: <bool>   # allow randAlphaNum, uuidv4, now, genCA, ... in Helm templates (default false)
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
| `layout` | No | `directory` | `directory`: environments as directories on one branch. `branch`: every environment's path is rendered (kustomize build, or helm template for a chart) and committed as plain manifests to its rendered branch; see [Rendered Manifests](rendered-manifests.md). |
| `provider` | No | (none) | **Not read by the controller.** The SCM provider is the controller's `--scm-provider` (`github`, `gitlab`, `forgejo`, `gitea`, `bitbucket` or `azuredevops`), or the one `providerRef` names; see [SCM Providers](scm-providers.md). The CRD accepts only `github` or `gitlab` here. Leave it unset. |
| `secretRef.name` | No | | `secretRef` is optional; when it is set, `name` must be too. Name of a Kubernetes Secret in the Pipeline's namespace containing a `token` field with a GitHub PAT or GitLab token. Needed when the HTTPS remote refuses git without a token (every push to a hosted provider, and the clone of a private repository); not needed for an ssh remote or a URL that carries its credentials. When it is not set, or the Secret does not exist, and the HTTPS remote refuses `git-clone` or `git-push` without a token, the step retries until the Secret exists; the step message says what is missing (see [Troubleshooting](troubleshooting.md#symptom-authentication-required-with-git-secret-not-found-or-specgitsecretref-is-not-set)). **Label the Secret `kardinal.io/referenceable: "true"`** (`kubectl label secret github-token kardinal.io/referenceable=true`): the token goes to the Pipeline's `git.url`, which the Pipeline's author chooses, so the label records that the Secret's owner allows it. Deprecated in v0.10.0: an unlabeled Secret still works, and the Pipeline has the condition `SecretReferenceable=False` (reason `SecretNotReferenceable`); v0.11 will refuse it ([#1506](https://github.com/pnz1990/kardinal-promoter/issues/1506)). |
| `secretRef.namespace` | No | Pipeline's namespace | Must be empty or the Pipeline's own namespace. Any other namespace fails the PromotionStep without reading the Secret, so a Pipeline cannot use another namespace's credentials. The Pipeline's `Ready` condition is `False` with reason `ValidationFailed`, and `kardinal validate` reports it when the file sets `metadata.namespace`. |
| `providerRef.name` | No | | Name of the [ScmProvider or ClusterScmProvider](scm-providers.md#several-scm-providers-scmprovider-and-clusterscmprovider) whose API and token this Pipeline's PRs use. When `providerRef` is not set, the controller's `--scm-provider` is used. The provider is resolved when a Bundle's Graph is built. If it is missing, does not allow the Pipeline's namespace (`allowedNamespaces` of a ClusterScmProvider), or does not allow the repository (`allowedRepositories`), the Bundle waits with the reason. |
| `providerRef.kind` | No | `ScmProvider` | `ScmProvider`, in the Pipeline's namespace, or `ClusterScmProvider`. |

### spec.environments[]

A Pipeline has 1 to 500 environments (see [Large Pipelines](#large-pipelines) for what fits in
one Bundle's Graph). The CRD rejects, at `kubectl apply` time:

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
| `update.strategy` | No | `kustomize` | How to update image references in manifests. `kustomize`: edits the `images:` list of the environment's `kustomization.yaml` the way `kustomize edit set image` does. `helm`: patches the image tag at `update.helm.imagePathTemplate` in `update.helm.valuesFile`; one image per Bundle, so use one Bundle per chart image, or kustomize. `argocd`: patches the Argo CD Application's `spec.source.helm.valuesObject` directly, with no Git commit or PR. The API server rejects `argocd` with `approval: pr-review`, and a config or mixed Bundle fails before its first environment when any environment it promotes uses `argocd`; see [Argo CD native promotion](argocd-native-promotion.md). `yaml`: sets any YAML paths, in any files of the environment directory, to a Bundle image's tag, digest or reference; see [The yaml update strategy](#the-yaml-update-strategy). |
| `update.helm.imagePathTemplate` | No | `.image.tag` | `helm` only. Dot path of the image tag in the values file. |
| `update.helm.valuesFile` | No | `values.yaml` | `helm` only. Values file to patch, relative to the environment `path`. |
| `update.helm.chartVersionFile` | No | `Chart.yaml` | `helm` only, for `chart` Bundles (from a [Helm Subscription](subscription.md#promoting-a-chart-version)). File the chart version is written to, relative to the environment `path`. A chart Bundle fails at build in an environment whose strategy is not `helm`, or whose `layout` is `branch` (the render Job does not get the chart version yet). |
| `update.helm.chartVersionPath` | No | `.dependencies[name=<chart>].version` | `helm` only. [YAML path](#yaml-paths) of the chart version in `chartVersionFile` (`.helmCharts[name=podinfo].version`, `.spec.chart.spec.version`, `.spec.source.targetRevision`). The default is the umbrella chart's dependency named after the Bundle's chart; the step fails when there is none. |
| `approval` | No | `auto` | `auto`: push directly to the target branch, no PR. `pr-review`: open a PR with promotion evidence, wait for human merge. The step list is fixed when an environment's step starts: an edit applies to steps that start after it, so an environment already promoting finishes with the approval it started with and uses the new one from the next Bundle. A step that started as `auto` still pushes straight to the target branch after an edit to `pr-review`. The Bundle in flight still finishes: its Graph turns Ready once its steps are Verified and its gates pass, whether or not they opened a PR. |
| `health.type` | No | `resource` | Health verification adapter: `resource`, `argocd`, `flux`, `argoRollouts` or `flagger`. `delivery.delegate`, when set, takes precedence. There is no auto-detection. The step is Verified only when the adapter sees the promoted revision (commit or Bundle images) healthy. See [Health Adapters](health-adapters.md). |
| `health.resource`, `health.argocd`, `health.flux`, `health.argoRollouts`, `health.flagger` | No | see [Health Check Defaults](#health-check-defaults) | Name and namespace of the object the adapter checks. `health.resource.kind` must be `Deployment`. Without `health.resource.namespace`, the `resource` check looks in the namespace named after the environment (environment `prod` checks namespace `prod`), not the Pipeline's namespace. |
| `health.timeout` | No | `10m` | Maximum time from the start of health checking to the first healthy check, and from the moment a `bake` window stops to the next healthy check. When it expires, it counts as a health failure and applies `onHealthFailure`. It does not cut a running `bake` window short. |
| `health.cluster` | No | (must be empty) | **Deprecated, not supported.** A non-empty value sets the Pipeline `Ready=False` and fails the PromotionStep. Use `health.kubeconfigSecretRef`, or check the hub's Argo CD Application (`type: argocd`) or Flux Kustomization (`type: flux`); see [Remote Clusters](health-adapters.md#remote-clusters). |
| `health.kubeconfigSecretRef` | No | — | `{name, key}` of a Secret in the Pipeline's namespace holding a kubeconfig (key default `kubeconfig`). The health check reads its object in that cluster. Inline credentials only; `exec`, `auth-provider` and file paths are refused. See [Remote Clusters](health-adapters.md#remote-clusters). |
| `health.labelSelector` | No | (none) | `health.type=resource` only. When set, **every** Deployment that matches these labels must pass the health check, in the namespace named after the environment unless `health.resource.namespace` is set. No match is unhealthy. Example: `{"app": "nginx", "kardinal.io/pipeline": "nginx-demo"}`. When unset, a single Deployment named after the Pipeline is checked. Ignored for `argocd`, `flux`, `argoRollouts`, and `flagger`. |
| `delivery.delegate` | No | `none` | Progressive delivery delegation. `argoRollouts`: watch Argo Rollouts Rollout status after promotion. `flagger`: watch Flagger Canary status. `none`: instant deploy (rolling update). |
| `shard` | No | (must be empty) | **Deprecated, not supported.** Distributed mode was removed. A non-empty value sets the Pipeline `Ready=False` (reason `NotImplemented`), `kardinal validate` fails, and every PromotionStep of the environment fails with `shard is not supported`. Remove it; the controller reconciles every environment. See [Multi-Cluster](distributed-mode.md). |
| `steps` | No | (none) | **Deprecated, not supported.** kardinal has no custom step engine: the controller always runs the sequence it infers from the Bundle type, `update.strategy`, `approval` and `layout`. The API server rejects a Pipeline that sets `steps` (an empty list is accepted). See [Promotion Steps](#promotion-steps). |
| `promotionTemplate` | No | (none) | **Deprecated, not supported.** The `PromotionTemplate` CRD was removed. The API server rejects a Pipeline that sets `promotionTemplate`. |
| `waitForMergeTimeout` | No | (none) | `pr-review` only. How long the step may wait for its PR to merge, as a Go duration (`24h`, `72h`). When it expires, the step is marked `Failed` and the controller closes the PR and deletes its head branch (`kardinal/<namespace hash>/<bundle>/<env>`), so a late merge cannot deliver the change: GitHub's API merges a closed PR whose branch is still there. Unset or `0` waits forever. |
| `stepTimeoutSeconds` | No | (none) | Maximum seconds one built-in step (`git-clone`, `kustomize-set-image`, `open-pr`, ...) may run. The step is cancelled and the error is handled like any other step error: a retryable error is retried with backoff, then the PromotionStep is marked `Failed`. Minimum 1. Unset means no per-step timeout. |
| `bake.minutes` | No | (none) | Contiguous-healthy soak window in minutes (K-01). When set, the step must observe healthy deployment status *continuously* for this many minutes before transitioning to Verified. A check that is not healthy stops the window; it starts again at the next healthy check, and `health.timeout` bounds the wait for it. A Waiting check (the workload is changing, such as a canary paused at a step) is not an alarm under either policy. |
| `bake.policy` | No | `reset-on-alarm` | What to do when a check is unhealthy during the bake window. `reset-on-alarm`: stop the window, increment `status.bakeResets`, stay in HealthChecking. `fail-on-alarm`: immediately apply `onHealthFailure` policy. A release that keeps flapping between healthy and unhealthy fails under `reset-on-alarm` when no full window completes by the first window's start + `bake.minutes` + `health.timeout` (see [Timings and failures](health-adapters.md#timings-and-failures)); `fail-on-alarm` fails it on the first unhealthy check. |
| `bake.maxDuration` | No | `bake.minutes` + `health.timeout` | Go duration (`36h`). The longest time from the first bake window's start (`status.bakeFirstStartedAt`) to a complete window. A window that stops after it, on an alarm or a Waiting check such as a paused canary, applies `onHealthFailure`. A value shorter than `bake.minutes` counts as `bake.minutes`. |
| `onHealthFailure` | No | `none` | What to do when `health.timeout` expires without a Healthy result, when the adapter reports a terminal failure (Deployment `ProgressDeadlineExceeded` from this promotion's rollout, Flagger `Failed`), or when health fails during bake with `policy: fail-on-alarm` (K-03). `none`: step → Failed (default behavior). `abort`: step → AbortedByAlarm; requires human intervention. `rollback`: create a rollback Bundle with the artifacts of the Bundle verified before the failing one in this environment; step → RollingBack, or AbortedByAlarm when there is nothing safe to roll back to (a step of a rollback Bundle → AbortedByAlarm instead, so rollbacks do not chain). See [Automatic Rollback](rollback.md#automatic-rollback). |
| `hooks` | No | (none) | Jobs run once per Bundle in this environment: `phase: pre` before the promotion starts (migrations), `phase: post` after the health check passed and before the environment is Verified (integration tests). Each is `{name, phase, job, timeout}`, `job` a `batch/v1` JobSpec. At most 10. A failed pre hook fails the step before it changes anything; a failed post hook applies `onHealthFailure`. See [Pre- and Post-Deploy Hooks](hooks.md). |
| `verification` | No | (none) | Argo Rollouts analysis after the health check: `{analysisTemplates: [{name, kind}], args: [{name, value}], inconclusive, timeout}`. One AnalysisRun per template, with the Bundle's `tag`, `image`, `environment` and more as args; the environment is Verified only when every run is `Successful`, and a failed run applies `onHealthFailure`. Needs Argo Rollouts installed: without it the Bundle fails. See [Analysis](analysis.md). |
| `regions` | No | (none) | **Deprecated, not supported.** Declare one environment per region instead (for example `prod-us` and `prod-eu`) and promote them in parallel with `wave` or `dependsOn`; each gets its own path, PR, gates and health check. Two or more regions set the Pipeline `Ready=False`, `kardinal validate` fails, and every Bundle fails when its Graph is built with `regions is not supported; declare one environment per region (prod-us, prod-eu) and use wave`. A single region is accepted and ignored. |

**Reserved and unsupported fields.** A `health.resource.kind` other than `Deployment` is not
implemented; `regions` with two or
more entries, `shard` and `health.cluster` are deprecated and not supported. A Bundle fails
when it reaches an environment that uses one (two or more `regions` fail it when its Graph is
built; the others fail the environment's step before it changes anything in git).
`kardinal validate` reports each of them, and the controller sets the Pipeline's `Ready` condition to `False` with reason `NotImplemented` and the same messages
(`kubectl get pipeline <name> -o jsonpath='{.status.conditions}'`). The API server rejects
`autoRollback`, a non-empty `spec.policyGates` (org gates use the `kardinal.io/applies-to` label) and the deprecated `steps` and `promotionTemplate` outright. A Pipeline stored
before those rules existed is still reported the same way, and its Bundles fail when their
Graph is built.

### spec.historyLimit

Number of finished Bundles (Verified, Failed or Superseded; Rejected Bundles are never deleted) to retain per Pipeline. Older ones are garbage-collected, oldest first, when a new Bundle is created. `kardinal rollback` can only target a retained Bundle. The Git PR history is permanent regardless of this setting.

Default: 50.

### spec.paused

When `true`, no PromotionStep of the Pipeline leaves `Pending`, and a step in `Promoting` holds before its next git step. Steps waiting for a PR merge or running a health check finish. The controller keeps a freeze PolicyGate named `freeze-<pipeline>` while the Pipeline is paused. `kardinal pause` / `kardinal resume` and the UI set this field. See [Pause and Resume](rollback.md#pause-and-resume).

Default: `false`.

### spec.holds

Environments pinned to a rollback. `kardinal rollback --hold` (or the UI) writes them, and
`kardinal release-hold` (or the UI) removes them. The controller also removes a hold at its
`expiresAt`. Each entry has these fields:

- `environment`
- `bundle`: the rollback Bundle
- `reason`: required, up to 1024 characters
- `createdBy` and `createdAt`
- `expiresAt` (optional)
- `artifacts`: the digest of the rollback's artifacts when the hold was made

An environment has at most one hold. While it lasts:

- only `bundle` promotes into the environment;
- `bundle` is never superseded or garbage-collected;
- if the controller verifies `bundle`, it passes the environment's PolicyGates, each pass with
  an `EXEMPT` reason and audited.

Changing `spec.holds` needs `update` on `pipelines/hold`, and the chart's admission policy pins
`createdBy` to the caller. `status.observedHolds` is the controller's record, from which it
writes the `HoldCreated` and `HoldReleased` AuditEvents. See
[Roll back and hold](rollback.md#roll-back-and-hold).

```yaml
spec:
  holds:
  - environment: prod
    bundle: my-app-rollback-3f9a1c
    reason: "INC-4521: v1.29.0 leaks connections"
    createdBy: alice
    createdAt: "2026-10-09T08:12:00Z"
    expiresAt: "2026-10-10T08:12:00Z"
    artifacts: "sha256:5b0f..."
```

Default: none.

### spec.maxConcurrentPromotions

Maximum number of this Pipeline's Bundles in the `Promoting` phase at once. A Bundle over the cap stays `Available` with the `Ready` condition reason `WaitingForSlot`, and starts when a promoting Bundle becomes Verified, Failed, Superseded or Rejected. `0` means no cap.

A `Failed` Bundle does not count, so it does not take a slot back while the cap is full. While the cap is full it has the condition `WaitingForSlot=True`: its Graph creates no new PromotionStep and its `Pending` steps do not start, so a failed step that is deleted is not recreated until a slot frees. A step that was already running keeps running. When a slot frees the condition is removed, and once nothing is failing the Bundle returns to `Promoting`. A `Failed` Bundle that a newer Bundle of its type replaced (one that is `Promoting` or `Verified`) is never held: it can only be superseded. A newer Bundle that is still `Available` does not count, since it may be waiting for the slot too. When several `Failed` Bundles wait and one slot frees, the hold is lifted on all of them at once, so a step recreated for each can start before the first of them returns to `Promoting`; the next ones are then held again, but their started steps keep running.

Default: `0`.

### spec.imageVerification

Requires the Bundle's images (selected by `images`, by digest) to carry a signature from one of
`authorities` (a cosign key or a Sigstore keyless identity), and a config Bundle's commit to be
signed when `commits.requireSigned`, before the Bundle is promoted into its first environments.
See [Image Signature Verification](image-verification.md).

### spec.policyNamespaces

Extra namespaces to read PolicyGates from. It only adds: the org policy namespaces (the controller's `--policy-namespaces`, default `platform-policies`) and the Pipeline's namespace are always read. A gate found only through it is a team gate unless it is labelled `kardinal.io/scope: org`, and it never grants a skip. See [Policy Gates](policy-gates.md).

Default: none.

### Large Pipelines

Each Bundle is promoted by one kro Graph, and a Graph is one Kubernetes object, which etcd stores
only up to 1.5 MiB. Two Graph shapes keep it in bounds:

- **nodes** (Pipelines with up to 100 environments): one Graph node per environment. `kubectl get
  graph -o yaml` shows each environment's PromotionStep as a node.
- **compact** (above 100 environments): the promotion order is data in the Graph, and one node
  creates every PromotionStep the order allows (upstream environments Verified, gates ready, the
  Bundle not superseded, rejected or waiting for a `maxConcurrentPromotions` slot). A step that exists is not removed when a gate later closes or
  the Bundle is superseded. The Graph has about a dozen nodes whatever the number of environments,
  and no health ref nodes (health is checked by the PromotionStep as in the nodes shape).

Both shapes promote the same way: the same PromotionSteps, PolicyGate instances and PRStatuses,
with the same names. Choose one for a Pipeline with the annotation `kardinal.io/graph-shape:
compact` or `nodes`; the controller's `--graph-compact-above` (chart `graph.compactAbove`) moves
the threshold. A Bundle keeps the shape its Graph was created with: a later Pipeline edit, a changed
annotation or threshold applies to new Bundles only, because switching the shape of a Graph in
flight would delete its PromotionSteps. The shape is read from the Graph's nodes; the Graph's
`kardinal.io/graph-shape` label only shows it.

In the compact shape every PromotionStep comes from one collection, so a PolicyGate instance or a
PromotionStep that kro cannot apply holds every environment of the Bundle, not only its own (the
Bundle's `GatesCreated` condition names a gate that cannot be created). A feature the compact shape
does not carry yet fails the Bundle with `GraphBuildFailed` naming the feature, and sets the
Pipeline `Ready=False` while its new Bundles would get a compact Graph. Per-promotion MetricChecks
(`spec.perPromotion`) are carried: the `MetricChecks` collection creates an environment's instances
once its upstream environments are Verified, as the nodes shape does. One difference in pruning: if
an upstream leaves Verified before the environment's step starts, the compact shape deletes that
environment's instances (they leave the collection, so kro prunes them) and creates them again once
the upstreams are Verified; once the step has started, its instances are kept.
[Hooks](hooks.md) (`spec.environments[].hooks`), [analysis](analysis.md)
(`spec.environments[].verification`), [image verification](image-verification.md)
(`spec.imageVerification`) and rendered manifests (`layout: branch`) are not carried yet: both
the Bundle and the Pipeline condition report them.

The Graph's size grows with environments and PolicyGates. Measured: 300 environments with one gate
each, fully promoted, 0.47 MB; 300 with three gates each about 0.9 MB. A Bundle whose Graph would be
over 1.2 MB, or create more than 4,500 objects, fails with `GraphBuildFailed` and the size in the
message.

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

**Branch layout** (`layout: branch`): the DRY source (Kustomize overlays or Helm charts) lives on
`spec.git.branch`, and kardinal renders each environment's path at promotion time and commits the
plain YAML to the environment's rendered branch (`render.branch`, default `env/<name>`), which
Argo CD or Flux syncs. A `pr-review` PR targets the rendered branch, so its diff is the rendered
YAML. See [Rendered Manifests](rendered-manifests.md).

### Many Pipelines on one repository and branch

Several Pipelines (and every environment of one Pipeline) can write the same repository
and branch at once, as long as each environment has its own `path`. kardinal never
force-pushes the base branch, so no writer's commit is lost:

- **auto environments**: when `git-push` finds that the branch moved since its clone
  (another writer pushed first), it fetches the new head and replays its commit onto it:
  every file this promotion added, changed or deleted takes the promotion's version, every
  other file the new head's. It pushes again, up to 6 times, without waiting in between. The
  step message then reads
  `pushed main after rebasing onto N newer commit(s) of other writers`. When the new commits
  changed one of the same files, or the branch keeps moving, the whole step sequence runs
  again from a fresh clone (at most 3 times in one reconcile), so the update is computed on
  the other writer's version. After that the step is retried with jittered backoff (at most
  2 minutes), counted in `status.contendedRetries` with no limit and not in
  `status.retryCount`, so contention slows a promotion down but does not fail it.
- **pr-review environments**: each promotion pushes its own branch
  `kardinal/<namespace hash>/<bundle>/<environment>` (the hash is the first 8 hex digits of
  the SHA-256 of the namespace, so Bundles of the same name in two namespaces get separate
  branches; a PR opened by an earlier release keeps its `kardinal/<bundle>/<environment>`
  branch). The branch starts at the base head of its clone. While the PR waits for its merge,
  the controller reads the base head every 30 seconds (one `git ls-remote` per repository,
  shared by every waiting PR). When the base moved, it reads the commits since the PR's base
  (the last 20 of the branch, or the last 500 when the PR's base is further back; once per new
  head) and:
  - when they changed none of the PR's paths (the environment's `path`, and a Helm `valuesFile`
    outside it), the PR still merges cleanly: only `status.outputs.baseSHA` moves, nothing is
    pushed;
  - when they changed one of its paths, or the PR's base is not among the last 500 (a
    force-push), or reading them takes longer than 30 seconds, it reruns the promotion's steps on a fresh clone of the new head and
    force-pushes the PR branch, so the PR is one commit on the current base
    (`status.outputs.prBranchRebuilds` counts it; when the history could not be read, the
    step message says the PR branch was rebuilt to be safe);
  - when the PR branch has a commit kardinal did not push (its head is not
    `status.outputs.pushedSHA`), it is never rebuilt, and the step message says so.

  A rebuild replaces only kardinal's own commit; a host set to dismiss stale approvals asks for
  the review again. PRs of different Pipelines change different paths, so
  each merges without a conflict however many merged before it.
- **Path isolation**: two environments that write the same path, or one inside the other,
  overwrite each other's files and their PRs conflict. The Pipeline reconciler checks every
  Pipeline the controller sees, including two environments of one Pipeline. A Pipeline writes
  each environment's `path` and, for `update.strategy: helm`, a `valuesFile` outside it (such
  as `../shared/values.yaml`). Repositories are compared by host and path, so the https, ssh
  and `git@host:org/repo` URLs of one repository match (case, userinfo, port and a trailing
  `.git` are ignored); branches must be equal. On an overlap the Pipeline gets
  `PathConflict=True` (reason `OverlappingPath`). The message names the environments and the
  Pipelines of the same namespace; Pipelines of other namespaces are only counted
  (`environment prod (apps/prod) and 2 other Pipeline(s) in other namespaces`). It does not
  stop promotions. Environments with `update.strategy: argocd` write no git and are not
  compared.

```bash
kubectl get pipelines -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}: {.status.conditions[?(@.type=="PathConflict")].message}{"\n"}{end}'
```

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
| Mixed Bundle | `git-clone`, `config-merge`, then the image Bundle's update step (`kustomize-set-image`, `helm-set-image` or `yaml-update`), `git-commit`, `git-push`, [`open-pr`, `wait-for-merge`,] `health-check` |
| Image Bundle, `update.strategy: yaml` | `git-clone`, `yaml-update`, `git-commit`, `git-push`, [`open-pr`, `wait-for-merge`,] `health-check` |
| `layout: branch` | `render` (waits for the environment's RenderRun, a Job that runs `git-clone`, the image update step (none for a config Bundle), `render-manifests`, `git-commit` and `git-push`), [`open-pr`, `wait-for-merge`,] `health-check` |
| `update.strategy: argocd` | `argocd-set-image`, `health-check` |

`open-pr` and `wait-for-merge` run only with `approval: pr-review`. When the files in git
already have the Bundle's change, `git-commit` finds nothing to commit: `git-push`, `open-pr`
and `wait-for-merge` then do nothing, no PR is opened, and the step goes on to the health check.
[Architecture: Steps Engine](architecture.md#steps-engine-pkgsteps)
describes each step.

kardinal has no custom step engine. `spec.environments[].steps` and
`spec.environments[].promotionTemplate` are deprecated and cannot change the sequence: the
API server rejects a Pipeline that sets either, and `kardinal validate` reports it. To run
your own work around the sequence, use [hooks](hooks.md): Jobs before the step starts and
after its health check.

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

### The yaml update strategy

`update.strategy: yaml` writes values of the Bundle's images into any YAML paths of any files
in the environment directory: plain manifests, Helm values, a Kustomize `images` entry, a
custom resource. Each `update.yaml.updates[]` entry sets one scalar:

```yaml
update:
  strategy: yaml
  yaml:
    updates:
      - file: deploy/deployment.yaml
        path: spec.template.spec.containers[0].image
        image: ghcr.io/org/api           # which Bundle image; optional with one image
        value: image                     # ghcr.io/org/api:1.4.2
      - file: deploy/deployment.yaml
        path: spec.template.spec.containers[1].image
        image: ghcr.io/org/sidecar
        value: imageWithDigest           # ghcr.io/org/sidecar:0.3.0@sha256:...
      - file: values.yaml
        path: api.image.tag
        image: ghcr.io/org/api           # value defaults to tag: 1.4.2
```

| `value` | Written |
|---|---|
| `tag` (default) | the tag |
| `digest` | the digest (`sha256:...`) |
| `tagWithDigest` | `<tag>@<digest>` |
| `image` | `<repository>:<tag>` |
| `imageWithDigest` | `<repository>:<tag>@<digest>`, or `<repository>@<digest>` without a tag |

- `path` uses the [YAML path](#yaml-paths) grammar, as `update.helm.chartVersionPath` does.
  Missing mapping keys are created; list elements are not.
- The step computes every edit before it writes anything. An edit that cannot be applied (a
  Bundle without the named image, a value the image does not have such as the digest of a
  tag-only image, a missing list element, a path through a scalar, a path that would replace a
  mapping or list, a file outside the repository) fails the step for good and no file is
  changed.
- Comments, key order, the quoting of the replaced value and the file mode are kept. Every edited
  file is parsed again before it is written and must still hold every value where it was set. The
  files are then written through new temporary files (never through a file or link already at that
  name) and renamed; if a rename fails, the files already replaced get their old content back.
- Refused, failing the step: a file with more than one YAML document (`---`; an empty or
  comment-only document after the first, such as a trailing `---` or `--- # end`, is not written
  back, and its comments move to the end of the file), an anchor or alias
  (`&`, `*`) or a merge key (`<<`) on the edited path, a key that appears twice in one mapping, a
  symbolic link anywhere on the path (the environment directory, a directory in `file`, or the
  file), a file over 4 MiB, and a `file` that is absolute or contains `..`.
- A Bundle without images (a config Bundle) changes nothing.

### YAML paths

`update.yaml.updates[].path` and `update.helm.chartVersionPath` name a scalar in a YAML file
with one grammar:

- Keys separated by `.`; a leading `.` is optional (`image.tag` and `.image.tag` are the same).
  A key is letters, digits, `_` and `-`. A key that contains `.` or `/` (an annotation such as
  `app.kubernetes.io/version`) cannot be addressed.
- `[N]` after a key picks a list element by position: `spec.template.spec.containers[0].image`.
  A digits-only key does the same when it reaches a list (`.dependencies.0.version`); in a
  mapping it is a key. An index has at most 9 digits.
- `[field=value]` picks the list element (a mapping) whose `field` has that value:
  `spec.template.spec.containers[name=app].image`, `.dependencies[name=podinfo].version`. The
  value is letters, digits and `_ - . / : @`.
- The element a list step names must exist, and so must the list: a missing or null value before
  `[N]`, `[field=value]` or a digits-only key fails the step for good instead of being created
  as a mapping.

The API server checks the grammar: a Pipeline with a path outside it is refused.

### Image signatures and tests

Checks that must hold for the running workload belong where it runs, not in the promoter:

- **Image signatures.** `spec.imageVerification` verifies cosign and Sigstore signatures
  before a Bundle is promoted ([Image Signature Verification](image-verification.md)). Also
  enforce them at admission in the cluster that runs the pods, with
  [Sigstore policy-controller](https://docs.sigstore.dev/policy-controller/overview/) or
  [Kyverno `verifyImages`](https://kyverno.io/docs/policy-types/cluster-policy/verify-images/):
  a check in the promoter is bypassed by anyone who can push to the GitOps repository;
  admission is not.
- **Tests after a deploy.** Run them as a [post-deploy hook](hooks.md): a Job kardinal runs
  after the health check passed; the environment is Verified only when it succeeded. Or run
  them as an Argo CD
  [PostSync hook](https://argo-cd.readthedocs.io/en/stable/user-guide/sync-waves/) Job and
  set `health.type: argocd`. Argo CD keeps the sync operation open while the hook runs and
  marks it failed when a PostSync hook fails. The argocd adapter is healthy only when the
  Application is Healthy and Synced on the promoted revision and its last operation is
  `Succeeded` (or there is none), so the step waits for the tests; a `Failed` or `Error`
  operation on that revision is a health failure and applies `onHealthFailure`.
- **Analysis.** Name Argo Rollouts AnalysisTemplates in `verification` (any Rollouts
  provider: Prometheus, Datadog, CloudWatch, New Relic, web, Job); see [Analysis](analysis.md).
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

### Rendered manifests (branch layout)

`examples/rendered-manifests/pipeline.yaml`: every environment renders its overlay into
`env/<name>`, prod through a PR. See [Rendered Manifests](rendered-manifests.md).
