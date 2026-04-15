# Changelog

All notable changes to kardinal-promoter are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

## [Unreleased]

### Added

- **Library-based git operations** — replaced `exec.Command("git")` with `go-git` library (`#517`). Controller no longer requires a `git` binary. Improves portability (distroless images) and performance.



**Enterprise CD patterns, UI control plane, ScheduleClock, Subscription CRD**

### Added

- **K-07: Integration test step** — `integration-test` built-in step runs a Kubernetes Job inline during promotion; triggers `onFailure: abort | rollback` on failure
- **K-08: PR review gate** — `bundle.pr["staging"].isApproved` and `.approvalCount` in CEL context via PRStatus CRD; no external SCM API calls in the hot path
- **K-09: `kardinal override`** — emergency gate override with mandatory reason + time limit; audit record written to Bundle status and surfaced in PR evidence body
- **K-10: Cross-stage history CEL** — `upstream.<env>.soakMinutes`, `.recentSuccessCount`, `.recentFailureCount`, `.lastPromotedAt` in gate expressions
- **UI control plane** — all 7 UI issues shipped: fleet health dashboard (#467), pipeline ops view (#462), per-stage bake countdown (#463), in-UI actions (#464), release metrics bar (#465), bundle timeline (#466), policy gate detail (#468)
- **ScheduleClock CRD** — event-driven re-evaluation for `schedule.*` gates; eliminates polling-based recheck
- **Subscription CRD** — reconciler creates Bundles on artifact changes; OCI and Git source watchers are stub implementations (always return `Changed: false`) — see #491, #493
- **K-01: Contiguous bake timer** — `bake.minutes` + `bake.policy: reset-on-alarm`; bake timer resets on health alarm
- **K-02: Pre-deploy gates** — `when: pre-deploy` on PolicyGate; evaluated before `git-clone` starts
- **K-03: onHealthFailure policy** — `rollback | abort | none` per environment
- **K-04: ChangeWindow CRD** — blackout and recurring allowed-hours windows; `changewindow["name"]` in CEL
- **K-05: Bundle.status.metrics** — commitToFirstStageMinutes, commitToProductionMinutes, bakeResets, operatorInterventions; `kardinal metrics` CLI
- **K-06: Wave topology** — `wave: N` field; Wave N automatically depends on all Wave N-1 stages
- **ValidatingAdmissionPolicy** — Kubernetes admission webhook validates CEL expressions in PolicyGate at apply time

### Fixed

- `policy simulate` now searches all namespaces — org-level gates in `platform-policies` were never found
- `pkg/cel` standalone CEL evaluator eliminated — evaluation moved inline to PolicyGate reconciler
- Rollback PR title and body now include rollback notice and the `kardinal/rollback` label

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
- **K-01: Contiguous bake timer** — `bake.minutes` + `bake.policy: reset-on-alarm` on environment spec; `BakeElapsedMinutes` and `BakeResets` in PromotionStep status
- **K-02: Pre-deploy gates** — `when: pre-deploy` on PolicyGate spec; blocks before `git-clone` starts
- **K-03: onHealthFailure policy** — `rollback | abort | none` per environment; rollback auto-creates a new Bundle at the previous image version
- **K-04: ChangeWindow CRD** — blackout and recurring allowed-hours windows; CEL context `changewindow["name"]`
- **K-05: Bundle.status.metrics** — commitToFirstStageMinutes, commitToProductionMinutes, bakeResets, operatorInterventions
- **K-06: Wave topology** — `wave: N` field on environment spec; Wave N automatically depends on all Wave N-1 stages
- **K-07: Integration test step** — built-in `integration-test` step runs a Kubernetes Job inline during promotion
- **K-08: PR review gate** — `bundle.pr["staging"].isApproved` and `.approvalCount` CEL attributes via PRStatus CRD
- **K-09: kardinal override** — emergency gate override with mandatory reason; override record in PR evidence body
- **K-10: Subscription CRD** — reconciler scaffolded; source watchers (OCI, Git) are stubs pending #491/#493
- **K-11: Cross-stage history CEL** — `upstream.staging.soakMinutes`, `.recentSuccessCount`, `.recentFailureCount`, `.lastPromotedAt` in CEL context

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

[Unreleased]: https://github.com/pnz1990/kardinal-promoter/compare/v0.6.0...HEAD
[v0.5.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.4.0...v0.5.0
[v0.4.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.3.0...v0.4.0
[v0.3.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.2.1...v0.3.0
[v0.2.1]: https://github.com/pnz1990/kardinal-promoter/compare/v0.2.0...v0.2.1
[v0.2.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/pnz1990/kardinal-promoter/releases/tag/v0.1.0
