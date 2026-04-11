# kardinal-promoter: Progress

> Created: 2026-04-09
> Last updated: 2026-04-11T01:10Z
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
| 9 | Embedded React UI | 📋 Planned | Depends on Stage 7 |
| 10 | PR Evidence, Labels, and Webhook Reliability | ✅ Complete | Items 016/018 merged: PRs #64, #69. Full 3-table PR body, labels, startup reconciliation via Runnable, health endpoint with metrics counter. |
| 11 | GitHub Actions Integration and `kardinal init` | 📋 Planned | Depends on Stage 8 |
| 12 | Helm Strategy and Config-Only Promotions | 📋 Planned | Depends on Stage 6 |
| 13 | Rollback and Pause/Resume | 📋 Planned | Depends on Stage 6 |
| 14 | Distributed Mode (Control Plane + Agents) | 📋 Planned | Depends on Stage 6 |
| 15 | MetricCheck CRD and Upstream Soak Time | 📋 Planned | Depends on Stage 4 |
| 16 | Custom Promotion Steps via Webhook | 📋 Planned | Depends on Stage 6 |
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
