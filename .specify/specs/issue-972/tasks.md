# Tasks — issue-972

## Add Prometheus metrics for step duration and gate blocking time

- [AI]  Write spec.md (10 Zone 1 obligations) — DONE
- [CMD] Read existing metrics.go and observability pattern — DONE
- [AI]  Add StepDurationSeconds histogram with {step} label — DONE
- [AI]  Add GateBlockingDurationSeconds histogram — DONE
- [AI]  Add PromotionStepAgeSeconds histogram — DONE
- [AI]  Register all 3 new metrics in init() — DONE
- [AI]  Call StepDurationSeconds.Observe() in updateStepStatuses (Completed + Failed) — DONE
- [AI]  Call GateBlockingDurationSeconds.Observe() in policygate patchStatus (blocked→allowed) — DONE
- [AI]  Call PromotionStepAgeSeconds.Observe() at Verified (handleHealthChecking) — DONE
- [AI]  Call PromotionStepAgeSeconds.Observe() at Failed (step engine error path) — DONE
- [AI]  Add 3 tests: StepDuration, GateBlocking, PromotionStepAge — DONE
- [AI]  Update TestMetricNames to include new metric names — DONE
- [CMD] go build ./... — DONE (clean)
- [CMD] go test ./... -race — DONE (all green)
- [CMD] go vet ./... — DONE (clean)
- [AI]  Update design doc 15 (🔲 → ✅) — DONE
- [CMD] Commit and push — TODO
- [CMD] Open PR — TODO
