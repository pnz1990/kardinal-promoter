# Spec: issue-1099 — Kubernetes Events from reconcilers

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — No Kubernetes Events emitted by reconcilers`
- **Implements**: Add EventRecorder to Bundle, PromotionStep, PolicyGate reconcilers (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: `BundleReconciler` emits a `Normal` Kubernetes Event on Bundle phase transitions:
  - `""` → `Available`: reason=`Available`, message=`Bundle available for promotion`
  - `Available` → `Promoting`: reason=`Promoting`, message=`Graph created, promotion started`
  - Any → `Verified`: reason=`Verified`, message=`Promotion verified in all environments`
  - Any → `Failed`: reason=`Failed` (Warning), message includes the failure reason
  - Any → `Superseded`: reason=`Superseded`, message=`Superseded by newer bundle`

**O2**: `PromotionStepReconciler` emits Kubernetes Events on state transitions:
  - Any → `Executing`: reason=`Executing`, message includes the step name
  - Any → `WaitingForMerge`: reason=`WaitingForMerge`, message includes PR URL
  - Any → `Verified`: reason=`Verified`, message=`Step completed successfully`
  - Any → `Failed`: reason=`Failed` (Warning), message includes step name and error

**O3**: `PolicyGateReconciler` emits a `Warning` Kubernetes Event when a gate first blocks (transitions to non-ready state): reason=`Blocked`, message includes the gate expression result.

**O4**: Events are only emitted when the state actually changes (idempotency). An event must NOT be emitted every reconcile loop when no phase transition occurred.

**O5**: EventRecorder is wired via `mgr.GetEventRecorderFor("kardinal-controller")` in `main.go` and passed as a field to each reconciler struct.

**O6**: All three reconcilers compile and pass `go build ./...` and `go vet ./...`. All existing tests pass (`go test ./... -race`).

**O7**: Tests for event emission exist in each reconciler's `_test.go` file using `record.NewFakeRecorder`.

---

## Zone 2 — Implementer's judgment

- Whether to emit events for intermediate state (e.g. PromotionStep.Executing with specific step name) or just terminal transitions.
- How many event types to support: `Normal` for success, `Warning` for failure/block.
- Event message format: should include relevant identifiers (pipeline name, environment) but not be verbose.
- Whether `PolicyGate` blocks should emit on every reconcile or only on first block.

---

## Zone 3 — Scoped out

- No changes to audit event ConfigMap (`writeAuditEvent`) — existing audit mechanism unchanged.
- No Grafana/Prometheus metrics changes in this PR.
- No changes to ScheduleClock, PRStatus, MetricCheck, NotificationHook reconcilers.
- No changes to API types.
