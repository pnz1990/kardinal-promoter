# 14 — v0.6.0 Roadmap Items

> Status: Active | Created: 2026-04-20
> Milestone: v0.6.0

---

## Summary

This design doc tracks the concrete v0.6.0 near-term items from the vision.
All stages 0-29 are complete. These are the next incremental improvements.

---

## Present

*(No items shipped yet in this doc)*

---

## Future

- 🔲 14.1 — Subscription OCI source watcher: implement `pkg/subscription/oci_watcher.go` — polls OCI registry for new image tags, creates Bundle automatically (issue #491)
- 🔲 14.2 — Subscription Git source watcher: implement `pkg/subscription/git_watcher.go` — watches Git repo for config changes, creates Bundle automatically (issue #493)
- 🔲 14.3 — Pipeline deployment metrics: persist `time-to-production`, `rollback-rate`, `operator-interventions` to `Pipeline.status.deploymentMetrics` after each Bundle completes (issue #498)
- 🔲 14.4 — ChangeWindow CEL functions: implement `changewindow.isAllowed()` and `changewindow.isBlocked()` as named CEL functions on the Graph environment (issue #506)
- 🔲 14.5 — kardinal-agent binary: separate `cmd/kardinal-agent/` entry point for spoke-cluster distributed mode; reads shard assignments, runs PromotionStep reconciler only (issue #508)
- 🔲 14.6 — PDCA: fix broken Playwright scenarios S25-S27 (bundle status, pipeline graph, rollback button) — verify E2E environment setup and Playwright config
- 🔲 14.7 — Security hardening: run `trivy image kardinal-promoter:latest` in CI and block on HIGH/CRITICAL vulnerabilities; target 0 HIGH
- 🔲 14.8 — NEEDS HUMAN resolution: investigate and resolve issue #871 (security-checks persistent 0-job failure on push)

---

## Zone 1 — Obligations

**O1 — Items 14.1–14.5 ship in v0.6.0 before cutting the release.**
The release is cut via `gh release create v0.6.0` once all items here are ✅ Present.

**O2 — PDCA (14.6) must pass before v0.6.0 is cut.**
The PDCA workflow is the agent's own smoke test of the product.

**O3 — No new logic leaks in 14.1–14.5.**
Graph Purity rules from docs/design/10 apply. Any new time.Now(), external HTTP call,
or cross-CRD mutation in a reconciler requires [NEEDS HUMAN] before merging.

---

## Zone 2 — Implementer's judgment

- OCI watcher: use `github.com/google/go-containerregistry` for tag polling
- Shard routing for kardinal-agent: use `kardinal.io/shard` label on PromotionStep
- Deployment metrics accumulation: use Bundle ownerReference to aggregate across Bundles

---

## Zone 3 — Scoped out

- v0.7.0 items (per-step progress observability, full security hardening)
- Argo Rollouts and Flagger delivery delegation improvements
- Multi-cloud provider support
