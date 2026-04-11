# Item 025: Pause/Resume — Freeze-Gate Injection

> **Stage**: Stage 13 (Rollback and Pause/Resume)
> **Queue**: queue-012
> **Priority**: high
> **Size**: m
> **Depends on**: 010 (PolicyGate reconciler), 015 (full CLI)
> **dependency_mode**: merged

## Context

`kardinal pause <pipeline>` and `kardinal resume <pipeline>` are implemented in the CLI
(item 015). The CLI creates/deletes a PolicyGate with `expression: "false"` named
`freeze-<pipeline>`. However, the full loop needs:

1. The PolicyGate reconciler to recognize and block all PromotionSteps when a freeze gate exists
2. The BundleReconciler to not start new promotions when a freeze gate exists for the Pipeline
3. `kardinal resume` to delete the freeze gate idempotently (already done in CLI, needs integration test)
4. Idempotency: reconcile a paused pipeline twice → same result, no duplicate gates

## Acceptance Criteria

- `kardinal pause nginx-demo` creates a PolicyGate named `freeze-nginx-demo` with `expression: "false"` in the Pipeline's namespace
- After pause, new PromotionStep transitions are blocked (PolicyGate status.ready=false blocks Graph advancement)
- `kardinal resume nginx-demo` deletes the freeze gate idempotently
- After resume, promotions that were in-flight advance normally
- Idempotency test: pausing twice does not create two freeze gates
- Integration test: pause → create bundle → verify bundle stays at Available → resume → verify bundle advances to Promoting
- `kardinal get pipelines` shows PAUSED badge for paused pipelines

## Files to Modify

- `pkg/reconciler/policygate/reconciler.go` — handle freeze gates (already evaluates CEL "false", verify this blocks correctly)
- `pkg/reconciler/bundle/reconciler.go` — skip promotion start when freeze gate active
- `cmd/kardinal/cmd/pause.go` — verify idempotent gate creation (use server-side apply or check-before-create)
- `cmd/kardinal/cmd/resume.go` — verify idempotent delete (ignore not-found errors)
- `pkg/reconciler/promotionstep/reconciler_test.go` — add pause/freeze integration test
- `test/e2e/journeys_test.go` — add J4 pause/resume test case

## Tasks

- [ ] T001 Verify freeze-gate injection in pause.go (server-side apply for idempotency)
- [ ] T002 Verify resume.go deletes idempotently (ignore not-found)
- [ ] T003 Add BundleReconciler check: skip new promotion if freeze gate exists for Pipeline
- [ ] T004 Integration test: pause → bundle creation → verify blocked → resume → verify advances
- [ ] T005 Table-driven idempotency test for pause (create twice → no duplicate)
