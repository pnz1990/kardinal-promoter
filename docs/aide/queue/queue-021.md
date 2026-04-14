# Queue 021 — Journey 2 Enablement + J7 Bootstrap

> Created: 2026-04-14
> Status: Active
> Purpose: Enable Journey 2 (multi-cluster fleet) and start Journey 7 (multi-tenant)

## Context

Journeys 1, 3, 4, 5, 6 are all ✅. Journey 2 (multi-cluster fleet) and Journey 7 (multi-tenant)
remain. This queue targets the minimal implementation needed to make J2 testable in CI.

Journey 2 needs:
1. `dependsOn` fan-out test (already implemented — translator handles DependsOn)
2. Parallel PromotionStep processing (fan-out creates two steps simultaneously)
3. The J2 test written to use fake client (similar to J1 test) without requiring Stage 14

Journey 7 needs ApplicationSet integration. This is a separate track.

## Items

| Item | Issue | Title | Priority | Size | Depends on |
|---|---|---|---|---|---|
| 800-j2-test | #400 | test(e2e): write Journey 2 test with dependsOn fan-out (fake client) | high | m | — |
| 801-appset-bootstrap | #467 (new) | feat(multi-tenant): ApplicationSet pipeline provisioning for J7 | medium | l | — |

## Notes

Item 800: The J2 test should use the existing fake client approach (like J1/J3/J4/J5/J6).
The test verifies: dependsOn fan-out creates two PromotionSteps simultaneously, both get PRs,
both reach Verified. Stage 14 distributed mode is not needed for this — a single reconciler
can process both shards in test mode.

Item 801: Bootstrap the ApplicationSet integration for J7. The ApplicationSet controller
watches for team folders in a central repo and provisions Pipeline CRDs automatically.
This requires defining how kardinal integrates with Argo CD ApplicationSet.
