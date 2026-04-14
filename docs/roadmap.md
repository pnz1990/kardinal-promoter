# Roadmap

This page describes what is currently available in kardinal-promoter and what is planned for future releases.

!!! info "Contributing"
    Roadmap priorities shift based on user feedback. Open a [GitHub Discussion](https://github.com/pnz1990/kardinal-promoter/discussions) if a feature matters to your use case.

---

## Currently Available (v0.4.0)

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

**UI** — embedded React DAG visualization with gate states, bundle timeline, health chips

**Distributed mode** — controller + shard agents

---

## Near-Term (v0.5.0 — active development)

### Subscription source watchers

The `Subscription` CRD is implemented but source watchers are stubs:

- **OCI image watcher** ([#491](https://github.com/pnz1990/kardinal-promoter/issues/491)) — use `google/go-containerregistry` to poll registries for new tags
- **Git watcher** ([#493](https://github.com/pnz1990/kardinal-promoter/issues/493)) — use `go-git` to fetch the latest commit SHA on a watched branch

Until these land, use `kardinal create bundle` or the CI webhook to create Bundles.

### Pipeline aggregate deployment metrics

`kardinal metrics` aggregates DORA stats in-memory from CRD reads.
[#498](https://github.com/pnz1990/kardinal-promoter/issues/498) moves this computation to
`PipelineReconciler` so that `Pipeline.status.deploymentMetrics` is persisted and available
to the UI trend chart:

```yaml
status:
  deploymentMetrics:
    rolloutsLast30Days: 12
    p50CommitToProdMinutes: 420
    p90CommitToProdMinutes: 960
    autoRollbackRate: 0.08
```

### `changewindow.isAllowed()` / `changewindow.isBlocked()` CEL functions

These named-argument CEL functions are now implemented (#506) as a cleaner alternative to map-access syntax:

```
changewindow.isAllowed("business-hours")    # true when the window is NOT currently blocking
changewindow.isBlocked("holiday-freeze")    # true when the window IS currently blocking
```

Both syntaxes work and are semantically equivalent:

```
# Equivalent expressions — both pass when holiday-freeze is inactive:
!changewindow["holiday-freeze"]
!changewindow.isBlocked("holiday-freeze")
changewindow.isAllowed("holiday-freeze")
```

---

## Planned (v0.6.0+)

### Flat DAG compilation ([#496](https://github.com/pnz1990/kardinal-promoter/issues/496))

Replace the in-process step mini-scheduler with a full flat DAG of `PromotionStepTask`
Graph nodes — each step (`git-clone`, `kustomize-set-image`, etc.) becomes a separate CRD
node with its own `readyWhen` expression. This is the final major Graph-first architecture
improvement.

### Library-based kustomize and git ([#494](https://github.com/pnz1990/kardinal-promoter/issues/494), [#495](https://github.com/pnz1990/kardinal-promoter/issues/495))

Replace `exec.Command("kustomize")` with `sigs.k8s.io/kustomize` library and
`exec.Command("git")` with `go-git` library. Eliminates subprocess dependencies and
improves performance and portability.

---

## UI — From Status Display to Control Plane

The current UI shows pipeline state. The target is a control plane where operators can understand and act without the CLI.

### Currently available

- DAG visualization with per-node health states
- Bundle timeline (basic)
- PolicyGate expression display
- HealthChip status chips
- Live polling with staleness indicator

### Planned: UI as control plane (issues #462–#468)

**Fleet-wide health dashboard (#467)** — home page shows all pipelines in a sortable table: blocked count, CI red count, interventions pending. Recent activity feed on the side.

**Pipeline operations view (#462)** — per-pipeline list with health columns: inventory age, last merge, blockage time, interventions/deploy, failed steps. Sortable, filterable, color-coded.

**Per-stage workflow detail (#463)** — bake countdown with health overlay, integration test pass rates, override history, alarm events that reset bake.

**In-UI actions (#464, priority/critical)** — approve gates, pause/resume bundles, rollback, override a gate with mandatory reason, restart failed steps. A user must be able to operate kardinal entirely from the UI during an incident.

**Release efficiency metrics (#465)** — inline metrics bar on pipeline detail: inventory age, P50/P90 commit-to-prod, rollback rate, operator interventions. Sparkline chart for trends.

**Bundle promotion timeline (#466)** — full artifact history: what version is in each environment, when was it deployed, what changed, who approved overrides, rollback records.

**Policy gate detail (#468)** — per-gate expansion showing current CEL variable values, evaluation history, time until unblocked.

---

## Not in Scope

These concerns are intentionally delegated to dedicated tools — kardinal integrates with them rather than duplicating them:

| Out of scope | Delegate to |
|---|---|
| Traffic splitting / canary weights | Argo Rollouts, Flagger |
| Load test gating | k6, Locust in CI |
| SAST / vulnerability scan gating | Trivy, govulncheck in CI |
| Code coverage gating | Codecov, SonarQube in CI |
