# Pipeline field reference

Every field of the Pipeline CRD, with its default and behaviour. For how to combine them, see
[Design a pipeline](../pipeline-reference.md); the generated [CRD API](api.md#pipeline) lists the
same fields from the Go types.

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
      agents: <string>                  # "auto", "approve" or "deny": the ceiling for agents' versions
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
      waitForMergeTimeout: <duration>   # pr-review only: fail the step and close the PR after this (default: wait forever)
      pr:                               # pr-review only: the PR (see PR Evidence, Customising the PR)
        titleTemplate: <template>       #   PR title (default: [kardinal] Promote <bundle> to <env>)
        bodyTemplate: <template>        #   PR body; the evidence sections are template functions
        labels: [<template>, ...]       #   Labels added to the kardinal labels
        reviewers: [<template>, ...]    #   Users asked to review
        teamReviewers: [<template>, ...] #  Teams asked to review
        assignees: [<template>, ...]    #   Users assigned, e.g. "{{ .Bundle.Author }}"
        merge:
          auto: <bool>                  #   Enable the SCM's auto-merge on the PR (default: false)
          allowImmediate: <bool>        #   Merge at once when nothing is pending; skips review (default: false)
          method: <string>              #   "merge" (default), "squash" or "rebase"; needs auto
          commitMessageTemplate: <template> # Merge commit message; needs auto
      stepTimeoutSeconds: <int>         # Per built-in step timeout in seconds, minimum 1 (default: none)
  paused: <bool>                        # Hold every promotion of the Pipeline (default: false)
  pausedFleets: [<string>, ...]         # Fleet environments that start no further target (default: none)
  maxConcurrentPromotions: <int>        # Bundles promoting at once; 0 is no cap (default: 0)
  policyNamespaces: [<string>, ...]     # Extra namespaces to read PolicyGates from (default: none)
  historyLimit: <int>                   # Number of Bundles to retain (default: 50)
```

## Field Details

### spec.git

| Field | Required | Default | Description |
|---|---|---|---|
| `url` | Yes | | URL of the GitOps repository: HTTPS, or ssh (`ssh://git@host/owner/repo.git`, `git@host:owner/repo.git`) with an ssh key in `secretRef` |
| `branch` | No | `main` | Base branch: `git-clone` checks it out, `approval: auto` pushes to it, and `pr-review` PRs target it. The API server sets `main` when the field is omitted, and the controller also reads an empty value as `main`. |
| `layout` | No | `directory` | `directory`: environments as directories on one branch. `branch`: every environment's path is rendered (kustomize build, or helm template for a chart) and committed as plain manifests to its rendered branch; see [Rendered Manifests](../rendered-manifests.md). |
| `secretRef.name` | No | | `secretRef` is optional; when it is set, `name` must be too. Name of a Kubernetes Secret in the Pipeline's namespace containing a `token` field with a GitHub PAT or GitLab token, GitHub App credentials (`githubAppID`, `githubAppInstallationID`, `githubAppPrivateKey`: git then uses installation tokens, see [GitHub App](../scm-providers.md#github-app)), or for an ssh `url` the `sshPrivateKey` and `knownHosts` keys ([SSH git authentication](../scm-providers.md#ssh-git-authentication)). Needed when the HTTPS remote refuses git without a token (every push to a hosted provider, and the clone of a private repository); not needed for a URL that carries its credentials (an ssh remote needs `sshPrivateKey` and `knownHosts` instead). When an HTTPS remote has no token this way (not set, a Secret that does not exist, or one with no `token` key and no GitHub App credentials), the Pipeline is `Ready=False` with reason `GitCredentialMissing` as soon as it is applied, and the message is the command that creates the Secret ([Troubleshooting](../troubleshoot-git.md#symptom-pipeline-readyfalse-with-reason-gitcredentialmissing)). When the remote refuses `git-clone` or `git-push` without a token, the step retries until the Secret exists; the step message says what is missing (see [Troubleshooting](../troubleshoot-git.md#symptom-authentication-required-with-git-secret-not-found-or-specgitsecretref-is-not-set)). **Label the Secret `kardinal.io/referenceable: "true"`** (`kubectl label secret github-token kardinal.io/referenceable=true`): the token goes to the Pipeline's `git.url`, which the Pipeline's author chooses, so the label records that the Secret's owner allows it. An unlabeled Secret is not used: the Pipeline is `Ready=False` with reason `SecretNotReferenceable` and the command that labels it, and every git step waits until it is labeled. |
| `secretRef.namespace` | No | Pipeline's namespace | Must be empty or the Pipeline's own namespace. Any other namespace fails the PromotionStep without reading the Secret, so a Pipeline cannot use another namespace's credentials. The Pipeline's `Ready` condition is `False` with reason `ValidationFailed`, and `kardinal validate` reports it when the file sets `metadata.namespace`. |
| `providerRef.name` | No | | Name of the [ScmProvider or ClusterScmProvider](../scm-providers.md#several-scm-providers-scmprovider-and-clusterscmprovider) whose API and token this Pipeline's PRs use. When `providerRef` is not set, the controller's `--scm-provider` is used. The provider is resolved when a Bundle's Graph is built. If it is missing, does not allow the Pipeline's namespace (`allowedNamespaces` of a ClusterScmProvider), or does not allow the repository (`allowedRepositories`), the Bundle waits with the reason. |
| `providerRef.kind` | No | `ScmProvider` | `ScmProvider`, in the Pipeline's namespace, or `ClusterScmProvider`. |

### spec.environments[]

A Pipeline has 1 to 500 environments (see [Large Pipelines](../pipeline-reference.md#large-pipelines) for what fits in
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
| `fleet` | No | (none) | Expands the environment into one environment per target, `<name>-<target>`, promoted at most `fleet.maxConcurrent` at a time, and stopped after `fleet.maxUnavailable` failures. The targets are listed in `fleet.targets` or picked by `fleet.selector` from Argo CD Applications or cluster inventory ClusterProfiles. See [Fleets](../pipeline-reference.md#fleets). |
| `wave` | No | (none) | Assigns this environment to a numbered deployment wave. Minimum 1. Environments with the same wave number are promoted in parallel. A wave depends on every environment of the next lower wave, and on the environment without a wave listed before it. Gaps in the numbers are allowed. Composable with `dependsOn`. See [Wave Topology](../pipeline-reference.md#wave-topology). |
| `update.strategy` | No | `kustomize` | How to update image references in manifests. `kustomize`: edits the `images:` list of the environment's `kustomization.yaml` the way `kustomize edit set image` does. `helm`: patches the image tag at `update.helm.imagePathTemplate` in `update.helm.valuesFile`; one image per Bundle, so use one Bundle per chart image, or kustomize. `argocd`: patches the Argo CD Application's `spec.source.helm.valuesObject` directly, with no Git commit or PR. The API server rejects `argocd` with `approval: pr-review`, and a config or mixed Bundle fails before its first environment when any environment it promotes uses `argocd`; see [Argo CD native promotion](../argocd-native-promotion.md). `yaml`: sets any YAML paths, in any files of the environment directory, to a Bundle image's tag, digest or reference; see [The yaml update strategy](../pipeline-reference.md#the-yaml-update-strategy). |
| `update.helm.imagePathTemplate` | No | `.image.tag` | `helm` only. Dot path of the image tag in the values file. |
| `update.helm.valuesFile` | No | `values.yaml` | `helm` only. Values file to patch, relative to the environment `path`. |
| `update.helm.chartVersionFile` | No | `Chart.yaml` | `helm` only, for `chart` Bundles (from a [Helm Subscription](../subscription.md#promoting-a-chart-version)). File the chart version is written to, relative to the environment `path`. A chart Bundle fails at build in an environment whose strategy is not `helm`, or whose `layout` is `branch` (the render Job does not get the chart version yet). |
| `update.helm.chartVersionPath` | No | `.dependencies[name=<chart>].version` | `helm` only. [YAML path](../pipeline-reference.md#yaml-paths) of the chart version in `chartVersionFile` (`.helmCharts[name=podinfo].version`, `.spec.chart.spec.version`, `.spec.source.targetRevision`). The default is the umbrella chart's dependency named after the Bundle's chart; the step fails when there is none. |
| `approval` | No | `auto` | `auto`: push directly to the target branch, no PR. `pr-review`: open a PR with promotion evidence, wait for its merge: by a person, or by the SCM with `pr.merge.auto`. The step list is fixed when an environment's step starts: an edit applies to steps that start after it, so an environment already promoting finishes with the approval it started with and uses the new one from the next Bundle. A step that started as `auto` still pushes straight to the target branch after an edit to `pr-review`. The Bundle in flight still finishes: its Graph turns Ready once its steps are Verified and its gates pass, whether or not they opened a PR. |
| `agents` | No | (none) | The environment's ceiling for versions an agent created (their actor stamp says kind `agent`): `auto`, `approve` (a person of another party approves each one here) or `deny` (they never enter). The agent's AgentIdentity `autonomy` applies too, and the stricter wins; unset leaves the AgentIdentity's alone. kardinal adds a `kardinal-agents-<ceiling>` gate to every environment of an agent's version, so the wait shows like any gate. Versions people create are not affected. See [Actor-aware policy](../agentic/policy.md). |
| `health.type` | No | `resource` | Health verification adapter: `resource`, `argocd`, `flux`, `argoRollouts` or `flagger`. `delivery.delegate`, when set, takes precedence. There is no auto-detection. The step is Verified only when the adapter sees the promoted revision (commit or Bundle images) healthy. See [Health Adapters](../health-adapters.md). |
| `health.resource`, `health.argocd`, `health.flux`, `health.argoRollouts`, `health.flagger` | No | see [Health Check Defaults](../pipeline-reference.md#health-check-defaults) | Name and namespace of the object the adapter checks. `health.resource.kind` must be `Deployment`. Without `health.resource.namespace`, the `resource` check looks in the namespace named after the environment (environment `prod` checks namespace `prod`), not the Pipeline's namespace. |
| `health.timeout` | No | `10m` | Maximum time from the start of health checking to the first healthy check, and from the moment a `bake` window stops to the next healthy check. When it expires, it counts as a health failure and applies `onHealthFailure`; a step reconciled late checks once more before it fails. It does not cut a running `bake` window short. |
| `health.kubeconfigSecretRef` | No | — | `{name, key}` of a Secret in the Pipeline's namespace holding a kubeconfig (key default `kubeconfig`). The health check reads its object in that cluster. Inline credentials only; `exec`, `auth-provider` and file paths are refused. See [Remote Clusters](../health-adapters.md#remote-clusters). |
| `health.labelSelector` | No | (none) | `health.type=resource` only. When set, **every** Deployment that matches these labels must pass the health check, in the namespace named after the environment unless `health.resource.namespace` is set. No match is unhealthy. Example: `{"app": "nginx", "kardinal.io/pipeline": "nginx-demo"}`. When unset, a single Deployment named after the Pipeline is checked. Ignored for `argocd`, `flux`, `argoRollouts`, and `flagger`. |
| `delivery.delegate` | No | `none` | Progressive delivery delegation. `argoRollouts`: watch Argo Rollouts Rollout status after promotion. `flagger`: watch Flagger Canary status. `none`: instant deploy (rolling update). |
| `waitForMergeTimeout` | No | (none) | `pr-review` only. How long the step may wait for its PR to merge, as a Go duration (`24h`, `72h`). When it expires, the step is marked `Failed` and the controller closes the PR and deletes its head branch (`kardinal/<namespace hash>/<bundle>/<env>`), so a late merge cannot deliver the change: GitHub's API merges a closed PR whose branch is still there. A PR recorded merged before the deadline is never failed by it, also while the step waits for the SCM to name who merged it. Unset or `0` waits forever. |
| `pr.titleTemplate`, `pr.bodyTemplate` | No | the default title and body | `pr-review` only. Go templates over the Bundle and the environment that replace the PR title and body; a custom body keeps the evidence sections with the `evidence`, `provenanceTable`, `gatesTable`, `changeRequestsTable`, `verificationTable`, `upstreamTable` and `rollbackNotice` functions. See [Customising the PR](../pr-evidence.md#customising-the-pr). |
| `pr.labels`, `pr.reviewers`, `pr.teamReviewers`, `pr.assignees` | No | (none) | `pr-review` only. Up to 20 templates each; every line a template renders is one label, reviewer, team or assignee. Which providers apply which list: [PR controls](../scm-providers.md#pr-controls). |
| `pr.merge.auto` | No | `false` | `pr-review` only. Enable the SCM's auto-merge on the PR after it is opened: the SCM merges it once the required checks and reviews pass. kardinal turns it off while the Pipeline is paused or a gate is closed. A PR with nothing pending is left for a merge by hand unless `pr.merge.allowImmediate` is `true`, which merges it at once and skips review. `pr.merge.method` (`merge`, `squash` or `rebase`), `pr.merge.commitMessageTemplate` and `pr.merge.allowImmediate` need `auto`; the API server rejects them without it. See [Auto-merge while the step waits](../pr-evidence.md#auto-merge-while-the-step-waits). |
| `stepTimeoutSeconds` | No | (none) | Maximum seconds one built-in step (`git-clone`, `kustomize-set-image`, `open-pr`, ...) may run. The step is cancelled and the error is handled like any other step error: a retryable error is retried with backoff, then the PromotionStep is marked `Failed`. Minimum 1. Unset means no per-step timeout. |
| `bake.minutes` | No | (none) | Contiguous-healthy soak window in minutes. When set, the step must observe healthy deployment status *continuously* for this many minutes before transitioning to Verified. A check that is not healthy stops the window; it starts again at the next healthy check, and `health.timeout` bounds the wait for it. A Waiting check (the workload is changing, such as a canary paused at a step) is not an alarm under either policy. |
| `bake.policy` | No | `reset-on-alarm` | What to do when a check is unhealthy during the bake window. `reset-on-alarm`: stop the window, increment `status.bakeResets`, stay in HealthChecking. `fail-on-alarm`: immediately apply `onHealthFailure` policy. A release that keeps flapping between healthy and unhealthy fails under `reset-on-alarm` when no full window completes by the first window's start + `bake.minutes` + `health.timeout` (see [Timings and failures](../health-adapters.md#timings-and-failures)); `fail-on-alarm` fails it on the first unhealthy check. |
| `bake.maxDuration` | No | `bake.minutes` + `health.timeout` | Go duration (`36h`). The longest time from the first bake window's start (`status.bakeFirstStartedAt`) to a complete window. A window that stops after it, on an alarm or a Waiting check such as a paused canary, applies `onHealthFailure`. A value shorter than `bake.minutes` counts as `bake.minutes`. |
| `onHealthFailure` | No | `none` | What to do when `health.timeout` expires without a Healthy result, when the adapter reports a terminal failure (Deployment `ProgressDeadlineExceeded` from this promotion's rollout, Flagger `Failed`), or when health fails during bake with `policy: fail-on-alarm`. `none`: step → Failed (default behavior). `abort`: step → AbortedByAlarm; requires human intervention. `rollback`: create a rollback Bundle with the artifacts of the Bundle verified before the failing one in this environment; step → RollingBack, or AbortedByAlarm when there is nothing safe to roll back to (a step of a rollback Bundle → AbortedByAlarm instead, so rollbacks do not chain). See [Automatic Rollback](../rollback.md#automatic-rollback). |
| `hooks` | No | (none) | Jobs run once per Bundle in this environment: `phase: pre` before the promotion starts (migrations), `phase: post` after the health check passed and before the environment is Verified (integration tests). Each is `{name, phase, job, timeout}`, `job` a `batch/v1` JobSpec. At most 10. A failed pre hook fails the step before it changes anything; a failed post hook applies `onHealthFailure`. See [Pre- and Post-Deploy Hooks](../hooks.md). |
| `verification` | No | (none) | Argo Rollouts analysis after the health check: `{analysisTemplates: [{name, kind}], args: [{name, value}], inconclusive, timeout}`. One AnalysisRun per template, with the Bundle's `tag`, `image`, `environment` and more as args; the environment is Verified only when every run is `Successful`, and a failed run applies `onHealthFailure`. Needs Argo Rollouts installed: without it the Bundle fails. See [Analysis](../analysis.md). |

**Reserved and unsupported fields.** A `health.resource.kind` other than `Deployment` is not
implemented: a Bundle fails the environment's step before it changes anything in git.
`kardinal validate` reports it, and the controller sets the Pipeline's `Ready` condition to `False` with reason `NotImplemented` and the same message
(`kubectl get pipeline <name> -o jsonpath='{.status.conditions}'`). The API server rejects
`autoRollback` outright.

### spec.historyLimit

Number of finished Bundles (Verified, Failed or Superseded; Rejected Bundles are never deleted) to retain per Pipeline. Older ones are garbage-collected, oldest first, when a new Bundle is created. `kardinal rollback` can only target a retained Bundle. The Git PR history is permanent regardless of this setting.

Default: 50.

### spec.paused

When `true`, no PromotionStep of the Pipeline leaves `Pending`, and a step in `Promoting` holds before its next git step. Steps waiting for a PR merge or running a health check finish. The controller keeps a freeze PolicyGate named `freeze-<pipeline>` while the Pipeline is paused. `kardinal pause` / `kardinal resume` and the UI set this field. See [Pause and Resume](../pause.md).

Default: `false`. `spec.pausedFleets` (default: none) pauses fleet environments on their own: no further target of the fleet starts, for any version, and the targets in flight finish. `kardinal pause <pipeline> --env <fleet>` and `kardinal resume <pipeline> --env <fleet>` (and the UI) set it; changing it needs `update` on `pipelines/pause`. See [Pause one fleet](../pause.md#pause-one-fleet).

### spec.holds

Environments pinned to a version: what runs there (`kardinal hold`) or a rollback (`kardinal rollback --hold`, or the UI). Either writes them, and
`kardinal unhold` (or the UI) removes them. The controller also removes a hold at its
`expiresAt`. Each entry has these fields:

- `environment`
- `bundle`: the held Bundle (the rollback, or what ran there when `kardinal hold` made it)
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
[Roll back and hold](../rollback.md#roll-back-and-hold).

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
With `attestations` (new in v0.13.0, unreleased) it also verifies each image's in-toto attestations
(SLSA provenance, vulnerability scans with VEX, any predicate) and records them for gates to read
as `attestations.*`: [Attestations](../image-verification.md#attestations).
See [Image Signature Verification](../image-verification.md).

### spec.policyNamespaces

Extra namespaces to read PolicyGates from. It only adds: the org policy namespaces (the controller's `--policy-namespaces`, default `platform-policies`) and the Pipeline's namespace are always read. A gate found only through it is a team gate unless it is labelled `kardinal.io/scope: org`, and it never grants a skip. See [Policy Gates](../policy-gates.md).

Any other namespace listed must agree to be read (new in v0.13.0, unreleased): the label `kardinal.io/policy-shareable: "true"` on it, or the controller's `--policy-shareable-namespaces`. One that has not is still read for now, and the `PolicyNamespacesShared` condition names it; a later release refuses it ([Sharing gates with other namespaces](../policy-gates.md#sharing-gates-with-other-namespaces)).

Default: none.

## Status

The Pipeline reconciler writes what runs where onto the Pipeline, so `kubectl` alone answers it:

```
$ kubectl get pipelines
NAME     SUMMARY                                                       PHASE       PAUSED   AGE
my-app   1.5.0 live in test, uat; waiting for PR merge in prod (#42)   Promoting   false    3d

$ kubectl get pipelines -o wide      # adds ENVS
ENVS
test=1.5.0 uat=1.5.0 prod=1.4.0

$ kubectl get bundles
NAME          VERSION   TYPE    PIPELINE   PHASE       AGE
my-app-x7k2p  1.5.0     image   my-app     Promoting   12m
```

| Field | Meaning |
|---|---|
| `status.summary` | One sentence: each version, its state in words (the labels the CLI and UI use) and the environments it is in. A fleet's targets are counted under the fleet's name, and so are the environments of a wave of more than six (`7 of 9 in wave 2`): `7.2.0 live in canary and 27 of 42 pops; promoting in 5 pops; queued in 10 pops` (the first count of a group gives its size, `all 42 pops` when that is all of it). Other environments are named, at most six per clause, then `N more` before any group (`e1, …, e6, 2 more and 4 of 10 pops`), and so are a clause's PRs. It starts with `Not ready (<reason>)` when the spec is invalid, and says `paused` when the Pipeline is paused. A sentence that starts with a version keeps the version's case (`sha-9349a3f live in test`, `main live in test`); one that starts with words is capitalised. |
| `status.environments[]` | Per environment, in spec order (a fleet environment lists its targets): `bundle` and `version`, what runs there (the newest Bundle whose change landed); `incomingBundle` and `incomingVersion`, a newer Bundle on its way in; `state`, the PromotionStep state of the newest Bundle there (an API value: `Verified`, `WaitingForMerge`, …); `since`, when that state began; `blockedBy`, the gates holding the newest Bundle back; `held`, a rollback hold keeps the environment on `bundle`; `frozenBy`, the freeze (ChangeWindow name) that holds the newest Bundle's step there (new in v0.13.0, unreleased); `receipts`, the `count` and `bytes` of the environment's promotion receipts when receipts are on (new in v0.13.0, unreleased; [Receipts](../receipts.md#size)); `prURL`, the PR waiting for its merge. |
| `status.environmentSummary` | `env=version` per environment, `-` where nothing runs yet: the `ENVS` column. |
| `status.failedVersion` | When the newest version failed: its `bundle` and `version`, the `reason` and `message` of the Bundle's `InvalidSpec` or `Failed` condition (`GraphBuildFailed`, `GraphRejected`, `InvalidIntent`, `StepFailed`, …; the message says what fixes it where known), the `environment` whose step failed (empty when it failed before any step) and `since`. Cleared once a newer version exists. `status.summary` adds `<version> failed before it started (<reason>)` when no environment shows it; `kardinal why` leads with it and `kardinal status <pipeline>` prints it as an `ERROR` line. |
| `status.phase` | The newest Bundle's progress: `Ready` (Verified everywhere it went), `Promoting`, `Degraded` (a version failed) or `Unknown` (no Bundle yet). |
| `Ready` condition | Whether the **spec** is valid (reasons `Valid`, `ValidationFailed`, `NotImplemented`, …). It says nothing about promotions: a `Ready=True` Pipeline can be `Degraded`. |
| `PolicyNamespacesShared` condition | (new in v0.13.0, unreleased) Only while `spec.policyNamespaces` names a namespace that has not agreed to share its gates: `False`, reason `NotShared` (`PolicyNamespaceUnreadable` while a read fails, keeping the last list), naming those namespaces. `True`, reason `Shared`, once every namespace listed agreed or needs nothing; absent without `spec.policyNamespaces`. Its `observedGeneration` says which generation was checked (`kardinal doctor` reports a Pipeline not checked for its current one). It does not change `Ready`. |

These fields are for reading: no controller decision uses them. A step moving between running
states (promoting, waiting for its PR, checking health) shows within about 2 seconds: the
controller writes every such change of that window in one status update, so a large Pipeline is
not rewritten for each step. A step that ends (Live, failed), a version's phase and a gate's
result show at once. `kardinal why` (and the MCP `why` tool) reads the steps themselves, so it
names a step's state without that delay. `kubectl get kardinal` lists the
kinds users write (Pipelines, Bundles, PolicyGates, MetricChecks, ChangeWindows, Subscriptions,
NotificationHooks, ScmProviders, ClusterScmProviders); `kubectl get kardinal-all` adds the ones
the controller generates (PromotionSteps, PRStatuses, AuditEvents, …).
