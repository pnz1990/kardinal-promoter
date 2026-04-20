# 14 — v0.6.0 Roadmap Items

> Status: Complete | Created: 2026-04-20 | Closed: 2026-04-20
> Milestone: v0.6.0

---

## Summary

This design doc tracks the concrete v0.6.0 near-term items from the vision.
All stages 0-29 are complete. All v0.6.0 items (14.1–14.8) are now ✅ Present.

---

## Present

- ✅ 14.1 — Subscription OCI source watcher: `pkg/source/oci.go` — polls OCI registry for new image tags, creates Bundle automatically (shipped in earlier release, issue #491)
- ✅ 14.2 — Subscription Git source watcher: `pkg/source/git.go` — watches Git repo for config changes, creates Bundle automatically (shipped in earlier release, issue #493)
- ✅ 14.3 — Pipeline deployment metrics: `pkg/reconciler/pipeline/metrics.go` — persists `rolloutsLast30Days`, `p50CommitToProdMinutes`, `p90CommitToProdMinutes`, `autoRollbackRate` to `Pipeline.status.deploymentMetrics` (shipped in earlier release, issue #498)
- ✅ 14.4 — ChangeWindow CEL functions: `changewindow.isAllowed()` / `changewindow.isBlocked()` implemented in `pkg/reconciler/policygate/reconciler.go` (shipped in earlier release, issue #506)
- ✅ 14.5 — kardinal-agent binary: `cmd/kardinal-agent/main.go` — separate entry point for spoke-cluster distributed mode; reads shard assignments, runs PromotionStep reconciler only (PR #886, 2026-04-20)
- ✅ 14.6 — PDCA: Playwright rollback button tests un-skipped — added `data-testid="node-detail"` to `NodeDetail` root element, enabling E2E Playwright test `web/test/e2e/journeys/011-rollback-button.spec.ts` Steps 2 and 3 (PR #881, 2026-04-20)
- ✅ 14.7 — Security hardening: `trivy-fs` job added to CI — scans Go modules for HIGH/CRITICAL CVEs with `exit-code: 1`, `ignore-unfixed: true`; blocks CI on findings (PR #882, 2026-04-20)
- ✅ 14.8 — NEEDS HUMAN resolved — security-checks workflow now succeeds on push; issue #871 closed (2026-04-20)

---

## Future

*(No outstanding Future items — all v0.6.0 items are shipped.)*

---

## Zone 1 — Obligations

**O1 — Items 14.1–14.5 ship in v0.6.0 before cutting the release.**
The release is cut via `gh release create v0.6.0` once all items here are ✅ Present.
**STATUS: ALL COMPLETE.**

**O2 — PDCA (14.6) must pass before v0.6.0 is cut.**
The PDCA workflow is the agent's own smoke test of the product.
**STATUS: COMPLETE.**

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
