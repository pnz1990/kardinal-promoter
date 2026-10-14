# Coming from Kargo

This page maps each Kargo concept to its kardinal counterpart, with the two side by side. To move
a Kargo project over step by step, see [Migrate from Kargo](guides/migrating-from-kargo.md).

The Kargo side follows the [Kargo documentation](https://docs.kargo.io/) for Kargo v1.12.
"Kargo Enterprise" is the commercial edition (Kargo on the Akuity Platform).

## The concepts at a glance

| Kargo | kardinal | In kardinal |
|---|---|---|
| Warehouse | [Subscription](subscription.md) | Watches an image registry, a Helm repository or a Git repository and creates a version for each new artifact |
| Freight | Version (a `Bundle`) | One immutable set of artifacts (images, a chart, a config commit) with its provenance |
| Stage | Environment | An entry of `Pipeline.spec.environments[]`; one Pipeline holds every environment of an application |
| Stage with several upstream Stages, `availabilityStrategy: All` | `dependsOn` with several environments | An environment starts once every environment it depends on is verified |
| `requiredSoakTime` | A soak gate, or `bake` | A gate on `upstream.<env>.soakMinutes`, or `bake.minutes` of continuous health in the environment itself |
| Auto-promotion (ProjectConfig `promotionPolicies`) | `approval: auto` (the default) | Every environment promotes on its own unless a gate, an approval or `approval: pr-review` holds it |
| Verification (AnalysisTemplates) | Health check, `verification`, MetricCheck | The health check always runs; AnalysisTemplates and metric gates are added per environment |
| Promotion template and steps | `update.strategy`, `layout`, hooks | A fixed promotion sequence per environment, with pre and post hooks as Jobs |
| Project | Namespace | A Pipeline, its versions, gates and Subscriptions live in one namespace |

## Warehouse to Subscription

A Warehouse subscribes to artifact sources and produces Freight. A kardinal Subscription watches
one source for one Pipeline and creates a version for each new artifact. A Warehouse with several
subscriptions becomes one Subscription per source.

=== "Kargo"

    ```yaml
    apiVersion: kargo.akuity.io/v1alpha1
    kind: Warehouse
    metadata:
      name: my-app
      namespace: my-project
    spec:
      subscriptions:
        - image:
            repoURL: ghcr.io/myorg/my-app
            semverConstraint: ^1.0.0
    ```

=== "kardinal"

    ```yaml
    apiVersion: kardinal.io/v1alpha1
    kind: Subscription
    metadata:
      name: my-app
      namespace: my-app
    spec:
      pipeline: my-app
      type: image
      image:
        registry: ghcr.io/myorg/my-app
        semverConstraint: ^1.0.0
        interval: 5m
    ```

`type: helm` watches a chart repository and `type: git` a branch, with `pathGlob` for Kargo's
`includePaths`. See [Subscription](subscription.md).

## Freight to version

Freight is a set of artifact references that moves from Stage to Stage. A kardinal version is a
`Bundle`: the images, chart or config commit it ships, with provenance (commit, CI run, author).
A Subscription creates it, or CI does:

=== "Kargo"

    ```bash
    # Freight comes from a Warehouse; promote it to a Stage
    kargo promote --project my-project --freight-alias wonky-wombat --stage test
    ```

=== "kardinal"

    ```bash
    # A version comes from a Subscription, or from CI
    kardinal ship my-app --image ghcr.io/myorg/my-app:1.4.0 --wait
    ```

A version cannot change once created: its gates and health checks were decided for those
artifacts. See [Concepts](concepts.md).

## Stage to environment

Each Kargo Stage is its own object that requests Freight from a Warehouse or from upstream Stages.
In kardinal one Pipeline lists every environment, and each environment follows the one before it
unless `dependsOn` says otherwise.

=== "Kargo"

    ```yaml
    apiVersion: kargo.akuity.io/v1alpha1
    kind: Stage
    metadata:
      name: uat
      namespace: my-project
    spec:
      requestedFreight:
        - origin:
            kind: Warehouse
            name: my-app
          sources:
            stages: [test]
    ```

=== "kardinal"

    ```yaml
    apiVersion: kardinal.io/v1alpha1
    kind: Pipeline
    metadata:
      name: my-app
      namespace: my-app
    spec:
      git:
        url: https://github.com/myorg/gitops-repo
        secretRef: { name: github-token }
      environments:
        - name: test
        - name: uat          # follows test
        - name: prod
          approval: pr-review
    ```

## Fan-in: aggregating upstream Stages with `dependsOn`

A Kargo Stage with several upstream Stages and `availabilityStrategy: All` takes Freight only once
it is verified in all of them. In kardinal, an environment that depends on several environments
starts only once the version is verified in every one:

=== "Kargo"

    ```yaml
    spec:
      requestedFreight:
        - origin: { kind: Warehouse, name: my-app }
          sources:
            stages: [prod-eu, prod-us]
            availabilityStrategy: All
    ```

=== "kardinal"

    ```yaml
    environments:
      - name: staging
      - name: prod-eu
        dependsOn: [staging]
      - name: prod-us
        dependsOn: [staging]
      - name: prod-global
        dependsOn: [prod-eu, prod-us]
    ```

`wave:` numbers group environments that promote in parallel, and `fleet:` expands one environment
into many targets with `maxConcurrent` and `maxUnavailable`. See
[Design a pipeline](pipeline-reference.md).

## `requiredSoakTime` to soak gates

Kargo's `requiredSoakTime` makes Freight wait in an upstream Stage before the next Stage may take
it. In kardinal a gate reads how long the version has been live and healthy upstream:

=== "Kargo"

    ```yaml
    sources:
      stages: [uat]
      requiredSoakTime: 30m
    ```

=== "kardinal"

    ```yaml
    apiVersion: kardinal.io/v1alpha1
    kind: PolicyGate
    metadata:
      name: uat-soak
      namespace: my-app
      labels:
        kardinal.io/applies-to: prod
    spec:
      expression: "upstream.uat.soakMinutes >= 30"
      message: "prod waits until the version has been live in uat for 30 minutes"
      recheckInterval: 1m
    ```

`upstream.<env>.soakMinutes` counts from the version's passing health check in that environment.
To require continuous health in the environment itself before it is Verified, set `bake.minutes`
on it: an unhealthy check restarts the window (`bake.policy: reset-on-alarm`, the default) or
applies the environment's `onHealthFailure` (`fail-on-alarm`). See [Policy gates](policy-gates.md).

## Auto-promotion

Kargo enables auto-promotion per Stage in the ProjectConfig. In kardinal, a version promotes on
its own into every environment unless something holds it: a gate, an approval gate, or
`approval: pr-review`, which opens a pull request and waits for its merge.

=== "Kargo"

    ```yaml
    apiVersion: kargo.akuity.io/v1alpha1
    kind: ProjectConfig
    metadata:
      name: my-project
      namespace: my-project
    spec:
      promotionPolicies:
        - stageSelector: { name: test }
          autoPromotionEnabled: true
    ```

=== "kardinal"

    ```yaml
    environments:
      - name: test            # approval: auto is the default
      - name: prod
        approval: pr-review   # a reviewed pull request
    ```

To stop automatic promotion for a while: `kardinal pause my-app` holds the whole pipeline, and
`kardinal hold my-app prod --reason "audit"` keeps one environment on what it runs. See
[Pause a pipeline](pause.md).

## Verification to health, MetricCheck and Analysis

Kargo verifies a Stage with Argo Rollouts AnalysisTemplates after a promotion. kardinal always
runs a health check ([Health checks](health-adapters.md): a Deployment, an Argo CD Application, a
Flux Kustomization, an Argo Rollout or a Flagger Canary), then the AnalysisTemplates you name, and
holds the next environment on metric gates:

=== "Kargo"

    ```yaml
    spec:
      verification:
        analysisTemplates:
          - name: success-rate
    ```

=== "kardinal"

    ```yaml
    environments:
      - name: prod
        health:
          type: argocd
        verification:            # the same AnalysisTemplates (needs Argo Rollouts)
          analysisTemplates:
            - name: success-rate
    ```

Without Argo Rollouts, a [MetricCheck](metric-checks.md) (Prometheus, Datadog, CloudWatch,
New Relic or a JSON web query) feeds a gate: `metrics["success-rate"].result == "Pass"`. See
[Verify a promotion](verify.md) and [Analysis](analysis.md).

## Promotion templates and steps to update strategies and hooks

A Kargo Stage composes built-in steps (`git-clone`, `kustomize-set-image`, `git-commit`,
`git-push`, `argocd-update`, ...) in its `promotionTemplate`. kardinal runs a fixed sequence per
environment, chosen by `update.strategy` and `layout`, and runs your own Jobs as hooks:

=== "Kargo"

    ```yaml
    promotionTemplate:
      spec:
        steps:
          - uses: git-clone
          - uses: kustomize-set-image
          - uses: git-commit
          - uses: git-push
          - uses: argocd-update
    ```

=== "kardinal"

    ```yaml
    environments:
      - name: prod
        update:
          strategy: kustomize    # kustomize | helm | yaml | argocd
        hooks:
          - name: migrate
            phase: pre           # before the promotion
            job:
              template:
                spec:
                  restartPolicy: Never
                  containers:
                    - name: migrate
                      image: ghcr.io/myorg/my-app-migrations:1.4.0
    ```

| Kargo step | kardinal |
|---|---|
| `kustomize-set-image` | `update.strategy: kustomize` |
| `helm-update-chart`, or `yaml-update` on a Helm values file | `update.strategy: helm` |
| `yaml-update` on other files | `update.strategy: yaml` |
| `kustomize-build`, `helm-template` | `layout: branch`, rendered in a sandboxed Job ([Rendered manifests](rendered-manifests.md)) |
| `argocd-update` | `update.strategy: argocd` ([Promote via Argo CD without git](argocd-native-promotion.md)) |
| `git-open-pr`, `git-wait-for-pr` | `approval: pr-review`, with the PR evidence body ([Read PR evidence](pr-evidence.md)) |
| A custom container step | A `pre` or `post` hook Job ([Hooks](hooks.md)) |

kardinal has no custom git or render steps: an environment that needs its own build or render
sequence fits Kargo's promotion templates better.

## Kargo Enterprise features that are open source in kardinal

These ship in kardinal under the Apache 2.0 license:

| Feature | Kargo | kardinal |
|---|---|---|
| Automatic rollback | Kargo Enterprise (beta): `autoRollback` in ProjectConfig `promotionPolicies` | `onHealthFailure: rollback`, or a RollbackPolicy |
| Approvals with a quorum and verified identity | Kargo Enterprise: the `wait-for-approval` step | Approval gates with `required`, `allowedUsers`, `allowedGroups` and `excludeAuthor` |
| Promotion windows with break-glass | Kargo Enterprise: promotion windows; break-glass is an administrator editing or removing the window | ChangeWindows, and `kardinal override` with a reason, an expiry and an audit record |
| Notifications | Kargo Enterprise: MessageChannels and EventRouters | `NotificationHook`: Slack, Microsoft Teams, JSON, CloudEvents or a templated body |

### Automatic rollback

When the health check, the bake, a post hook or an analysis fails after the change merged, the
environment rolls back to the version that ran there before. The rollback is a new version that
goes through the same gates and pull request flow. See [Roll back](rollback.md).

```yaml
environments:
  - name: prod
    onHealthFailure: rollback   # rollback | abort | none
```

### Approvals with verified identity and `excludeAuthor`

An approval gate waits for N distinct allowed people. The API server checks every Approval
against the caller's Kubernetes identity, and `excludeAuthor` keeps the version's verified creator
from approving it. See [Require approvals](approvals.md).

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: two-approvers
  namespace: my-app
  labels:
    kardinal.io/applies-to: prod
spec:
  expression: "true"
  approval:
    required: 2
    allowedGroups: [release-managers]
    excludeAuthor: true
```

```bash
kardinal approve my-app prod --comment "checked the migration"
```

### ChangeWindows with break-glass

A cluster-scoped ChangeWindow describes a freeze or the hours promotions are allowed, and any gate
that reads it holds while it is active. To break glass, override the gate: the override names you,
verified by the API server, needs a reason, ends on its own and writes an AuditEvent. See
[Policy gates](policy-gates.md#changewindow-attributes) and
[Override a gate](overrides.md).

```yaml
apiVersion: kardinal.io/v1alpha1
kind: ChangeWindow
metadata:
  name: q4-holiday-freeze
spec:
  type: blackout
  start: "2026-12-20T00:00:00Z"
  end: "2027-01-02T00:00:00Z"
  reason: "Q4 holiday freeze"
---
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: holiday-freeze
  namespace: my-app
  labels:
    kardinal.io/applies-to: prod
spec:
  expression: '!changewindow.isBlocked("q4-holiday-freeze")'
  message: "No prod changes during the Q4 holiday freeze"
```

```bash
kardinal override my-app prod --gate holiday-freeze \
  --reason "P0 hotfix, INC-4521" --expires-in 2h
```

### NotificationHook

A NotificationHook posts promotion events (a version verified, failed or superseded, a gate
blocked, a step failed, a PR opened, an approval waiting) to Slack, Microsoft Teams or any HTTP
endpoint, signed with HMAC when you set a key. See [Notify](notifications.md).

```yaml
apiVersion: kardinal.io/v1alpha1
kind: NotificationHook
metadata:
  name: releases
  namespace: my-app
spec:
  webhook:
    secretRef: { name: slack-webhook }
  format: slack
  events: [Bundle.Verified, Bundle.Failed, PolicyGate.Blocked]
```

## More that maps across

### Supersession

The most upvoted open Kargo issue asks to supersede queued promotions
([akuity/kargo#3108](https://github.com/akuity/kargo/issues/3108)). In kardinal, a newer version
of the same type supersedes an older one that has not finished: the older version stops, its open
pull requests are closed, and an older image never lands over a newer one. Supersession is always
on; `spec.maxConcurrentPromotions` caps how many versions promote at once. See
[Bundle supersession](architecture.md#bundle-supersession).

### Reject

`kardinal reject` marks a version as never to be promoted again: its unfinished steps are
cancelled, its open PRs closed, rollbacks never return to it, and a Subscription that sees one of
its artifacts again creates no version for it. The rejection names who made it, verified by the
API server. See [Reject a version](reject.md).

```bash
kardinal reject my-app-v1-29-0 --reason "CVE-2026-1234 in the base image"
```

### Webhook receivers

Kargo's webhook receivers make a Warehouse discover new artifacts at once. A Subscription with
`spec.webhook` does the same for Docker Hub, GHCR, Harbor, Quay, Artifactory or a generic signed
sender, at `POST /webhook/subscriptions/<namespace>/<subscription>/<provider>`. See
[Webhooks](subscription.md#webhooks).

### Sharding

Kargo assigns Stages to shards with `spec.shard`. kardinal shards by namespace: label a namespace
`kardinal.io/shard=<name>` and run one controller installation per shard. See
[Sharding controllers](sharding.md).

## Next

- [Migrate from Kargo](guides/migrating-from-kargo.md), step by step
- [Comparison](comparison.md) of kardinal, Kargo and GitOps Promoter
- [Quickstart](quickstart.md)
