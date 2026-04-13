# Item 401: K-07 — integration-test step (Kubernetes Job)

> Queue: queue-017
> Issue: #449
> Priority: high
> Size: m
> Milestone: v0.6.0 — Pipeline Expressiveness

## Summary

New built-in step `integration-test` in `pkg/steps/steps/`. Creates a Kubernetes Job, waits for completion, writes result to PromotionStep status.outputs. On failure: triggers configured `onFailure` (abort/rollback).

## Acceptance Criteria

- [ ] `integration-test` step registered in step registry
- [ ] Creates a `batch/v1 Job` in target namespace with configured image+command
- [ ] Watches Job until `status.succeeded >= 1` (pass) or `status.failed >= 1` (fail)
- [ ] On pass: outputs `{test_result: "passed", exit_code: "0"}` in step outputs
- [ ] On fail: returns error to reconciler with `onFailure` signal respected
- [ ] Job cleanup: Job is deleted (or TTL) after result is written to step outputs
- [ ] Timeout: `config.timeout` (default 30m) cancels the watch and fails the step
- [ ] Step config validated: `image` is required
- [ ] Unit tests cover: pass, fail, timeout, missing image config
- [ ] `examples/integration-test/pipeline.yaml` demonstrates the step
- [ ] `docs/pipeline-reference.md` documents `uses: integration-test` step config

## Package

`pkg/steps/steps/integration_test_step.go` — new step
`pkg/steps/steps/registry.go` — register step
