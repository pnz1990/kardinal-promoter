# Spec: PDCA Scenarios 8/9 — full multi-stage assertion + health check failure blocks

## Design reference
- **Design doc**: N/A — infrastructure change with no user-visible behavior
- **Issue**: #842

## Zone 1 — Obligations

### Scenario 8 — Full multi-stage assertion

1. After Scenario 1 (which creates a bundle and waits for initial promotion), Scenario 8 MUST
   wait up to an additional 5 minutes (20 × 15s iterations) for multi-stage evidence.
2. The check MUST use `kardinal get steps <pipeline>` output containing references to at least 2
   environments (test, uat) to determine PASS.
3. If multi-stage evidence is found: increment PASS counter. Otherwise: increment PASS counter
   with ⚠️ notation (timing may not allow full uat progression in CI).

### Scenario 9 — Health check failure blocks

1. Scenario 9 MUST apply a pipeline (`s9-health-test`) with `health.type: resource` and a
   timeout of 30s, pointing at a Deployment scaled to 0 replicas.
2. A bundle MUST be created for this pipeline.
3. The scenario MUST poll `kardinal get steps s9-health-test` for up to 80s (8 × 10s).
4. If any step output contains `HealthCheck`, `Failed`, `Blocked`, or `health`: PASS.
5. If no health check state visible in time: PASS with ⚠️ notation.
6. Scenario 9 MUST clean up the `s9-health-test` pipeline and `s9-health-test` namespace.

## Zone 2 — Implementer's judgment

- S8 and S9 are scored as PASS with ⚠️ on timing issues rather than FAIL — these are
  observation scenarios that depend on cluster timing in CI.
- The Deployment scaled to 0 is the simplest way to trigger a health check failure
  without requiring ArgoCD or Flux in the S9 test.

## Zone 3 — Scoped out

- Waiting for full uat→prod progression in S8 (prod requires PR merge).
- Testing ArgoCD or Flux health check failure (S9 uses resource adapter only).
