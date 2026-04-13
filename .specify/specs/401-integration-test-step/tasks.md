# Tasks: Integration Test Step (401-integration-test-step)

## Task Groups

### FR-010: StepState.K8sClient
- [x] Add K8sClient client.Client to StepState in pkg/steps/step.go
- [x] Populate K8sClient in handlePromoting() in promotionstep/reconciler.go
- [x] Populate K8sClient in handleHealthChecking() in promotionstep/reconciler.go

### FR-001-009: integration-test step
- [x] Register integrationTestStep in init()
- [x] Name() returns "integration-test"
- [x] Validate integration_test.image (FR-008, AC-006)
- [x] Validate K8sClient not nil (FR-010, AC-007)
- [x] Parse integration_test.timeout duration (default 30m) (FR-007)
- [x] Parse integration_test.command (FR-009)
- [x] Deterministic Job name from env+bundle hash (FR-002, AC-004)
- [x] Create batch/v1 Job with configured image + command (FR-002, AC-001)
- [x] Return StepPending + RequeueAfter:15s when running (FR-004)
- [x] Return StepSucceeded when succeeded >= 1 (FR-005, AC-002)
- [x] Return StepFailed when failed > 0 (FR-006, AC-003)
- [x] Return StepFailed on timeout (FR-007, AC-005)
- [x] Idempotent: Get Job first; only create if NotFound (FR-003, AC-004)

### Tests (9 tests)
- [x] TestIntegrationTestStep_MissingK8sClient
- [x] TestIntegrationTestStep_MissingImage
- [x] TestIntegrationTestStep_CreatesPending
- [x] TestIntegrationTestStep_JobSucceeded
- [x] TestIntegrationTestStep_JobFailed
- [x] TestIntegrationTestStep_JobRunning
- [x] TestIntegrationTestStep_InvalidTimeout
- [x] TestIntegrationTestStep_ParseCommand
- [x] TestIntegrationTestStep_Idempotent

### Docs
- [x] docs/pipeline-reference.md: integration-test step section
- [x] examples/integration-test/pipeline.yaml: runnable example

## Verify Tasks

All [x] items have real implementation. Zero phantom completions.

Evidence:
- pkg/steps/step.go: K8sClient field (line ~117)
- pkg/steps/steps/integration_test_step.go: 264 lines
- pkg/steps/steps/integration_test_test.go: 287 lines, 9 tests
- pkg/reconciler/promotionstep/reconciler.go: K8sClient at 2 sites
- go test ./pkg/steps/...: PASS (9 tests)
