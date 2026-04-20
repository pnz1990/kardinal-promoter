# Spec: Reconciler panic recovery — verify and document controller-runtime default

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Lens 2: Production stability`
- **Implements**: "No reconciler panic recovery — enable WithRecoverPanic" (🔲 → ✅)

## Verification finding (critical thinking applied)

The design doc states "there are zero `recover()` calls in any reconciler" and suggests
using `WithRecoverPanic`. Before implementing, I verified against the actual framework:

**controller-runtime v0.23.3 (our current version):**
- `pkg/internal/controller/controller.go`: `RecoverPanic *bool` field, **defaults to `true`**
- When a panic occurs in `Reconcile()`, the framework: increments `ReconcilePanics` counter,
  calls panic handlers (logging), wraps the error as `"panic: <value> [recovered]"`, and
  returns it to the workqueue for exponential backoff.
- See: `$GOPATH/pkg/mod/sigs.k8s.io/controller-runtime@v0.23.3/pkg/internal/controller/controller.go`

**Conclusion**: panic recovery is already active for all kardinal reconcilers. The design doc
described a gap that controller-runtime resolved before we adopted it.

## Zone 1 — Obligations (falsifiable)

O1. The controller manager MUST NOT be configured to disable panic recovery
    (`RecoverPanic: ptr(false)` must not appear in `ctrl.Options{}`).
    Violation: a panic in any reconciler crashes the controller binary.

O2. A comment in `cmd/kardinal-controller/main.go` MUST document that controller-runtime's
    RecoverPanic default is relied upon, so future engineers do not add redundant recovery code.
    Violation: no comment exists.

O3. The design doc `docs/design/15-production-readiness.md` MUST be updated to reflect that
    this is handled by the framework (🔲 → ✅) with the verification finding.
    Violation: design doc still shows this as a Future gap.

O4. A unit test in `cmd/kardinal-controller/` MUST verify the manager is configured with
    RecoverPanic active (i.e. no explicit `false` override).
    Violation: no test exists covering this.

## Zone 2 — Implementer's judgment

- Exact wording of the comment in main.go.
- Whether to add a test that uses a panicking fake reconciler to verify the behavior end-to-end
  (more comprehensive but heavier) vs a test that checks manager options (lighter).

## Zone 3 — Scoped out

- Custom panic handler hooks (e.g. Sentry/PagerDuty integration): not in scope.
- Panic recovery in the http.Handler webhook server: separate concern, different mechanism.
- Panic recovery in the kardinal-agent binary: separate tracking.
