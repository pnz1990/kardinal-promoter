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

---

## Future

### Lens 1: Kargo parity — capability gaps that lose competitive evaluations

- ✅ **Bundle history GC — `historyLimit` enforced by bundle reconciler** (PR #910, 2026-04-20) — `Pipeline.spec.historyLimit` is now enforced in `pkg/reconciler/bundle/reconciler.go:enforceHistoryLimit`. On each new Bundle creation, terminal Bundles (Verified/Failed/Superseded) beyond the limit are deleted oldest-first. Default limit: 50. Kargo parity achieved.

- 🔲 **No outbound event notifications** — Kargo integrates with Argo CD Notifications engine for Slack/PagerDuty/Teams webhooks on promotion events (started, succeeded, failed, blocked). kardinal has zero outbound notification capability. A platform team that deploys this to production today cannot be paged when a promotion fails or a gate blocks prod for 48 hours. Minimum viable: a `NotificationHook` CRD with a `webhook.url` + optional `Authorization` header + a template for the event body. Emit on: Bundle.Verified, Bundle.Failed, PolicyGate blocked (first block, not every re-eval), PromotionStep.Failed.

- 🔲 **No ArgoCD-native image update step** — Kargo's `argocd-update` promotion step directly patches the ArgoCD Application's `spec.source.helm.valuesObject` or triggers a refresh without a git commit. This is the dominant Kargo use case for teams that store application config inside the ArgoCD Application rather than a GitOps repo. kardinal only supports git-write promotion (kustomize, helm values.yaml patch, config-merge). Teams using ArgoCD with inline image references cannot use kardinal without restructuring their ArgoCD setup. Adding an `argocd-set-image` step (patches `Application.spec` directly via the Kubernetes API) would unlock this cohort.

- 🔲 **No GitHub Actions native bundle creation** — Kargo has a GitHub Action (`akuity/kargo-action`) that creates Freight directly from a workflow step. kardinal requires `kardinal create bundle` or a `POST /api/v1/bundles` HTTP call, which requires the CI system to have network access to the in-cluster endpoint. Without an in-cluster ingress (which most teams don't set up initially), CI cannot create Bundles. A GitHub Action wrapper (`pnz1990/kardinal-action`) that wraps the HTTP call and handles ingress/port-forward discovery would remove this adoption blocker for GitHub Actions users.

- 🔲 **No UI for Bundle creation / triggering promotions** — Kargo has a UI button to manually trigger a promotion for testing. kardinal's UI is read-only for Bundle lifecycle (aside from pause/resume/rollback actions). A "Create Bundle" dialog in the UI (with image input + provenance fields) would let platform engineers test pipelines without CLI access. This is a table-stakes demo feature for a competitive evaluation.

- 🔲 **Warehouse-equivalent: no automatic discovery mode** — Kargo's Warehouse concept passively watches OCI registries and Git repos and creates Freight without CI pipeline integration. kardinal's `Subscription` CRD and `OCIWatcher`/`GitWatcher` are implemented (K-10), but there is no UI for managing Subscriptions and no `kardinal get subscriptions` CLI command. The capability exists but is invisible to users who don't read the API reference. Surface Subscription status in `kardinal get pipelines` and add a `kardinal get subscriptions` command.

### Lens 2: Production stability — what breaks after a week in production

- 🔲 **No reconciler panic recovery** — there are zero `recover()` calls in any reconciler. A malformed CRD (e.g. a Pipeline with a CEL expression that panics the kro library) will crash the controller binary, and the controller will restart in a loop until the CRD is fixed. This is a production-availability issue. Wrap each reconciler's `Reconcile()` method with a deferred `recover()` that logs the panic and returns a non-fatal error with exponential backoff. controller-runtime's `WithRecoverPanic` option may handle this — evaluate and enable it.

- ~~🔲 **No PromotionStep timeout**~~ ✅ Done (PR #906) — `environment.waitForMergeTimeout` added to Pipeline environments.

- 🔲 **Bundle reconciler orphan guard races with Pipeline deletion** — `pkg/reconciler/bundle/reconciler.go:134` handles the case where the parent Pipeline was deleted by self-deleting the Bundle. This is triggered by checking `isNotFound` on the Pipeline. If the Pipeline is being deleted (DeletionTimestamp set but finalizers not cleared), the check may transiently pass, causing premature Bundle deletion before the Pipeline's owned resources are cleaned up. Add a check for `pipeline.DeletionTimestamp != nil` and requeue instead of deleting.

- 🔲 **Git credential rotation with zero downtime** — kardinal reads the SCM token from a Kubernetes Secret at controller startup (or at first reconcile). If the Secret is rotated (new PAT issued, old PAT expired), the controller must be restarted to pick up the new token. This causes a gap in promotions during the restart window. The SCM factory should watch the Secret and reinitialize providers on change without a controller restart. Kargo handles credential rotation natively. This is a production-operations requirement for any team that rotates credentials on a schedule.

### Lens 3: Observability — can an operator understand a stall without Go logs?

- 🔲 **Missing Prometheus metrics for step duration and gate blocking time** — `pkg/reconciler/observability/metrics.go` exports 4 counters (bundles_total, steps_total, gate_evaluations_total, pr_duration_seconds). There is no metric for: (a) per-step execution duration (git-clone latency, kustomize latency), (b) PolicyGate blocking duration histogram (how long has this gate been blocking?), (c) PromotionStep age histogram (how old is the oldest in-flight step?), (d) reconciler queue depth. Without (b) and (c), a Grafana dashboard cannot answer "which gates are blocking prod right now and for how long?" — the most common on-call question.

- 🔲 **No `kardinal status` command for in-flight promotion details** — `kardinal get pipelines` shows environment states. `kardinal explain` shows gate details. Neither command answers "my prod promotion is stuck — what exactly is it waiting for right now?" A `kardinal status <pipeline> [--bundle <name>]` command should show: current state of every PromotionStep (with active step highlighted), which PolicyGates are blocking (with CEL expression + current variable values), which PRs are open and their review status. This is the first command a new user needs when something is wrong.

- 🔲 **No structured `kardinal logs` for promotion step output** — `kardinal logs` is listed in the CLI help but there is no evidence it surfaces individual step output (git-clone stderr, kustomize output). If kustomize fails mid-promotion, the error is in `PromotionStep.status.message` but not surfaced as structured log lines the way `kubectl logs` works. `kardinal logs <pipeline> --env prod --follow` should stream the active PromotionStep's step messages as they are written to status.

### Lens 4: Security posture — what a Series B security review would flag

- 🔲 **UI API has no authentication** — `cmd/kardinal-controller/ui_api.go` exposes all pipeline, bundle, gate, and audit data with zero authentication. The listen address (`:8082`) binds to all interfaces. Any pod in the cluster can enumerate all pipelines, all bundles, all PolicyGate CEL expressions, and all promotion history with a `curl`. Add at minimum a `--ui-auth-token` flag (same mechanism as the Bundle API) or Kubernetes TokenReview-based auth. Tracked also in `docs/design/06-kardinal-ui.md`.

- 🔲 **No admission webhook for `dependsOn` cycle detection** — `ValidatingAdmissionPolicy` (now shipped, see Present) covers required fields and enum values for Pipeline, Bundle, and PolicyGate. It cannot detect graph cycles: a Pipeline where `prod` depends on `uat` and `uat` depends on `prod` is accepted at admission and only fails at translator time. A traditional `ValidatingAdmissionWebhook` (not VAP) is needed to detect cycles, since CEL cannot express graph traversal. Until the webhook is added, the translator must return a clear `InvalidSpec` status condition rather than a reconciler error log. Kargo detects cycles on `kubectl apply`.

- 🔲 **No SBOM attestation for the controller image** — trivy CVE scanning is present (PR #882). cosign image signing and SLSA provenance were added in v0.8.1. But there is no Software Bill of Materials (SBOM) attached to the controller image. A security team at a regulated company (financial services, healthcare) requires SBOM for any controller running in their cluster. Add `syft` SBOM generation to the release workflow and attach the SBOM to the OCI image via `cosign attach sbom`.

- 🔲 **SCM token scopes are not validated at startup** — the GitHub token is read from the Secret but its scopes are never verified. A token with only `read:repo` scope will fail silently when the controller tries to open a PR (403 response surfaces hours later in a reconcile log). Add a startup preflight check (similar to `kardinal doctor`) that calls the SCM provider's "whoami" endpoint and logs a warning if required scopes are missing. This surfaces misconfiguration in minutes rather than hours.

### Lens 5: Adoption — what makes a platform engineer close the GitHub tab

- 🔲 **`helm install` to first Bundle in under 10 minutes is not achievable** — the quickstart requires: create a GitOps repo with Kustomize overlays, configure ArgoCD Applications, create a GitHub PAT with correct scopes, set up the git branch structure, then install kardinal. A new user with no existing GitOps repo cannot complete the quickstart in under 30 minutes. Add a `kardinal init` command that scaffolds the GitOps repo structure (creates `env/test`, `env/uat`, `env/prod` branches, adds a kustomization.yaml with a placeholder image) and a `--demo` mode for `helm install` that deploys the `kardinal-test-app` as a demo target automatically. The "time to first promotion" metric must be under 10 minutes on a fresh kind cluster.

- 🔲 **No `kardinal get subscriptions` CLI command** — `Subscription` CRD and watchers are shipped (K-10) but invisible from the CLI. `kardinal get pipelines` does not show whether a pipeline has an active Subscription. A user who installed the Subscription CRD cannot easily verify it is working without `kubectl get subscriptions`. Add `kardinal get subscriptions` with columns: name, pipeline, source type, last check time, last bundle created.

- 🔲 **No community presence** — zero GitHub Discussions, zero Discord/Slack, no Stack Overflow tag. Kargo has an active community in their GitHub Discussions and a Discord server. A platform engineer who hits a problem has no place to ask for help except filing a GitHub issue. The single biggest reason someone closes the GitHub tab within 60 seconds is the perception that the project is abandoned. Add a GitHub Discussions board with seeded topics (Getting Started, Show & Tell, Feature Requests, Q&A) as the minimum. The automated agent should monitor Discussions for support questions and respond.

- 🔲 **No ADOPTERS.md or case studies** — zero public deployers. Kargo lists production adopters in their README. Even a single "we use this in our CI pipeline for the test app" entry (written by the agent about its own PDCA validation) would signal active use. Create `ADOPTERS.md` with the PDCA validation as the first entry: "kardinal-promoter uses itself — the PDCA workflow runs promotions of `kardinal-test-app` through `kardinal-demo` on every 6-hour cycle."

- 🔲 **No `kardinal completion` works for all shells** — shell completion is listed as shipped (`#606`) but there is no test that verifies the completion script generates valid output for bash/zsh/fish. Add a CI test that runs `kardinal completion bash` and `kardinal completion zsh` and verifies the output is non-empty and contains expected command names. Without working completion, power users get frustrated immediately.

### Lens 6: New gaps identified by Kargo comparison scan (2026-04-20)

- 🔲 **Bundle `status.conditions` are declared but never populated** — `api/v1alpha1/bundle_types.go` declares `Status.Conditions []metav1.Condition` but `pkg/reconciler/bundle/reconciler.go` never calls `apimeta.SetStatusCondition`. `kubectl describe bundle <name>` shows `Conditions: <none>`. Operators and automation that use standard K8s condition watches (e.g. `kubectl wait --for=condition=Ready`) cannot use them. Contrast: `PromotionStep` does populate conditions. Populate `Ready`, `Promoting`, and `Failed` conditions on Bundle status. This is also required for GitOps controllers (Flux, ArgoCD) that gate on resource readiness.

- 🔲 **No namespace-scoped controller mode** — the Helm chart deploys a `ClusterRole` that grants kardinal read/write access to `kardinal.io` CRDs across all namespaces. In a multi-tenant cluster, a platform team that installs kardinal for one team inadvertently grants it visibility into all namespaces. Kargo offers both cluster-scoped and namespace-scoped install modes. Add a `controller.watchNamespace` Helm value (default `""` = cluster-wide) that, when set, limits the controller's cache and ClusterRole/Role binding to that namespace only. A security review at a company with shared clusters will block installation without this.

- 🔲 **Bitbucket and Azure DevOps SCM providers are absent** — kardinal supports GitHub, GitLab, and Forgejo. Kargo supports GitHub, GitLab, Bitbucket, and Azure DevOps (the latter via `azure-devops` provider). Azure DevOps is the dominant SCM at enterprise accounts in regulated industries. `pkg/scm/factory.go` returns an error for `"bitbucket"` input. Teams on Bitbucket or Azure DevOps cannot use kardinal at all. Add `pkg/scm/bitbucket.go` and `pkg/scm/azuredevops.go` providers. PR templates, webhook validation, and PR-open/wait-for-merge operations must be implemented for both.

- 🔲 **No reusable PromotionTemplate concept** — Kargo has reusable promotion step sequences via `PromotionTemplate` spec that can be referenced across Stages. In kardinal, every Pipeline.spec.environment must repeat the full promotion step list (git-clone, kustomize, git-commit, open-pr, wait-for-merge, health-check). For an organization with 50 pipelines, every step list change requires 50 Pipeline YAML edits. Add a `PromotionPolicy` CRD that encapsulates a named step sequence. Environments reference it via `spec.promotionPolicy: name`. The translator inlines the steps at graph-build time. This is an adoption blocker for large-scale platform teams.

- 🔲 **`kardinal init` generates Pipeline YAML but does not scaffold the GitOps repo** — `cmd/kardinal/cmd/init.go` generates a Pipeline CRD YAML interactively. It does not create the GitOps repository branch structure (`env/test`, `env/uat`, `env/prod`), does not write `kustomization.yaml` overlays, and has no `--demo` mode. The Future item in Lens 5 (time-to-first-Bundle under 10 min) depends on `kardinal init` doing the repo scaffold. The command exists but solves only 20% of the onboarding problem. A new user still needs to understand GitOps repo structure before they can create a working Pipeline.

- 🔲 **HTTP plain-text for UI and webhook endpoints is a TLS gap** — both `http.ListenAndServe` calls in `cmd/kardinal-controller/main.go` (UI on `:8082`, webhooks on `:8083`) use plain HTTP. Any internal network observer (or a compromised pod) can read promotion events and pipeline state in transit. Add `--tls-cert-file` / `--tls-key-file` flags (or auto-provision via cert-manager annotation on the Service) for both servers. Tracked in `docs/design/06-kardinal-ui.md` but not yet in the triage/blocker list.

---

## Triage notes

**Must-fix before v1.0 (any one of these is a production-blocker):**
1. ~~Bundle history GC (historyLimit)~~ ✅ Done (PR #910) — enforced in bundle reconciler
2. ~~PromotionStep timeout~~ ✅ Done (PR #906) — WaitingForMerge timeout added
3. UI API authentication — security review failure
4. Reconciler panic recovery — crash loop on malformed CRDs
5. Bundle `status.conditions` never populated — breaks `kubectl wait` and GitOps tooling
6. HTTP plain-text for UI and webhook servers — TLS gap flagged in enterprise security reviews

**Must-fix for competitive parity with Kargo:**
1. Outbound event notifications (Slack/webhook)
2. ArgoCD-native image update step
3. ~~`kubectl get` printer columns on Bundle/PromotionStep CRDs~~ ✅ Done (PR #903)
4. Bitbucket and Azure DevOps SCM providers — blocks enterprise adoption
5. Namespace-scoped controller mode — required for multi-tenant clusters

**Adoption wins (high effort/impact):**
1. `kardinal init` full GitOps repo scaffolding (currently generates Pipeline YAML only)
2. GitHub Actions wrapper action
3. GitHub Discussions community presence
4. Reusable PromotionTemplate CRD
