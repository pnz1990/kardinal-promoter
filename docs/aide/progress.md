# kardinal-promoter: Progress

> Created: 2026-04-09
> Last updated: 2026-04-10T19:45Z
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
| 3 | Graph Generation and kro Integration | 📋 Planned | Depends on Stage 2 |
| 4 | PolicyGate CEL Evaluator | 📋 Planned | Depends on Stage 3 |
| 5 | Git Operations and GitHub PR Flow | 📋 Planned | Depends on Stage 3 |
| 6 | PromotionStep Reconciler and Full Promotion Loop | 📋 Planned | Depends on Stage 4 + 5 |
| 7 | Health Adapters | 📋 Planned | Depends on Stage 6 |
| 8 | CLI | 📋 Planned | Depends on Stage 2 |
| 9 | Embedded React UI | 📋 Planned | Depends on Stage 7 |
| 10 | PR Evidence, Labels, and Webhook Reliability | 📋 Planned | Depends on Stage 6 |
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

## Spec Status

| Spec | Title | Status | Notes |
|---|---|---|---|
| 001 | Graph Integration Layer | 📋 Planned | Design doc: docs/design/01-graph-integration.md |
| 002 | Pipeline-to-Graph Translator | 📋 Planned | Design doc: docs/design/02-pipeline-to-graph-translator.md |
| 003 | PromotionStep Reconciler | 📋 Planned | Design doc: docs/design/03-promotionstep-reconciler.md |
| 004 | PolicyGate Reconciler | 📋 Planned | Design doc: docs/design/04-policygate-reconciler.md |
| 005 | Health Adapters | 📋 Planned | Design doc: docs/design/05-health-adapters.md |
| 006 | kardinal-ui | 📋 Planned | Design doc: docs/design/06-kardinal-ui.md |
| 007 | Distributed Architecture | 📋 Planned | Design doc: docs/design/07-distributed-architecture.md |
| 008 | Promotion Steps Engine | 📋 Planned | Design doc: docs/design/08-promotion-steps-engine.md |
| 009 | Config-Only Promotions | 📋 Planned | Design doc: docs/design/09-config-only-promotions.md |
