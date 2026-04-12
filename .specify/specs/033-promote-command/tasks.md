# Tasks: 033-promote-command

## Phase 1 — Setup

- [ ] T001 Create feature branch: 033-promote-command — file: (git)
- [ ] T002 Read existing CLI structure: cmd/kardinal/cmd/ — file: cmd/kardinal/cmd/root.go

## Phase 2 — Tests First

- [ ] T003 Write table-driven tests for promote command validation — file: cmd/kardinal/cmd/promote_test.go
- [ ] T004 Write test: pipeline not found returns error — file: cmd/kardinal/cmd/promote_test.go [P]
- [ ] T005 Write test: env not in pipeline returns error — file: cmd/kardinal/cmd/promote_test.go [P]

## Phase 3 — Implementation

- [ ] T006 Create promote.go with cobra command definition — file: cmd/kardinal/cmd/promote.go
- [ ] T007 Implement promote handler: lookup pipeline, validate env, create Bundle — file: cmd/kardinal/cmd/promote.go
- [ ] T008 Register promote subcommand in root.go — file: cmd/kardinal/cmd/root.go
- [ ] T009 Update docs/cli-reference.md with promote command — file: docs/cli-reference.md

## Phase 4 — Validation

- [ ] T010 Run go build ./..., go test ./..., go vet ./... — file: (all)
- [ ] T011 /speckit.verify-tasks.run — verify all tasks implemented — file: (all)
