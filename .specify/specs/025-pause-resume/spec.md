# Spec: Pause/Resume — Freeze-Gate Injection

> Feature branch: `025-pause-resume`
> Stage: 13 (Rollback and Pause/Resume)
> Depends on: 010, 015

## User Scenarios

### Scenario 1: Pause blocks new promotions
Given a running Pipeline `nginx-demo`
When `kardinal pause nginx-demo` is executed
Then a PolicyGate named `freeze-nginx-demo` with `expression: "false"` is created
And new Bundle promotions do not advance past the freeze gate
And `kardinal get pipelines` shows a PAUSED badge

### Scenario 2: Resume restores promotion flow
Given Pipeline `nginx-demo` is paused with a freeze gate
When `kardinal resume nginx-demo` is executed
Then the `freeze-nginx-demo` PolicyGate is deleted
And in-flight promotions advance normally

### Scenario 3: Pause is idempotent
Given Pipeline `nginx-demo` is already paused
When `kardinal pause nginx-demo` is executed again
Then exactly one freeze gate exists (no duplicate)
And no error is returned

### Scenario 4: Pause blocks in-flight promotions
Given a Bundle is in the `Promoting` state for `nginx-demo`
When `kardinal pause nginx-demo` is executed
Then the in-flight promotion halts at the freeze gate
And the Bundle remains in `Promoting` state (not failed)

## Requirements

- FR-001: `kardinal pause <pipeline>` MUST create a PolicyGate named `freeze-<pipeline>` with `expression: "false"` in the Pipeline's namespace
- FR-002: The PolicyGate reconciler MUST evaluate the freeze gate and set `status.ready=false`
- FR-003: The Graph MUST block downstream PromotionStep advancement when the freeze gate is not ready
- FR-004: `kardinal resume <pipeline>` MUST delete the freeze gate idempotently (no error if already deleted)
- FR-005: `kardinal get pipelines` MUST display a PAUSED badge when a freeze gate exists
- FR-006: Pausing twice MUST NOT create duplicate freeze gates
- FR-007: Every reconciler function MUST be idempotent (safe to call multiple times)

## Go Package Structure

```
pkg/reconciler/policygate/
  reconciler.go     # freeze gate recognition (expression: "false" fast-path)
cmd/kardinal/
  pause.go          # pause command (creates freeze PolicyGate)
  resume.go         # resume command (deletes freeze PolicyGate)
  get_pipelines.go  # add PAUSED badge when freeze gate exists
```

## Success Criteria

- SC-001: `go test ./pkg/reconciler/policygate/... -race` passes with freeze gate test cases
- SC-002: `go test ./cmd/kardinal/... -race` passes with pause/resume integration tests
- SC-003: Idempotency test: pause called twice produces exactly one freeze gate
- SC-004: Integration test: pause → create bundle → verify stays at Available → resume → verify advances
