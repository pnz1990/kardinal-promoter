# Changelog

All notable changes to kardinal-promoter are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

**DX improvements, Prometheus metrics, dark/light mode, URL routing**

### Added

- **Shell completion** — `kardinal completion bash|zsh|fish|powershell` (#731)
- **Color output for `kardinal explain`** — `--color` flag, auto-detected TTY; Pass=green, Block=red, Pending=yellow (#730)
- **Dark/light mode** — system-aware theme with manual toggle in the embedded UI (#734)
- **Prometheus metrics** — `kardinal_bundles_total{phase}`, `kardinal_steps_total{type,result}`, `kardinal_gate_evaluations_total{result}`, `kardinal_pr_duration_seconds` histogram emitted from reconcilers (#726)
- **`kardinal validate`** — validate Pipeline and PolicyGate YAML files offline; checks CEL syntax and schema (#713)
- **`kardinal status`** — show controller health, CRD versions, and resource summary (#715)
- **Improved explain error** — `kardinal explain <pipeline> --env <bogus>` now lists available environments (#717)
- **Improved circular dependency error** — cycle path displayed in full (#711)
- **`--dry-run` for `kardinal create bundle`** — print what would be created without submitting; useful for CI validation (#741)
- **URL routing in embedded UI** — selecting a pipeline, node, or bundle diff panel updates the URL hash fragment; page reload and back/forward navigation restore selection (#742)

### Changed

- **UI CSS theme tokens** — migrated 206 hardcoded hex color values to CSS custom properties (`--color-*` tokens); consistent theming across dark/light mode (#738)

---

## [v0.8.1] — 2026-04-17

**Security release: supply chain hardening — trivy, cosign, SBOM, SLSA, krocodile scan**

---

## [v0.8.0] — 2026-04-17

**Audit log, SCM circuit breaker, Bundle Watch node, Graph-first cleanup, DX improvements**

---

## [v0.7.0] — 2026-04-17

**WatchKind O(1) health checks, krocodile 81c5a03 upgrade, reactive PromotionStep reconciler, graph-first cleanup**

### Added

- **WatchKind health nodes** — `health.labelSelector` on Pipeline environments switches from Watch (O(n) full list per event) to WatchKind (O(1) incremental cache). Requires krocodile `745998f`+ (#652)
- **Kargo migration guide** — concept mapping, side-by-side Pipeline vs Kargo YAML, 7-step migration walkthrough in `docs/guides/` (#640)
- **Operations runbook expanded** — PolicyGate debugging, SCM failure modes, RBAC issues, krocodile restarts, performance tuning added (#639)
- **Bundle image diff in NodeDetail** — UI compares the current bundle's image against the previous bundle for that environment; closes a Kargo parity gap (#638)
- **Per-step progress observability** — `PromotionStep.status.steps[]` exposes each step with individual state, start time, and duration (#630)
- **`kardinal get pipelines --watch`** — real-time promotion progress with live table refresh (#629)
- **PrometheusRule CRD** — 6 pre-built alerting rules in Helm chart: promotion stuck, high rollback rate, policy gate blocked, SCM errors (#621)
- **UI: conditions summary and reason** — NodeDetail shows Kubernetes Conditions table with reason column; improved empty-state onboarding (#529, #530)
- **UI: Kubernetes events stream** — timestamped event history per PromotionStep in NodeDetail (#560)
- **UI: cross-environment error aggregation** — groups PromotionStep failures by type across environments; shows affected count (#564)
- **krocodile upgraded to `745998f`** — Decorator bootstrap primitive, Definition compile-time type inference, forEach array format support, DAG finalizer guard for non-resource nodes (#614)

### Fixed

- **Bundle reconciler watches Pipeline changes** — Graph is regenerated when Pipeline spec changes (new environments, updated policyNamespaces, changed git config). Previously Pipeline changes were invisible to in-flight Bundles (#634)
- **Subscription deduplication under HA** — uses label selector (`kardinal.io/source-digest`) instead of status field comparison; safe under concurrent reconciles and multiple controller replicas (#636)
- **CEL documentation accuracy** — corrected false claims about `pkg/cel/NewCELEnvironment()` (does not exist) and `schedule.*` (map variable, not CEL library function) in design docs and code comments (#631)
- **Shell completion** — bash, zsh, fish, and PowerShell completion scripts via `kardinal completion <shell>` (#606)
- **`kardinal doctor`** — pre-flight cluster health check: validates CRD installation, krocodile, RBAC, and GitHub token before first use (#607)
- **Graceful shutdown** — controller drains in-flight reconcile loops on SIGTERM; no promotion steps interrupted by pod restarts (#605)
- **PodDisruptionBudget + topology spread** — minAvailable: 1 PDB and `topologySpreadConstraints` in Helm chart for HA deployments (#598)
- **krocodile bundled in Helm chart** — single `helm install` now installs both kardinal-promoter and the krocodile Graph controller; no separate `hack/install-krocodile.sh` step needed (#590)
- **Library-based git operations** — replaced `exec.Command("git")` with `go-git` library (`#517`). Controller no longer requires a `git` binary. Improves portability (distroless images) and performance.
- Controller `/tmp` mount — `emptyDir` volume added for git-clone with `readOnlyRootFilesystem: true` (#609)
- `policy simulate` now searches all namespaces — org-level gates in `platform-policies` were never found
- `pkg/cel` standalone CEL evaluator eliminated — evaluation moved inline to PolicyGate reconciler
- Rollback PR title and body now include rollback notice and the `kardinal/rollback` label

---

## [v0.6.0] — 2026-04-14

**Live-cluster validation infrastructure, J7 multi-tenant self-service, OCI/Git source watchers, pipeline deployment metrics**

### Added

- **Multi-tenant self-service (J7)** — ApplicationSet + Pipeline template bootstrap; team onboarding via Git directory; org PolicyGates automatically inherited (#489)
- **OCI + Git source watchers** — `OCIWatcher` and `GitWatcher` Subscription reconcilers poll registries and Git branches, creating Bundles on new images/commits (#491, #493)
- **Pipeline deployment metrics** — `Pipeline.status.deploymentMetrics` aggregated by `PipelineReconciler`: `rolloutsLast30Days`, `p50CommitToProdMinutes`, `p90CommitToProdMinutes`, `autoRollbackRate` (#498)
- **`changewindow.isAllowed()` / `changewindow.isBlocked()` CEL functions** — named-argument helpers for ChangeWindow gates (#506)
- **krocodile upgraded to `948ad6c`** — DNS-1123 node ID validation, drift timers (30 min), propagation hash includes `propagateWhen` state
- **Cardinal logo** — added across docs site, UI sidebar, and README

### Fixed

- `kardinal-promoter` controller image rebuilt correctly when Graph CR is deleted externally (#490)
- PDCA live-cluster validation workflow: fixed pipeline name mismatch, missing `platform-policies` namespace, missing controller install step (#514)
- CI: `enforce_admins: true` on branch protection; 8 required status checks; concurrency guards on Docs and E2E workflows (#513)

---

## [v0.5.0] — 2026-04-13

**Pipeline Expressiveness (K-Series), Enterprise UI Control Plane, krocodile upgrade**

### Added

- **K-01: Contiguous healthy soak** — `bake.minutes` + `bake.policy: reset-on-alarm` on environment spec; `BakeElapsedMinutes` and `BakeResets` tracked in PromotionStep status
- **K-02: Pre-deploy gate type** — `when: pre-deploy` on PolicyGate spec; blocks PromotionStep in `Waiting` state before `git-clone` starts
- **K-03: Auto-rollback with ABORT vs ROLLBACK distinction** — `onHealthFailure: rollback | abort | none` per environment
- **K-04: ChangeWindow CRD** — blackout and recurring allowed-hours windows; `changewindow["name"]` CEL function evaluates to `true` when the window is active/blocking
- **K-05: Bundle.status.metrics** — commitToFirstStageMinutes, commitToProductionMinutes, bakeResets, operatorInterventions; `kardinal metrics` CLI command
- **K-06: Wave topology** — `wave: N` field on environment spec; Wave N automatically depends on all Wave N-1 stages
- **K-07: Integration test step** — built-in `integration-test` step runs a Kubernetes Job as part of the promotion sequence
- **K-08: PR review gate** — `bundle.pr["staging"].isApproved` and `.approvalCount` in CEL context via PRStatus CRD
- **K-09: `kardinal override` with audit record** — emergency gate override with mandatory reason + time limit; override record in Bundle status and PR evidence body
- **K-10: Cross-stage history CEL** — `upstream.<env>.soakMinutes`, `.recentSuccessCount`, `.recentFailureCount`, `.lastPromotedAt` in gate expressions
- **UI control plane** — all 7 UI issues shipped (#462–#468): fleet health dashboard, pipeline ops view, per-stage bake countdown, in-UI actions (pause/resume/rollback/override), release metrics bar, bundle timeline, policy gate detail panel

### Fixed

- krocodile upgraded to `948ad6c` — DNS-1123 node ID validation, drift timers, propagation hash improvements
- `changewindow.isAllowed()` / `changewindow.isBlocked()` CEL helpers added alongside the map-style access

---

## [v0.4.0] — 2026-04-12

**Distributed Mode, Argo Rollouts delegation, graph purity, K-series features**

### Added

- **Distributed mode** — `--shard` flag routes PromotionSteps to matching shard agents; supports multi-cluster deployments where each spoke cluster runs its own agent
- **Argo Rollouts delivery delegation** — `delivery.delegate: argo-rollouts` in Pipeline env spec hands off rollout progression to an existing `Rollout` resource
- **GitLab + Forgejo/Gitea SCM providers** — `scm.provider: gitlab` and `scm.provider: forgejo` in Pipeline spec
- **PRStatus CRD** — makes PR merge/close signal observable by the Graph (eliminates 6 GitHub API call paths from the reconciler hot path)
- **RollbackPolicy CRD** — auto-rollback threshold comparison moved to dedicated reconciler
- **Graph purity milestone** — all 41 krocodile-independent logic leaks eliminated (see `docs/design/11-graph-purity-tech-debt.md`)
- **K-01–K-11** — all Pipeline Expressiveness features (see v0.5.0 above for full list; initial implementation in this release)

### Fixed

- Pause enforcement via `bundle.status.paused` (eliminates in-memory pause state)
- Step cleanup on pipeline deletion no longer leaves orphaned PromotionSteps
- PolicyGate re-evaluation after TTL now fires correctly when graph resumes

---

## [v0.3.0] — 2026-04-12

**Observability: embedded UI, PR evidence, GitHub Actions**

### Added

- **Embedded React UI** — promotion DAG visualization with 6-state health chips, CEL expression display, live polling with staleness indicator, blocked-gate banner
- **PR evidence body** — structured markdown in every prod PR: image digest, CI run link, gate results, upstream soak time
- **kardinal diff** — `kardinal diff <bundle-a> <bundle-b>` shows artifact delta
- **kardinal approve** — approve a Bundle bypassing upstream gate requirements
- **kardinal metrics** — DORA-style promotion metrics (deployment frequency, lead time, fail rate)
- **kardinal refresh / dashboard / logs** — operational CLI commands
- **History command** — `kardinal history <pipeline>` shows previous promotions

### Fixed

- DAG graph deduplicates gate nodes (was showing duplicate PolicyGate cards)
- Pipeline phase uses `status.phase` not condition reason for display
- Explain command deduplicates gate output when multiple instances match

---

## [v0.2.1] — 2026-04-12

**Graph Purity: all krocodile-independent logic leaks eliminated**

### Added

- **PRStatus CRD** — replaces in-reconciler GitHub API polling for PR state
- **RollbackPolicy CRD** — moves auto-rollback threshold logic out of PromotionStepReconciler
- **ScheduleClock CRD** — writes `status.tick` on a configurable interval to drive time-based policy gate re-evaluation via real Kubernetes watch events; replaces the `ctrl.Result{RequeueAfter}` timer loop pattern

### Fixed

- `time.Now()` calls moved outside reconciler hot paths into CRD status writes
- Cross-CRD status mutations eliminated — each reconciler writes only to its own CRD
- `exec.Command()` in reconciler replaced with library call

---

## [v0.2.0] — 2026-04-11

**Workshop 1 Complete — first end-to-end validated release**

### Milestone

kardinal-promoter now executes the [AWS Platform Engineering on EKS workshop](https://catalog.workshops.aws/platform-engineering-on-eks/en-US/30-progressiveapplicationdelivery/40-production-deploy-kargo) end-to-end on a live kind cluster.

### Added

- **MetricCheck CRD** — Prometheus-backed policy gates: block promotions when error rate > threshold
- **Custom promotion steps** — HTTP webhook steps for extensible promotion workflows
- **Auto-rollback** — configurable failure threshold triggers rollback PR after N consecutive health failures
- **Pause/resume** — `kardinal pause/resume <pipeline>` halts in-flight promotions
- **Policy simulate** — `kardinal policy simulate` evaluates gates without creating a Bundle
- **Policy test** — `kardinal policy test` validates CEL syntax offline
- **Policy list** — lists active PolicyGates scoped to a pipeline/environment
- **Promote command** — `kardinal promote` creates a Bundle from the last verified image
- **Config Bundle type** — promotes Git commit SHAs through the same pipeline as image Bundles
- **Rendered manifests step** — `pre-render` strategy generates environment-specific YAML

### Fixed

- kind E2E infrastructure (`make setup-e2e-env`) sets up krocodile + ArgoCD + test/uat/prod namespaces
- `kardinal get pipelines` shows per-environment status columns
- `kardinal explain` shows active PolicyGates with CEL expression and current value

---

## [v0.1.0] — 2026-04-11

**Foundation: CRDs, Controller, Graph Integration, PolicyGate**

### Added

- **Go module scaffold** — directory layout, Makefile, CI pipeline (build/lint/test/vet)
- **CRD types** — Pipeline, Bundle, PolicyGate, PromotionStep (kubebuilder markers, deep copy, validation)
- **Controller manager** — BundleReconciler, PipelineReconciler, PromotionStepReconciler, PolicyGateReconciler
- **Helm chart** — controller deployment, RBAC, CRDs packaged for OCI registry
- **Graph integration** — kro Graph builder and translator: Pipeline → Graph spec
- **PolicyGate CEL evaluator** — `!schedule.isWeekend`, `upstream.uat.soakMinutes >= 30`, kro CEL library
- **SCM provider** — GitHub: push branch, open PR, detect merge, post comments
- **Health adapters** — Kubernetes Deployment readiness, ArgoCD Application sync, Flux Kustomization
- **Steps engine** — kustomize-set-image, helm-set-image, git-commit, open-pr, wait-for-merge, health-check
- **CLI foundation** — `kardinal get pipelines/bundles/steps`, `kardinal explain`, `kardinal rollback`, `kardinal version`, `kardinal init`
- **Embedded React UI** — scaffolded with Vite + React 19, embedded via `go:embed`

---

[Unreleased]: https://github.com/pnz1990/kardinal-promoter/compare/v0.8.1...HEAD
[v0.8.1]: https://github.com/pnz1990/kardinal-promoter/compare/v0.8.0...v0.8.1
[v0.8.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.7.0...v0.8.0
[v0.7.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.6.0...v0.7.0
[v0.6.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.5.0...v0.6.0
[v0.5.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.4.0...v0.5.0
[v0.4.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.3.0...v0.4.0
[v0.3.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.2.1...v0.3.0
[v0.2.1]: https://github.com/pnz1990/kardinal-promoter/compare/v0.2.0...v0.2.1
[v0.2.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/pnz1990/kardinal-promoter/releases/tag/v0.1.0
