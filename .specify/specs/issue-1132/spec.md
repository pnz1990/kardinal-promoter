# Spec: fix PDCA S1 — controller readyz probe gates on cache sync

## Design reference
- **Design doc**: `docs/design/39-demo-e2e-reliability.md`
- **Section**: `§ Future`
- **Implements**: Fix PDCA S1 failure caused by readyz probe not gating on cache sync

## Zone 1 — Obligations (falsifiable)

1. The `/readyz` endpoint MUST return 200 only after the controller-runtime informer cache is synced (`WaitForCacheSync` returns `true`).
2. When the pod reports as ready (Kubernetes readiness probe passes), the Bundle reconciler MUST be able to process incoming Bundle objects.
3. `helm upgrade --wait` on the controller chart MUST guarantee that Bundle reconciliation starts within 15 seconds after the command returns.
4. The PDCA S1 scenario (create Bundle, wait 5 min, check for Verified/Promoting/WaitingForMerge) MUST pass on CI runners with constrained resources.
5. The existing `healthz.Ping` for the liveness probe (`/healthz`) is NOT changed — liveness still uses a simple ping.
6. The fix MUST NOT break the existing `AddReadyzCheck` call pattern.
7. The PDCA S1 wait time is increased from 20×15s (5min) to 30×15s (7.5min) as defense-in-depth.

## Zone 2 — Implementer's judgment

- Use a `healthz.Checker` that calls `mgr.GetCache().WaitForCacheSync(ctx)` with a short timeout (5s per check).
- The checker should return `nil` (healthy) when cache is synced and an error when not yet synced.
- The implementation should be inline in main.go — no new package needed.

## Zone 3 — Scoped out

- Does not fix the root cause of the first Helm install timing out (setup-e2e-env.sh).
- Does not change leader election behavior.
- Does not change the liveness probe.
