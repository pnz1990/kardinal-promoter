# Tasks: Pause/Resume — Freeze-Gate Injection

**Input**: `.specify/specs/025-pause-resume/spec.md`
**Feature branch**: `025-pause-resume`
**Test command**: `go test ./... -race`

---

## Phase 1: Setup

- [ ] T001 Add `freeze-<pipeline>` naming constant to `pkg/reconciler/policygate/reconciler.go` — file: `pkg/reconciler/policygate/reconciler.go`
- [ ] T002 [P] Add `PausedBadge` constant and PAUSED display logic stub to `cmd/kardinal/get_pipelines.go` — file: `cmd/kardinal/get_pipelines.go`

## Phase 2: Tests First

- [ ] T003 Write `TestFreezeGateBlocksPromotion` in `pkg/reconciler/policygate/reconciler_test.go`: freeze gate with expression "false" sets status.ready=false — file: `pkg/reconciler/policygate/reconciler_test.go`
- [ ] T004 [P] Write `TestPauseCreatesFreezeGate` in `cmd/kardinal/pause_test.go`: pause command creates exactly one PolicyGate named freeze-<pipeline> — file: `cmd/kardinal/pause_test.go`
- [ ] T005 [P] Write `TestResumeDeletesFreezeGate` in `cmd/kardinal/resume_test.go`: resume deletes the freeze gate idempotently — file: `cmd/kardinal/resume_test.go`
- [ ] T006 [P] Write `TestPauseIdempotent` in `cmd/kardinal/pause_test.go`: calling pause twice produces exactly one gate — file: `cmd/kardinal/pause_test.go`
- [ ] T007 [P] Write `TestGetPipelinesPausedBadge` in `cmd/kardinal/get_pipelines_test.go`: PAUSED badge shown when freeze gate exists — file: `cmd/kardinal/get_pipelines_test.go`

## Phase 3: Implementation

- [ ] T008 Implement freeze gate fast-path in PolicyGate reconciler: if expression == "false" skip CEL eval, set ready=false directly — file: `pkg/reconciler/policygate/reconciler.go`
- [ ] T009 [P] Implement `pause.go` command: create PolicyGate freeze-<pipeline> with expression "false", check for existing gate first — file: `cmd/kardinal/pause.go`
- [ ] T010 [P] Implement `resume.go` command: delete freeze gate if exists, no-op if not — file: `cmd/kardinal/resume.go`
- [ ] T011 Implement PAUSED badge in `get pipelines`: check for freeze-<pipeline> gate in namespace, display badge — file: `cmd/kardinal/get_pipelines.go`

## Phase 4: Validation

- [ ] T012 Run integration test: pause → create bundle → verify bundle stays at Available (not Promoting) → resume → verify bundle advances — file: `pkg/reconciler/policygate/integration_test.go`
- [ ] T013 Run /speckit.verify-tasks.run to confirm no phantom completions
