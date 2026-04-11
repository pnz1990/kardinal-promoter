# Tasks: 031-explain-cel-expression

## Phase 1 — Setup
- [ ] T001 Read `cmd/kardinal/cmd/explain.go` to understand current PolicyGate output structure — file: `cmd/kardinal/cmd/explain.go`

## Phase 2 — Tests First
- [ ] T002 Write failing test: explain output for PolicyGate includes expression column — file: `cmd/kardinal/cmd/explain_test.go`
- [ ] T003 Write failing test: unevaluated gate shows VALUE=`-` — file: `cmd/kardinal/cmd/explain_test.go`

## Phase 3 — Implementation
- [ ] T004 Add EXPRESSION and VALUE columns to PolicyGate rows in explain — file: `cmd/kardinal/cmd/explain.go`

## Phase 4 — Validation
- [ ] T005 Verify `go test ./... -race -count=1 -timeout 120s` passes
- [ ] T006 /speckit.verify-tasks.run
