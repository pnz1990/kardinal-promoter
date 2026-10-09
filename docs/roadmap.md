# Roadmap

This page describes what is currently available in kardinal-promoter and what is planned for future releases.

!!! info "Contributing"
    Roadmap priorities shift based on user feedback. Open a [GitHub issue](https://github.com/pnz1990/kardinal-promoter/issues) if a feature matters to your use case.

---

## Currently Available (v0.9.0)

> v0.9.0 released 2026-10-02. kardinal runs on upstream kro Graph (v0.10.0-rc.0), and unfinished features (distributed mode, regions, custom steps, PromotionTemplate, the admission webhook) are removed. For the full list and the upgrade steps see the [changelog](changelog.md).

All of the following are implemented and shipped:

**Core promotion engine**
- Pipeline CRD with DAG-native stage ordering and fan-out
- Bundle CRD with image and config artifact types
- PolicyGate CRD with CEL expressions over bundle, schedule, environment, upstream soak, metrics and changewindow context, plus the `json`, `maps`, `lists` and `random` functions adapted from kro's CEL library
- PromotionStep reconciler — full git-clone → kustomize/helm → commit → PR → merge → health loop
- Graph-first architecture via kro Graph (see [Graph Coverage](graph-coverage.md) for what is not on the Graph yet)

**Manifest update strategies** (`update.strategy`)
- `kustomize` — edits the `images:` list in `kustomization.yaml`
- `helm` — patches the image tag in `values.yaml` at a configurable path
- `argocd` — patches the Argo CD Application, with no git commit (`approval: auto` only)

Config and mixed Bundles run the `config-merge` step, which copies the environment directory from the `configRef` commit.

**Health adapters**
- `resource` — Kubernetes Deployment condition
- `argocd` — Argo CD Application health + sync
- `flux` — Flux Kustomization Ready
- `argoRollouts` — Argo Rollouts Rollout phase
- `flagger` — Flagger Canary phase

**SCM providers** (one per controller, chosen with `--scm-provider`)
- GitHub (webhooks + polling)
- GitLab (webhooks + polling)
- Forgejo/Gitea (webhooks + polling)
- Bitbucket Cloud and Azure DevOps (webhooks + polling; newer and less tested)

**Gates and policies**
- CEL context: schedule, bundle metadata, upstream soak time, metrics, changewindow
- MetricCheck CRD (PromQL-based metric injection into CEL)
- Org-level gates (mandatory: a team Pipeline cannot remove or weaken them; `kardinal override` can force-pass one for a limited time with a recorded reason, so restrict `patch` on PolicyGates in team namespaces)
- Team-level gates (additive)
- SkipPermission gates

**K-01: Contiguous healthy soak**
- `bake.minutes` + `bake.policy: reset-on-alarm` on environment spec
- `BakeElapsedMinutes` and `BakeResets` tracked in PromotionStep status
- Bake timer resets on health alarm when `policy=reset-on-alarm`

**K-02: Gates re-checked before a step starts**
- Every gate holds back the creation of its environment's PromotionStep, and is re-checked right before `git-clone` starts: the step starts only on a ready result evaluated at or after the step was created. Otherwise it stays `Pending` with the message `waiting for gate <name>` (or `... to be re-evaluated`) and no PR is opened
- `when` on the PolicyGate spec is deprecated and has no effect: `pre-deploy` and `post-deploy` behave the same

**K-03: Auto-rollback with ABORT vs ROLLBACK distinction**
- `onHealthFailure: rollback | abort | none` on environment spec
- Rollback creates a new Bundle at the previous image version
- Abort freezes the deployment, requiring human decision

**K-04: ChangeWindow CRD**
- Cluster-scoped blackout (`type: blackout`) and recurring (`type: recurring`) windows
- CEL context: `changewindow["window-name"]` evaluates to `true` when the window is active/blocking
- `ScheduleClock` CRD drives time-based re-evaluation via Kubernetes watch events

**K-05: Deployment metrics**
- `Bundle.status.metrics` — commitToProductionMinutes, bakeResets, operatorInterventions
- `kardinal metrics` CLI command displays per-Bundle DORA metrics

**K-06: Wave topology**
- `wave: N` field on environment spec — Wave N stages automatically depend on all Wave N-1 stages
- Composable with explicit `dependsOn`

**K-07: Integration test step (removed)**
- The `integration-test` step could run only from a custom step sequence (`spec.environments[].steps`), which kardinal does not support, so it is removed
- Run tests as an Argo CD PostSync hook with `health.type: argocd`, or gate on a `MetricCheck` (see [Image signatures and tests](pipeline-reference.md#image-signatures-and-tests))

**K-08: PR review gate**
- `bundle.pr["staging"].isApproved` and `bundle.pr["staging"].approvalCount` in CEL context
- Reads `PRStatus` CRD; no external SCM API calls in the reconciler hot path

**K-09: `kardinal override` with a recorded reason**
- `kardinal override` adds a time-limited override to the gate instances of the stage
- The override stays in the instance's `spec.overrides[]`; while it is active the gate reason (`OVERRIDDEN by ...`) shows in the PR evidence gate table and in `kardinal explain`

**K-10: Subscription CRD (passive Bundle creation)**
- `Subscription` CRD definition complete; reconciler creates Bundles on new artifacts
- `OCIWatcher` polls OCI registries for new image tags matching a regex filter (#491)
- `GitWatcher` polls Git branches for new commits via Git Smart HTTP protocol (#493)

**K-11: Cross-stage history CEL functions**
- `upstream.staging.soakMinutes` — elapsed minutes since upstream Verified
- `upstream.staging.recentSuccessCount` — Verified promotions among the pipeline's last 10 Bundles
- `upstream.staging.recentFailureCount` — Failed promotions among the pipeline's last 10 Bundles
- `upstream.staging.lastPromotedAt` — RFC3339 timestamp of last Verified promotion

**Operations**
- `RollbackPolicy` CRD + automated rollback PR
- Pause/resume (`Pipeline.spec.paused`)
- Supersession for concurrent Bundles
- Multi-cluster through an Argo CD or Flux hub (see [Multi-Cluster](distributed-mode.md); `health.cluster` kubeconfig Secrets are not supported)

**CLI** — `get`, `explain`, `status`, `create`, `promote`, `rollback`, `pause`, `resume`, `override`, `history`, `audit`, `diff`, `logs`, `metrics`, `policy`, `validate`, `doctor`, `init`, `refresh`, `delete`, `dashboard`, `completion`, `version` (see [CLI Reference](cli-reference.md))

**UI** — embedded control plane UI: fleet health bar and pipeline operations table, pipeline lane and DAG views, bundle promotion timeline with bundle comparison, policy gates panel and gate details (CEL expression, last evaluation), release efficiency metrics bar, and actions: create bundle, pause/resume, promote, roll back. Overriding a gate is CLI-only (`kardinal override`)

**Multi-tenant self-service** — ApplicationSet + Pipeline template bootstrap; team onboarding by committing a folder to Git; org PolicyGates automatically inherited; namespace isolation enforced by RBAC.

**Pipeline deployment metrics** — `Pipeline.status.deploymentMetrics` persisted by the PipelineReconciler: `rolloutsLast30Days`, `p50CommitToProdMinutes`, `p90CommitToProdMinutes`, `autoRollbackRateMillis`, `operatorInterventionRateMillis`.

**`changewindow.isAllowed()` / `changewindow.isBlocked()` CEL functions** — named-argument helpers for ChangeWindow gates:

```
changewindow.isAllowed("business-hours")    # true when the window is NOT currently blocking
changewindow.isBlocked("holiday-freeze")    # true when the window IS currently blocking
```

**Per-step progress observability** — `PromotionStep.status.steps[]` exposes each step (git-clone, kustomize-set-image, git-commit, open-pr, wait-for-merge, health-check) with individual state, start time, and duration. (#630)

**`kardinal get pipelines --watch`** — real-time promotion progress with live table refresh. (#629)

**`kardinal doctor`** — pre-flight cluster health check: the controller's `kardinal-version` ConfigMap, the kardinal CRDs, the kro controller Pod and Graph CRD, the controller's SCM token (`GITHUB_TOKEN` on the controller Deployment, and the Secret it comes from), and optionally one Pipeline (`--pipeline`). (#607)

**Shell completion** — bash, zsh, fish, and PowerShell completion via `kardinal completion <shell>`. (#606)

**PrometheusRule CRD in Helm chart** — 5 pre-built alerting rules: controller down, high reconcile error rate, Bundle reconciler stalled, work-queue backlog, slow PolicyGate reconciles. (#621)

---

## On main for v0.10.0 (unreleased)

Merged since v0.9.0; they ship with v0.10.0. See the [changelog](changelog.md) "Unreleased" section.

- `NotificationHook` with native Slack (Block Kit) and Microsoft Teams (Adaptive Card) bodies, templated bodies, Secret-backed auth, more events and an egress allowlist (#1474)
- DORA change failure rate and time to restore, with documented per-step timings (#1488)
- OpenAPI description of the REST API (`GET /api/v1/openapi.json`) and ServiceAccount token guidance (#1500)
- Per-owner SCM circuit breakers that survive token reload, one comment on PRs kardinal closes, and `scm.allowedRepositories` (#1483)
- Gate instances and PRStatuses as Graph `forEach` collections; gate status written only on change (#1496)
- A warning when a git Secret lacks `kardinal.io/referenceable` (enforced in v0.11) (#1508)
- Kubernetes 1.30 or newer required (#1426)

## In progress for v0.10.0

Open pull requests; each moves to the list above when it merges.

- Subscriptions: private registries and repos, Helm charts, tag filters, `pathGlob`, inbound webhooks (#1478)
- Approval gates with quorum and verified identity (#1510); verified override identity (#1503); reject a Bundle (#1489)
- Per-environment PR controls: templates, labels, reviewers, auto-merge (#1477); gate results as SCM commit statuses (#1518)
- GitHub App and ssh git auth (#1491); Bitbucket Data Center (#1501); several SCM providers per controller (#1517)
- Many Pipelines on one branch (#1504); controller sharding by namespace (#1505); a compact Graph for 500 environments (#1516); retiring finished Graphs (#1527)
- Pre- and post-deploy hooks (#1493); Argo Rollouts analysis (#1502); MetricCheck Datadog, CloudWatch, New Relic and web (#1479); image signature verification (#1521)
- Rendered manifests with `layout: branch` (#1515); the generic YAML update strategy (#1498)
- Remote-cluster health through kubeconfig Secrets (#1495); OpenTelemetry tracing (#1499); AuditEvent retention (#1523)
- UI: fleet board and step timings (#1519), Bundle type badges and keyboard navigation (#1524), approvals and rejected Bundles (#1525)
- Planned, no PR yet: fleets of targets (#1457), rollback with an environment hold (#1528), SCM metrics (#1529), an API access log (#1530), signed notifications (#1531)

## UI — Full Control Plane (shipped v0.5.0–v0.6.0)

The UI work from #462–#468 shipped in v0.5.0–v0.6.0. This is what the UI shows and does today.

### Currently available

- DAG visualization with per-node health states
- Bundle timeline: the 10 newest Bundles as chips colored by phase (the selected and comparison Bundles stay visible); click a chip to show that Bundle, Shift-click another and press Compare to compare the two
- PolicyGate expression display with CEL highlighting
- HealthChip status chips
- Live polling with staleness indicator
- **Fleet-wide health bar (#467)** — on the home page: counts of blocked, CI red (a failed step), and promoting pipelines; click a count to filter the list
- **Pipeline operations view (#462)** — sortable pipeline table: status, blocking gates, failed steps, inventory age, last merge
- **Per-stage detail (#463)** — click an environment for the steps the controller runs, their conditions, Kubernetes events, and elapsed time
- **In-UI actions (#464)** — create a bundle, pause/resume a pipeline, promote an environment whose upstream environments are Verified, roll back a Verified environment. Pause, resume, promote, and roll back ask for confirmation first
- **Release efficiency metrics bar (#465)** — over the last 10 bundles: mean time from bundle creation to the last environment's health check, rollback rate, deploys to the last environment
- **Bundle promotion timeline (#466)** — the 10 newest bundles, colored by phase; shift-click a second bundle and press Compare to see images, environments, and provenance side by side
- **Policy gates (#468)** — a panel with each gate of the bundle on screen, its state, and its CEL expression; click a gate for the highlighted expression, when it was last evaluated, and a syntax check

Not in the UI: overriding a gate (use `kardinal override`), the bake countdown, and gate override history (see `kardinal explain` or `kubectl get policygate <name> -o yaml`).

---

## Planned

- kro v0.10.0 (v0.9.1, #1424)
- `layout: branch`: promote rendered manifests (#1271)
- `kardinal override` writes an AuditEvent with the cluster identity (#1286)
- Secret-backed auth for MetricCheck (#1267, in #1479)
- `rollback` and `promote --env` without re-running upstream environments (#1311)
- `kardinal approve`: a real gate bypass, or removal (#1309)

---

## Not in Scope

These concerns are intentionally delegated to dedicated tools — kardinal integrates with them rather than duplicating them:

| Out of scope | Delegate to |
|---|---|
| Traffic splitting / canary weights | Argo Rollouts, Flagger |
| Load test gating | k6, Locust in CI |
| SAST / vulnerability scan gating | Trivy, govulncheck in CI |
| Code coverage gating | Codecov, SonarQube in CI |
