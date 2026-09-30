# Changelog

All notable changes to kardinal-promoter are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

- **Upstream kro Graph** — kardinal now runs on upstream kro `kro.run/v1alpha1` Graph (v0.10.0-rc.0, `GraphKind` feature gate) instead of the forked Graph controller. The chart no longer bundles a Graph controller; install kro with `hack/install-kro.sh`. Pipeline changes update the Graph in place instead of re-running Verified environments. See [Graph Coverage](graph-coverage.md)
- **Promotion lifecycle fixes** — a Bundle becomes `Verified` when every environment it targets is Verified, and `Failed` when a step fails or kro rejects the Graph; a Pipeline, intent or PolicyGate that cannot be built into a Graph fails the Bundle with the `InvalidSpec` condition instead of retrying forever, and a Failed Bundle retries after its Pipeline changes, and deleting its Graph no longer re-runs it; `intent.targetEnvironment` Bundles finish; the Graph's Accepted/Ready conditions are surfaced on the Bundle; `maxConcurrentPromotions` counts only promoting Bundles; supersession orders Bundles created in the same second by the `kardinal.io/created-at` annotation. Upgrade note: in-flight Bundles are re-translated once after upgrading
- **Rollback restores the previous verified Bundle** — `kardinal rollback`, the UI, `onHealthFailure: rollback` and RollbackPolicy share one implementation: the target is the most recent Bundle, other than the current one, that was Verified in the environment; the rollback Bundle copies its images and config ref; `--to` must name a Bundle that was Verified in the environment; the failing image is never promoted again, a Bundle that was already rolled back from is never picked again, the automatic paths do not roll back a rollback, and nothing is created when there is nothing safe to roll back to. Like any Bundle with a target environment, a rollback first goes through every environment upstream of the target, so those environments are rolled back too. See [Rollback](rollback.md#multi-environment-rollback)
- **Promote copies the Bundle verified upstream** — `kardinal promote` and the UI Promote button copy the artifacts of the newest Bundle Verified in every upstream environment, and refuse instead of creating a Bundle without images
- **Pause holds promotions** — `kardinal pause` and the UI stop new steps and hold in-flight ones at the next safe point; resume continues them. The Pipeline reconciler keeps the `freeze-<pipeline>` gate in step with `spec.paused` and reports it in a `Paused` condition; a PolicyGate of that name that kardinal does not own is left alone and the condition is False with reason `FreezeGateNameConflict`. See [Pause and Resume](rollback.md#pause-and-resume). Upgrade note: a Pipeline paused from the old UI, or by setting `spec.paused: true` directly, had no freeze gate, so it was not actually paused; after upgrading the reconciler creates the gate and that Pipeline stops promoting. Run `kardinal resume <pipeline>` for any Pipeline that should keep running (`kubectl get pipelines -o custom-columns=NAME:.metadata.name,PAUSED:.spec.paused` lists them)
- **`kardinal approve` is deprecated** — it never bypassed anything; it now fails and points to `kardinal override`
- **Digest references** — `kardinal create bundle --image repo@sha256:...` records a digest, not a tag
- **Pipeline status** — the Pipeline `Ready` condition is True once the spec is valid (False with the reason for duplicate names, unknown `dependsOn`, a cycle or a `git.secretRef` in another namespace, and False with reason `NotImplemented` when a reserved field such as `steps` or `layout: branch` is set) instead of staying `Initializing`; `status.phase` is `Promoting` while a Bundle is in flight or held by a PolicyGate, so the UI no longer shows such a Pipeline as Idle, and follows the same newest Bundle as the UI and CLI (Superseded Bundles and their steps are skipped); `historyLimit` defaults to 50 (the docs said 20)
- **`kardinal override` works on real gate names** — it patched nothing and exited 1 for any gate whose instance name is over 63 characters (all of them in practice); it now patches a named instance directly, and for a template name patches that gate's instances in the pipeline's in-progress Bundles
- **`explain`, `status` and `get pipelines` describe the current Bundle** — the newest Bundle that is not Superseded, as in the UI; a Failed Bundle is shown only in environments it reached, gates that are not ready (including skip-permission gates) are listed first, and an old Bundle's error is hidden once a newer Bundle exists. `audit summary` counts rollback Bundles
- **PolicyGate `when` documented as it behaves** — every gate holds its environment's PromotionStep back until it is ready; `pre-deploy` adds a re-check right before git-clone and `post-deploy` adds nothing (#1323 tracks real post-deploy semantics)
- **Subscription digest label** — `kardinal.io/source-digest` keeps the first 63 characters of the digest instead of the last 63, so it matches the digest's prefix. Bundles labelled by older releases still deduplicate. Downgrade note: an older controller does not recognise the new label and its Subscription goes to Error instead of creating a duplicate Bundle
- **Rollback PR title** — reads "(restores <bundle>)" instead of "(reverts <bundle>)"; rollback PRs carry `kardinal/rollback` in addition to `kardinal/promotion`
- **UI blocked banner counts only gates that hold the Bundle** — the pipeline page's blocking-promotion banner, **Show blocked** and the Policy Gates panel's "blocked" count use the rule behind the Blocked label and the Blockers column; a gate that is not ready but does not hold the Bundle (its environment not reached yet, or the Bundle failed) is shown as Waiting instead of Block, and a superseded Bundle's gates as Superseded. The UI API's bundle graph and gate list give each gate the same `state` and mark the ones holding the Bundle with `holding`. See [Inspecting PolicyGates](policy-gates.md#inspecting-policygates)
- **A Superseded Bundle stops promoting** — its Graph could still create a PromotionStep when a gate or upstream turned ready later, for example after a controller restart re-evaluated its soak gate. Every step node now resolves only while its Bundle is not Superseded, so the Graph creates no new step and keeps the existing ones; the Bundle's PolicyGates are no longer evaluated and keep their last status; a step created just before supersession that never started is failed without a `PromotionSuperseded` AuditEvent, so `kardinal audit summary` no longer counts it. Upgrade note: a Graph built before the upgrade keeps its old spec, so it can still create a step at the moment its Bundle is superseded; that step fails without an AuditEvent, and the Graph's PolicyGates are frozen like any other Superseded Bundle's. The supersession docs also said the Graph was deleted and PRs were left open; neither was true
- **`GraphReady` after completion** — a Verified Bundle's `GraphReady` condition turns True once kro marks its Graph ready, instead of staying False
- **`kardinal explain` gate states match the UI** — a gate that is not ready is **Block** only while it holds the Bundle (the rule behind the UI's Blocked label); otherwise it is **Waiting** (evaluated, environment not reached or Bundle failed), **Pending** (not evaluated yet) or **Superseded**, as in the UI. Waiting is colored like Pending (yellow), Superseded is uncolored. See [Inspecting PolicyGates](policy-gates.md#inspecting-policygates)
- **PR body CI run link** — the provenance table no longer renders an empty `[CI run]()` link when the Bundle has provenance without `ciRunURL`; the cell is `—`, as it is for a URL that is not `http(s)`, and a `ciRunURL` can no longer break the table or add markup. An empty commit or author is `—` too. See [PR Evidence](pr-evidence.md#artifact-provenance)
- **`provenance.ciRunURL` is checked on Bundle creation** — the Bundle API returns `400` unless it is empty or an absolute `http(s)` URL without user info, spaces or control characters. Bundles created another way or before the check are not rejected, but the PR body, the UI bundle header and the bundle comparison show `—` instead of linking such a URL, and promote and rollback do not copy it. See [Provenance](ci-integration.md#provenance)
- **`kardinal validate`** — skips non-kardinal kinds such as Namespace, so the shipped quickstart and demo gate files validate, and reports the same unimplemented fields as the Pipeline status and the admission webhook (which warns)
- **UI API access control** — bearer-token auth with `--ui-auth-token` (#924) or Kubernetes TokenReview (#1015); CORS limited with `--cors-allowed-origins` (#940); TLS with `--tls-cert-file` / `--tls-key-file` (#937); the UI warns on an insecure connection (#941)
- **Bitbucket Cloud and Azure DevOps SCM providers** (#1035, #1040)
- **NotificationHook CRD** — outbound webhook notifications for promotion events (#942)
- **`argocd-set-image` step** — promotes by setting the image on the Argo CD Application, without git operations (#966)
- **Create Bundle from GitHub Actions** — composite action in `.github/actions/create-bundle` (#953); the UI has a Create Bundle dialog (#950)
- **`maxConcurrentPromotions`** — per-Pipeline cap on Bundles promoting at once (#1059)
- **`stepTimeoutSeconds`** — per-environment limit on how long a promotion step runs (#1123)
- **`environment.waitForMergeTimeout`** — fails a PromotionStep whose PR is not merged in time; no timeout by default (#906, #908)
- **`health.resource`** — names the Deployment the resource health adapter checks, when it differs from the pipeline name or namespace (#1117)
- **Bundle history limit** — old Bundles beyond `historyLimit` are deleted (#919)
- **Bundle `status.conditions`** — set on every phase transition (#991)
- **SCM credential rotation without restart** — the controller picks up a changed token Secret (#994, #1060); it checks the token's scopes at startup (#996)
- **Namespace-scoped install** — chart value `controller.watchNamespace` (#1024)
- **Chart `demo.enabled`** — quickstart mode (#1043); **Grafana dashboard** — chart value `grafanaDashboard.enabled` ships the dashboard as a ConfigMap (#1139)
- **Metrics** — step duration, gate blocking time and PromotionStep age (#992)
- **Readiness** — `/readyz` fails until the informer caches have synced (#1147)
- **CLI** — `kardinal get subscriptions` and a SUB column in `get pipelines` (#948); `kardinal logs` shows a per-step table (#1012) and streams with `--follow` (#1124); `kardinal status` lists in-flight promotions per pipeline (#997); `kardinal get pipelines` shows `dependsOn` errors (#1071); `kardinal init --scaffold-gitops` and `--demo` (#1022); `kardinal delete bundle <name>` (#851)
- **`kubectl get` printer columns** — Bundle shows Type, Pipeline, Phase, Age; PromotionStep (`ps`) shows Pipeline, Env, Bundle, State, Age (#903)
- **UI** — skeleton loading states (#784), `/` focuses the pipeline filter (#800), virtual scrolling for lists over 50 pipelines (#817)
- **Demo coverage for every health adapter** — examples and tests for resource, argocd, flux, argoRollouts and flagger (#821)
- **RBAC** — the ClusterRole and Role grant access to NotificationHooks and AuditEvents (#1095)
- **Fixed: Bundle requeue hot loop** — a 1 ms `RequeueAfter` is now at least 500 ms (#988)
- **Fixed: `AbortedByAlarm` and `RollingBack` steps no longer reset to Pending** — the PromotionStep reconciler handles both states (#789)

---

## [v0.8.1] — 2026-04-17

**Security release: supply chain hardening — trivy, cosign, SBOM, SLSA, Graph controller image scan**

The v0.8.1 tag points to `bd2bcf3`, a merge commit that is not on main. Its tree is identical to `34d8524` (#787) on main.

### Added

- **Image vulnerability scan** — the release workflow scans the controller image with trivy and uploads the SARIF report; findings do not block the release (#696, #787). The Graph controller fork image is scanned too (#708)
- **Keyless image signing** — the controller image is signed with cosign through GitHub Actions OIDC (#700)
- **SBOM** — Syft SPDX SBOM, attached to the image as a cosign attestation (#703)
- **SLSA provenance** — GitHub build provenance attestation for the controller image (#707)
- **CLI** — `kardinal completion bash|zsh|fish|powershell` (#731); `kardinal validate` checks Pipeline and PolicyGate YAML offline (#713); `kardinal status` shows controller health and a resource summary (#715); `kardinal explain --color` (#730); `kardinal create bundle --dry-run` (#741); `explain` with an unknown environment lists the valid ones (#717); a dependency cycle error shows the cycle (#711)
- **Prometheus metrics** — `kardinal_bundles_total`, `kardinal_steps_total`, `kardinal_gate_evaluations_total`, `kardinal_pr_duration_seconds` (#726)
- **UI** — dark and light mode (#734); selection kept in the URL (#742); keyboard shortcuts `?`, `r`, `Esc` with a focus-trapped help modal (#750, #785); error boundaries with Retry (#755); copy-to-clipboard on pipeline names and bundle hashes (#764); the stale-data indicator turns red after 30 s (#767)
- **Demo environment** — complete demo setup (#786)

### Changed

- **UI theme tokens and WCAG 2.1 AA** — colors moved to CSS custom properties (#738) and meet 4.5:1 contrast in both themes (#760, #772); axe-core checks run in the Playwright suite (#756, #759, #771)

---

## [v0.8.0] — 2026-04-17

**Audit log, SCM circuit breaker, Bundle Watch node, Graph-first cleanup, DX improvements**

### Added

- **AuditEvent CRD** — an immutable promotion event log, with gate evaluation and rollback events (#679, #681)
- **`kardinal get auditevents`** (#684) and **`kardinal audit summary`** (#686)
- **Admission webhook** for Pipeline and Bundle validation (#670)
- **SCM circuit breaker** — exponential backoff that respects rate limits (#666)
- **Bundle Watch node** — `bundle.*` is in Graph CEL scope (#667)
- **Actionable CLI error hints** for common failures (#689)

### Changed

- **Graph controller fork pin** `81c5a03` → `05db829` (#677)

---

## [v0.7.0] — 2026-04-16

**WatchKind O(1) health checks, Graph controller fork 81c5a03 upgrade, reactive PromotionStep reconciler, graph-first cleanup**

### Added

- **WatchKind health nodes** — `health.labelSelector` on Pipeline environments switches from Watch (O(n) full list per event) to WatchKind (O(1) incremental cache). Requires Graph controller fork `745998f`+ (#652)
- **Kargo migration guide** — concept mapping, side-by-side Pipeline vs Kargo YAML, 7-step migration walkthrough in `docs/guides/` (#640)
- **Operations runbook expanded** — PolicyGate debugging, SCM failure modes, RBAC issues, Graph controller restarts, performance tuning added (#639)
- **Bundle image diff in NodeDetail** — UI compares the current bundle's image against the previous bundle for that environment; closes a Kargo parity gap (#638)
- **Per-step progress observability** — `PromotionStep.status.steps[]` exposes each step with individual state, start time, and duration (#630)
- **`kardinal get pipelines --watch`** — real-time promotion progress with live table refresh (#629)
- **PrometheusRule CRD** — 6 pre-built alerting rules in Helm chart: promotion stuck, high rollback rate, policy gate blocked, SCM errors (#621)
- **UI: conditions summary and reason** — NodeDetail shows Kubernetes Conditions table with reason column; improved empty-state onboarding (#529, #530)
- **UI: Kubernetes events stream** — timestamped event history per PromotionStep in NodeDetail (#560)
- **UI: cross-environment error aggregation** — groups PromotionStep failures by type across environments; shows affected count (#564)
- **Graph controller fork upgraded to `745998f`** — Decorator bootstrap primitive, Definition compile-time type inference, forEach array format support, DAG finalizer guard for non-resource nodes (#614)

### Fixed

- **Bundle reconciler watches Pipeline changes** — Graph is regenerated when Pipeline spec changes (new environments, updated policyNamespaces, changed git config). Previously Pipeline changes were invisible to in-flight Bundles (#634)
- **Subscription deduplication under HA** — uses label selector (`kardinal.io/source-digest`) instead of status field comparison; safe under concurrent reconciles and multiple controller replicas (#636)
- **CEL documentation accuracy** — corrected false claims about `pkg/cel/NewCELEnvironment()` (does not exist) and `schedule.*` (map variable, not CEL library function) in design docs and code comments (#631)
- **Shell completion** — bash, zsh, fish, and PowerShell completion scripts via `kardinal completion <shell>` (#606)
- **`kardinal doctor`** — pre-flight cluster health check: validates CRD installation, the Graph controller, RBAC, and GitHub token before first use (#607)
- **Graceful shutdown** — controller drains in-flight reconcile loops on SIGTERM; no promotion steps interrupted by pod restarts (#605)
- **PodDisruptionBudget + topology spread** — minAvailable: 1 PDB and `topologySpreadConstraints` in Helm chart for HA deployments (#598)
- **Graph controller bundled in Helm chart** — single `helm install` installed both kardinal-promoter and the pre-upstream Graph controller fork (#590; reverted when kardinal moved to upstream kro, which is installed separately)
- **Library-based git operations** — replaced `exec.Command("git")` with `go-git` library (`#517`). Controller no longer requires a `git` binary. Improves portability (distroless images) and performance.
- Controller `/tmp` mount — `emptyDir` volume added for git-clone with `readOnlyRootFilesystem: true` (#609)
- `policy simulate` now searches all namespaces — org-level gates in `platform-policies` were never found
- `pkg/cel` standalone CEL evaluator eliminated — evaluation moved inline to PolicyGate reconciler
- Rollback PR title and body now include rollback notice and the `kardinal/rollback` label

---

## [v0.6.0] — 2026-04-14

**Live-cluster validation infrastructure, J7 multi-tenant self-service, OCI/Git source watchers, pipeline deployment metrics**

The v0.6.0 tag points to `369be4c`, a merge commit that is not on main. Its tree is identical to `a316253` (#515) on main.

### Added

- **Multi-tenant self-service (J7)** — ApplicationSet + Pipeline template bootstrap; team onboarding via Git directory; org PolicyGates automatically inherited (#489)
- **OCI + Git source watchers** — `OCIWatcher` and `GitWatcher` Subscription reconcilers poll registries and Git branches, creating Bundles on new images/commits (#491, #493)
- **Pipeline deployment metrics** — `Pipeline.status.deploymentMetrics` aggregated by `PipelineReconciler`: `rolloutsLast30Days`, `p50CommitToProdMinutes`, `p90CommitToProdMinutes`, `autoRollbackRate` (#498)
- **`changewindow.isAllowed()` / `changewindow.isBlocked()` CEL functions** — named-argument helpers for ChangeWindow gates (#506)
- **Graph controller fork upgraded to `948ad6c`** — DNS-1123 node ID validation, drift timers (30 min), propagation hash includes `propagateWhen` state
- **Cardinal logo** — added across docs site, UI sidebar, and README

### Fixed

- `kardinal-promoter` controller image rebuilt correctly when Graph CR is deleted externally (#490)
- PDCA live-cluster validation workflow: fixed pipeline name mismatch, missing `platform-policies` namespace, missing controller install step (#514)
- CI: `enforce_admins: true` on branch protection; 8 required status checks; concurrency guards on Docs and E2E workflows (#513)

---

## [v0.5.0] — 2026-04-13

**Pipeline Expressiveness (K-Series), Enterprise UI Control Plane, Graph controller upgrade**

### Added

- **K-01: Contiguous healthy soak** — `bake.minutes` + `bake.policy: reset-on-alarm` on environment spec; `BakeElapsedMinutes` and `BakeResets` tracked in PromotionStep status
- **K-02: Pre-deploy gate type** — `when: pre-deploy` on PolicyGate spec; holds the PromotionStep in `Pending` before `git-clone` starts
- **K-03: Auto-rollback with ABORT vs ROLLBACK distinction** — `onHealthFailure: rollback | abort | none` per environment
- **K-04: ChangeWindow CRD** — blackout and recurring allowed-hours windows; `changewindow["name"]` CEL map variable is `true` when the window is active/blocking
- **K-05: Bundle.status.metrics** — commitToProductionMinutes, bakeResets, autoRollbacks, operatorInterventions; `kardinal metrics` CLI command
- **K-06: Wave topology** — `wave: N` field on environment spec; Wave N automatically depends on all Wave N-1 stages
- **K-07: Integration test step** — built-in `integration-test` step runs a Kubernetes Job as part of the promotion sequence
- **K-08: PR review gate** — `bundle.pr["staging"].isApproved` and `.approvalCount` in CEL context via PRStatus CRD
- **K-09: `kardinal override` with audit record** — emergency gate override with mandatory reason + time limit; the override is recorded in the gate's `spec.overrides[]`, and the gate reason shows it in the PR evidence body
- **K-10: Cross-stage history CEL** — `upstream.<env>.soakMinutes`, `.recentSuccessCount`, `.recentFailureCount`, `.lastPromotedAt` in gate expressions
- **UI control plane** — all 7 UI issues shipped (#462–#468): fleet health dashboard, pipeline ops view, per-stage bake countdown, in-UI actions (pause/resume/rollback/override), release metrics bar, bundle timeline, policy gate detail panel

### Fixed

- Graph controller fork upgraded to `948ad6c` — DNS-1123 node ID validation, drift timers, propagation hash improvements
- `changewindow.isAllowed()` / `changewindow.isBlocked()` CEL helpers added alongside the map-style access

---

## [v0.4.0] — 2026-04-11

**Distributed Mode, Argo Rollouts delegation, graph purity, K-series features**

### Added

- **Distributed mode** — `--shard` flag routes PromotionSteps to matching shard agents; supports multi-cluster deployments where each spoke cluster runs its own agent
- **Argo Rollouts delivery delegation** — `delivery.delegate: argoRollouts` in Pipeline env spec hands off rollout progression to an existing `Rollout` resource
- **GitLab + Forgejo/Gitea SCM providers** — selected per controller with `--scm-provider gitlab` or `--scm-provider forgejo` (also `gitea`)
- **PRStatus CRD** — makes PR merge/close signal observable by the Graph (eliminates 6 GitHub API call paths from the reconciler hot path)
- **RollbackPolicy CRD** — auto-rollback threshold comparison moved to dedicated reconciler
- **Graph purity milestone** — all 41 Graph-independent logic leaks eliminated (see `docs/design/11-graph-purity-tech-debt.md`)
- **K-01–K-11** — all Pipeline Expressiveness features (see v0.5.0 above for full list; initial implementation in this release)

### Fixed

- Pause enforcement via `bundle.status.paused` (eliminates in-memory pause state)
- Step cleanup on pipeline deletion no longer leaves orphaned PromotionSteps
- PolicyGate re-evaluation after TTL now fires correctly when graph resumes

---

## [v0.3.0] — 2026-04-11

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

## [v0.2.1] — 2026-04-11

**Graph Purity: all Graph-independent logic leaks eliminated**

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

- kind E2E infrastructure (`make setup-e2e-env`) sets up the Graph controller + ArgoCD + test/uat/prod namespaces
- `kardinal get pipelines` shows per-environment status columns
- `kardinal explain` shows active PolicyGates with CEL expression and current value

---

## [v0.1.0] — 2026-04-10

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
