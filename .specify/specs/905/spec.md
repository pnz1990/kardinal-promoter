# Spec 905: WaitingForMerge timeout to prevent stuck promotions

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future → Lens 2: Production stability — No PromotionStep timeout`
- **Implements**: WaitingForMerge timeout: step transitions to Failed when PR is not merged within the timeout window (🔲 → ✅ partial — health check timeout already exists)

---

## Zone 1 — Obligations (falsifiable)

**O1**: `EnvironmentSpec` MUST have a `WaitForMergeTimeout` field (type: `string`, Go duration format, e.g. `"24h"`, `"72h"`).

**O2**: When `WaitForMergeTimeout` is set to a non-empty, non-zero duration on the environment, and the PromotionStep has been in `WaitingForMerge` state for longer than that duration, the PromotionStep MUST transition to `Failed` with a message containing "wait-for-merge timeout".

**O3**: When `WaitForMergeTimeout` is NOT set (or is empty/zero), the WaitingForMerge state MUST have no timeout — the existing behavior of waiting indefinitely MUST be preserved.

**O4**: The timeout expiry MUST be stored as `status.waitForMergeExpiry` on the PromotionStep (a `*metav1.Time` field). This field MUST be written on the first reconcile after entering `WaitingForMerge` (idempotent: if already set, do not reset).

**O5**: The comparison of `time.Now()` against `status.waitForMergeExpiry` MUST follow the Graph-first pattern: `time.Now()` is called only when writing to CRD status, never as a standalone conditional. (Acceptable: `time.Now().After(ps.Status.WaitForMergeExpiry.Time)` is a read of the stored value, matching the HealthCheckExpiry pattern.)

**O6**: A unit test MUST cover: (a) timeout fires and step fails, (b) timeout not set = no failure, (c) timeout not yet reached = no failure.

**O7**: The `status.waitForMergeExpiry` field MUST be cleared (set to nil) when the PromotionStep transitions out of `WaitingForMerge` (to `HealthChecking` or `Failed`), to avoid stale expiry on requeue.

**O8**: The `PromotionStepStatus` struct MUST include the `WaitForMergeExpiry` field with a descriptive comment explaining its purpose.

---

## Zone 2 — Implementer's judgment

- Default timeout value when none is set (no default; no timeout unless explicitly configured).
- Exact error message format (must contain "wait-for-merge timeout").
- Whether to also pass the timeout through `spec.inputs` from the translator (not required by spec — the reconciler can read the Pipeline directly, as it does for health config).

---

## Zone 3 — Scoped out

- Timeout for `Promoting` state (git-clone, kustomize, etc. — these are fast; not needed now).
- Notification on timeout (separate feature, design doc 15 §Lens 1 — No outbound event notifications).
- Automatic PR close when timeout fires (GitHub API call in reconciler — Graph-first violation; not allowed).
