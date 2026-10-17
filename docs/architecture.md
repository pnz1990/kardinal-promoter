# Architecture

kardinal-promoter is a Kubernetes-native controller. All state lives in etcd as CRDs. The CLI, UI, and webhook API are convenience layers that create and read CRDs — you can operate the entire system with `kubectl`.

---

## Components

```component-map
title: kardinal's components and what they talk to
caption: Red dots are what kardinal runs; grey ones are the cluster and the systems it talks to. An arrow is a network call, from the caller. Inside the cluster the components talk to each other only through the Kubernetes API server, and Argo CD or Flux applies the workloads through it too.
zones:
  people: {name: Outside the cluster, kind: outside}
  external: {name: Outside the cluster, kind: outside}
  cluster: {name: Kubernetes cluster, kind: cluster}
  system: {name: kardinal-system, in: cluster}
  kro: {name: kro-system, in: cluster}
  gitops: {name: argocd · flux-system, in: cluster}
  apps: {name: your namespaces, in: cluster}
components:
  people: {name: People, at: [0, 0], span: 2, zone: people, role: [the UI in a browser · the kardinal CLI and kubectl]}
  controller:
    name: kardinal-controller
    at: [0, 1]
    zone: system
    chip: Deployment
    kardinal: true
    role: [":8082 UI", ":8083 Bundle API, webhooks", ":8080 metrics"]
    items:
      - promotion: [bundle · pipeline, promotionstep, prstatus]
      - gates: [policygate, changewindow, scheduleclock]
      - checks: [metriccheck, imageverification]
      - jobs: [renderrun · hookrun]
      - sources: [subscription, scmprovider]
      - more: [rollbackpolicy, notificationhook, graphcleanup, auditretention]
  api: {name: Kubernetes API server, at: [0, 2], span: 2, zone: cluster, role: [every object is a CRD, in etcd · 9 ValidatingAdmissionPolicies]}
  kro: {name: kro, at: [0, 3], zone: kro, chip: Deployment, role: [the Graph controller]}
  agent: {name: Argo CD or Flux, at: [1, 3], zone: gitops, role: [syncs env branches]}
  workloads: {name: Workloads, at: [0, 4], zone: apps, role: [a namespace per env]}
  pipelines: {name: Render and hook Jobs, at: [1, 4], zone: apps, kardinal: true, role: ["render: git only", "hooks: your image"]}
  notify: {name: Notifications, at: [2, 0], zone: external, role: [Slack · Teams, webhooks]}
  ci: {name: CI, at: [2, 1], zone: external, role: [create-bundle, action]}
  metrics: {name: Metrics backends, at: [2, 2], zone: external, role: [Prometheus, Datadog · CloudWatch, New Relic]}
  registries: {name: Registries, at: [2, 3], zone: external, role: [OCI images, Helm charts]}
  git: {name: Git host, at: [2, 4], zone: external, role: [GitOps repo · PRs, GitHub · GitLab, Forgejo · Bitbucket, Azure DevOps]}
edges:
  - people -> controller: "HTTPS :8082"
  - people -> api: kubeconfig
  - ci -> controller: "HTTPS :8083 Bundle API"
  - controller -> api: watch · write
  - kro -> api: watch · create
  - agent -> workloads: apply (API)
  - controller -> notify: signed webhooks
  - controller <-> metrics: query · scrape
  - controller <-> registries: OCI API · webhooks
  - controller <-> git: git · PR API · webhooks
  - agent -> git: pull
  - pipelines -> git: git push
```

- **kardinal-controller** is one Deployment, usually in `kardinal-system`. It runs every
  reconciler in `pkg/reconciler` and serves four ports: the UI and its API on 8082; the Bundle
  API (`POST /api/v1/bundles`), the SCM webhooks (`/webhook/scm`) and the Subscription webhooks
  (`/webhook/subscriptions/`) on 8083; metrics on 8080; and the health probes on 8081.
- **Everything it decides is written to the Kubernetes API server.** That includes the Graph
  for each Bundle and the refresh annotation that asks Argo CD or Flux to sync now; with
  `update.strategy: argocd`, it also writes the Application's image override. The chart's
  ValidatingAdmissionPolicies run in the API server: they limit the controller's writes on
  Argo CD and Flux objects and who may change a Pipeline's holds, and they make approvals,
  overrides, rejections and new Bundles name their real author. There is no admission webhook.
- **kro** turns each Graph into its objects in the Pipeline's namespace: PromotionSteps,
  PolicyGate instances, PRStatuses, MetricChecks, HookRuns, RenderRuns, ImageVerifications
  and Argo Rollouts AnalysisRuns. A Graph with an approval gate also reads the Bundle's
  Approvals.
- **The Jobs run in the Pipeline's namespace.** A RenderRun's Job (`layout: branch`) clones,
  renders and pushes the rendered branch itself, and the render process refuses every request
  except git to the Pipeline's repository. Hook Jobs run your image.
- **The CLI talks to the Kubernetes API server**, with your kubeconfig; `kardinal dashboard`
  port-forwards to the UI. CI calls the Bundle API, or creates the Bundle with kubectl or the CLI.
- **Requests to addresses that namespace users write** go through an egress guard that refuses
  loopback, link-local and cloud metadata addresses. These are ScmProvider API URLs,
  Subscription sources, registries for signature checks, Prometheus and web MetricChecks,
  NotificationHooks and remote health clusters. `--egress-allowlist` limits NotificationHook,
  MetricCheck and Subscription requests to a list. Git runs in process (go-git); there is no
  git binary.

## What happens to a Bundle

```station-map
title: What the controller does with a Bundle
caption: Each station is a Kubernetes object; CI creates the Bundle through the API (POST /api/v1/bundles) or kubectl. kubectl, the kardinal CLI and the UI only read and write these objects.
lead: {into: Bundle, label: CI, note: creates it}
stations:
  Pipeline: {notes: [environments, update strategy]}
  Bundle: {notes: [image ref, provenance]}
  PolicyGate: {notes: [CEL expressions]}
  Graph: {name: kro Graph, notes: [one per Bundle, the promotion DAG]}
  PromotionStep: {notes: [one per environment, runs the steps]}
  gate instance: {name: PolicyGate instance, notes: [one per gate, CEL → status.ready]}
  next: {name: next PromotionStep, notes: [upstream Verified, and gates ready], lane: 0}
  rollback: {name: rollback Bundle, state: pending, notes: [on failure, onHealthFailure], lane: -1.5}
edges:
  - Pipeline -> Graph
  - Bundle -> Graph
  - PolicyGate -> Graph
  - Graph -> PromotionStep, gate instance
  - PromotionStep -> next, rollback
  - gate instance -> next
```

---

## Core Components

### Controller (`cmd/kardinal-controller`)

The controller manager runs these reconcilers:

| Reconciler | CRD | Responsibility |
|---|---|---|
| `BundleReconciler` | `Bundle` | Calls the translator to build a kro Graph; watches Graph status |
| `PipelineReconciler` | `Pipeline` | Validates pipeline config; computes aggregate pipeline status |
| `PromotionStepReconciler` | `PromotionStep` | Runs the steps engine: git-clone → image update → PR → health check |
| `PolicyGateReconciler` | `PolicyGate` (instances) | Evaluates CEL expression; writes `status.ready` |
| `MetricCheckReconciler` | `MetricCheck` | Queries Prometheus, Datadog, CloudWatch, New Relic or a JSON web API; writes result to status |
| `PRStatusReconciler` | `PRStatus` | Polls SCM for PR merge/close signal; writes `status.merged`, `status.mergeCommitSHA` (or `status.mergeCommitUnavailable` once it stops asking for it) and, for a 401/403/404/410, `status.pollError` |
| `RollbackPolicyReconciler` | `RollbackPolicy` | Reads one Bundle's PromotionSteps in one environment; creates a rollback Bundle at the failure threshold |
| `ScheduleClockReconciler` | `ScheduleClock` | Writes `status.tick` on a configurable interval for time-based gates |
| `ChangeWindowReconciler` | `ChangeWindow` | Writes `status.active` and requeues at the next window boundary |
| `SubscriptionReconciler` | `Subscription` | Polls OCI registries, Git repositories and Helm chart repositories; creates Bundles on new artifacts |
| `NotificationHookReconciler` | `NotificationHook` | Sends webhooks for the events it selects; writes delivery status |
| `HookRunReconciler` | `HookRun` | Runs a Pipeline hook's Job and records its result ([Pipeline hooks](hooks.md)) |
| `ImageVerificationReconciler` | `ImageVerification` | Checks the signatures of a Bundle's images (sigstore) and of a config Bundle's commit; writes the verdict to its status |
| `ScmProviderReconciler` | `ScmProvider`, `ClusterScmProvider` | Checks the provider's settings and Secrets; writes its `Ready` condition ([SCM providers](scm-providers.md)) |
| `RenderRunReconciler` | `RenderRun` | Runs a `layout: branch` environment's render in a sandboxed Job and records the rendered commit ([Rendered manifests](rendered-manifests.md)) |
| Audit retention (`pkg/reconciler/auditretention`) | `AuditEvent` | Deletes records past `audit.retention` (on by default; `audit.retention.enabled: false` turns it off) |
| Graph cleanup (`pkg/reconciler/graphcleanup`) | kro `Graph` | Lets a Graph in a terminating namespace go once kro can no longer tear it down; at startup, removes the health ref nodes and reader RoleBindings of earlier releases |

### Translator (`pkg/translator`) and Graph builder (`pkg/graph`)

The translator collects what a Bundle's Graph needs (the Pipeline, its PolicyGate templates, analysis templates, the SCM provider) and the builder in `pkg/graph` turns it into a [kro](https://github.com/kubernetes-sigs/kro) `Graph` spec. The Graph encodes the full promotion DAG:

- One node per `PromotionStep` (environment)
- PolicyGate instances and PRStatuses as `forEach` collection nodes, one item per gate or environment
- `readyWhen` expressions wired so the Graph controller advances nodes in dependency order

[Graph coverage](graph-coverage.md) lists what the Graph decides and what stays in a reconciler.

### kro Graph Controller (`kro-system`)

kardinal-promoter does **not** implement graph coordination itself. It delegates to the
[kro](https://github.com/kubernetes-sigs/kro) Graph controller (`kro.run/v1alpha1` `Graph`,
`GraphKind` feature gate), which manages the DAG lifecycle:

- Creates owned resources (PromotionSteps, PolicyGate instances, PRStatuses and the other node kinds listed under [Components](#components)) in topological order
- Advances to the next node when `readyWhen` is satisfied
- Stops the DAG on failure, preventing downstream promotions

> **Dependency note**: kardinal-promoter requires kro v0.10.0-rc.0+ with the Graph controller enabled.
> See [Installation](installation.md#install-kro) for setup.

### Steps Engine (`pkg/steps`)

The `PromotionStepReconciler` runs a sequence of built-in steps for each environment:

```station-map
title: The steps of a promotion
caption: The upper line runs only for approval pr-review.
stations:
  git-clone: {}
  kustomize-set-image: {}
  git-commit: {}
  git-push: {}
  open-pr: {notes: [pr-review only], lane: -1}
  wait-for-merge: {notes: [pr-review only], lane: -1}
  health-check: {lane: 0}
edges:
  - git-clone -> kustomize-set-image
  - kustomize-set-image -> git-commit
  - git-commit -> git-push
  - git-push -> open-pr, health-check
  - open-pr -> wait-for-merge
  - wait-for-merge -> health-check
```

`open-pr` and `wait-for-merge` run only for `approval: pr-review`. `update.strategy: helm`
uses `helm-set-image` instead of `kustomize-set-image`, config Bundles use `config-merge`,
mixed Bundles run `config-merge` and then the image update step,
and `update.strategy: argocd` runs only `argocd-set-image` and `health-check`
(`pkg/steps/defaults.go`). The sequence is picked when the step leaves Pending and recorded in
`status.steps`; every later reconcile runs the recorded list, not one rebuilt from the live
Pipeline.

Built-in step implementations:

| Step | Description |
|---|---|
| `git-clone` | Clones the GitOps repo into a work directory owned by this PromotionStep (one per namespace, Pipeline, Bundle and environment). For config Bundles it also checks out the `configRef` commit into a separate directory next to it |
| `kustomize-set-image` | Edits the environment's `kustomization.yaml` `images:` list the way `kustomize edit set image` does (no binary needed) |
| `render` | `layout: branch` only, in the controller: asks for the environment's RenderRun and waits for its result (the commit pushed, the DRY commit rendered). The render itself (`git-clone`, the image update, `render-manifests`, `git-commit`, `git-push`) runs in the RenderRun's sandboxed Job, never in the controller ([Rendered Manifests](rendered-manifests.md)) |
| `render-manifests` | `layout: branch` only, in the render Job (`kardinal-render`; it refuses to run anywhere else): renders the environment path of the DRY checkout (kustomize, or Helm for a chart), checks the rendered branch for drift against `.kardinal/rendered.yaml`, and writes one file per object into the rendered branch checkout that `git-commit` commits |
| `helm-set-image` | Updates `values.yaml` image tag for Helm-based repos |
| `argocd-set-image` | Patches the Argo CD Application's image override directly, with no Git commit (`update.strategy: argocd`) |
| `config-merge` | Copies the environment directory of the Bundle's `configRef` commit over the environment directory (config and mixed Bundles). Files deleted in the config commit are not deleted |
| `git-commit` | Commits the working tree changes. When nothing changed it records that, and the later steps skip the push and the PR |
| `git-push` | A sequence with `open-pr` (`pr-review` when the step started): force-pushes `kardinal/<namespace hash>/<bundle>/<env>`, so a re-run after a restart replaces the earlier push. Otherwise (`auto`): pushes the base branch; if it moved, replays the promotion's files onto the new head and pushes again (up to 6 times, no wait), then restarts the sequence from a fresh clone (at most 3 times per reconcile), then retries the step with jittered backoff. While a PR waits for its merge, a moved base branch rebuilds the PR branch on the new head |
| `open-pr` | Opens a pull request via the SCM provider with promotion evidence |
| `wait-for-merge` | Polls `PRStatus` until the PR is merged or closed |
| `health-check` | Queries Kubernetes Deployment readiness or ArgoCD/Flux/Rollouts/Flagger sync status. For `argocd` and `flux` it first asks the Application, or the Kustomization and its GitRepository, to sync the commit now, once ([Sync on promotion](health-adapters.md#sync-on-promotion)) |

There is no custom step engine: every environment runs one of these sequences. For image signature
checks and tests, see [Pipeline Reference: Image signatures and tests](pipeline-reference.md#image-signatures-and-tests).

### PolicyGate Evaluator (`pkg/reconciler/policygate`)

Evaluates CEL expressions against the promotion context, with the `json.*`, `maps.*`,
`lists.*` and `random.*` functions adapted from the
[kro CEL library](https://github.com/kubernetes-sigs/kro/tree/main/pkg/cel/library)
(`pkg/cel/library`) and the standard string extensions.

See [CEL Context Reference](reference/cel-context.md) for the full variable list.

### SCM Provider (`pkg/scm`)

Abstracts Git hosting operations. Current implementations:

| Provider | Status |
|---|---|
| GitHub | GA |
| GitLab | Beta |
| Forgejo, Gitea | Beta |
| Bitbucket Cloud, Azure DevOps, Bitbucket Data Center | Newer, less tested |

The controller's default provider is chosen with `--scm-provider`; a Pipeline can name an
`ScmProvider` or `ClusterScmProvider` instead (`spec.git.providerRef`). See [SCM Providers](scm-providers.md).

### Health Adapters (`pkg/health`)

Checks whether a promotion is healthy after merging:

| Adapter | Status |
|---|---|
| Kubernetes `Deployment` readiness | GA |
| ArgoCD `Application` sync status | GA |
| Argo Rollouts `Rollout` status | Beta |
| Flux `Kustomization` ready status | GA |
| Flagger `Canary` phase | Beta |

---

## Data Flow: Bundle → Verified

A Bundle's way to Verified, in order. Every step is a write to, or a watch on, the Kubernetes
API server; nothing passes between the components in memory.

1. CI asks the kardinal API to create a Bundle (image and pipeline), and the API creates the
   Bundle object.
2. The BundleReconciler is notified and creates the Bundle's kro Graph (the DAG spec).
3. kro creates the PolicyGate instances of the first environment, test.
4. The PolicyGateReconciler evaluates each one's CEL expression and writes `status.ready = true`
   when it passes.
5. Once every gate of test is ready, the PromotionStep for test resolves and kro creates it.
6. The PromotionStepReconciler runs its steps (image update, commit, PR when the environment
   reviews by PR, merge, health check) and writes `status.state = Verified`.
7. uat's nodes resolve and kro advances to uat. Steps 3 to 6 repeat for uat, and then for prod.
8. When every PromotionStep is Verified, the Bundle's `status.phase` is Verified.

---

## Bundle lifecycle

### When a Failed Bundle promotes again

A Bundle is Failed when a promotion step or health check failed, kro rejected the Graph, or the
Pipeline, the Bundle (its intent, or no images or config commit for its type) or a PolicyGate
cannot be built into a Graph (condition `InvalidSpec`, with the reason). It promotes again when
the failed step is retried or, for `InvalidSpec`, when the Pipeline changes or, for reason
`GraphBuildFailed`, a PolicyGate that applies to one of its environments changes.

### Bundle supersession

When a newer Bundle of the same type is created for the same Pipeline, an older Bundle that is
still waiting or promoting (`Available` or `Promoting`) is **superseded**. A `Failed` Bundle
stays `Failed`; if it would resume, a newer Bundle supersedes it instead. The new Bundle starts
promoting from the beginning, and the older one:

- The older Bundle's status transitions to `Superseded`, which is final
- None of its PromotionSteps pushes any more: right before every `git-push` a step reads its
  Bundle from the API server and fails ("promotion cancelled") if the Bundle is Superseded or
  rejected. A push to the environment's branch is also refused when a Bundle that supersedes it
  (newer, of the same type, neither rejected nor Failed) has already pushed there (each such push
  first records `status.outputs.pushIntent`), so an older image never lands over a newer one: that
  step ends `Superseded`, and so does its Bundle, which no failure count (Bundle failure,
  `maxUnavailable`, DORA, `onHealthFailure`) includes
- Its unfinished PromotionSteps are failed, and a PR one of them opened that is still
  open is closed with a comment and its head branch
  (`kardinal/<namespace hash>/<bundle>/<env>`) is deleted, so the closed PR cannot be merged
  later. If the SCM fails to close it or to delete its branch, the
  step keeps its state and retries after 10s, 20s, 40s, 80s and 2m, with the
  `SupersededCloseFailed` condition `True` and `status.nextRetryAt`; after the last retry it
  fails, and its message says to close the PR (or delete its branch) by hand
- Its Graph, PromotionSteps and PolicyGates are kept as history until the Graph is retired
  ([Graph retirement](#graph-retirement)), but the Graph creates no new PromotionStep,
  and its PolicyGates are no longer evaluated: they keep the status they had when the
  Bundle was superseded
- A step the Graph created just before the Bundle was superseded, and that never started,
  is failed with "superseded before this step started" and writes no AuditEvent
- Deleting the Bundle deletes its Graph and everything the Graph created, and closes, with a
  comment, a PR one of its steps opened that is still open

Supersession is tracked independently by Bundle type. A new `image` Bundle does not
supersede an in-flight `config` Bundle, and vice versa.

```bash
kardinal get bundles my-app
# VERSION                 TYPE    PHASE                       AGE
# 1.29.0 (my-app-7xk2p)   image   Replaced by newer version   10m
# 1.30.0 (my-app-9qd4s)   image   Promoting                   3m
```

### Graph retirement

kro keeps every Graph in memory, so kardinal does not keep the Graph of every finished Bundle.
Once a Bundle has finished, and every one of its PromotionSteps has settled (`Verified`, `Failed`,
`RollingBack`, `AbortedByAlarm` or `Superseded`, with no PR left to close), its Graph is **retired** after a delay
that starts when the last step settles:

| Bundle | Retired after (chart value, controller flag) |
|---|---|
| Superseded, or Verified and replaced in every environment by a newer Verified Bundle of the same type | 1m (`graph.retire.superseded`, `--graph-retire-superseded-after`) |
| Verified and still deployed in an environment | 1h (`graph.retire.verified`, `--graph-retire-verified-after`) |
| Failed, unless a newer Verified Bundle replaced it in every environment it touched (then the first row); or not Superseded with a step stopped by a health alarm (`AbortedByAlarm`, which waits for a person to resume or roll back) | 24h (`graph.retire.failed`, `--graph-retire-failed-after`) |

When a Bundle turns Verified, the older Verified and Failed Bundles of its Pipeline and type are
checked again, so one it replaced everywhere retires after the first delay. `0s` keeps those Graphs;
with all three at `0s` only the Bundles of Pipelines that set the annotation below are retired. A negative flag stops the controller at startup. The
annotation `kardinal.io/graph-retire-after: <duration>` on a Pipeline replaces all three delays for
its Bundles (`"0"` keeps them); a value that is not a duration is ignored, named in the
`GraphRetired` condition and in a Warning Event `InvalidRetireDelay`.

Retiring writes one record per PromotionStep to the Bundle's `status.retiredSteps` (name,
environment, step type, final state and message, PR URL, creation and Verified times, health check
expiry), sets the condition `GraphRetired` to `True`, and deletes the Graph. kro then deletes the
Graph's PromotionSteps, PolicyGate instances and PRStatuses. `GraphRetired` is `Unknown` with
reason `WaitingForSteps` while a step has not settled, and `False` with reason `Scheduled` while the
delay runs. A Bundle with more than 1,000 PromotionSteps, or whose records would take more than
512 KiB, keeps its Graph (reason `TooManySteps`).

What a retired Bundle keeps and loses:

- **Kept**: rollback and `kardinal promote` (which Bundle is deployed where, which were Verified),
  `kardinal history`, `kardinal metrics` and the Pipeline's `status.deploymentMetrics`, the
  Pipeline phase, `kardinal status`, `kardinal explain` and `kardinal get pipelines`, the UI's
  graph and step list (state, message and PR link per environment), `status.environments`, and
  the AuditEvents, which never belonged to the Graph.
- **Lost**: the PolicyGate instances (gate results and overrides; the AuditEvents keep each
  evaluation), the PRStatuses, a step's per-step detail (`status.steps[]`, outputs other than the
  PR URL) and its Kubernetes Events.
- A retired Bundle is final: a retired Failed Bundle no longer recovers, and a Pipeline change
  does not rebuild it. Roll back or create a new Bundle instead.

`spec.historyLimit` still decides how many finished Bundles, and so how many records, a Pipeline
keeps.

---

## How kardinal Relates to ArgoCD and Flux

kardinal-promoter is **GitOps-agnostic**: with the `kustomize` and `helm` update strategies
it only writes to Git. Instead of talking to Argo CD or Flux:

1. `PromotionStepReconciler` opens a Git pull request with the updated image reference.
2. A human (or automated process) merges the PR.
3. kardinal asks ArgoCD or Flux to sync the commit now (an annotation on the Application, or
   on the Kustomization and its GitRepository, once per step; `health.refreshTarget` names the
   object when the health check reads a Rollout, a Canary or a Deployment). It does so only for
   the Pipeline's own objects, those in its namespace or deploying there, and those
   `health.refresh.namespaces` admits ([Sync on promotion](health-adapters.md#sync-on-promotion)).
   Without that request, or when it fails or is skipped, they find the change at their next
   poll. They sync the cluster.
4. kardinal's **health check** then queries ArgoCD or Flux to confirm sync is complete
   before advancing the DAG.

This means kardinal works with any GitOps engine — or even without one (raw Kubernetes deployments).

The exception is `update.strategy: argocd`, which patches the Argo CD `Application`
directly instead of committing to Git. It needs the chart's
`rbac.argocdApplicationsWrite=true` (see [Argo CD native promotion](argocd-native-promotion.md)).

---

## State Management

All state is stored in Kubernetes CRDs:

| CRD | Purpose |
|---|---|
| `Pipeline` | Defines environments, update strategy, SCM config |
| `Bundle` | Immutable deployment unit; created by CI |
| `PromotionStep` | Per-environment promotion progress; owned by Graph |
| `PolicyGate` | Namespaced policy check: templates, and the per-Bundle instances the Graph creates |
| `PRStatus` | Tracks a promotion PR's open, merged or closed state |
| `RollbackPolicy` | Consecutive-failure rollback trigger for one Bundle in one environment (user-created; see [Rollback](rollback.md#rollbackpolicy)) |
| `MetricCheck` | Metric query (Prometheus, Datadog, CloudWatch, New Relic, web) with a pass/fail threshold, created by the user or, per promotion, by the Graph from a `perPromotion` template; gates read it as `metrics.<name>` |
| `ScheduleClock` | Writes `status.tick` on a configurable interval; enables time-based policy gates |
| `ChangeWindow` | Cluster-scoped blackout/recurring allow windows for pipeline promotions |
| `Subscription` | Watches OCI registries or Git repos; auto-creates Bundles on new artifacts |
| `NotificationHook` | Sends promotion events to a webhook URL |
| `AuditEvent` | Append-only record of promotion events (see [Security](guides/security.md#audit-logging)) |
| `Approval` | One person's approval decision on one Bundle in one environment, created by `kardinal approve` |
| `HookRun` | One run of a Pipeline hook's Job and its result ([Pipeline hooks](hooks.md)) |
| `RenderRun` | One render of a `layout: branch` environment in a sandboxed Job ([Rendered manifests](rendered-manifests.md)) |
| `ImageVerification` | The signature check of a Bundle's images or config commit |
| `ScmProvider`, `ClusterScmProvider` | SCM settings and credentials a Pipeline names with `spec.git.providerRef` ([SCM providers](scm-providers.md)) |

There is no database. The controller is **stateless**: it can be restarted at any time without
data loss, and every reconciler is idempotent. All state is recovered by re-reading CRDs.

---

## Further Reading

- [Graph Coverage](graph-coverage.md) — what runs on the kro Graph today and what does not yet
- [Concepts](concepts.md) — Bundles, Pipelines, PolicyGates explained
- [Policy Gates](policy-gates.md) — CEL expression reference
- [CEL Context Reference](reference/cel-context.md) — variables available in gate expressions
- [Installation](installation.md) — how to install kardinal-promoter
