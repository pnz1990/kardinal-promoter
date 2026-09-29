# Roadmap

This page describes what is currently available in kardinal-promoter and what is planned for future releases.

!!! info "Contributing"
    Roadmap priorities shift based on user feedback. Open a [GitHub Discussion](https://github.com/pnz1990/kardinal-promoter/discussions) if a feature matters to your use case.

---

## Currently Available (v0.8.1+)

> v0.8.1 released 2026-04-17. Includes supply chain hardening (trivy, cosign, SBOM, SLSA) and DX improvements. For the full feature list see the [changelog](changelog.md).

All of the following are implemented and shipped:

**Core promotion engine**
- Pipeline CRD with DAG-native stage ordering and fan-out
- Bundle CRD with image and config artifact types
- PolicyGate CRD with CEL expressions (kro library: schedule, soak, metrics, upstream, changewindow)
- PromotionStep reconciler — full git-clone → kustomize/helm → commit → PR → merge → health loop
- Graph-first architecture via kro Graph (see [Graph Coverage](graph-coverage.md) for what is not on the Graph yet)

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
- Watches completion; a failed or timed-out Job fails the promotion (there is no per-step `onFailure` policy)

**K-08: PR review gate**
- `bundle.pr["staging"].isApproved` and `bundle.pr["staging"].approvalCount` in CEL context
- Reads `PRStatus` CRD; no external SCM API calls in the reconciler hot path

**K-09: `kardinal override` with audit record**
- `kardinal override` patches PolicyGate with a time-limited override
- Override record written to Bundle status and surfaced in PR evidence body

**K-10: Subscription CRD (passive Bundle creation)**
- `Subscription` CRD definition complete; reconciler creates Bundles on new artifacts
- `OCIWatcher` polls OCI registries for new image tags matching a regex filter (#491)
- `GitWatcher` polls Git branches for new commits via Git Smart HTTP protocol (#493)

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

**UI** — embedded control plane UI: fleet health bar and pipeline operations table, pipeline lane and DAG views, bundle promotion timeline with bundle comparison, policy gates panel and gate details (CEL expression, last evaluation), release efficiency metrics bar, and actions: create bundle, pause/resume, promote, roll back. Approving a bundle and overriding a gate are CLI-only (`kardinal approve`, `kardinal override`)

**Distributed mode** — shard routing: `shard:` field on Pipeline environments routes PromotionSteps to the correct controller instance. The `kardinal-agent` standalone binary for spoke clusters is available (PR #886).

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

**`kardinal doctor`** — pre-flight cluster health check: validates CRD installation, the kro Graph controller, RBAC, and GitHub token. (#607)

**Shell completion** — bash, zsh, fish, and PowerShell completion via `kardinal completion <shell>`. (#606)

**PrometheusRule CRD in Helm chart** — 6 pre-built alerting rules: promotion stuck, high rollback rate, policy gate blocked, SCM errors. (#621)

---

## UI — Full Control Plane (shipped v0.5.0–v0.6.0)

The UI work from #462–#468 shipped in v0.5.0–v0.6.0. This is what the UI shows and does today.

### Currently available

- DAG visualization with per-node health states
- Bundle timeline with env status chips, PR links, pagination
- PolicyGate expression display with CEL highlighting
- HealthChip status chips
- Live polling with staleness indicator
- **Fleet-wide health bar (#467)** — on the home page: counts of blocked, CI red (a failed step), promoting, and full-CD pipelines; click a count to filter the list
- **Pipeline operations view (#462)** — sortable pipeline table: status, blocking gates, failed steps, inventory age, last merge, CD level
- **Per-stage detail (#463)** — click an environment for the steps the controller runs, their conditions, Kubernetes events, and elapsed time
- **In-UI actions (#464)** — create a bundle, pause/resume a pipeline, promote an environment whose upstream environments are Verified, roll back a Verified environment. Pause, resume, promote, and roll back ask for confirmation first
- **Release efficiency metrics bar (#465)** — over the last 10 bundles: mean time from bundle creation to the last environment's health check, rollback rate, deploys to the last environment
- **Bundle promotion timeline (#466)** — the 10 newest bundles, colored by phase; shift-click a second bundle to compare images, environments, and provenance side by side
- **Policy gates (#468)** — a panel with each gate of the bundle on screen, its state, and its CEL expression; click a gate for the highlighted expression, when it was last evaluated, and a syntax check

Not in the UI: approving a bundle and overriding a gate (use `kardinal approve` and `kardinal override`), the bake countdown, and gate override history (see `kardinal explain` or `kubectl get policygate <name> -o yaml`).

---

## Not in Scope

These concerns are intentionally delegated to dedicated tools — kardinal integrates with them rather than duplicating them:

| Out of scope | Delegate to |
|---|---|
| Traffic splitting / canary weights | Argo Rollouts, Flagger |
| Load test gating | k6, Locust in CI |
| SAST / vulnerability scan gating | Trivy, govulncheck in CI |
| Code coverage gating | Codecov, SonarQube in CI |
