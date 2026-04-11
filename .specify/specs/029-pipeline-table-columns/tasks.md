# Tasks: 029-pipeline-table-columns

## Phase 1 — Setup
- [ ] T001 Read `cmd/kardinal/cmd/get.go` FormatPipelineTable and related types — file: `cmd/kardinal/cmd/get.go`

## Phase 2 — Tests First
- [ ] T002 Write failing test: `FormatPipelineTable` 3-env pipeline shows per-env columns — file: `cmd/kardinal/cmd/get_test.go`
- [ ] T003 Write failing test: multi-pipeline union columns — file: `cmd/kardinal/cmd/get_test.go`

## Phase 3 — Implementation
- [ ] T004 Implement per-environment column logic in `FormatPipelineTable` — file: `cmd/kardinal/cmd/get.go`

## Phase 4 — Validation
- [ ] T005 Verify `go test ./... -race -count=1 -timeout 120s` passes
- [ ] T006 /speckit.verify-tasks.run
