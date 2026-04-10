# Queue 003 — Stage 2: Bundle and Pipeline Reconcilers (No-Op Baseline)

> **Generated**: 2026-04-10
> **Stage**: 2 — Bundle and Pipeline Reconcilers (No-Op Baseline)
> **Roadmap ref**: docs/aide/roadmap.md Stage 2
> **Batch size**: 2 items
> **Status**: active

---

## Purpose

Implement the controller binary with the full controller-runtime Manager (leader election,
health probes, metrics) and two reconcilers that watch `Bundle` and `Pipeline` objects.
No real promotion logic — this establishes the reconciler loop, status patching, and
structured logging that every downstream stage builds on.

Stage 2 produces a running controller deployable via the Helm chart.

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 006 | 006-controller-manager-and-reconcilers | Controller Manager + BundleReconciler + PipelineReconciler | 005 (merged) | immediately |
| 007 | 007-helm-chart-controller-deploy | Helm chart controller Deployment + RBAC + integration test | 006 (branch) | after 006 branch exists |

---

## Assignment Wave 1

- **006-controller-manager-and-reconcilers** → ENGINEER-1 (no blockers, assign immediately)

## Assignment Wave 2 (after 006 branch exists on remote)

- **007-helm-chart-controller-deploy** → ENGINEER-2 (dependency_mode: branch)

---

## Acceptance Gate

Both items `done` before advancing to Stage 3 queue.

- Controller starts and passes liveness/readiness probes within 5 seconds
- `kubectl apply -f examples/quickstart/pipeline.yaml` followed by `kubectl apply -f examples/quickstart/bundle.yaml` results in expected status within 10 seconds
- `make test` passes with integration test
- No goroutine leaks under rapid creates/deletes (`go test -race`)
