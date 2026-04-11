# Item 028: Custom Promotion Steps via HTTP Webhook

> **Stage**: Stage 16 (Custom Promotion Steps via Webhook)
> **Queue**: queue-013
> **Priority**: high
> **Size**: m
> **Depends on**: 013 (PromotionStep reconciler)
> **dependency_mode**: merged

## Context

Stage 16 allows teams to add custom logic to the promotion sequence via HTTP webhooks.
Any step `uses:` value not matching a built-in step name is dispatched as an HTTP POST
to `spec.steps[].webhook.url`.

The webhook contract:
- POST JSON: `{bundle, environment, inputs, outputs_so_far}`
- Response JSON: `{result: "pass|fail", outputs: {key: value}, message: string}`

Custom steps must be idempotent: the reconciler may call them multiple times if a
crash occurs between the call and the status patch.

## Acceptance Criteria

- Custom step dispatch in `PromotionStepReconciler`:
  - Any `uses` value not matching a built-in step is treated as custom
  - Dispatches HTTP POST to `spec.steps[].webhook.url`
  - Body: `{bundle, environment, inputs, outputs_so_far}`
  - Response: `{result: "pass|fail", outputs: {}, message: string}`
  - Timeout: `spec.steps[].webhook.timeoutSeconds` (default 300)
  - Retry: 3 attempts with 30-second backoff on 5xx errors
- Webhook authentication: `spec.steps[].webhook.secretRef` — K8s Secret with `Authorization` header
- Step output accumulator: custom step outputs merged into `PromotionStep.status.outputs`
- Example server: `examples/custom-step/` with a sample Go HTTP server and Pipeline referencing it
- Documentation: `docs/custom-steps.md`
- Tests:
  - Custom step returning pass: next step receives outputs
  - Custom step returning fail: PromotionStep → Failed
  - Webhook timeout: marked Failed with `DeadlineExceeded`
  - Auth header from Secret: included in request

## Files to Create/Modify

- `pkg/steps/custom.go` — custom HTTP step implementation (moved to parent pkg)
- `pkg/steps/steps/custom_test.go` — unit tests with mock HTTP server
- `pkg/steps/registry.go` — extend Lookup to dispatch unknown steps to custom
- `api/v1alpha1/pipeline_types.go` — add `WebhookConfig` + `StepSpec` types
- `examples/custom-step/server.go` — example custom step server
- `examples/custom-step/pipeline.yaml` — example Pipeline with custom step
- `docs/custom-steps.md` — documentation

## Tasks

- [x] T001 Add `WebhookConfig` to `StepSpec` in pipeline_types.go
- [x] T002 Write failing tests for custom HTTP step (pass, fail, timeout, auth)
- [x] T003 Implement `pkg/steps/custom.go` with HTTP dispatch
- [x] T004 Extend step registry to dispatch unknown step names to custom
- [x] T005 Write integration test: custom step pass → next step receives outputs
- [x] T006 Create examples/custom-step/ server and pipeline
- [x] T007 Create docs/custom-steps.md
- [x] T008 Verify go test -race passes
