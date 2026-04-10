# Item 006: Controller Manager, BundleReconciler, and PipelineReconciler

> **Queue**: queue-003 (Stage 2)
> **Branch**: `006-controller-manager-and-reconcilers`
> **Depends on**: 005 (merged — CRD types complete)
> **Dependency mode**: merged
> **Assignable**: immediately
> **Contributes to**: All journeys (controller foundation)

---

## Goal

Wire the full controller-runtime Manager in `cmd/kardinal-controller/main.go` and
implement two reconcilers: `BundleReconciler` (sets `status.phase = Available` on new
Bundles) and `PipelineReconciler` (sets `status.conditions` on new Pipelines, validates
environment names).

No real promotion logic. Establishes the reconciler loop, status patching via the
`status` subresource with optimistic locking, and structured zerolog logging.

---

## Spec Reference

`docs/aide/roadmap.md` — Stage 2: Bundle and Pipeline Reconcilers (No-Op Baseline)
`docs/design/design-v2.1.md` — Architecture section

---

## Deliverables

### 1. `cmd/kardinal-controller/main.go` — full Manager setup

Replace the Stage 0 stub with a working controller-runtime Manager:

```go
// Wire:
// - controller-runtime Manager with leader election via Kubernetes Lease
// - Health probe endpoint on :8081 (/healthz + /readyz)
// - Metrics endpoint on :8080
// - Configurable log level via --zap-log-level flag (zerolog level)
// - Register BundleReconciler and PipelineReconciler with the Manager
// - Signal handling (SIGTERM/SIGINT → graceful shutdown)
```

Required flags:
- `--leader-elect` (bool, default false)
- `--zap-log-level` (string, default "info"; values: "debug", "info", "warn", "error")
- `--metrics-bind-address` (string, default ":8080")
- `--health-probe-bind-address` (string, default ":8081")

Use `github.com/rs/zerolog` for logging. Inject logger into context via `zerolog.Ctx(ctx)`.
Do NOT use `fmt.Println` or `log.Printf`.

### 2. `pkg/reconciler/bundle/reconciler.go` — BundleReconciler

```go
// BundleReconciler watches Bundle objects.
// On each reconcile:
// 1. Get the Bundle
// 2. If status.phase is empty, set it to Available
// 3. Patch status via status subresource (use client.MergeFrom + client.Status().Patch)
// 4. Emit structured log event: bundle name, type, target Pipeline
// 5. Return reconcile.Result{}
//
// Error handling: wrap all errors with fmt.Errorf("context: %w", err)
// Idempotency: if phase is already set, skip patching (no-op)
```

Create: `pkg/reconciler/bundle/reconciler.go`

### 3. `pkg/reconciler/pipeline/reconciler.go` — PipelineReconciler

```go
// PipelineReconciler watches Pipeline objects.
// On each reconcile:
// 1. Get the Pipeline
// 2. Validate: all environment names are unique; all dependsOn references name
//    environments in the same Pipeline
// 3. If validation fails: set status.conditions = [{type: Ready, status: False,
//    reason: ValidationFailed, message: <error>}]
// 4. If validation passes: set status.conditions = [{type: Ready, status: False,
//    reason: Initializing, message: "Pipeline initialized, awaiting first Bundle"}]
// 5. Patch status via status subresource
// 6. Emit structured log event: pipeline name, environment count
// 7. Return reconcile.Result{}
//
// Idempotency: if conditions are already correct, skip patching
```

Create: `pkg/reconciler/pipeline/reconciler.go`

### 4. Unit tests (TDD — write tests first)

`pkg/reconciler/bundle/reconciler_test.go`:
- `TestBundleReconciler_SetsAvailablePhase`: creates a Bundle with empty phase, reconciles, verifies phase=Available
- `TestBundleReconciler_Idempotent`: Bundle already has phase=Available, reconcile is a no-op (no status patch called)
- Table-driven, use `testify/assert` and `testify/require`
- Use `sigs.k8s.io/controller-runtime/pkg/client/fake` for the fake client

`pkg/reconciler/pipeline/reconciler_test.go`:
- `TestPipelineReconciler_SetsInitializingCondition`: new Pipeline with 3 envs, reconciles, verifies Ready=False/Initializing
- `TestPipelineReconciler_DuplicateEnvironmentNames`: Pipeline with duplicate env names gets ValidationFailed condition
- `TestPipelineReconciler_DependsOnNonExistentEnv`: Pipeline with bad dependsOn gets ValidationFailed condition
- `TestPipelineReconciler_Idempotent`: already has correct conditions, reconcile is no-op
- Table-driven

### 5. `go test ./pkg/reconciler/...` must pass

All tests green before opening PR.

### 6. Update `examples/quickstart/pipeline.yaml` and `examples/quickstart/bundle.yaml`

Ensure the quickstart examples use the correct field names from Stage 1 CRD types.
If the files already use correct names, no change needed. Verify with `kubectl apply --dry-run=client`.

---

## Acceptance Criteria (from roadmap Stage 2)

- [ ] Controller builds and starts (`go build ./cmd/kardinal-controller/`)
- [ ] `BundleReconciler` sets `status.phase = Available` on a newly created Bundle (verified in test)
- [ ] `PipelineReconciler` sets `status.conditions[0].type = Ready` on a new Pipeline (verified in test)
- [ ] `PipelineReconciler` rejects Pipelines with duplicate environment names (test)
- [ ] `PipelineReconciler` rejects Pipelines where `dependsOn` references non-existent envs (test)
- [ ] All reconcilers are idempotent: running twice produces no extra patches (test)
- [ ] `go test ./pkg/reconciler/... -race` passes
- [ ] `go vet ./...` passes
- [ ] `make build` produces `bin/kardinal-controller`
- [ ] Copyright header on all new files
- [ ] No banned filenames

---

## Journey Contribution

This item begins the path toward J1 (Quickstart) by creating the controller that
reacts to Bundle creation. After Stage 2, J1 step 4 (`kubectl apply -f pipeline.yaml`)
will result in a Pipeline with `Ready=False` conditions visible via kubectl.

Journey validation step for PR body:
```bash
# Run the reconciler tests and show output
go test ./pkg/reconciler/... -race -v 2>&1 | tail -30
```

---

## Anti-patterns to Avoid

- Do NOT add `util.go`, `helpers.go`, or `common.go`
- Do NOT import `kro` in go.mod
- Do NOT use `fmt.Println` — use `zerolog.Ctx(ctx)`
- Do NOT mutate Deployments/Services directly
- Use `fmt.Errorf("context: %w", err)` for all error wrapping
- Every reconciler must be idempotent — safe to re-run after crash

---

## Notes for Engineer

The existing `pkg/reconciler/policygate/` and `pkg/reconciler/promotionstep/` packages
are stubs from Stage 0. Do NOT modify them — they are for Stages 4 and 6.

Create NEW packages: `pkg/reconciler/bundle/` and `pkg/reconciler/pipeline/`.

The controller-runtime fake client (`sigs.k8s.io/controller-runtime/pkg/client/fake`)
is already in go.mod from the Stage 0 scaffold. Use `fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()`.

For status patching: use `r.Status().Patch(ctx, obj, patch)` NOT `r.Update()`.
Status is a subresource — regular Update will not persist status fields.
