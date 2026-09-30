# Changelog

All notable changes to kardinal-promoter are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
Versioning follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

---

## [v0.9.0-rc.1] — 2026-09-30

**Release candidate: kardinal runs on upstream kro Graph. Breaking changes: read [Upgrading from v0.8.1](https://pnz1990.github.io/kardinal-promoter/installation/#upgrading-from-v081) first.**

Pin the version: without `--version 0.9.0-rc.1`, `helm install` and `helm upgrade` pick v0.8.1. The CLI is on the release page (`kardinal-<os>-<arch>`).

### Before you upgrade

1. Install kro v0.10.0-rc.0 with the `GraphKind` feature gate (`hack/install-kro.sh`). The chart no longer bundles a Graph controller.
2. On Kubernetes older than 1.30, first remove the fields the API server now rejects from stored objects (list below). These clusters do not ratchet CRD validation, so every update to such an object fails, the controller's status writes included.
3. Apply the new CRDs by hand, before the controller: Helm never updates CRDs. Without them, new status fields are dropped on save.
4. Remove `controller.shard` from your values file, even if it is empty. The chart rejects it.
5. Check the controller's UI exposure: with no UI auth mode set, the UI API answers only `kubectl port-forward` clients.

The API server now rejects: `spec.environments[].steps` (non-empty), `promotionTemplate`, `autoRollback`, `update.strategy: argocd` with `approval: pr-review`, a reserved environment name (such as `kind`, `spec` or `bundle`), a non-empty `spec.policyGates`, a PolicyGate `spec.selector`, and a PolicyGate name over 63 characters. The fields were ignored or failed only at promotion time; a reserved name clashes with kro Graph node IDs.

### Changed

- **Upstream kro Graph** — kardinal renders one upstream kro `kro.run/v1alpha1` Graph per Bundle, instead of using a forked Graph controller. Pipeline changes update the Graph in place instead of re-running Verified environments. In-flight Bundles are re-translated once after upgrading. What still runs outside the Graph: [Graph Coverage](https://pnz1990.github.io/kardinal-promoter/graph-coverage/)
- **Gates are re-checked before a step starts** — a PromotionStep starts only when every gate it requires is ready, with a result evaluated at or after the step was created. A step that has started is not stopped by a gate that turns false later. Messages: `waiting for gate <name>` and `waiting for gate <name> to be re-evaluated`. `PolicyGate.spec.when` is deprecated and has no effect. Right after the upgrade, a Pending step whose gate results are older than the step waits for the next evaluation; the controller re-evaluates every gate when it starts
- **Stale MetricCheck results fail closed** — MetricCheck has `status.validUntil` (three intervals after the evaluation, at least 30s). After it, gates see `metrics.<name>.result` as `"Stale"`, `.value` as `""` and `.stale` as `true`. Expressions written as `result != "Fail"` pass on a stale result; write `result == "Pass"`. Right after the upgrade every MetricCheck is stale until its first evaluation
- **Pause holds promotions** — `kardinal pause` and the UI stop new steps and hold in-flight ones at the next safe point; resume continues them within a minute. The Pipeline reconciler keeps the `freeze-<pipeline>` gate in step with `spec.paused` and reports it in a `Paused` condition. A Pipeline paused from the old UI, or with `spec.paused: true` set directly, had no freeze gate and was not actually paused; after upgrading it stops promoting. Run `kardinal resume <pipeline>` for any that should keep running (`kubectl get pipelines -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,PAUSED:.spec.paused`)
- **Rollback restores the previous verified Bundle** — `kardinal rollback`, the UI, `onHealthFailure: rollback` and RollbackPolicy share one implementation. The target is the most recent other Bundle Verified in the environment; the rollback restores every image the deployed Bundle changed, using the environment's history for images the target does not name, and refuses when one cannot be found. `--to` must name a Bundle Verified in the environment and of the same type. A rolled-back image is never promoted again, the automatic paths never roll back a rollback, and a rollback also goes through the environments upstream of the target. Rollback PR titles say "(restores <bundle>)" and carry `kardinal/rollback`. A refused auto-rollback shows as `RollbackRefused=True` on the RollbackPolicy, a Warning Event and a `Refused` column. See [Rollback](https://pnz1990.github.io/kardinal-promoter/rollback/)
- **`kardinal promote`** copies the artifacts of the newest Bundle Verified in every upstream environment, and refuses instead of creating a Bundle without images
- **A Superseded Bundle stops promoting** — its Graph creates no new step, its gates keep their last status, and a step that never started is failed without an AuditEvent. A Graph built before the upgrade can still create one step at the moment its Bundle is superseded; that step fails the same way. Gates of Verified Bundles are no longer evaluated either
- **UI API is local-only without auth** — with no UI auth mode set, `/api/` answers only local clients (`kubectl port-forward`). Through an Ingress, NodePort or LoadBalancer it returns `403`: set `ui.auth.tokenReview=true` or `ui.auth.tokenSecretRef.name`. Behind a service-mesh sidecar, set an auth mode too
- **Egress guard** — NotificationHook webhooks and MetricCheck Prometheus URLs can no longer reach loopback, link-local (cloud metadata), unspecified or multicast addresses. The check runs on the resolved address at connect time, and, when `HTTP(S)_PROXY` is set, on the target before it goes to the proxy, so the controller pod then needs DNS for the target hosts. Private addresses and cluster Services still work
- **Controller Events** use `events.k8s.io/v1`, with an action and a note of at most 1024 bytes. Custom RBAC must allow create and patch on `events.k8s.io` events. Repeated events merge within about 6 minutes, and the UI's event count refreshes every 30 minutes
- **Secret RBAC is `get` only** — the controller never lists or watches Secrets; the release-namespace Role gets `get` on the SCM token Secret by name
- **Flux health on `pr-review` steps** waits for the PR's merge commit instead of passing on a Kustomization Ready on the previous commit; the GitHub and GitLab webhooks record the merge commit. **Argo CD health** is not ready while a sync is running or has failed. Flux waits for `observedGeneration`; Flagger messages include Flagger's reason
- **PRStatus** — a PR closed without merging can be reopened within 5 minutes and the promotion continues. After that kardinal comments once, stops polling and fails the step (`closedAt`, `closedFinal`)
- **Bundle lifecycle** — a Bundle is `Verified` when every environment it targets is Verified and `Failed` when a step fails or kro rejects the Graph. A Pipeline, intent or PolicyGate that cannot be built into a Graph fails the Bundle with `InvalidSpec` instead of retrying forever, and so does a Bundle without the artifacts its type needs (including a `mixed` Bundle created with kubectl without `configRef.commitSHA`). A Failed Bundle retries after its Pipeline changes. `intent.targetEnvironment` Bundles finish. The Graph's `Accepted` and `Ready` conditions show on the Bundle; `GraphReady` turns True when a Verified Bundle's Graph is ready. `maxConcurrentPromotions` counts only promoting Bundles, read from the API server. Bundles created in the same second are ordered by `kardinal.io/created-at`
- **Pipeline status** — `Ready` is True once the spec is valid, and False with the reason otherwise (duplicate names, unknown `dependsOn`, a cycle, a `git.secretRef` in another namespace, or `NotImplemented` for `layout: branch`, `shard` or two or more `regions`). `status.phase` is `Promoting` while a Bundle is in flight or held by a gate. `historyLimit` defaults to 50
- **`kardinal explain`, `status` and `get pipelines`** describe the current Bundle (the newest that is not Superseded, as in the UI). `explain` and `status <pipeline>` add a BUNDLE column and show the Bundle deployed in an environment the current Bundle has not reached: scripts that parse columns by position need an update. Gate states match the UI: **Block** only while the gate holds the Bundle, otherwise **Waiting**, **Pending** or **Superseded**. The UI's blocked banner, **Show blocked** and the gate panel count only gates that hold the Bundle
- **`kardinal override`** works on real gate instance names (it patched nothing for any name over 63 characters) and, for a template name, patches that gate's instances in the pipeline's in-progress Bundles. `Bundle.status.metrics.operatorInterventions` counts overrides
- **`kardinal create bundle`** applies the same checks as `POST /api/v1/bundles` and adds `--config-commit`, `--config-repo`, `--commit`, `--author` and `--ci-run-url`. `--image repo@sha256:...` records a digest. `provenance.ciRunURL` must be an absolute `http(s)` URL; the PR body, UI and bundle comparison show `—` instead of linking anything else
- **Subscription digest label** `kardinal.io/source-digest` keeps the first 63 characters of the digest. Older labels still deduplicate; an older controller does not recognise the new label
- **Example Pipelines** github-demo, flagger-demo, flux-demo and argo-rollouts-demo are named after their directories instead of `kardinal-test-app`. If you applied one, delete the old `kardinal-test-app` Pipeline and the old-named Kustomizations, Canary, Rollout and Application, and re-apply the example

### Removed

- **Distributed mode** — `kardinal-agent`, `--shard` / `KARDINAL_SHARD` and the chart value `controller.shard`. The controller reconciles every PromotionStep. `shard` on an environment sets the Pipeline `Ready=False`
- **Per-region fan-out** — `regions` and `PromotionStep.spec.region` are deprecated. Two or more regions fail at Graph build; declare one environment per region and use `wave`
- **The Pipeline admission webhook** (`--pipeline-admission-webhook`, `POST /webhook/validate/pipeline`). The controller exits at startup when it is set: remove the setting and delete your ValidatingWebhookConfiguration. Invalid Pipelines are marked `Ready=False` instead
- **The PromotionTemplate CRD and `PromotionStep.spec.inputs`**, and the custom `webhook`, `verify-image` and `integration-test` steps. No Pipeline could run them. Helm does not delete CRDs: run `kubectl delete crd promotiontemplates.kardinal.io --ignore-not-found` (only clusters that ran a build from `main` have it). For image signatures, use admission-time verification; for tests, Argo CD PostSync hooks or MetricCheck gates. See [Image signatures and tests](https://pnz1990.github.io/kardinal-promoter/pipeline-reference/#image-signatures-and-tests)
- **Never-written fields** — AuditEvent `spec.actor` and `spec.bundleImage`; Bundle `status.metrics.autoRollbacks`, `status.environments[].prMergedAt`, `.mergedBy` and `.gateResults`. The UI "CD Level" column, "Full CD" counter and `cdLevel` in `/api/v1/ui/pipelines` are gone (they counted `spec.policyGates`)
- **The EKS e2e Terraform** and `make eks-up`, `eks-down` and `setup-multi-cluster-env`. If you created `kardinal-e2e-prod` with them, destroy it from an older checkout

### Deprecated

- `PolicyGate.spec.when`, `spec.environments[].health.cluster` (still rejected: use an Argo CD or Flux hub), `regions`, `Pipeline.spec.git.provider` (ignored: the controller's `--scm-provider` / Helm `scm.provider` selects the provider), `kardinal rollback --emergency` (no effect: use `kardinal override`), `kardinal approve` (fails and points to `kardinal override`), and the chart values `rbac.integrationTestJobs` (no effect, removed in v0.10) and `validatingAdmissionPolicy.enabled` (no effect)

### Added

- **SCM providers** — Bitbucket Cloud and Azure DevOps (#1035, #1040)
- **UI API access control** — a static bearer token (`ui.auth.tokenSecretRef`) or Kubernetes TokenReview (`ui.auth.tokenReview`); CORS with `--cors-allowed-origins`; TLS with `--tls-cert-file` / `--tls-key-file`; the UI warns on an insecure connection (#924, #1015, #940, #937, #941)
- **NotificationHook CRD** — outbound webhooks on Bundle Verified, PolicyGate Blocked and PromotionStep Failed (#942)
- **`update.strategy: argocd`** — sets the image on the Argo CD Application without git operations (`argocd-set-image`, #966)
- **Create Bundle from GitHub Actions** — `.github/actions/create-bundle` (#953); the UI has a Create Bundle dialog (#950)
- **Pipeline and environment limits** — `maxConcurrentPromotions` (#1059), `stepTimeoutSeconds` (#1123), `waitForMergeTimeout` (#906, #908), `historyLimit` (#919)
- **`health.resource`** names the Deployment the resource adapter checks (#1117)
- **Chart** — `controller.watchNamespace` for a namespace-scoped install (#1024), `demo.enabled` (#1043), `grafanaDashboard.enabled` (#1139), `serviceMonitor.enabled` with `interval` and `labels` (#1268)
- **SCM token** — a changed token Secret is picked up without a restart (#994, #1060); the startup scope check runs in Helm installs too, in the background (#996, #1275). Bitbucket and Azure DevOps have no startup check
- **Metrics** — step duration, gate blocking time and PromotionStep age (#992); `/readyz` fails until the caches have synced (#1147)
- **CLI** — `get subscriptions` and a SUB column in `get pipelines` (#948); `logs` per-step table and `--follow` (#1012, #1124); `status` in-flight promotions (#997); `init --scaffold-gitops` and `--demo` (#1022); `delete bundle` (#851); `doctor` prints a version-pinned install command
- **`kubectl get` printer columns** for Bundle and PromotionStep (#903)
- **UI** — skeleton loading states (#784), `/` focuses the pipeline filter (#800), virtual scrolling over 50 pipelines (#817)
- **Examples** for every health adapter: resource, argocd, flux, argoRollouts and flagger (#821)

### Fixed

- Bundle requeue hot loop: a 1 ms `RequeueAfter` is now at least 500 ms (#988)
- `AbortedByAlarm` and `RollingBack` steps no longer reset to Pending (#789)
- The PR body's CI run cell no longer renders an empty link; an empty commit or author is `—`
- `kardinal validate` skips non-kardinal kinds and reports the same unimplemented fields as the Pipeline status
- The ClusterRole and Role grant access to NotificationHooks and AuditEvents (#1095)

### Release and CI

- Releases are cut only from tags on `main`. A prerelease gets no `latest` image tag and is not marked latest. The notes come from this changelog
- GitHub Actions are pinned to commit SHAs; kind, kubectl and argocd downloads are checked by sha256
- The web UI builds with npm only, and CI fails when the committed `web/dist` differs from a fresh build

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

- **WatchKind health nodes** — `health.labelSelector` on Pipeline environments switches from Watch (O(n) full list per event) to WatchKind (O(1) incremental cache). Requires Graph controller fork `745998f`+ (#652, docs #659)
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

### Changed

- **PromotionStep `spec.upstreamStates`** replaces `spec.upstreamVerified` and `spec.upstreamVerified2`. The CRD declared only those two, so an environment with more than two upstream environments failed; the list has no limit. The Graph sets the field (#660)
- **Graph controller fork pin** `745998f` → `81c5a03` — health Watch nodes drop `readyWhen`, so the new fork does not patch the watched Deployment or Application; WatchKind nodes are scoped to the environment namespace (#654)

### Fixed

- **PromotionStep reacts to PRStatus and PolicyGate changes** — the reconciler watches both, so a merged PR or a gate that changes state moves the step on at once instead of at the next requeue (#655)
- **Bundle reconciler watches Pipeline changes** — Graph is regenerated when Pipeline spec changes (new environments, updated policyNamespaces, changed git config). Previously Pipeline changes were invisible to in-flight Bundles (#634)
- **Subscription deduplication under HA** — uses label selector (`kardinal.io/source-digest`) instead of status field comparison; safe under concurrent reconciles and multiple controller replicas (#636)
- **CEL documentation accuracy** — corrected false claims about `pkg/cel/NewCELEnvironment()` (does not exist) and `schedule.*` (map variable, not CEL library function) in design docs and code comments (#631)
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
- **Pipeline deployment metrics** — `Pipeline.status.deploymentMetrics` aggregated by `PipelineReconciler`: `rolloutsLast30Days`, `p50CommitToProdMinutes`, `p90CommitToProdMinutes`, `autoRollbackRate` (#498, #511)
- **`changewindow.isAllowed()` / `changewindow.isBlocked()` CEL functions** — named-argument helpers for ChangeWindow gates (#506)
- **ScheduleClock CRD** — writes `status.tick` on a configurable interval to drive time-based policy gate re-evaluation via real Kubernetes watch events; replaces the `ctrl.Result{RequeueAfter}` timer loop pattern (#484)
- **Graph controller fork upgraded to `948ad6c`** — DNS-1123 node ID validation, drift timers (30 min), propagation hash includes `propagateWhen` state
- **Cardinal logo** — added across docs site, UI sidebar, and README (#515)

### Changed

- **`kustomize-set-image` without the kustomize binary** — the step edits `kustomization.yaml` in Go. `kustomize-build` still runs the kustomize binary (#512)

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
- **K-04: ChangeWindow CRD** — blackout and recurring allowed-hours windows; `changewindow["name"]` CEL map variable is `true` when the window is active/blocking (#460)
- **K-05: Bundle.status.metrics** — commitToProductionMinutes, bakeResets, autoRollbacks, operatorInterventions; `kardinal metrics` CLI command
- **K-06: Wave topology** — `wave: N` field on environment spec; Wave N automatically depends on all Wave N-1 stages
- **K-07: Integration test step** — built-in `integration-test` step runs a Kubernetes Job as part of the promotion sequence (#470)
- **K-08: PR review gate** — `bundle.pr["staging"].isApproved` and `.approvalCount` in CEL context via PRStatus CRD (#472)
- **K-09: `kardinal override` with audit record** — emergency gate override with mandatory reason + time limit; the override is recorded in the gate's `spec.overrides[]`, and the gate reason shows it in the PR evidence body (#471)
- **K-10: Cross-stage history CEL** — `upstream.<env>.soakMinutes`, `.recentSuccessCount`, `.recentFailureCount`, `.lastPromotedAt` in gate expressions (#473)
- **Policy test** — `kardinal policy test` checks PolicyGate YAML and CEL syntax offline (#235)
- **UI control plane** — all 7 UI issues shipped (#462–#468): fleet health dashboard, pipeline ops view, per-stage bake countdown, in-UI actions (pause/resume/rollback/override), release metrics bar, bundle timeline, policy gate detail panel

### Fixed

- Graph controller fork upgraded to `948ad6c` — DNS-1123 node ID validation, drift timers, propagation hash improvements
- `changewindow.isAllowed()` / `changewindow.isBlocked()` CEL helpers added alongside the map-style access

---

## [v0.4.0] — 2026-04-11

**Distributed Mode, Argo Rollouts delegation, graph purity, K-series features**

### Added

- **Argo Rollouts delivery delegation** — `delivery.delegate: argoRollouts` in Pipeline env spec hands off rollout progression to an existing `Rollout` resource (#197)
- **GitLab + Forgejo/Gitea SCM providers** — selected per controller with `--scm-provider gitlab` or `--scm-provider forgejo` (also `gitea`)
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
- **Distributed mode** — `--shard` flag routes PromotionSteps to matching shard agents; supports multi-cluster deployments where each spoke cluster runs its own agent (#196)
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
- **Health Watch nodes** — for each environment with `health.type`, the Graph watches the health resource (Deployment, Argo CD Application, Flux Kustomization, Argo Rollout, Flagger Canary) (#191, #194)
- **Promote command** — `kardinal promote` creates a Bundle from the last verified image (#160)
- **UI: 5s polling and bundle history** — the UI refreshes every 5 seconds and lists earlier Bundles of the selected pipeline (#170)

### Fixed

- **Promotion working directory** — the git working directory is recorded in `PromotionStep.status` and removed when the step finishes (#195)
- `kardinal policy list` shows `Pending` for a gate that was not evaluated yet, as `kardinal explain` does, instead of `unknown` (#170)
- `time.Now()` calls moved outside reconciler hot paths into CRD status writes
- Cross-CRD status mutations eliminated — each reconciler writes only to its own CRD
- `exec.Command()` in reconciler replaced with library call

---

## [v0.2.0] — 2026-04-11

**Workshop 1 Complete — first end-to-end validated release**

### Milestone

kardinal-promoter now executes the [AWS Platform Engineering on EKS workshop](https://catalog.workshops.aws/platform-engineering-on-eks/en-US/30-progressiveapplicationdelivery/40-production-deploy-kargo) end-to-end on a live kind cluster.

### Fixed

- kind E2E infrastructure (`make setup-e2e-env`) sets up the Graph controller + ArgoCD + test/uat/prod namespaces
- `kardinal get pipelines` shows per-environment status columns (#128)
- `kardinal explain` shows active PolicyGates with CEL expression and current value (#129)
- **Graph node IDs are CEL-safe** — node IDs use underscores, because CEL reads a hyphen as minus; the Kubernetes object names keep hyphens
- **git push** — pushes `HEAD:<branch>`, so a push no longer fails with `src refspec does not match any`
- **Git token from the Pipeline** — the token is read from the Secret in `Pipeline.spec.git.secretRef`
- **PromotionStep and PolicyGate schemas** — PromotionStep spec declares `upstreamVerified` and `requiredGates`, and PolicyGate spec declares `upstreamEnvironment`, the fields the Graph writes; without them the Graph controller rejected the objects
- **RBAC for health checks** — the ClusterRole can read Deployments, Argo CD Applications and Flux Kustomizations
- **Existing promotion PR** — when the PR is already open (for example after a controller restart), the controller finds it instead of failing with `422`
- **GitHub token in the Helm chart** — `github.secretRef` and `github.token` values pass `GITHUB_TOKEN` to the controller
- **Graph controller fork pin** upgraded to `9c18aa34`, which re-evaluates `propagateWhen` after the managed resource's status changes

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
- **MetricCheck CRD** — Prometheus-backed policy gates: block promotions when error rate > threshold, with `metrics.<name>.result` and upstream soak time in gate expressions (#114)
- **Custom promotion steps** — HTTP webhook steps for extensible promotion workflows (#124)
- **Auto-rollback** — configurable failure threshold triggers rollback PR after N consecutive health failures (#77)
- **Pause/resume** — `kardinal pause/resume <pipeline>` halts in-flight promotions (#63, #110)
- **Policy simulate and list** — `kardinal policy simulate` evaluates gates without creating a Bundle; `kardinal policy list` lists the PolicyGates of a pipeline or environment (#63)
- **Config Bundle type** — promotes Git commit SHAs through the same pipeline as image Bundles (#78)
- **Rendered manifests step** — `layout: branch` with `kustomize-build` writes environment-specific YAML (#82)
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
