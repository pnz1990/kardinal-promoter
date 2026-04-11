# Item 018: Startup Reconciliation + Webhook Health Endpoint (Stage 10 completion)

> **Queue**: queue-008
> **Branch**: `018-startup-reconciliation`
> **Depends on**: 016 (merged — PR evidence + webhook foundation)
> **Dependency mode**: merged
> **Assignable**: after 017 (sequential to avoid conflicts in webhook.go)
> **Contributes to**: J1 (webhook reliability), J4 (rollback PR detection)
> **Priority**: MEDIUM — improves reliability for J1 end-to-end pass

---

## Goal

Complete the Stage 10 startup reconciliation feature: on controller start, scan all
Bundles in `Promoting` phase and reconcile their PR status from GitHub. This ensures
PRs merged during controller downtime are detected and promotion continues.

---

## Deliverables

### 1. Startup reconciliation in BundleReconciler

In `pkg/reconciler/bundle/reconciler.go`:
- On manager Start (via `Runnable` interface), list all Bundles with `status.phase = Promoting`
- For each Bundle, list its PromotionSteps in `WaitingForMerge` state
- For each such step, call `SCMProvider.GetPRStatus` using the PR number from step outputs
- If merged: transition step to `HealthChecking` (via status patch)
- Log: `startup reconciliation: re-checking N in-flight PRs`
- This runs once on startup; normal webhook handles ongoing events

### 2. `/webhook/scm/health` endpoint

In `cmd/kardinal-controller/webhook.go`, verify the health endpoint is accessible:
- `GET /webhook/scm/health` returns `200 OK` with body `{"status":"ok","webhookConfigured":true/false}`
- Already partially implemented — verify it works and add a test

### 3. Metrics counter

In `cmd/kardinal-controller/webhook.go`:
- Add a package-level `webhookEventsTotal` counter (simple `sync/atomic` int64, not Prometheus — full metrics in Stage 19)
- Increment on each webhook event processed
- Expose in `/webhook/scm/health` response: `{"eventsProcessed": N}`

### 4. Unit tests

- `TestStartupReconciliation_RechecksInFlightPRs`: mock SCM returns merged=true; verify PromotionStep transitions to HealthChecking
- `TestStartupReconciliation_SkipsCompletedBundles`: Bundles with phase=Verified are skipped
- `TestWebhookHealth_ReturnsOK`: GET /webhook/scm/health returns 200

---

## Acceptance Criteria

- [ ] On controller start, in-flight `WaitingForMerge` PromotionSteps have their PR status re-checked
- [ ] PRs merged during downtime are detected on next startup
- [ ] `/webhook/scm/health` returns `200 OK` with webhookConfigured field
- [ ] Startup reconciliation logs the number of in-flight PRs checked
- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames
