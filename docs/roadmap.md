# Roadmap

This page describes what is currently available in kardinal-promoter and what is planned for future releases.

!!! info "Contributing"
    Roadmap priorities shift based on user feedback. Open a [GitHub Discussion](https://github.com/pnz1990/kardinal-promoter/discussions) if a feature matters to your use case.

---

## Currently Available (v0.7.0+)

> v0.7.0 released 2026-04-17. Highlights: `health.labelSelector` WatchKind mode (O(1) incremental health checks), krocodile `81c5a03` upgrade (P0 correctness fixes), reactive PromotionStep reconciler (Watch on PRStatus/PolicyGate), graph-first cleanup.

All of the following are implemented and shipped:

**Core promotion engine**
- Pipeline CRD with DAG-native stage ordering and fan-out
- Bundle CRD with image and config artifact types
- PolicyGate CRD with CEL expressions (kro library: schedule, soak, metrics, upstream, changewindow)
- PromotionStep reconciler — full git-clone → kustomize/helm → commit → PR → merge → health loop
- Graph-first architecture via krocodile

**Manifest update strategies**
- `kustomize` — `kustomize edit set-image`
- `helm` — patch values.yaml at configurable path
- `config-merge` — GitOps config-only promotions

**Health adapters**
- `resource` — Kubernetes Deployment condition
- `argocd` — Argo CD Application health + sync
- `flux` — Flux Kustomization Ready
- `argoRollouts` — Argo Rollouts Rollout phase
- `flagger` — Flagger Canary phase

**SCM providers**
- GitHub (webhooks + polling)
- GitLab (webhooks + polling)
- Forgejo/Gitea (webhooks + polling)

**Gates and policies**
- CEL context: schedule, bundle metadata, upstream soak time, metrics, changewindow
- MetricCheck CRD (PromQL-based metric injection into CEL)
- Org-level gates (mandatory, cannot be bypassed by teams)
- Team-level gates (additive)
- SkipPermission gates

**K-01: Contiguous healthy soak**
- `bake.minutes` + `bake.policy: reset-on-alarm` on environment spec
- `BakeElapsedMinutes` and `BakeResets` tracked in PromotionStep status
- Bake timer resets on health alarm when `policy=reset-on-alarm`

**K-02: Pre-deploy gate type**
- `when: pre-deploy` on PolicyGate spec — evaluated before `git-clone` starts
- Blocks PromotionStep in `Waiting` state without opening a PR

**K-03: Auto-rollback with ABORT vs ROLLBACK distinction**
- `onHealthFailure: rollback | abort | none` on environment spec
- Rollback creates a new Bundle at the previous image version
- Abort freezes the deployment, requiring human decision

**K-04: ChangeWindow CRD**
- Cluster-scoped blackout (`type: blackout`) and recurring (`type: recurring`) windows
- CEL context: `changewindow["window-name"]` evaluates to `true` when the window is active/blocking
- `ScheduleClock` CRD drives time-based re-evaluation via Kubernetes watch events

**K-05: Deployment metrics**
- `Bundle.status.metrics` — commitToFirstStageMinutes, commitToProductionMinutes, bakeResets, operatorInterventions
- `kardinal metrics` CLI command displays per-Bundle DORA metrics

**K-06: Wave topology**
- `wave: N` field on environment spec — Wave N stages automatically depend on all Wave N-1 stages
- Composable with explicit `dependsOn`

**K-07: Integration test step**
- Built-in `integration-test` step runs a Kubernetes Job as part of the promotion
- Watches completion; triggers `onFailure: abort | rollback` policy on failure

**K-08: PR review gate**
- `bundle.pr["staging"].isApproved` and `bundle.pr["staging"].approvalCount` in CEL context
- Reads `PRStatus` CRD; no external SCM API calls in the reconciler hot path

**K-09: `kardinal override` with audit record**
- `kardinal override` patches PolicyGate with a time-limited override
- Override record written to Bundle status and surfaced in PR evidence body

**K-10: Subscription CRD (passive Bundle creation)**
- `Subscription` CRD definition complete; reconciler creates Bundles on new artifacts

    !!! warning "Source watchers are stubs"
        OCI and Git source watchers always return `Changed: false`. Bundle auto-creation
        from Subscriptions does not work yet. See [#491](https://github.com/pnz1990/kardinal-promoter/issues/491)
        and [#493](https://github.com/pnz1990/kardinal-promoter/issues/493).

**K-11: Cross-stage history CEL functions**
- `upstream.staging.soakMinutes` — elapsed minutes since upstream Verified
- `upstream.staging.recentSuccessCount` — successful promotions in last N days
- `upstream.staging.recentFailureCount` — failed promotions in last N days
- `upstream.staging.lastPromotedAt` — RFC3339 timestamp of last Verified promotion

**Operations**
- `RollbackPolicy` CRD + automated rollback PR
- Pause/resume (`Bundle.spec.paused`)
- Supersession for concurrent Bundles
- Multi-cluster via kubeconfig Secrets

**CLI** — full command set: `get`, `explain`, `create`, `rollback`, `approve`, `pause`, `resume`, `history`, `policy`, `diff`, `logs`, `metrics`, `version`, `override`

**UI** — full control plane UI: fleet health dashboard, pipeline operations view, per-stage bake countdown, bundle promotion timeline, policy gate detail panel, release efficiency metrics bar, in-UI actions (approve/pause/resume/rollback/override)

**Distributed mode** — shard routing: `shard:` field on Pipeline environments routes PromotionSteps to the correct controller instance. The `kardinal-agent` standalone binary (for running in spoke clusters without inbound connectivity) is near-term (#508).

**Multi-tenant self-service** — ApplicationSet + Pipeline template bootstrap; team onboarding by committing a folder to Git; org PolicyGates automatically inherited; namespace isolation enforced by RBAC.

**Subscription CRD + source watchers** — `OCIWatcher` polls container registries; `GitWatcher` polls Git branches; Bundles are created automatically on new images or commits. No CI pipeline integration needed.

**Pipeline deployment metrics** — `Pipeline.status.deploymentMetrics` persisted by the PipelineReconciler: `rolloutsLast30Days`, `p50CommitToProdMinutes`, `p90CommitToProdMinutes`, `autoRollbackRate`.

**`changewindow.isAllowed()` / `changewindow.isBlocked()` CEL functions** — named-argument helpers for ChangeWindow gates:

```
changewindow.isAllowed("business-hours")    # true when the window is NOT currently blocking
changewindow.isBlocked("holiday-freeze")    # true when the window IS currently blocking
```

**Per-step progress observability** — `PromotionStep.status.steps[]` exposes each step (git-clone, kustomize-set-image, git-commit, open-pr, wait-for-merge, health-check) with individual state, start time, and duration. (#630)

**`kardinal get pipelines --watch`** — real-time promotion progress with live table refresh. (#629)

**`kardinal doctor`** — pre-flight cluster health check: validates CRD installation, krocodile, RBAC, and GitHub token. (#607)

**Shell completion** — bash, zsh, fish, and PowerShell completion via `kardinal completion <shell>`. (#606)

**PrometheusRule CRD in Helm chart** — 6 pre-built alerting rules: promotion stuck, high rollback rate, policy gate blocked, SCM errors. (#621)

---

## Near-Term (v0.8.0 — planned)

### `kardinal-agent` standalone binary

Lightweight `kardinal-agent` binary for spoke clusters behind firewalls. The reconciler is
already shard-aware; the agent is a thin wrapper around it.

---

## UI — Full Control Plane (shipped v0.5.0–v0.6.0)

All 7 UI issues (#462–#468) are implemented and shipped.

### Currently available

- DAG visualization with per-node health states
- Bundle timeline with env status chips, PR links, pagination
- PolicyGate expression display with CEL highlighting
- HealthChip status chips
- Live polling with staleness indicator
- **Fleet-wide health dashboard (#467)** — home page sortable table: blocked count, CI red, interventions pending, recent activity feed
- **Pipeline operations view (#462)** — per-pipeline list with sortable health columns: inventory age, last merge, blockage time, interventions/deploy
- **Per-stage workflow detail (#463)** — step list, bake countdown with health overlay, override history
- **In-UI actions (#464)** — approve gates, pause/resume bundles, rollback, override with mandatory reason, restart failed steps
- **Release efficiency metrics bar (#465)** — inline P50/P90 commit-to-prod, rollback rate, operator interventions
- **Bundle promotion timeline (#466)** — full artifact history with diff links, rollback records, override audit trail
- **Policy gate detail panel (#468)** — CEL expression highlighting, current variable values, blocking duration, override history

---

## Not in Scope

These concerns are intentionally delegated to dedicated tools — kardinal integrates with them rather than duplicating them:

| Out of scope | Delegate to |
|---|---|
| Traffic splitting / canary weights | Argo Rollouts, Flagger |
| Load test gating | k6, Locust in CI |
| SAST / vulnerability scan gating | Trivy, govulncheck in CI |
| Code coverage gating | Codecov, SonarQube in CI |
