<!--
Copyright 2026 The kardinal-promoter Authors.
Licensed under the Apache License, Version 2.0
-->

# Design 15: Production Readiness — Competitive Gap Analysis

> Created: 2026-04-20
> Status: Active — gap tracking
> Lens: "Would a platform team at a Series B company choose kardinal-promoter over Kargo
> in a competitive evaluation today?" Every 🔲 item below is a reason the answer is No.

This doc is maintained by the vibe-vision-auto scan. Engineers pick items from this
doc's Future section and open `kind/enhancement` issues to close them.

---

## Purpose

The standard design docs (01–14) track feature implementation. This doc tracks
**competitive gaps, production-stability defects, and adoption blockers** that have no
other home. The PDCA scenarios test what we have — they do not test what we are missing.

Every item in this doc was identified by examining the live codebase against five lenses:

1. **Kargo parity** — what Kargo does that kardinal cannot model at all
2. **Production stability** — what would a platform team find broken after a week in prod
3. **Observability** — can an operator understand a stalled Bundle without reading Go logs?
4. **Security posture** — would a security review at a Series B company pass this?
5. **Adoption** — what makes a platform engineer close the GitHub tab within 60 seconds?

---

## Present ✅

- ✅ **`kubectl get` printer columns on Bundle and PromotionStep CRDs** — Bundle now shows Type, Pipeline, Phase, Age. PromotionStep shows Pipeline, Env, Bundle, State, Age. Eliminates the need to `kubectl describe` to find which pipeline a step belongs to. (PR #903, 2026-04-20)
- ✅ **WaitingForMerge timeout** — `environment.waitForMergeTimeout` on Pipeline environments (e.g. `"24h"`) causes a PromotionStep stuck in `WaitingForMerge` to transition to `Failed` after the configured duration. No timeout by default (existing behavior preserved). Closes production-blocker: abandoned PR reviewers no longer stall pipelines indefinitely. (PR #906, 2026-04-20)
- ✅ **ScheduleClock minimum interval guard** — `pkg/reconciler/scheduleclock/reconciler.go` enforces `minInterval = 5 * time.Second` in `parseInterval()`. A zero or negative `spec.interval` is clamped to 5s, not looped at 0. (verified 2026-04-20; this item was incorrectly listed as Future)
- ✅ **SBOM attestation on the controller image** — `.github/workflows/release.yml` generates an SBOM with `anchore/sbom-action` (syft) and attaches it as a cosign attestation via `cosign attest`. SLSA Level 2 provenance also attached. (verified 2026-04-20; item was incorrectly listed as Future)
- ✅ **ValidatingAdmissionPolicy for Pipeline, Bundle, PolicyGate CRDs** — `chart/kardinal-promoter/templates/validating-admission-policy.yaml` validates required fields, updateStrategy enum, approval enum, and spec.expression at admission time. Requires Kubernetes 1.28+. Enabled by default; set `validatingAdmissionPolicy.enabled=false` for older clusters. Remaining gap: cycle detection in `dependsOn` (requires a webhook, not expressible in CEL VAP) — see Future item below. (verified 2026-04-20)
- ✅ **Bundle history GC — `historyLimit` enforced by bundle reconciler** (PR #910, 2026-04-20) — `Pipeline.spec.historyLimit` is now enforced in `pkg/reconciler/bundle/reconciler.go:enforceHistoryLimit`. On each new Bundle creation, terminal Bundles (Verified/Failed/Superseded) beyond the limit are deleted oldest-first. Default limit: 50. Kargo parity achieved.
- ✅ **Reconciler panic recovery — handled by controller-runtime default** (PR #920, 2026-04-20) — Verified against controller-runtime v0.23.3 source: `RecoverPanic` defaults to `true` in the controller-runtime internal controller package. A panic in any reconciler's `Reconcile()` increments `ReconcilePanics` metric, calls panic handlers, and returns a wrapped error for exponential backoff — no crash loop. DO NOT set `RecoverPanic: false` in `ctrl.Options{}`. See comment in `cmd/kardinal-controller/main.go`.
- ✅ **UI API Bearer token authentication** (PR #924, 2026-04-20) — `--ui-auth-token` flag (env: `KARDINAL_UI_TOKEN`) added to `cmd/kardinal-controller/main.go`. When set, all `/api/v1/ui/*` routes require `Authorization: Bearer <token>`. Static `/ui/*` assets bypass auth. Constant-time comparison via `crypto/subtle`. Warn-level log on startup when token is not set. Default is open (no token) for backwards compatibility. Tests in `cmd/kardinal-controller/ui_auth_test.go`. Remaining gaps: CORS lockdown (see Future items below).
- ✅ **TLS support for UI and webhook endpoints** (PR #937, 2026-04-20) — `--tls-cert-file` / `--tls-key-file` flags (env: `KARDINAL_TLS_CERT_FILE`, `KARDINAL_TLS_KEY_FILE`) added to both UI (`:8082`) and webhook (`:8083`) servers via `listenAndServeWithTLS()`. When both flags are set, `http.ListenAndServeTLS` is used; if neither is set, falls back to plain HTTP (backwards compatible). Helm chart exposes `controller.tlsCertFile` and `controller.tlsKeyFile` values for cert-manager or secret-mounted certificates. Remaining gap: CORS lockdown for cross-origin dashboard use (see Future items below).

---

## Future

### Lens 1: Kargo parity — capability gaps that lose competitive evaluations

- ✅ **NotificationHook CRD for outbound event notifications** (PR #914) — `NotificationHook` CRD added with `spec.webhook.url`, optional `spec.webhook.authorizationHeader`, `spec.events` (Bundle.Verified/Failed, PolicyGate.Blocked, PromotionStep.Failed), and optional `spec.pipelineSelector`. Reconciler watches Bundle, PolicyGate, and PromotionStep objects and fires HTTP POST webhooks at-most-once per event (idempotent via `status.lastEventKey`). JSON payload includes `event`, `pipeline`, `bundle`, `environment`, `message`, and `timestamp`. User docs at `docs/notifications.md` with Slack example.

- ✅ **ArgoCD-native image update step** (PR #915, 2026-04-21) — `update.strategy: argocd` added. The `argocd-set-image` built-in step patches `spec.source.helm.valuesObject.<imageKey>` on the ArgoCD `Application` resource via the Kubernetes API — no git commit, no PR. Promotion sequence: `argocd-set-image → health-check`. Configured via `update.argocd.{application, namespace, imageKey}` in the Pipeline environment spec. `ArgoCDUpdateConfig` API type added. Unlocks teams using inline ArgoCD Application Helm values without restructuring their GitOps setup. User docs at `docs/argocd-native-promotion.md`.

- ✅ **No GitHub Actions native bundle creation** — `.github/actions/create-bundle/action.yml` added as a composite GitHub Action. Inputs: `pipeline` (required), `image` (single image shorthand), `digest` (override digest), `images` (newline-separated multi-image list), `namespace`, `kardinal-url`, `type`. Authenticates via `KARDINAL_TOKEN` env var. Outputs: `bundle-name`, `bundle-namespace`, `bundle-status-url` (points to `${kardinal-url}/ui#pipeline=${pipeline}`). Retries up to 3× with exponential backoff on transient failures; does not retry on HTTP 4xx. Logic tested by `.github/actions/create-bundle/test.sh` (no network required). CI runs the test in the `action-tests` job. `docs/ci-integration.md` updated with complete input/output table and multi-image example. (PR #916)

- ✅ **No UI for Bundle creation / triggering promotions** — `CreateBundleButton` component added to the pipeline header (ActionBar area). Clicking opens `CreateBundleDialog` with required image input (id=`bundle-image`), optional commitSHA (id=`bundle-commit-sha`) and author fields. On submit, calls `POST /api/v1/ui/bundles` — a new endpoint on the UI API server that creates a Bundle CRD with `spec.type="image"`, `spec.images=[{repository, tag|digest}]`, and `spec.provenance.{commitSHA, author}`. Image references parsed by `parseUIImageRef` (handles `repo:tag`, `repo@sha256:digest`, bare repo). Returns 201 on success; inline error on failure; buttons disabled during loading. Backend tests in `cmd/kardinal-controller/ui_api_test.go`; frontend tests in `web/src/components/CreateBundleDialog.test.tsx`. (PR #917, 2026-04-21)

- ✅ **Warehouse-equivalent: subscription CLI visibility** — `kardinal get subscriptions` command added with columns: NAME, TYPE, PIPELINE, PHASE, LAST-CHECK, LAST-BUNDLE, AGE. Supports `--all-namespaces` / `-A` and `-o json`/`-o yaml`. Aliases: `subscription`, `sub`. `kardinal get pipelines` now includes a SUB column showing the count of actively-watching Subscriptions per pipeline. The Subscription CRD (K-10) is now surfaced to users without requiring `kubectl get subscriptions`. (PR #918)

### Lens 2: Production stability — what breaks after a week in production

- ✅ **Bundle reconciler orphan guard races with Pipeline deletion** — `pkg/reconciler/bundle/reconciler.go:134` handles the case where the parent Pipeline was deleted by self-deleting the Bundle. This is triggered by checking `isNotFound` on the Pipeline. If the Pipeline is being deleted (DeletionTimestamp set but finalizers not cleared), the check may transiently pass, causing premature Bundle deletion before the Pipeline's owned resources are cleaned up. Add a check for `pipeline.DeletionTimestamp != nil` and requeue instead of deleting. (PR #919)

- ✅ **Git credential rotation with zero downtime** — `--scm-token-secret-name` (env: `KARDINAL_SCM_TOKEN_SECRET_NAME`) flag added. When set, the controller creates a `DynamicProvider` (`pkg/scm/dynamic.go`) that wraps the `SCMProvider` behind an `atomic.Pointer` and a `SecretWatcher` (`pkg/scm/secret_watcher.go`) that polls the named Kubernetes Secret every 30s. On token change, `DynamicProvider.Reload` atomically swaps the inner provider. Subsequent reconcile calls use the new token without a controller restart. The initial token from `--github-token` / `GITHUB_TOKEN` is used for bootstrapping. `--scm-token-secret-namespace` defaults to `POD_NAMESPACE` → `kardinal-system`. `--scm-token-secret-key` defaults to `"token"`. Kargo parity achieved. (issue #969)

### Lens 3: Observability — can an operator understand a stall without Go logs?

- ✅ **Missing Prometheus metrics for step duration and gate blocking time** — Added three new histograms to `pkg/reconciler/observability/metrics.go`: (a) `kardinal_step_duration_seconds{step}` — emitted per step type (git-clone, kustomize, open-pr, etc.) in `updateStepStatuses`; (b) `kardinal_gate_blocking_duration_seconds` — emitted in policygate `patchStatus` when a gate transitions from blocked to allowed (uses CreationTimestamp as upper-bound proxy); (c) `kardinal_promotionstep_age_seconds` — emitted at terminal state transitions (Verified and Failed) in the promotionstep reconciler. Grafana dashboards can now answer "which steps are slow?" and "which gates are blocking prod right now?" (PR #972 series, 2026-04-21)

- ✅ **`kardinal status <pipeline>` shows in-flight promotion details** — `cmd/kardinal/cmd/status.go` extended. `kardinal status` (no args) preserves existing cluster summary. `kardinal status <pipeline>` shows: active bundle(s), PromotionStep table with ENV/STATE/ACTIVE-STEP/PR/AGE columns (in-progress states marked with `▶`), and a "Blocking Policy Gates" table (GATE/ENV/EXPRESSION/REASON/LAST-CHECKED) when gates have `status.ready=false`. `ACTIVE STEP` column shows the first non-terminal step from `status.steps[]` — tells operators exactly which step is running. Terminal-state hint shown when all steps are Verified/Failed. 5 new unit tests. (issue #973)

- ✅ **`kardinal logs` surfaces static snapshot only — per-step granularity missing** — `cmd/kardinal/cmd/logs.go` now renders `status.steps[]` entries as a tabulated section below each PromotionStep header. Each row shows step name, state, duration (e.g. `2.5s` or `-` if not yet complete), and message (truncated to 80 chars). If `status.steps[]` is empty, the table is omitted. Operators can now see exactly which step (git-clone, kustomize-set-image, open-pr, etc.) failed and the associated error message without reading `kubectl describe` YAML. 5 unit tests added in `logs_test.go`. (PR #974 series, 2026-04-21)

### Lens 4: Security posture — what a Series B security review would flag

- ✅ **Kubernetes TokenReview-based auth for UI API** (PR #tbd, 2026-04-21) — `--ui-tokenreview-auth` flag (env: `KARDINAL_UI_TOKENREVIEW_AUTH=true`) added. When enabled (and `--ui-auth-token` is not set), the UI API server validates each bearer token by calling `authenticationv1.TokenReview` against the Kubernetes API server. Cluster users can authenticate with their kubeconfig tokens — no shared static secret required. Static `--ui-auth-token` takes precedence when both flags are set (O4). Fail-closed: if the TokenReview API call fails, the server returns 503 (not 200 open). Implementation in `pkg/uiauth/` with `TokenReviewer` interface for testability. 7 unit tests in `pkg/uiauth/tokenreview_test.go`.

- ✅ **No admission webhook for `dependsOn` cycle detection** (PR #tbd, 2026-04-21) — `pkg/admission/pipeline_webhook.go` adds `PipelineWebhookHandler` (a `ValidatingAdmissionWebhook` handler) mounted at `POST /webhook/validate/pipeline` on the existing webhook server (`:8083`). Enabled via `--pipeline-admission-webhook` flag (env: `KARDINAL_PIPELINE_ADMISSION_WEBHOOK=true`). The handler decodes the `AdmissionReview`, calls `graph.DetectCycle` (pure, no I/O), and returns `allowed=false` with the cycle path in the `status.message` when a cycle is found. Operators must install a `ValidatingWebhookConfiguration` pointing at this endpoint — not auto-created to avoid requiring cluster-admin at install time. As a fallback (webhook disabled or bypassed), the bundle reconciler now sets `InvalidSpec/CircularDependency` condition on the Bundle instead of the generic `Failed/TranslationError`, making the root cause observable via `kubectl get bundle` without reading Go logs. 5 unit tests in `pkg/graph/cycle_test.go` and 4 in `pkg/admission/pipeline_webhook_test.go`.

- ✅ **SCM token scopes are not validated at startup** — `pkg/scm/token_validator.go` adds `ValidateGitHubTokenScopes`, `ValidateGitLabTokenScopes`, and `ValidateForgejoTokenScopes`. At controller startup (before `mgr.Start()`), `main.go` calls the appropriate validator. GitHub: calls `GET /user`, inspects `X-OAuth-Scopes` header — warns if `repo` and `public_repo` are both absent. GitLab: calls `/api/v4/personal_access_tokens/self`, warns if `api` scope absent. Forgejo: calls `/api/v1/user`, warns on 401. Fine-grained PATs (no `X-OAuth-Scopes` header) are skipped — GitHub does not expose their scopes via `/user`. Network errors are logged at debug level (non-fatal). The check runs only when `--scm-token-secret-name` is NOT set (static token path). The check is never run in a reconciler — no Graph-purity violation. (issue #977)

### Lens 5: Adoption — what makes a platform engineer close the GitHub tab

- ✅ **`helm install` to first Bundle in under 10 minutes** — `demo.enabled=true` added to the Helm chart (`chart/kardinal-promoter/values.yaml`). When set, a `demo` Pipeline CR is created automatically targeting `pnz1990/kardinal-demo` with three environments (test=auto, uat=auto, prod=pr-review). The user runs three commands: `helm install --set demo.enabled=true --set github.token=$PAT`, `kardinal get pipelines`, and `kardinal create bundle demo --image <sha>`. No GitOps repo scaffolding required. `docs/quickstart.md` updated with a "Fast Start — under 10 minutes" section at the top. (PR #tbd, 2026-04-22)

- ✅ **`kardinal get subscriptions` CLI command** — `Subscription` CRD and watchers are shipped (K-10) but were invisible from the CLI. `kardinal get pipelines` now shows a SUB column. `kardinal get subscriptions` lists all Subscriptions with NAME, TYPE, PIPELINE, PHASE, LAST-CHECK, LAST-BUNDLE, AGE columns. Supports `--all-namespaces` and `-o json`/`-o yaml`. Aliases: `subscription`, `sub`. A user can now verify Subscription operation without `kubectl get subscriptions`. (PR #918)

- ✅ **Community presence — initial community health files** (PR #tbd, 2026-04-22) — `CONTRIBUTING.md` added at the repository root with build/test instructions, contribution workflow, code standards, and links to GitHub Issues and Discussions. `docs/index.md` updated with a "Community" section. GitHub Discussions board enablement requires repo admin access (API returns 403 with the agent token) — this is documented as a `[NEEDS HUMAN]` step: a human must enable Discussions via the GitHub repo Settings UI. Once enabled, the "Getting Started", "Show & Tell", "Feature Requests", and "Q&A" categories should be seeded. See [issue #979](https://github.com/pnz1990/kardinal-promoter/issues/979) for the open tracking item.

- ✅ **No ADOPTERS.md or case studies** — `ADOPTERS.md` created at repository root. First entry: the PDCA validation loop (self-use). The file includes a Markdown table with Organization/Use Case/Environment/Added columns and instructions for adding new entries. (PR #tbd, 2026-04-21)

- ✅ **No `kardinal completion` works for all shells** — shell completion tests now verify: (a) bash completion is non-empty and contains `__start_kardinal`; (b) zsh completion is non-empty and contains `_kardinal`; (c) `TestCompletion_CoreSubcommandsComplete` exercises cobra's `__complete` protocol to verify all core subcommands (`get`, `explain`, `logs`, `status`, `rollback`, `approve`) are reachable. The `__complete` test catches command tree mis-wiring that the static script tests cannot — cobra V2 completion scripts are dynamic and do not embed command names. (PR #1001, 2026-04-21)

### Lens 6: New gaps identified by Kargo comparison scan (2026-04-20)

- ✅ **Bundle `status.conditions` are declared but never populated** — `pkg/reconciler/bundle/reconciler.go` now calls `setBundleCondition()` at every phase transition: `Ready=False/Available` (new bundle received), `Ready=False/Promoting` (graph created, promotion in progress), `Ready=False/Failed + Failed=True/TranslationError` (translator error), `Ready=False/Superseded` (superseded by newer bundle), and `Ready=True/Verified` (all environments verified via handleSyncEvidence). Operators can now use `kubectl wait --for=condition=Ready bundle/<name>` and GitOps controllers (Flux, ArgoCD) can gate on standard K8s conditions. (PR #982 series, 2026-04-21)

- ✅ **No namespace-scoped controller mode** — `controller.watchNamespace` Helm value added (default `""` = cluster-wide). When set to a namespace name: (a) the controller binary uses `cache.Options{DefaultNamespaces: {ns: {}}}` to limit its informer cache; (b) the Helm chart renders a `Role`/`RoleBinding` scoped to the watch namespace instead of `ClusterRole`/`ClusterRoleBinding`. A security review at a company with shared clusters can now install kardinal with namespace-scoped RBAC. (PR #tbd, 2026-04-21)

- ✅ **Bitbucket and Azure DevOps SCM providers are absent** — `pkg/scm/bitbucket.go` adds `BitbucketProvider` implementing `SCMProvider` against Bitbucket Cloud API v2.0 (`Bearer` token auth, HMAC-SHA256 webhook validation via `X-Hub-Signature`). `pkg/scm/azuredevops.go` adds `AzureDevOpsProvider` implementing `SCMProvider` against Azure DevOps REST API v7.1 (PAT via `Basic base64(:<PAT>)` auth, shared-secret webhook validation via `X-AzureDevOps-Token`). Both providers support `OpenPR` (idempotent: returns existing PR on conflict), `ClosePR`, `CommentOnPR`, `GetPRStatus`, `GetPRReviewStatus`, `ParseWebhookEvent`, and `AddLabelsToPR` (no-op for both platforms — neither supports PR labels natively). `factory.go` updated: `"bitbucket"` and `"azuredevops"` are now valid provider types. Teams on Bitbucket Cloud or Azure DevOps can now use kardinal with `scm.provider: bitbucket` or `scm.provider: azuredevops` in their Pipeline. Kargo parity achieved for SCM platform coverage. (PR #tbd, 2026-04-21)

- ✅ **No reusable PromotionTemplate concept** — `PromotionTemplate` CRD added (`api/v1alpha1/promotiontemplate_types.go`). Environments reference it via `spec.environments[].promotionTemplate: {name: <template>, namespace: <ns>}`. The translator inlines the steps at graph-build time (`pkg/translator/template_resolver.go:inlinePromotionTemplates`). Local `env.steps` takes precedence when both are set. The CRD is registered in `config/crd/bases/kardinal.io_promotiontemplates.yaml`; RBAC in `chart/kardinal-promoter/templates/clusterrole.yaml`. Documented in `docs/concepts.md §PromotionTemplate`. (PR #985, 2026-04-21)

- ✅ **`kardinal init` generates Pipeline YAML but does not scaffold the GitOps repo** — `cmd/kardinal/cmd/init.go` now supports `--scaffold-gitops` (creates `environments/<env>/kustomization.yaml` overlays in a `--gitops-dir` directory, default `.gitops`) and `--demo` (uses `ghcr.io/pnz1990/kardinal-test-app:sha-DEMO` as the placeholder image). The scaffold is idempotent — existing files are never overwritten. This closes the 80% of the onboarding gap where users needed to understand GitOps repo structure before creating a working Pipeline. (PR #tbd, 2026-04-21)

### Lens 7: New gaps identified by competitive scan (2026-04-20)

- ✅ **`RequeueAfter: time.Millisecond` in bundle reconciler is a production hot loop** — replaced with `RequeueAfter: 500*time.Millisecond` in `pkg/reconciler/bundle/reconciler.go`. The 1ms value bypassed controller-runtime rate limiting and would cause API server CPU and etcd write pressure under concurrent Bundle load (>10 pipelines). 500ms is the minimum safe floor; the controller-runtime workqueue deduplicates simultaneous events within this window. (PR #987 series, 2026-04-21)

- 🔲 **No `maxConcurrentPromotions` cap per pipeline** — Kargo enforces `maxConcurrentPromotions` on a Stage, preventing runaway promotion storms (e.g. when a CI system creates 50 Bundles within seconds after a maintenance window). kardinal has no such field. The bundle reconciler will attempt to promote all available Bundles simultaneously. For large organizations with many pipelines, this can saturate git hosts with concurrent PR-open requests, exhaust GitHub API rate limits (5000 req/hr), and create merge conflicts in the GitOps repo. Add `Pipeline.spec.maxConcurrentPromotions` (default: 0 = unlimited, compatible with existing behavior) enforced in the bundle reconciler before creating the Graph.

- 🔲 **No image signature verification step** — Kargo v1.10 added a `verify-image` promotion step that runs `cosign verify` against the container image before it advances to the next stage. kardinal has no equivalent: any image digest (including a compromised or unauthorized one) will be promoted without verifying the publisher's signature. For a security-conscious platform team, this is a supply-chain control gap. Add a `verify-image` step type in `pkg/steps/steps/` that runs `cosign verify --certificate-oidc-issuer <issuer> --certificate-identity-regexp <regex> <image>` and fails the PromotionStep if the signature is absent or invalid. This is also a differentiator: both Kargo and kardinal can advertise cosign integration.

- 🔲 **No Kubernetes Events emitted by reconcilers** — reconcilers in `pkg/reconciler/` write audit records to a ConfigMap (`writeAuditEvent`) and update CRD status fields. They do NOT emit Kubernetes Events via `EventRecorder`. This means `kubectl describe bundle <name>` and `kubectl describe promotionstep <name>` show no event history — the most common operator debugging tool is silent. Add `EventRecorder` to each reconciler and emit `Normal`/`Warning` events on: Bundle phase transitions (Available→Promoting→Verified/Failed), PolicyGate first-block, PromotionStep state transitions (Executing→WaitingForMerge→Verified), and PromotionStep failure with the step name and error message. This surfaces all promotion activity in `kubectl get events -n kardinal-system` without requiring Go log access.

- 🔲 **No multi-tenant project isolation** — Kargo has a Project CRD that namespaces all Stages, Promotions, and Warehouses under a single owner entity, with RBAC scoped to the project. kardinal has no equivalent: all Pipelines and Bundles share the same namespace with the same ClusterRole. A platform team running kardinal for 20 application teams cannot grant Team A write access to their Pipeline without also granting them read access to Team B's pipelines and bundles. Until namespace-scoped controller mode is added (see Lens 6), document the recommended workaround: one kardinal install per application namespace. This workaround is costly (multiple controller replicas), but it is the only safe multi-tenant configuration today.

### Lens 8: New gaps identified by vision scan (2026-04-20)

- 🔲 **No per-step execution timeout** — `pkg/steps/steps/git_clone.go`, `kustomize.go`, and `helm_set_image.go` have no per-execution timeout. A `git clone` against a slow SCM host or a `kustomize build` on a large repo can block a PromotionStep's `Reconcile()` call indefinitely. With controller-runtime's default `MaxConcurrentReconciles=1` per resource type, a single hung step can stall ALL other PromotionSteps of that pipeline. Add a `Pipeline.spec.environments[].stepTimeoutSeconds` field (default: 300) propagated via `StepState.Config["stepTimeoutSeconds"]`. Each step executor must wrap its operation in `context.WithTimeout`. This is especially critical for `git-clone` where network hangs are common in restricted egress environments.

- 🔲 **`kardinal logs` has no `--follow` / streaming mode** — `cmd/kardinal/cmd/logs.go` renders a static snapshot of `PromotionStep.status.stepMessages` at the time of the call. There is no `--follow` flag to stream messages as steps execute. For a platform engineer watching an active `git-clone` → `open-pr` sequence, they must repeatedly run `kardinal logs <pipeline>` to see progress. Add a `--follow` flag that polls for status changes every 2s (or watches the resource) and streams new `stepMessages` entries as they are appended. This is the key observability feature a new user needs to trust that something is actually happening.

- ✅ **`kardinal status <pipeline>` is not per-pipeline** — `cmd/kardinal/cmd/status.go` extended with pipeline-name argument. `kardinal status <pipeline>` now shows: active bundle(s), PromotionStep table with ENV/STATE/ACTIVE-STEP/PR/AGE columns, and a "Blocking Policy Gates" table (GATE/ENV/EXPRESSION/REASON/LAST-CHECKED) when gates have `status.ready=false`. (PR #997, 2026-04-21)

- ✅ **No CORS lockdown on UI API** — the UI API (`/api/v1/ui/*`) has no `Access-Control-Allow-Origin` header restriction. Any web page loaded in the same browser as the kubectl port-forward session can issue cross-origin requests to the UI API and read all pipeline/bundle/gate data. For a security-conscious operator who port-forwards the UI to localhost, this is a cross-origin data exfiltration risk. Add CORS middleware that: (a) restricts `Access-Control-Allow-Origin` to `localhost:*` and the configured external UI URL when `--cors-allow-origin` is set; (b) sets `Access-Control-Allow-Credentials: false` (UI uses Bearer, not cookies); (c) allows only `GET`, `POST`, `OPTIONS` methods. This is a prerequisite for any future external dashboard deployment. Tracked in `docs/design/06-kardinal-ui.md`. (PR #940)

### Lens 9: New gaps identified by vision scan (2026-04-20, pressure lens pass 2)

- 🔲 **No Grafana dashboard shipped with the Helm chart** — `docs/aide/roadmap.md` claims "Grafana dashboard JSON in `config/monitoring/`" as a delivery in a completed milestone, but `config/monitoring/` does not exist. `docs/guides/monitoring.md` says "Dashboard ID: pending submission to grafana.com". A platform team that installs kardinal and opens their Grafana instance has nothing to import. `PrometheusRule` alerting rules are shipped (see chart template), but a dashboard JSON (bundle phase counts, step duration histograms, gate blocking time) is the operability artifact that makes Prometheus useful day-to-day. Ship a `config/monitoring/kardinal-promoter-dashboard.json` and a Helm chart ConfigMap template (`grafana.sidecar.dashboards.enabled=true` compatible) so Grafana auto-discovers it.

- ✅ **`kardinal logs` does not render per-step `status.steps[]` entries** — `logsFn` now iterates `status.steps[]` and renders a table with STEP/STATE/DURATION/MESSAGE columns. Duration is shown in seconds (`2.5s`) when `DurationMs > 0`, `-` otherwise. Message is truncated at 80 chars. Table is omitted when `status.steps[]` is empty. This is the primary debugging surface for a failed promotion. (PR #974 series, 2026-04-21)

- ✅ **`kardinal status <pipeline>` shows cluster summary, not per-pipeline detail** — per-pipeline view shipped. `kardinal status <pipeline>` now shows in-flight PromotionStep states (active step highlighted), blocking PolicyGates (CEL expression and current result), and open PR URLs. (PR #997, 2026-04-21)

- 🔲 **PrometheusRule alert runbook URLs reference missing anchors in `docs/troubleshooting.md`** — `chart/kardinal-promoter/templates/prometheusrule.yaml` references four `runbook_url` anchors: `#start-here-kardinal-doctor` (exists), `#promotion-is-stuck` (missing), `#bundle-not-promoting` (missing), `#graph-controller-issues` (missing). `docs/troubleshooting.md` has the `## Start here: \`kardinal doctor\`` section (rendering as `#start-here-kardinal-doctor`), but the three other anchors are absent. A production alert that fires and shows a dead runbook URL is worse than no runbook at all — the operator wastes time on a 404. Add the three missing sections to `docs/troubleshooting.md`. ⚠️ Inferred from pressure lens: production-readiness requires that every runbook URL in a shipped alert resolves to an actual section.

- ✅ **`kardinal completion` CI test is absent — completion scripts may silently break** — `TestCompletion_CoreSubcommandsComplete` in `cmd/kardinal/cmd/completion_test.go` verifies all core subcommands are reachable via cobra's `__complete` protocol. The test exercises `__complete ""` directly, which returns the top-level command list — this is what tab-completion actually uses at runtime, catching command tree mis-wiring that static script inspection cannot catch. (PR #1001, 2026-04-21)

### Lens 10: Structural gaps identified by pressure lens scan (2026-04-21)

- 🔲 **Doc-15 items are structurally starved by the COORDINATOR's alphabetic doc ordering — product gaps never ship** — the COORDINATOR's queue-gen reads `docs/design/` in alphabetic order. Doc 12 has 45 items; doc 13 has 15 items. All of these are evaluated before doc 15. At a rate of 2–4 items per batch, and with doc-12/13 items also being added by vision scans, doc-15 items will never be reached in steady state. The consequence is concrete: the competitive gaps documented here (Azure DevOps SCM, image signature verification, Grafana dashboard, `kardinal logs --follow`, community presence) will never ship without human re-prioritization. The `COORDINATOR alphabetic doc ordering` item in doc 12 identifies this as a structural bug. This item is the doc-15 counterpart: it marks these items as high-priority from the product's perspective. When the doc-12 ordering fix is implemented, doc-15 must be the primary source for the round-robin queue, not a residual. Until the ordering fix ships, the COORDINATOR must be told explicitly: queue one doc-15 item per batch. Items in this doc are competitive blockers, not meta-system improvements. ⚠️ Inferred from pressure lens: "SM health signal says GREEN but products not advancing fast enough" — the root cause is not queue depth, it is queue source ordering that systematically excludes product-competitive items.

- 🔲 **Kargo community issue monitoring is not automated — competitive intelligence is manual** — the PM role is defined to monitor Kargo and GitOps Promoter release notes and issue trackers. But this is a human task with no automation: no scheduled scan reads `gh api repos/akuity/kargo/issues` and adds newly-opened high-priority issues that represent features kardinal lacks. New Kargo capabilities land weekly; kardinal's competitive gap tracking in this doc was last updated manually. Add a weekly step to the PM's batch routine: (1) fetch the 20 newest open issues from `akuity/kargo` with labels `kind/feature` or `enhancement`; (2) cross-reference against existing `🔲` items in this doc; (3) for any Kargo feature request with >5 thumbsup reactions that has no kardinal counterpart, add it as a new `🔲` item under the appropriate Lens. This converts competitive intelligence from a manual audit to an automated gap-filling mechanism. Without it, doc-15 will drift from the actual competitive landscape faster than vision scans can correct. ⚠️ Inferred from pressure lens: "Is the loop honest enough?" — competitive gap tracking that is not continuously updated is a snapshot, not a monitoring system.

---

## Triage notes

**Must-fix before v1.0 (any one of these is a production-blocker):**
1. ~~Bundle history GC (historyLimit)~~ ✅ Done (PR #910) — enforced in bundle reconciler
2. ~~PromotionStep timeout~~ ✅ Done (PR #906) — WaitingForMerge timeout added
3. ~~Reconciler panic recovery~~ ✅ Done (PR #920) — handled by controller-runtime v0.23.3 default (RecoverPanic=true)
4. ~~UI API authentication~~ ✅ Done (PR #924) — `--ui-auth-token` Bearer token auth implemented; upgrade to TokenReview is a Future item
5. ~~HTTP plain-text for UI and webhook servers~~ ✅ Done (PR #937) — TLS support added via `--tls-cert-file`/`--tls-key-file`; cert-manager compatible
6. ~~Bundle `status.conditions` never populated~~ ✅ Done — Ready/Failed/Promoting conditions now populated on every phase transition
7. ~~`RequeueAfter: time.Millisecond` hot loop in bundle reconciler~~ ✅ Done — replaced with 500ms minimum safe floor
8. No per-step execution timeout — a hung `git-clone` stalls all PromotionStep reconciles for that pipeline

**Must-fix for competitive parity with Kargo:**
1. Outbound event notifications (Slack/webhook)
2. ArgoCD-native image update step
3. ~~`kubectl get` printer columns on Bundle/PromotionStep CRDs~~ ✅ Done (PR #903)
4. Bitbucket and Azure DevOps SCM providers — blocks enterprise adoption
5. ~~Namespace-scoped controller mode~~ ✅ Done — `controller.watchNamespace` Helm value + Role/RoleBinding (PR #tbd, 2026-04-21)
6. Image signature verification step (cosign verify) — supply-chain control gap vs Kargo v1.10
7. `maxConcurrentPromotions` cap per pipeline — prevents promotion storms from CI bursts
8. No Kubernetes Events emitted by reconcilers — `kubectl describe` is silent; operators cannot diagnose without Go logs

**Adoption wins (high effort/impact):**
1. ~~`kardinal init` full GitOps repo scaffolding~~ ✅ Done — `--scaffold-gitops` and `--demo` flags added (PR #tbd, 2026-04-21)
2. GitHub Actions wrapper action
3. GitHub Discussions community presence
4. Reusable PromotionTemplate CRD
5. ~~`kardinal status <pipeline>` per-pipeline in-flight view~~ ✅ Done (PR #997, 2026-04-21)
6. `kardinal logs --follow` streaming mode — new users need real-time feedback during first promotion
7. Grafana dashboard JSON shipped in Helm chart — makes Prometheus useful out-of-the-box
8. `kardinal logs` per-step `status.steps[]` rendering — primary debugging surface for failed promotions ✅ (PR #974 series, 2026-04-21)
