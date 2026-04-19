# Spec: fix(graph): PromotionStep cycles Verified→Promoting — krocodile upgrade + empty-hash guard

## Design reference
- **Design doc**: `docs/design/01-graph-integration.md`
- **Section**: `## Present` / krocodile upgrade cadence
- **Implements**: Two fixes for the Verified→Promoting cycling regression (#789).

---

## Root cause analysis

Two contributing causes were identified:

**Primary (krocodile)**: krocodile commit `3bcbe92` fixes "propagation trigger on self-state
refresh". When Path 2 (self-state changed) refreshed a template node's scope, dependents
were hash-skipped — they received no propagation trigger, so downstream nodes seeing
`test.status.state` never re-evaluated when `test` reached `Verified`. The `uat` node was
permanently hash-skipped, UAT never started, and krocodile's internal inconsistency caused
the Graph controller to re-dispatch `test` in a degraded state, cycling Verified→Promoting.

**Secondary (bundle reconciler)**: `ensurePipelineSpecCurrent` treats `b.Status.PipelineSpecHash == ""`
as "spec changed" and deletes the Graph. This would affect Bundles promoted before PR #634
(which introduced PipelineSpecHash). Each deletion resets all PromotionSteps to empty status.

---

## Zone 1 — Obligations (falsifiable)

O1. The krocodile pinned commit in `hack/install-krocodile.sh` MUST be updated from `cdc4bb9`
    to `3376810` (includes the propagation trigger fix in `3bcbe92`).

O2. The `krocodile.pinnedCommit` in `chart/kardinal-promoter/values.yaml` MUST match `3376810`.

O3. The `krocodile.commit` annotation in `chart/kardinal-promoter/Chart.yaml` MUST match `3376810`.

O4. `ensurePipelineSpecCurrent` MUST NOT delete the Graph when `b.Status.PipelineSpecHash == ""`
    (uninitialised). An empty stored hash means "not yet observed". The function MUST update
    the stored hash and return nil without deleting the Graph.

O5. A unit test MUST verify that `ensurePipelineSpecCurrent` with `PipelineSpecHash == ""`
    does NOT delete the Graph and DOES save the current hash.

O6. `go build ./...` and `go test ./... -race -count=1 -timeout 120s` pass.

---

## Zone 2 — Implementer's judgment

- O4 fix: guard with `if b.Status.PipelineSpecHash == "" { save hash; return nil }` before
  the deletion path.
- Compat check between `cdc4bb9` and `3376810` shows only cosmetic renames and type-level
  changes to krocodile internals (ForEachBinding, dag.NodeTypes). Kardinal does not import
  krocodile internals — it uses the CRD YAML format only.
- Tests for AbortedByAlarm and RollingBack idempotency are added as regression guards,
  even though the switch already handles them (discovered during investigation).

---

## Zone 3 — Scoped out

- Live cluster E2E validation (requires kind cluster — tracked in definition-of-done.md).
- Upstream krocodile PR for the propagation trigger fix (already merged as 3bcbe92).
- Changes to PromotionStep CRD types or enum validation.
