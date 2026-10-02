<!--
Copyright 2026 The kardinal-promoter Authors.
Licensed under the Apache License, Version 2.0
-->

# Design 15: Production Readiness — Competitive Gap Analysis

> Created: 2026-04-20
> Status: Historical — gap tracking stopped (#1247, #1343)
> Lens: "Would a platform team at a Series B company choose kardinal-promoter over Kargo
> in a competitive evaluation today?" No 🔲 item is left. Every gap below is closed,
> removed or withdrawn.

The agent loop that kept this doc current was retired (#1247, #1343). This doc is now a
historical record. Open an issue for a new gap.

---

## Purpose

The standard design docs (01–14) track feature implementation. This doc tracks
**competitive gaps, production-stability defects, and adoption blockers** that have no
other home. The PDCA scenarios (removed in #1378) tested what we had — they did not test what we were missing.

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
- ✅ **ValidatingAdmissionPolicy for Pipeline, Bundle, PolicyGate CRDs** — Removed. The chart ships no ValidatingAdmissionPolicy. `validatingAdmissionPolicy.enabled` is deprecated and has no effect. The CRD schemas and CEL rules, the Pipeline Ready condition and `kardinal validate` do these checks. (verified 2026-04-20; removed in v0.9.0-rc.1)
- ✅ **Bundle history GC — `historyLimit` enforced by bundle reconciler** (PR #910, 2026-04-20) — `Pipeline.spec.historyLimit` is now enforced in `pkg/reconciler/bundle/reconciler.go:enforceHistoryLimit`. On each new Bundle creation, terminal Bundles (Verified/Failed/Superseded) beyond the limit are deleted oldest-first. Default limit: 50. Kargo parity achieved.
- ✅ **Reconciler panic recovery — handled by controller-runtime default** (PR #920, 2026-04-20) — Verified against controller-runtime v0.23.3 source: `RecoverPanic` defaults to `true` in the controller-runtime internal controller package. A panic in any reconciler's `Reconcile()` increments `ReconcilePanics` metric, calls panic handlers, and returns a wrapped error for exponential backoff — no crash loop. DO NOT set `RecoverPanic: false` in `ctrl.Options{}`. See comment in `cmd/kardinal-controller/main.go`.
- ✅ **UI API Bearer token authentication** (PR #924, 2026-04-20) — `--ui-auth-token` flag (env: `KARDINAL_UI_TOKEN`) added to `cmd/kardinal-controller/main.go`. When set, all `/api/v1/ui/*` routes require `Authorization: Bearer <token>`. Static `/ui/*` assets bypass auth. Constant-time comparison via `crypto/subtle`. Warn-level log on startup when token is not set. With no auth mode set, `/api/` answers only local clients (`kubectl port-forward`). Other clients get 403 (#1262). `--cors-allowed-origins` sets CORS. Tests in `cmd/kardinal-controller/ui_auth_test.go`.
- ✅ **TLS support for UI and webhook endpoints** (PR #937, 2026-04-20) — `--tls-cert-file` / `--tls-key-file` flags (env: `KARDINAL_TLS_CERT_FILE`, `KARDINAL_TLS_KEY_FILE`) added to both UI (`:8082`) and webhook (`:8083`) servers via `listenAndServeWithTLS()`. When both flags are set, `http.ListenAndServeTLS` is used; if neither is set, falls back to plain HTTP (backwards compatible). Helm chart exposes `controller.tlsCertFile` and `controller.tlsKeyFile` values for cert-manager or secret-mounted certificates. `--cors-allowed-origins` (PR #940) sets CORS for cross-origin dashboard use. **Update (audit remediation, 2026-09-29):** `listenAndServeWithTLS()` is replaced by `httpServer` (`cmd/kardinal-controller/http_server.go`), a manager Runnable: both servers start after the informer caches sync, have read/write/idle timeouts, drain in-flight requests on shutdown, and a bind or serve error stops the controller instead of only being logged. Setting only one of the two TLS flags is now a startup error instead of a silent fallback to plain HTTP.
- ✅ **`maxConcurrentPromotions` cap per pipeline** (PR #1059, 2026-04-22) — `Pipeline.spec.maxConcurrentPromotions` (default: 0 = unlimited) is enforced in `pkg/reconciler/bundle/reconciler.go:handleAvailable` before creating the Graph. When cap > 0 and the active Promoting bundle count for the pipeline equals or exceeds the cap, the Available bundle is requeued with a 30s delay. Prevents promotion storms from CI bursts saturating git hosts or exhausting GitHub API rate limits. Backward-compatible: cap=0 means unlimited.

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

- ✅ **Kubernetes TokenReview-based auth for UI API** (PR #1015, 2026-04-21) — `--ui-tokenreview-auth` flag (env: `KARDINAL_UI_TOKENREVIEW_AUTH=true`) added. When enabled (and `--ui-auth-token` is not set), the UI API server validates each bearer token by calling `authenticationv1.TokenReview` against the Kubernetes API server. Cluster users can authenticate with their kubeconfig tokens — no shared static secret required. Static `--ui-auth-token` takes precedence when both flags are set (O4). Fail-closed: if the TokenReview API call fails, the server returns 503 (not 200 open). Implementation in `pkg/uiauth/` with `TokenReviewer` interface for testability. 7 unit tests in `pkg/uiauth/tokenreview_test.go`. **Update (audit remediation, 2026-09-29):** TokenReview mode now also authorizes: every object the UI API reads or writes for the caller is checked with a `SubjectAccessReview` (`pkg/uiauth/access.go`, `AuthorizingClient`), so a user cannot see or change more through the UI than through `kubectl`. Denied → 403, review API failure → 503, and the controller exits at startup if the review clients cannot be built (previously it logged and served an open UI). Results are cached for 30s per token and action. The web client now sends the token (kept in `sessionStorage`) and shows a sign-in dialog on 401; before this, enabling either auth mode made the UI unusable. User RBAC is documented in `docs/guides/security.md` §UI API Access Control.

- ❌ **No admission webhook for `dependsOn` cycle detection** — withdrawn (#1264, 2026-09-30). The optional `--pipeline-admission-webhook` and `pkg/admission` were removed; the controller exits at startup when the flag or `KARDINAL_PIPELINE_ADMISSION_WEBHOOK` is set. Its checks remain: the API server rejects an empty `spec.environments` (`MinItems=1`); the Pipeline reconciler sets `Ready=False` (`ValidationFailed` for cycles, unknown `dependsOn` and a cross-namespace `git.secretRef`; `NotImplemented` for reserved and unsupported fields); `kardinal validate` runs the same checks offline; and Bundles fail at Graph build. History: (PR #1020, 2026-04-21) — `pkg/admission/pipeline_webhook.go` adds `PipelineWebhookHandler` (a `ValidatingAdmissionWebhook` handler) mounted at `POST /webhook/validate/pipeline` on the existing webhook server (`:8083`). Enabled via `--pipeline-admission-webhook` flag (env: `KARDINAL_PIPELINE_ADMISSION_WEBHOOK=true`). The handler decodes the `AdmissionReview`, calls `graph.DetectCycle` (pure, no I/O), and returns `allowed=false` with the cycle path in the `status.message` when a cycle is found. Operators must install a `ValidatingWebhookConfiguration` pointing at this endpoint — not auto-created to avoid requiring cluster-admin at install time. As a fallback (webhook disabled or bypassed), the bundle reconciler now sets `InvalidSpec/CircularDependency` condition on the Bundle instead of the generic `Failed/TranslationError`, making the root cause observable via `kubectl get bundle` without reading Go logs. 5 unit tests in `pkg/graph/cycle_test.go` and 4 in `pkg/admission/pipeline_webhook_test.go`.

- ✅ **SCM token scopes are not validated at startup** — `pkg/scm/token_validator.go` adds `ValidateGitHubTokenScopes`, `ValidateGitLabTokenScopes`, and `ValidateForgejoTokenScopes`. At controller startup, `main.go` calls the appropriate validator (in the background since #1275, via `checkSCMTokenAtStartup` in `cmd/kardinal-controller/scm_token_check.go`). GitHub: calls `GET /user`, inspects `X-OAuth-Scopes` header — warns if `repo` and `public_repo` are both absent. GitLab: calls `/api/v4/personal_access_tokens/self`, warns if `api` scope absent. Forgejo: calls `/api/v1/user`, warns on 401. Fine-grained PATs (no `X-OAuth-Scopes` header) are skipped — GitHub does not expose their scopes via `/user`. Network errors are logged at debug level (non-fatal). The check runs whenever a token is set. **Update (#1275):** it used to run only when `--scm-token-secret-name` was NOT set, and the chart always sets that flag with `GITHUB_TOKEN`, so no chart install ran it; now it also runs in chart installs. Bitbucket and Azure DevOps have no startup check (their token identity endpoints need an `account` scope or an organization, see #1275). The check is never run in a reconciler — no Graph-purity violation. (issue #977)

### Lens 5: Adoption — what makes a platform engineer close the GitHub tab

- ✅ **`helm install` to first Bundle in under 10 minutes** — `demo.enabled=true` added to the Helm chart (`chart/kardinal-promoter/values.yaml`). When set, a `demo` Pipeline CR is created automatically targeting `pnz1990/kardinal-demo` with three environments (test=auto, uat=auto, prod=pr-review). The user runs three commands: `helm install --set demo.enabled=true --set github.token=$PAT`, `kardinal get pipelines`, and `kardinal create bundle demo --image <sha>`. No GitOps repo scaffolding required. `docs/quickstart.md` updated with a "Fast Start — under 10 minutes" section at the top. (PR #1043, 2026-04-22)

- ✅ **`kardinal get subscriptions` CLI command** — `Subscription` CRD and watchers are shipped (K-10) but were invisible from the CLI. `kardinal get pipelines` now shows a SUB column. `kardinal get subscriptions` lists all Subscriptions with NAME, TYPE, PIPELINE, PHASE, LAST-CHECK, LAST-BUNDLE, AGE columns. Supports `--all-namespaces` and `-o json`/`-o yaml`. Aliases: `subscription`, `sub`. A user can now verify Subscription operation without `kubectl get subscriptions`. (PR #918)

- ✅ **Community presence — initial community health files** (PR #1044, 2026-04-22) — `CONTRIBUTING.md` added at the repository root with build/test instructions, contribution workflow, code standards, and links to GitHub Issues. `docs/index.md` updated with a "Community" section. GitHub Discussions is not planned (#1319); GitHub Issues is the one channel.

- ✅ **No ADOPTERS.md or case studies** — `ADOPTERS.md` created at repository root. First entry: the PDCA validation loop (self-use); that entry is gone and the table is now empty. The file includes a Markdown table with Organization/Use Case/Environment/Added columns and instructions for adding new entries. (PR #1025, 2026-04-21)

- ✅ **No `kardinal completion` works for all shells** — shell completion tests now verify: (a) bash completion is non-empty and contains `__start_kardinal`; (b) zsh completion is non-empty and contains `_kardinal`; (c) `TestCompletion_CoreSubcommandsComplete` exercises cobra's `__complete` protocol to verify all core subcommands (`get`, `explain`, `logs`, `status`, `rollback`, `override`) are reachable. `approve` is deprecated: it fails and points to `kardinal override`, and cobra leaves it out of completion. The `__complete` test catches command tree mis-wiring that the static script tests cannot — cobra V2 completion scripts are dynamic and do not embed command names. (PR #1001, 2026-04-21)

### Lens 6: New gaps identified by Kargo comparison scan (2026-04-20)

- ✅ **Bundle `status.conditions` are declared but never populated** — `pkg/reconciler/bundle/reconciler.go` now calls `setBundleCondition()` at every phase transition: `Ready=False/Available` (new bundle received), `Ready=False/Promoting` (graph created, promotion in progress), `Ready=False/Failed + Failed=True/TranslationError` (translator error), `Ready=False/Superseded` (superseded by newer bundle), and `Ready=True/Verified` (every environment the Bundle targets is Verified, including `intent.targetEnvironment` and rollback Bundles; a failed step sets `Ready=False/Failed`). Operators can use `kubectl wait --for=condition=Ready bundle/<name>` for a Bundle that finishes; a Superseded or Failed Bundle never becomes Ready, so wait with a timeout. GitOps controllers (Flux, ArgoCD) can gate on standard K8s conditions. (PR #982 series, 2026-04-21; lifecycle audit fixes 2026-09)

- ✅ **No namespace-scoped controller mode** — `controller.watchNamespace` Helm value added (default `""` = cluster-wide). When set to a namespace name: (a) the controller binary uses `cache.Options{DefaultNamespaces: {ns: {}}}` to limit its informer cache; (b) the Helm chart renders a `Role`/`RoleBinding` scoped to the watch namespace instead of `ClusterRole`/`ClusterRoleBinding`. A security review at a company with shared clusters can now install kardinal with namespace-scoped RBAC. (PR #1024, 2026-04-21)

- ✅ **Bitbucket and Azure DevOps SCM providers are absent** — `pkg/scm/bitbucket.go` adds `BitbucketProvider` implementing `SCMProvider` against Bitbucket Cloud API v2.0 (`Bearer` token auth, HMAC-SHA256 webhook validation via `X-Hub-Signature`). `pkg/scm/azuredevops.go` adds `AzureDevOpsProvider` implementing `SCMProvider` against Azure DevOps REST API v7.1 (PAT via `Basic base64(:<PAT>)` auth, shared-secret webhook validation via `X-AzureDevOps-Token`). Both providers support `OpenPR` (idempotent: returns existing PR on conflict), `ClosePR`, `CommentOnPR`, `GetPRStatus`, `GetPRReviewStatus`, `ParseWebhookEvent`, and `AddLabelsToPR` (no-op for both platforms — neither supports PR labels natively). `factory.go` updated: `"bitbucket"` and `"azuredevops"` are now valid provider types. Teams on Bitbucket Cloud or Azure DevOps can now use kardinal with `scm.provider: bitbucket` or `scm.provider: azuredevops` in their Pipeline. Kargo parity achieved for SCM platform coverage. (PR #1035, 2026-04-21)

- ❌ **No reusable PromotionTemplate concept — removed, by design (#1282).** The CRD and `PromotionStep.spec.inputs` were deleted, and the API server rejects `spec.environments[].steps` and `promotionTemplate` (CRD CEL). Every environment runs the default step sequence. History: PR #985 (2026-04-21) added the `PromotionTemplate` CRD and the `spec.environments[].promotionTemplate: {name, namespace}` reference, but the steps never reached the PromotionStep, because `PromotionStepSpec` had no steps field and the reconciler always ran the default sequence. The 2026-09 audit reopened it (C01-graph-27), `graph.Build` and `kardinal validate` started rejecting both fields, and #1282 removed the CRD, its RBAC grant and its docs. `graph.Build` still rejects the fields for Pipelines stored before the CEL rules.

- ✅ **`kardinal init` generates Pipeline YAML but does not scaffold the GitOps repo** — `cmd/kardinal/cmd/init.go` now supports `--scaffold-gitops` (creates `environments/<env>/kustomization.yaml` overlays in a `--gitops-dir` directory, default `.gitops`) and `--demo` (uses `ghcr.io/pnz1990/kardinal-test-app:sha-DEMO` as the placeholder image). The scaffold is idempotent — existing files are never overwritten. This closes the 80% of the onboarding gap where users needed to understand GitOps repo structure before creating a working Pipeline. (PR #1022, 2026-04-21)

### Lens 7: New gaps identified by competitive scan (2026-04-20)

- ✅ **`RequeueAfter: time.Millisecond` in bundle reconciler is a production hot loop** — replaced with `RequeueAfter: 500*time.Millisecond` in `pkg/reconciler/bundle/reconciler.go`. The 1ms value bypassed controller-runtime rate limiting and would cause API server CPU and etcd write pressure under concurrent Bundle load (>10 pipelines). 500ms is the minimum safe floor; the controller-runtime workqueue deduplicates simultaneous events within this window. (PR #987 series, 2026-04-21)

- ❌ **No image signature verification step — removed, by design.** The `verify-image` step (PR #1091, 2026-04-22) could not be used: `graph.Build` rejects `environments[].steps` and the controller image has no `cosign`. It was deleted in #1278/#1282. Verify signatures at admission in the target cluster instead (Sigstore policy-controller or Kyverno `verifyImages`); see [Pipeline Reference: Image signatures and tests](../pipeline-reference.md#image-signatures-and-tests).

- ✅ **Kubernetes Events emitted by reconcilers** — `EventRecorder` added to `BundleReconciler`, `PromotionStepReconciler`, and `PolicyGateReconciler`. Bundle emits `Normal` events on Available/Promoting/Superseded/Verified and `Warning` on Failed. PromotionStep emits `Normal` on Promoting/WaitingForMerge/HealthChecking/Verified and `Warning` on Failed/AbortedByAlarm/RollingBack; since the 2026-09 audit (C03-promotionstep-14) every state change goes through one transition helper (`pkg/reconciler/promotionstep/transition.go`), so no path skips its Event or AuditEvent. PolicyGate emits `Warning` (Blocked) on first block and on newly-blocked transitions, `Normal` (Allowed) when unblocked. All events visible in `kubectl describe bundle <name>`, `kubectl describe promotionstep <name>`, and `kubectl get events -n kardinal-system`. Recorder field is backward-compatible (nil = no events, existing tests unaffected). (PR #1099, 2026-04-22)

- ✅ **No multi-tenant project isolation** — Kargo has a Project CRD that namespaces all Stages, Promotions, and Warehouses under a single owner entity, with RBAC scoped to the project. kardinal has no equivalent Project CRD. The recommended workaround — one kardinal install per application namespace using `controller.watchNamespace` (see Lens 6) — is now documented in `docs/guides/security.md §Multi-tenant isolation`. The workaround is costly (multiple controller replicas) but is the only safe multi-tenant configuration today. A Project CRD remains a future milestone. (PR #1127-docs, 2026-04-23)

### Lens 8: New gaps identified by vision scan (2026-04-20)

- ✅ **Per-step execution timeout** — `Pipeline.spec.environments[].stepTimeoutSeconds` (optional, `Minimum=1`) is propagated via `StepState.StepTimeoutSeconds` to `Engine.ExecuteFrom`. When set, each step is executed under `context.WithTimeout(ctx, N*time.Second)`. A hung `git-clone` or `kustomize-build` is cancelled and the PromotionStep transitions to Failed rather than blocking the reconciler indefinitely. Table-driven tests in `pkg/steps/engine_test.go` verify both timeout cancellation and propagation of `context.DeadlineExceeded`. (PR #1121-impl, 2026-04-22)

- ✅ **`kardinal logs --follow` streaming mode** — `cmd/kardinal/cmd/logs.go` now accepts `--follow` / `-f`. When set, polls the PromotionStep list every 2 seconds, printing only newly-appeared `status.steps[]` entries (cursor-tracked per step). Exits when all active PromotionSteps reach a terminal state (Verified, Failed, Superseded, AbortedByAlarm). Signal-safe: exits cleanly on SIGINT. Static output (no `--follow`) is unchanged. Tests in `cmd/kardinal/cmd/logs_test.go` verify flag registration, allTerminal logic, and follow-exits-on-terminal behaviour. (PR #1122-impl, 2026-04-22)

- ✅ **`kardinal status <pipeline>` is not per-pipeline** — `cmd/kardinal/cmd/status.go` extended with pipeline-name argument. `kardinal status <pipeline>` now shows: active bundle(s), PromotionStep table with ENV/STATE/ACTIVE-STEP/PR/AGE columns, and a "Blocking Policy Gates" table (GATE/ENV/EXPRESSION/REASON/LAST-CHECKED) when gates have `status.ready=false`. (PR #997, 2026-04-21)

- ✅ **CORS lockdown on UI API** (PR #940; Host check added in the 2026-09 audit remediation) — `applyCORSMiddleware` (`cmd/kardinal-controller/main.go`) wraps the UI API. Cross-origin requests to `/api/v1/ui/*` get CORS headers only for origins listed in `--cors-allowed-origins` (env: `KARDINAL_CORS_ORIGINS`); with no list the API is same-origin only. A request counts as same-origin only when its `Host` is `localhost`, `127.0.0.1`, `::1` or a name in `--ui-allowed-hosts` (env: `KARDINAL_UI_ALLOWED_HOSTS`), so a DNS-rebound page does not pass. While UI auth is off, every `/api/` request to any other `Host` gets 403. Tests: `cmd/kardinal-controller/cors_test.go`.

### Lens 9: New gaps identified by vision scan (2026-04-20, pressure lens pass 2)

- ✅ **Grafana dashboard shipped with the Helm chart** — `config/monitoring/kardinal-promoter-dashboard.json` ships a full dashboard covering: promotion overview stats (Verified/Failed/Superseded/error rate), bundle phase throughput, step duration P50/P99 by step type, PR review latency, gate evaluation rate and blocking duration, reconciler health (rate/errors/latency/queue depth), and Go runtime. Helm chart `grafanaDashboard.enabled=true` creates a ConfigMap with the sidecar discovery label (`grafana_dashboard=1`) for kube-prometheus-stack auto-discovery. Manual import supported via docs/guides/monitoring.md. (PR #1128-series, 2026-04-22)

- ✅ **`kardinal logs` does not render per-step `status.steps[]` entries** — `logsFn` now iterates `status.steps[]` and renders a table with STEP/STATE/DURATION/MESSAGE columns. Duration is shown in seconds (`2.5s`) when `DurationMs > 0`, `-` otherwise. Message is truncated at 80 chars. Table is omitted when `status.steps[]` is empty. This is the primary debugging surface for a failed promotion. (PR #974 series, 2026-04-21)

- ✅ **`kardinal status <pipeline>` shows cluster summary, not per-pipeline detail** — per-pipeline view shipped. `kardinal status <pipeline>` now shows in-flight PromotionStep states (active step highlighted), blocking PolicyGates (CEL expression and current result), and open PR URLs. (PR #997, 2026-04-21)

- ✅ **PrometheusRule alert runbook URLs — all anchors present in `docs/troubleshooting.md`** — Verified 2026-04-22: all four runbook anchors exist: `#start-here-kardinal-doctor` (`## Start here: \`kardinal doctor\``), `#promotion-is-stuck` (`## Promotion is stuck`), `#bundle-not-promoting` (`## Bundle not promoting`), `#graph-controller-issues` (`## Graph controller issues`). The design doc item was stale — the sections were already present. (verified issue #1129, 2026-04-22)

- ✅ **`kardinal completion` CI test is absent — completion scripts may silently break** — `TestCompletion_CoreSubcommandsComplete` in `cmd/kardinal/cmd/completion_test.go` verifies all core subcommands are reachable via cobra's `__complete` protocol. The test exercises `__complete ""` directly, which returns the top-level command list — this is what tab-completion actually uses at runtime, catching command tree mis-wiring that static script inspection cannot catch. (PR #1001, 2026-04-21)

### Lens 10: Structural gaps identified by pressure lens scan (2026-04-21)

- ✅ **Kargo community issue monitoring is not automated** — `scripts/kargo-gap-check.sh` fetches the 20 newest open `kind/enhancement` issues from `akuity/kargo`, cross-references them against existing `🔲` items in this doc by keyword matching, and outputs any request with >5 thumbsup reactions not covered in doc-15. The PM runs this script in the batch routine (`bash scripts/kargo-gap-check.sh`) and reviews the output to add new `🔲 ⚠️ Inferred` items as appropriate. Exits 0 on success. Supports `--min-reactions N` and `--json` flags. (PR #1131-impl, 2026-04-23) **Update (2026-09-29):** the script was deleted with the retired agent loop (#1247).

### Lens 11: Product-level gaps identified by pressure lens scan (2026-04-22)

PDCA was removed in #1378. The live e2e suites replace it. The two items below are history.

- ✅ **PDCA Journey 1 (Quickstart) has been failing for 3+ consecutive runs — root causes identified and fixed** — Root cause 1: NotificationHook RBAC was missing from ClusterRole, causing controller crash-loop and preventing ALL bundle reconciliation (PR #1095, 2026-04-22). Root cause 2: PDCA scenario S9 used a non-existent `health.resource` CRD field causing `bash -e` crash, skipping S10-S24 (fix pending in branch `feat/pdca-fix-20260422` — blocked on GitHub App `workflows` permission, see issue #1136). Root cause 3: CRD schemas were stale — `stepTimeoutSeconds` field existed in Go types but not CRD YAML (PR #1126, 2026-04-22). The PDCA should pass on the next scheduled run (April 23). (issue #1132, 2026-04-22)

- ✅ **PDCA 5 consecutive failures — root causes fixed** — The loop shipped features while PDCA was failing because the COORDINATOR halt gate requires `pdca_status=failure` in `state.json`, but this was not being updated by the scheduler. Root causes identified and fixed: RBAC crash (PR #1095), S9 invalid CRD field (fix in progress issue #1136), stale CRD schema (PR #1126). The halt gate has been separately verified in doc 12 — the issue was automated PDCA status not being written to `state.json` on failure. (issue #1133, 2026-04-22)

### Lens 12: Product gaps identified by pressure lens scan (2026-04-23)

- ✅ **GitOps Promoter parity gap analysis** — `scripts/gitops-promoter-gap-check.sh` added following the same pattern as `kargo-gap-check.sh`. Fetches newest `kind/enhancement` and `kind/feature` issues from `argoproj-labs/gitops-promoter`, cross-references against doc-15 🔲 items, outputs gaps with >3 thumbsup reactions. Supports `--min-reactions N` and `--json` flags. PM runs this to detect GitOps Promoter differentiators (native PullRequest CRD, CommitStatus CRD, multi-commit batching) that kardinal has not yet addressed. (PR #1184, 2026-04-23) **Update (2026-09-29):** the script was deleted with the retired agent loop (#1247).

---

## Triage notes

**Must-fix before v1.0 (any one of these is a production-blocker):**
1. ~~Bundle history GC (historyLimit)~~ ✅ Done (PR #910) — enforced in bundle reconciler
2. ~~PromotionStep timeout~~ ✅ Done (PR #906) — WaitingForMerge timeout added
3. ~~Reconciler panic recovery~~ ✅ Done (PR #920) — handled by controller-runtime v0.23.3 default (RecoverPanic=true)
4. ~~UI API authentication~~ ✅ Done (PR #924) — `--ui-auth-token` Bearer token auth implemented; TokenReview auth also done (PR #1015)
5. ~~HTTP plain-text for UI and webhook servers~~ ✅ Done (PR #937) — TLS support added via `--tls-cert-file`/`--tls-key-file`; cert-manager compatible
6. ~~Bundle `status.conditions` never populated~~ ✅ Done — Ready/Failed/Promoting conditions now populated on every phase transition
7. ~~`RequeueAfter: time.Millisecond` hot loop in bundle reconciler~~ ✅ Done — replaced with 500ms minimum safe floor
8. ~~No per-step execution timeout~~ ✅ Done (PR #1121-impl) — `stepTimeoutSeconds` field on `EnvironmentSpec`, propagated to engine via `StepState`, cancels hung steps via `context.WithTimeout`

**Must-fix for competitive parity with Kargo:**
1. ~~Outbound event notifications (Slack/webhook)~~ ✅ Done — NotificationHook CRD (PR #914)
2. ~~ArgoCD-native image update step~~ ✅ Done — `update.strategy: argocd` (PR #915, 2026-04-21)
3. ~~`kubectl get` printer columns on Bundle/PromotionStep CRDs~~ ✅ Done (PR #903)
4. ~~Bitbucket and Azure DevOps SCM providers~~ ✅ Done — `BitbucketProvider` + `AzureDevOpsProvider` (PR #1035, 2026-04-21)
5. ~~Namespace-scoped controller mode~~ ✅ Done — `controller.watchNamespace` Helm value + Role/RoleBinding (PR #1024, 2026-04-21)
6. ~~Image signature verification step (cosign verify)~~ Removed (#1278, #1282) — the unusable `verify-image` step was deleted. Signature checks belong at admission in the target cluster (Sigstore policy-controller or Kyverno `verifyImages`).
7. ~~`maxConcurrentPromotions` cap per pipeline~~ ✅ Done (PR #1059, 2026-04-22)
8. ~~No Kubernetes Events emitted by reconcilers~~ ✅ Done (PR #1099, 2026-04-22) — `kubectl describe` now shows events; Bundle/PromotionStep/PolicyGate all emit events

**Adoption wins (high effort/impact):**
1. ~~`kardinal init` full GitOps repo scaffolding~~ ✅ Done — `--scaffold-gitops` and `--demo` flags added (PR #1022, 2026-04-21)
2. ~~GitHub Actions wrapper action~~ ✅ Done — `.github/actions/create-bundle/action.yml` (PR #916)
3. ~~GitHub Discussions community presence~~ ✅ Done — `CONTRIBUTING.md` + community section (PR #1044, 2026-04-22); Discussions: not planned (#1319)
4. ~~Reusable PromotionTemplate CRD~~ Removed (#1282) — the `PromotionTemplate` CRD (PR #985, 2026-04-21) could never be used, because `graph.Build` rejects `environments[].promotionTemplate` and `environments[].steps`. The CRD was deleted and the API server now rejects both fields.
5. ~~`kardinal status <pipeline>` per-pipeline in-flight view~~ ✅ Done (PR #997, 2026-04-21)
6. ~~`kardinal logs --follow` streaming mode~~ ✅ Done (PR #1122-impl) — polls every 2s, cursor-tracked incremental step output, exits on terminal state
7. ~~Grafana dashboard JSON shipped in Helm chart~~ ✅ Done — `config/monitoring/kardinal-promoter-dashboard.json` (PR #1128-series, 2026-04-22)
8. ~~`kardinal logs` per-step `status.steps[]` rendering~~ ✅ Done (PR #974 series, 2026-04-21)
