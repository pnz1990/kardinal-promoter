# Changelog

All notable changes to kardinal-promoter are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added
- Professional documentation site (MkDocs Material, GitHub Pages)
- Auto-generated CLI reference from Cobra commands
- Architecture, FAQ, Comparison, CEL Context Reference pages
- Security and Monitoring guides

---

## [v0.4.0] — 2026-04-12

**Distributed Mode, Argo Rollouts delegation, graph purity**

### Added

- **Distributed mode** — `--shard` flag routes PromotionSteps to matching shard agents; supports multi-cluster deployments where each spoke cluster runs its own agent
- **Argo Rollouts delivery delegation** — `delivery.delegate: argo-rollouts` in Pipeline env spec hands off rollout progression to an existing `Rollout` resource
- **GitLab SCM provider** — `scm.provider: gitlab` in Pipeline spec; supports GitLab hosted and self-managed
- **PRStatus CRD** — makes PR merge/close signal observable by the Graph (eliminates 6 GitHub API call paths from the reconciler hot path)
- **RollbackPolicy CRD** — auto-rollback threshold comparison moved to dedicated reconciler
- **Graph purity milestone** — all 41 krocodile-independent logic leaks eliminated (see `docs/design/11-graph-purity-tech-debt.md`)

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
- **SoakTimer CRD** — moves `time.Now()` calls from PolicyGate reconciler into dedicated CRD status

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

[Unreleased]: https://github.com/pnz1990/kardinal-promoter/compare/v0.4.0...HEAD
[v0.4.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.3.0...v0.4.0
[v0.3.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.2.1...v0.3.0
[v0.2.1]: https://github.com/pnz1990/kardinal-promoter/compare/v0.2.0...v0.2.1
[v0.2.0]: https://github.com/pnz1990/kardinal-promoter/compare/v0.1.0...v0.2.0
[v0.1.0]: https://github.com/pnz1990/kardinal-promoter/releases/tag/v0.1.0
