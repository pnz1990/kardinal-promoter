# 14 — v0.6.0 Roadmap Items

> Status: Active | Created: 2026-04-20
> Milestone: v0.6.0

---

## Summary

This design doc tracks the concrete v0.6.0 near-term items from the vision.
All stages 0-29 are complete. These are the next incremental improvements.

---

## Present

- ✅ 14.6 — PDCA: Playwright rollback button tests un-skipped — added `data-testid="node-detail"` to `NodeDetail` root element, enabling E2E Playwright test `web/test/e2e/journeys/011-rollback-button.spec.ts` Steps 2 and 3 (PR #881, 2026-04-20)
- ✅ 14.8 — NEEDS HUMAN resolved — security-checks workflow now succeeds on push; issue #871 closed (2026-04-20)

---

## Future
- 🔲 14.1 — Subscription OCI source watcher: implement `pkg/subscription/oci_watcher.go` — polls OCI registry for new image tags, creates Bundle automatically (issue #491)
- 🔲 14.2 — Subscription Git source watcher: implement `pkg/subscription/git_watcher.go` — watches Git repo for config changes, creates Bundle automatically (issue #493)
- 🔲 14.3 — Pipeline deployment metrics: persist `time-to-production`, `rollback-rate`, `operator-interventions` to `Pipeline.status.deploymentMetrics` after each Bundle completes (issue #498)
- 🔲 14.4 — ChangeWindow CEL functions: implement `changewindow.isAllowed()` and `changewindow.isBlocked()` as named CEL functions on the Graph environment (issue #506)
- 🔲 14.5 — kardinal-agent binary: separate `cmd/kardinal-agent/` entry point for spoke-cluster distributed mode; reads shard assignments, runs PromotionStep reconciler only (issue #508)
- 🔲 14.7 — Security hardening: run `trivy image kardinal-promoter:latest` in CI and block on HIGH/CRITICAL vulnerabilities; target 0 HIGH

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
