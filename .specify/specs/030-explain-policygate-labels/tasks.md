# Tasks: 030-explain-policygate-labels

## Phase 1 — Setup
- [ ] T001 Read `cmd/kardinal/cmd/explain.go` and `pkg/graph/builder.go` to find the label key mismatch — file: `cmd/kardinal/cmd/explain.go`, `pkg/graph/builder.go`

## Phase 2 — Tests First
- [ ] T002 Write failing test: explain with PolicyGates in fake client returns them in output — file: `cmd/kardinal/cmd/explain_test.go`

## Phase 3 — Implementation
- [ ] T003 Fix label key in explain.go to match builder.go — file: `cmd/kardinal/cmd/explain.go`

## Phase 4 — Validation
- [ ] T004 Verify `go test ./... -race -count=1 -timeout 120s` passes
- [ ] T005 /speckit.verify-tasks.run
