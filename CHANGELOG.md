# Changelog

All notable changes are documented here. Maintained automatically by SM §4a.

## [Unreleased]

- feat(chart): add demo.enabled mode for under-10-minute quickstart (#1043)
- fix(ci): restore missing newline — opencode step YAML invalid (#1041)
- hotfix(ci): restore opencode SHA for v1.14.20 (#1039)
- chore(ci): upgrade opencode to v1.14.20 (#1038)
- docs(comparison): add Bitbucket Cloud and Azure DevOps to SCM providers row (#1037)

- feat(scm): add Bitbucket Cloud and Azure DevOps SCM providers (#1035)
- feat(api): PromotionTemplate CRD — reusable step sequences for Pipeline environments (#1032)
- feat(pm): board_project_id + active_milestone config (design doc 43) (#1030)
- docs(comparison): add NotificationHook webhook notifications row (#1028)

- test(bundle): assert RequeueAfter >= 500ms in handleNew regression guard (#1027)
- docs: add ADOPTERS.md with PDCA self-use entry (#1025)
- feat(helm): add controller.watchNamespace for namespace-scoped install mode (#1024)
- feat(cli): add --scaffold-gitops and --demo flags to kardinal init (#1022)
- feat(controller): ValidatingAdmissionWebhook for Pipeline dependsOn cycle detection (#1020)
- chore: update REPORT_ISSUE to today's daily report (#892) (#1019)
- fix(lint): resolve golangci-lint gofmt/goimports/gocritic failures (#1018)

- feat(security): add Kubernetes TokenReview-based auth for UI API (#1015)
- 2 PRs merged: metrics + logs steps table. (#1013)
- feat(cli): render status.steps[] per-step table in kardinal logs (#1012)
- docs(comparison): replace 'Where Kargo Leads' with 'Known limitations' (#1010)
- feat(docs): auto-generate cli-reference.md — published site always current (design doc 41) (#1004)
- feat(tooling): add make validate-manifests target (kubeconform local check) (#1003)
- feat(test): PDCA BROKEN guard — flag TOTAL==0 as workflow failure (#1000)
- feat(observability): add step duration, gate blocking, and PromotionStep age metrics (#992)

- feat(cli): verify kardinal completion for all shells via __complete protocol (#1001)
- fix(ci): gofmt formatting in status_test.go (#999)
- feat(cli): extend 'kardinal status' with per-pipeline in-flight promotion view (#997)
- feat(scm): validate SCM token scopes at controller startup (#996)
- feat(scm): zero-downtime SCM credential rotation via Secret watcher (#994)
- feat(bundle): populate status.conditions on all phase transitions (#991)

- fix(bundle): replace 1ms RequeueAfter hot loop with 500ms safe floor (#988)
- feat(steps): add argocd-set-image step for ArgoCD-native promotion (#966)
- feat(ci): GitHub Actions native bundle creation via composite action (#953)
- Scans clean; 6 new Future gaps added. (#952)
- feat(ui): Create Bundle dialog — POST /api/v1/ui/bundles + React dialog (#917) (#950)
- feat(cli): kardinal get subscriptions + SUB column in get pipelines (#948)
- 5 promoted, 5 reverted, 6 new gaps added. (#946)
- 9 gaps logged; 7 new Future items added. (#944)
- fix(workflow): Step 3 bash syntax error — restore scheduled runs (#943)

## [v0.8.1] — 2026-04-17

- feat(demo): complete demo environment and fix v0.8.1 release CI (#786)
- feat(ui): keyboard shortcuts modal with focus trap accessibility (#783, #785)
- fix(ui): resolve 3 CI failures from WCAG 2.1 AA enablement (#771, #780)
- feat(ui): enable color-contrast and nested-interactive axe rules — WCAG 2.1 AA (#761, #762, #771)
- fix(release): trivy exit-code 0 — report CVEs but don't block release (#787)
- feat(scm): zero-downtime SCM credential rotation via Secret watcher (#994)
- feat(cli): `kardinal completion` for all shells via `__complete` protocol (#1001)
- fix(bundle): replace 1ms RequeueAfter hot loop with 500ms safe floor (#988)
- feat(bundle): populate `status.conditions` on all phase transitions (#991)
- feat(scm): validate SCM token scopes at controller startup (#996)

## [v0.8.0] — 2026-04-17

- feat(api): AuditEvent CRD — immutable promotion event log (#673, #679)
- feat(api): AuditEvent step 2 — gate evaluation and rollback events (#680, #681)
- feat(cli): `kardinal get auditevents` — list promotion events (#683, #684)
- feat(cli): `kardinal audit summary` — aggregate promotion metrics (#685, #686)
- feat(cli): improve error messages — actionable hints for common failures (#688, #689)
- feat(api): admission webhook for Pipeline and Bundle validation (#573, #670)
- chore(graph): upgrade krocodile pin 81c5a03 → 05db829 — explicit-keyword schema (#677)

## [v0.7.0] — 2026-04-16

- feat(graph): WatchKind health nodes for O(1) incremental cache (#611, #652)
- fix(ui): fix Playwright E2E tests — 32/32 pass (#632, #650)
- feat(docs): Kargo migration guide — concept mapping, side-by-side YAML, 7-step guide (#640)
- fix(controller): add Watch on PRStatus and PolicyGate in PromotionStep reconciler (#644, #655)
- chore(graph): upgrade krocodile pin 745998f → 81c5a03 with compat fixes (#646, #654)
- refactor(graph): replace upstreamVerifiedN fields with upstreamStates list (#625, #660)
- docs(health): document health.labelSelector WatchKind mode (#656, #659)
- feat(helm): Helm chart with multi-cluster and RBAC support (#664)

## [v0.6.0] — 2026-04-14

- feat(pipeline): aggregate deployment metrics in Pipeline.status.deploymentMetrics (#498, #511)
- chore(steps): replace exec.Command(kustomize) with pure-Go YAML (#494, #512)
- fix(ci): enforce PR discipline, CI green gate, and live-cluster validation standard (#513)
- fix(pdca): repair live-cluster validation infrastructure (#514)
- feat(brand): cardinal logo across docs, UI, and README (#515)

## [v0.5.0] — 2026-04-13

- feat(policygate): PR review gate — bundle.pr[stage].isApproved CEL function (#452, #472)
- feat(policygate): cross-stage history CEL context — upstream.\<env\>.recentSuccessCount (#453, #473)
- feat(cli): `kardinal override` command with audit record (#451, #471)
- feat(steps): integration-test step — Kubernetes Job as promotion step (#449, #470)
- feat(policygate): MetricCheck result in CEL context — metrics.\<name\>.result (#452)
- feat(api): ChangeWindow CRD — time-bounded deployment gates (#453)
- fix(crd): regenerate deepcopy + PolicyGate CRD schema for all new fields

## [v0.4.0] — 2026-04-11

- fix(reconciler): implement delivery.delegate for argoRollouts health delegation (#122, #197)
- docs(pipeline-reference): update delivery.delegate to mark argoRollouts as implemented (#197)

## [v0.3.0] — 2026-04-11

- fix(controller): wire --shard flag to PromotionStepReconciler for distributed mode (#121, #196)

## [v0.2.1] — 2026-04-11

- fix(steps): persist workDir to PromotionStep.status and cleanup on terminal state (#195)
- feat(translator): inject health Watch nodes for HE-1/HE-2/HE-3 (#194)
- fix(graph-purity): eliminate PS-2/BU-1/BU-2/BU-4/PG-3 logic leaks (#193)
- feat(health): add WatchNodeTemplate for krocodile ShapeWatch node generation (#191)
- feat(ui): add 5s polling, bundle history panel (#170)
- fix(cli): extract policyGatePhase helper

## [v0.2.0] — 2026-04-11

- fix(graph): use CEL-safe identifiers for Graph node IDs (hyphens → underscores)
- fix(graph,scm): separate CEL-safe and K8s-safe node naming; fix git-push
- fix(reconciler): resolve git token from Pipeline.spec.git.secretRef
- fix(api,rbac): add upstreamVerified/requiredGates to PromotionStepSpec; RBAC for Deployments
- fix(api): add upstreamEnvironment to PolicyGateSpec for kro Graph data-flow
- fix: 3 bugs found during Workshop 1 PROD step execution

## [v0.1.0] — 2026-04-10

- Initial release of kardinal-promoter
- feat(steps): custom HTTP webhook steps (#124)
- feat(metriccheck): MetricCheck CRD + Prometheus evaluator + CEL soak time context (#114)
- feat(policygate): PolicyGate CRD with CEL expression evaluation
- feat(api): Pipeline + Bundle + PromotionStep CRDs
- feat(cli): `kardinal` CLI binary with get/create/explain/rollback commands
- feat(controller): kardinal-controller binary with full reconciler stack
- feat(controller): NotificationHook CRD for outbound event webhook notifications (#942)
- feat(ui): insecure connection warning banner + port-forward documentation (#941)
- feat(controller): CORS lockdown for UI API via --cors-allowed-origins flag (#940)
- feat(workflow): upgrade_policy — auto-upgrade agent version in Step 3 (#938)
- feat(controller): add TLS support via --tls-cert-file / --tls-key-file flags (#937)
- chore: pin agent_version to v0.2.0 (#936)
- fix(workflow): App token validation — use /repos/:repo not /user (#935)
- security(m2): pin agent_version — close attack vector 3B (otherness clone unversioned) (#934)
- fix(ci): replace Python heredoc with inline one-liners to fix YAML parse error (#933)
- fix(ci): manifest schema validation — prevent Demo E2E drift (design doc 39) (#931)
- test(api): add CRD printer column drift detection test (#930)
- docs(security): add UI API Access Control section (#929)
- fix(ci): remove invalid health sub-fields from demo and example YAML (#927)
- feat(ui): add Bearer token auth to UI API via --ui-auth-token flag (#924)
- vision(auto): autonomous scan — promote shipped items, fix stale flags, infer Kargo-competitive gaps (#923)
- 3 PRs merged: CI fix, Bundle GC, panic docs (#922)
- feat(controller): document and guard RecoverPanic invariant in controller manager (#921)
- feat(reconciler): enforce Bundle historyLimit to prevent etcd accumulation (#919)
- fix(crd): regenerate CRD schemas and deepcopy for WaitForMergeExpiry field (#908)
- feat(reconciler): add WaitingForMerge timeout to prevent stuck promotions (#906)
- feat(crd): add kubectl printer columns to Bundle and PromotionStep CRDs (#903)

