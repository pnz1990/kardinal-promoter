# kardinal-promoter: Progress

> Created: 2026-04-09
> Last updated: 2026-04-11T05:45Z
> Based on: docs/aide/roadmap.md

## Status Icons

📋 Planned | 🚧 In Progress | ✅ Complete | ⏸️ Deferred | ❌ Excluded

---

## Stages

| Stage | Title | Status | Notes |
|---|---|---|---|
| 0 | Project Skeleton | ✅ Complete | All 4 items merged: PRs #8, #9, #10, #11. |
| 1 | CRD Types and Validation | ✅ Complete | Item 005 merged: PR #19. All 4 CRD types complete. |
| 2 | Bundle and Pipeline Reconcilers (No-Op Baseline) | ✅ Complete | Items 006/007 merged: PRs #23, #24. Controller manager, BundleReconciler, PipelineReconciler, Helm chart, integration tests. |
| 3 | Graph Generation and kro Integration | ✅ Complete | Items 008/009 merged: PRs #27, #29. GraphClient, Builder, Translator, BundleReconciler extension, 30+ tests. |
| 4 | PolicyGate CEL Evaluator | ✅ Complete | Item 010 merged: PR #31. CEL environment, evaluator, PolicyGate reconciler with time-based gates. |
| 5 | Git Operations and GitHub PR Flow | ✅ Complete | Item 012 merged: PR #57. SCMProvider, GitClient, steps engine, 7 built-in steps, PR template. |
| 6 | PromotionStep Reconciler and Full Promotion Loop | ✅ Complete | Item 013 merged: PR #58. Full state machine, Bundle supersession, webhook, kardinal explain. |
| 7 | Health Adapters | ✅ Complete | Item 014 merged: PR #62. DeploymentAdapter, ArgoCDAdapter, FluxAdapter, AutoDetector, remote kubeconfig. |
| 8 | CLI | ✅ Complete | Items 011/015 merged: PRs #37, #63. Full CLI: version, get, explain, create bundle, rollback, pause/resume, policy list/simulate, history. |
| 9 | Embedded React UI | ✅ Complete | Item 019 merged: PR #73. React 19 + dagre DAG view, UI API, go:embed, --ui-listen-address flag. |
| 10 | PR Evidence, Labels, and Webhook Reliability | ✅ Complete | Items 016/018 merged: PRs #64, #69. Full 3-table PR body, labels, startup reconciliation via Runnable, health endpoint with metrics counter. |
| 11 | GitHub Actions Integration and `kardinal init` | ✅ Complete | Items 017/020/023 merged: PRs #67, #72, #81. Bundle webhook, GitHub Action, kardinal init, E2E journey tests J1/J3/J4/J5 with fake client. |
| 12 | Helm Strategy and Config-Only Promotions | ✅ Complete | Item 021 merged: PR #78. helm-set-image, config-merge steps, type-aware sequence routing, Config Bundle supersession, examples/config-promotion/. |
| 13 | Rollback and Pause/Resume | ✅ Complete | Items 022/025 merged: PRs #77, #110. Auto-rollback, CLI rollback/pause/resume, reconciler pause enforcement. |
| 14 | Distributed Mode (Control Plane + Agents) | 📋 Planned | Depends on Stage 6 |
| 15 | MetricCheck CRD and Upstream Soak Time | ✅ Complete | Item 027 merged: PR #114. MetricCheck CRD, Prometheus evaluator, CEL soak time context. |
| 16 | Custom Promotion Steps via Webhook | ✅ Complete | Item 028 merged: PR #124. CustomWebhookStep, registry fallback, 10 tests, example server + Pipeline, docs/custom-steps.md. |
| 17 | GitLab Support | 📋 Planned | Depends on Stage 5 |
| 18 | Subscription CRD | 📋 Planned | Depends on Stage 6 |
| 19 | Security Hardening and Production Readiness | 📋 Planned | Depends on all prior stages |

---

## Stage 0 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 001 | Go module, directory layout, Makefile | ✅ Complete | #8 merged | pkg/ layout, go.mod, stubs |
| 002 | kubebuilder CRD scaffold + controller-gen | ✅ Complete | #11 merged | CRD types, controller-gen, samples |
| 003 | Dockerfile + Helm chart skeleton | ✅ Complete | #10 merged | Multi-stage Dockerfile, chart/ |
| 004 | GitHub Actions CI + golangci-lint | ✅ Complete | #9 merged | CI pipeline, .golangci.yml |

---

## Stage 1 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 005 | Complete CRD types, validation markers, generated YAML | ✅ Complete | #19 merged | All 4 CRD types, roundtrip tests |

---

## Stage 2 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 006 | Controller Manager + BundleReconciler + PipelineReconciler | ✅ Complete | #23 merged | Full manager setup, zerolog, reconcilers with status patching |
| 007 | Helm chart controller deployment + RBAC + integration test | ✅ Complete | #24 merged | Deployment, RBAC, make install/uninstall, integration tests |

---

## Stage 3 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 008 | Add PropagateWhen + fix Graph API group (experimental.kro.run) | ✅ Complete | #27 merged | GraphNode.PropagateWhen, GraphGVK/GVR updated, design docs updated |
| 009 | Graph Builder, GraphClient, Translator, BundleReconciler extension | ✅ Complete | #29 merged | Full translation algorithm, 27 builder tests, 7 reconciler tests |

---

## Stage 4 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 010 | PolicyGate CEL evaluator + PolicyGate reconciler | ✅ Complete | #31 merged | CEL env, evaluator, reconciler, time-based + soak gates |

---

## Stage 5 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 012 | SCM Provider, Steps Engine, Git Built-ins | ✅ Complete | #57 merged | SCMProvider, GitClient, Engine, 7 steps, PR template, 29 tests |

---

## Stage 6 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 013 | PromotionStep Reconciler — Full Promotion Loop | ✅ Complete | #58 merged | Full state machine, Bundle supersession, POST /webhook/scm, kardinal explain |

---

## Stage 7 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 014 | Health Adapters — Deployment, Argo CD, Flux | ✅ Complete | #62 merged | Adapter interface, DeploymentAdapter, ArgoCDAdapter, FluxAdapter, AutoDetector, remote kubeconfig |

---

## Stage 8 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 011 | CLI foundation: cobra + version/get commands | ✅ Complete | #37 merged | kardinal version, get pipelines/bundles/steps |
| 015 | Full CLI — create bundle, policy, rollback, pause/resume, history | ✅ Complete | #63 merged | create bundle, rollback, pause, resume, policy list/simulate, history |

---

## Stage 10 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 016 | PR Evidence, Labels, Webhook Reliability | ✅ Complete | #64 merged | Full 3-table PR body, AddLabelsToPR interface+impl, EnsureLabels, label application |
| 018 | Startup Reconciliation + Webhook Health Endpoint | ✅ Complete | #69 merged | Start(ctx) Runnable, atomic event counter, JSON health endpoint, 5 new tests |

---

## Stage 9 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 019 | Embedded React UI (Stage 9) | ✅ Complete | #73 merged | React 19 + dagre DAG view, PipelineList, NodeDetail, UI API, go:embed |

---

## Stage 11 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 017 | kardinal init + Quickstart docs | ✅ Complete | #67 merged | Interactive wizard, Pipeline YAML generation, examples updated |
| 020 | Bundle Webhook + GitHub Action | ✅ Complete | #72 merged | POST /api/v1/bundles, rate limit, Bearer auth, GitHub Action |
| 023 | E2E Journey Tests (fake-client) | ✅ Complete | #81 merged | Journey tests J1/J3/J4/J5 with real reconciler + fake K8s client |
| 026 | Kind Cluster E2E GitHub Actions Workflow | ✅ Complete | #111 merged | e2e.yml triggers on main push, hack/e2e-setup.sh, kind_test.go (skip without KUBECONFIG) |

---

## Stage 12 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 021 | Helm Strategy + Config-Only Bundle Promotions | ✅ Complete | #78 merged | helm-set-image, config-merge, DefaultSequenceForBundle, type-aware supersession, examples |
| 024 | Rendered Manifests — branch layout + kustomize-build | ✅ Complete | #82 merged | layout:branch field, kustomize-build step, DefaultSequenceForBundle extended, docs/examples |

---

## Stage 13 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 022 | Automatic Rollback on Health Failure | ✅ Complete | #77 merged | AutoRollbackSpec, ConsecutiveHealthFailures, maybeCreateAutoRollback (idempotent) |
| 025 | Pause/Resume — BundleReconciler + PromotionStepReconciler | ✅ Complete | #110 merged | Pipeline.Spec.Paused enforcement in both reconcilers, idempotency tests, docs updated |

---

## Stage 15 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 027 | MetricCheck CRD + Prometheus Evaluator + CEL soak time | ✅ Complete | #114 merged | MetricCheck CRD, MetricCheckReconciler, PrometheusProvider, CEL metrics + soak context |

---

## Stage 16 Item Breakdown

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 028 | Custom Promotion Steps via HTTP Webhook | ✅ Complete | #124 merged | CustomWebhookStep, registry fallback, StepSpec/WebhookConfig in Pipeline, 10 tests, example server |

---

## Workshop 1 Parity Items (queue-014)

| Item | Title | Status | PR | Notes |
|---|---|---|---|---|
| 029 | Fix FormatPipelineTable per-environment columns | ✅ Complete | #128 merged | PIPELINE BUNDLE ENV... AGE format, union columns, PromotionStep state lookup |
| 030 | Fix kardinal explain zero PolicyGates label mismatch | ✅ Complete | #129 merged | Add pipeline/bundle/env labels to PolicyGate node templates in builder.go |
| 031 | Show CEL expression + current value in kardinal explain | ✅ Complete | #129 merged | EXPRESSION column added to explain output; Step rows show '-' |

---

## Spec Status

| Spec | Title | Status | Notes |
|---|---|---|---|
| 001 | Graph Integration Layer | ✅ Complete | PRs #25, #27. GraphNode types, PropagateWhen, experimental.kro.run API group, GraphClient |
| 002 | Pipeline-to-Graph Translator | ✅ Complete | PR #29. Full translation algorithm Steps 1-7, 27 unit tests |
| 003 | PromotionStep Reconciler | ✅ Complete | PR #58. Full state machine, Bundle supersession, webhook endpoint, evidence copy. |
| 004 | PolicyGate Reconciler | ✅ Complete | PR #31. CEL environment, evaluator, PolicyGate reconciler, time-based/soak gates. |
| 005 | Health Adapters | 📋 Planned | Design doc: docs/design/05-health-adapters.md |
| 006 | kardinal-ui | 📋 Planned | Design doc: docs/design/06-kardinal-ui.md |
| 007 | Distributed Architecture | 📋 Planned | Design doc: docs/design/07-distributed-architecture.md |
| 008 | Promotion Steps Engine | ✅ Complete | PR #57. SCMProvider, GitClient, Engine, 7 built-in steps, DefaultSequence. |
| 009 | Config-Only Promotions | 📋 Planned | Design doc: docs/design/09-config-only-promotions.md |
