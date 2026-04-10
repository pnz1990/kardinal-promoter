# Item 013: PromotionStep Reconciler — Full Promotion Loop (Stage 6)

> **Queue**: queue-006
> **Branch**: `013-promotionstep-reconciler`
> **Depends on**: 012 (merged — SCM + Steps Engine)
> **Dependency mode**: merged
> **Assignable**: after 012 is merged
> **Contributes to**: J1, J3, J4, J5 (end-to-end promotion)
> **Priority**: HIGH — critical path to J1

---

## Goal

Implement the `PromotionStepReconciler` that watches `PromotionStep` CRDs and
drives them through the step engine. Wire the full promotion loop end-to-end:
Bundle → Graph → PromotionSteps → PRs → Verified.

Design spec: `docs/design/03-promotionstep-reconciler.md`, roadmap Stage 6.

---

## Deliverables

### 1. `pkg/reconciler/promotionstep/reconciler.go`

State machine:
- `Pending` → `Promoting`: start step engine execution
- `Promoting` → `WaitingForMerge`: open-pr step completed, prURL stored in status
- `WaitingForMerge` → `HealthChecking`: wait-for-merge step returned Success
- `HealthChecking` → `Verified`: health-check step returned Success
- `Any` → `Failed`: any step returned Failed

Reconciler behavior:
- Reads `PromotionStep.spec.stepType` to determine step sequence (via `steps.Defaults`)
- Calls `Engine.Execute` at current step index
- Stores outputs in `PromotionStep.status.outputs`
- Persists `currentStepIndex` in status for crash recovery (idempotent re-runs)
- Uses `fmt.Errorf("context: %w", err)` — no bare errors
- Uses `zerolog.Ctx(ctx)` for logging

### 2. Bundle phase machine in `pkg/reconciler/bundle/reconciler.go`

Extend existing BundleReconciler to:
- Watch PromotionStep statuses and aggregate into Bundle phase:
  - All steps `Verified` → Bundle `Verified`
  - Any step `Failed` → Bundle `Failed`
  - Otherwise → Bundle `Promoting`
- Bundle supersession: when a new Bundle for the same Pipeline is created, older
  Bundles in `Promoting` are set to `Superseded` and their PromotionSteps deleted

### 3. Webhook endpoint in controller HTTP server

Add to the controller HTTP server:
- `POST /webhook/scm` endpoint
  - Validates `X-Hub-Signature-256` HMAC
  - On `pull_request` event with `action: closed, merged: true`, reconciles the owning Bundle
- Startup reconciliation: on controller start, re-list all in-flight PRs and re-check merge status

### 4. `kardinal explain` CLI command

In `cmd/kardinal/cmd/explain.go`:
- `kardinal explain <pipeline> --env <environment> [--watch]`
- Lists PromotionSteps + PolicyGates for the pipeline/environment
- Table columns: ENVIRONMENT | STEP | TYPE | STATE | REASON
- `--watch` streams updates with ANSI in-place refresh

### 5. Integration test

`test/e2e/promotion_loop_test.go`:
- Kind cluster + mock GitHub server (httptest)
- Apply Pipeline + Bundle
- Verify Bundle reaches `Verified` within test timeout
- Verify idempotency: delete and re-create a PromotionStep, no duplicate PRs

---

## Acceptance Criteria

- [ ] PromotionStepReconciler drives full state machine: Pending → Promoting → WaitingForMerge → HealthChecking → Verified
- [ ] Bundle phase aggregated from PromotionStep statuses
- [ ] Bundle supersession: older Bundle set to Superseded when new Bundle created for same Pipeline
- [ ] Idempotency: re-running reconciler does not duplicate PRs (checked via pr_number in status)
- [ ] `/webhook/scm` endpoint validates signature and reconciles Bundle
- [ ] `kardinal explain <pipeline> --env <env>` shows step/gate states
- [ ] Integration test passes with mock GitHub server
- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames

---

## Notes

- Uses `pkg/steps.Engine` from item 012
- Webhook secret sourced from environment variable `KARDINAL_WEBHOOK_SECRET`
- `kardinal explain --watch` can use a simple polling loop (no SSE required)
- No real GitHub API calls in integration tests — use `httptest.NewServer` mock
