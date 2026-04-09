# Tasks: Promotion Steps Engine

**Input**: `.specify/specs/008-promotion-steps-engine/spec.md` + `docs/design/08-promotion-steps-engine.md`
**Feature Branch**: `008-promotion-steps-engine`
**Test command**: `go test ./pkg/steps/... -race`

---

## Phase 1: Setup

**Checkpoint**: `go build ./pkg/steps/...` succeeds.

- [ ] T001 Create `pkg/steps/step.go`: Step interface, StepState, StepResult, StepStatus types — file: `pkg/steps/step.go`
- [ ] T002 [P] Create `pkg/steps/registry.go`: built-in step registry map, Register(), Get() — file: `pkg/steps/registry.go`
- [ ] T003 [P] Create `pkg/steps/defaults.go`: InferDefaultSteps() function skeleton — file: `pkg/steps/defaults.go`
- [ ] T004 [P] Create `pkg/steps/engine.go`: Engine struct, RunStep() dispatch — file: `pkg/steps/engine.go`
- [ ] T005 [P] Create `pkg/steps/webhook.go`: dispatchWebhook() with HTTP POST, StepRequest/StepResponse types — file: `pkg/steps/webhook.go`
- [ ] T006 [P] Create step file skeletons in `pkg/steps/steps/` for all 10 built-ins — files: `git_clone.go`, `kustomize.go`, `helm.go`, `kustomize_build.go`, `config_merge.go`, `git_commit.go`, `git_push.go`, `open_pr.go`, `wait_for_merge.go`, `health_check.go`

---

## Phase 2: Tests First

**Checkpoint**: Tests compile but fail.

- [ ] T007 Write `TestDefaultInference_KustomizeAuto` — file: `pkg/steps/steps_test.go`
- [ ] T008 [P] Write `TestDefaultInference_KustomizePR` — file: `pkg/steps/steps_test.go`
- [ ] T009 [P] Write `TestDefaultInference_ConfigBundle` — file: `pkg/steps/steps_test.go`
- [ ] T010 [P] Write `TestKustomizeSetImage_Idempotent`: image already set → no-op success — file: `pkg/steps/steps_test.go`
- [ ] T011 [P] Write `TestGitCommit_NoChanges`: no uncommitted changes → no-op success — file: `pkg/steps/steps_test.go`
- [ ] T012 [P] Write `TestOpenPR_PRAlreadyExists`: existing PR → returns it — file: `pkg/steps/steps_test.go`
- [ ] T013 [P] Write `TestWaitForMerge_Pending`: prMerged=false → returns Pending — file: `pkg/steps/steps_test.go`
- [ ] T014 [P] Write `TestWaitForMerge_Merged`: prMerged=true → returns Success — file: `pkg/steps/steps_test.go`
- [ ] T015 [P] Write `TestCustomWebhook_Success`: mock server returns 200 → Success — file: `pkg/steps/steps_test.go`
- [ ] T016 [P] Write `TestCustomWebhook_Non2xx`: mock server returns 500 → Failed — file: `pkg/steps/steps_test.go`
- [ ] T017 [P] Write `TestOutputPassing`: open-pr reads branch from git-push output — file: `pkg/steps/steps_test.go`

---

## Phase 3: Implementation

**Checkpoint**: `go test ./pkg/steps/... -race` passes.

- [ ] T018 Implement `defaults.go`: InferDefaultSteps() with image/config/helm branches — file: `pkg/steps/defaults.go`
- [ ] T019 Implement `git_clone.go`: shallow clone + cache check (idempotent) — file: `pkg/steps/steps/git_clone.go`
- [ ] T020 [P] Implement `kustomize.go`: check current image, run `kustomize edit set-image` — file: `pkg/steps/steps/kustomize.go`
- [ ] T021 [P] Implement `git_commit.go`: check uncommitted changes, commit with structured message — file: `pkg/steps/steps/git_commit.go`
- [ ] T022 [P] Implement `git_push.go`: push branch, handle conflict → StepFailed — file: `pkg/steps/steps/git_push.go`
- [ ] T023 [P] Implement `open_pr.go`: check existing PR (idempotent), call SCM CreatePR with evidence body — file: `pkg/steps/steps/open_pr.go`
- [ ] T024 [P] Implement `wait_for_merge.go`: check status flags prMerged/prClosed — file: `pkg/steps/steps/wait_for_merge.go`
- [ ] T025 [P] Implement `health_check.go`: delegate to health.Adapter, return Pending if not healthy — file: `pkg/steps/steps/health_check.go`
- [ ] T026 [P] Implement `webhook.go`: HTTP POST with timeout, parse StepResponse — file: `pkg/steps/webhook.go`
- [ ] T027 Register all built-in steps in registry — file: `pkg/steps/registry.go`

---

## Phase 4: Validation

- [ ] T028 Verify `go test ./pkg/steps/... -race` passes
- [ ] T029 [P] Apache 2.0 headers
- [ ] T030 Run `/speckit.verify-tasks.run`
