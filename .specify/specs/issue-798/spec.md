# Spec: chore(graph): krocodile upgrade 3376810→d6cbc54

## Design reference
- **Design doc**: `docs/design/01-graph-integration.md`
- **Section**: `§ Present` (krocodile upgrade cadence)
- **Implements**: krocodile upgrade cadence check — 5 commits behind threshold reached

## Zone 1 — Obligations

O1. `hack/install-krocodile.sh` MUST have `KROCODILE_COMMIT` updated from `3376810` to `d6cbc54`.

O2. `chart/kardinal-promoter/values.yaml` MUST have `krocodile.pinnedCommit` updated to `"d6cbc54"`.

O3. `chart/kardinal-promoter/Chart.yaml` MUST have `krocodile.commit` annotation updated to `"d6cbc54"`.

O4. All existing tests MUST continue to pass after the upgrade.

O5. No kardinal source files need changes — the 5 new krocodile commits are additive performance
    improvements to forEach that don't break the kardinal integration.

## Zone 2 — Implementer's judgment

- Whether to update any comment text referencing the old commit hash.

## Zone 3 — Scoped out

- No changes to kardinal Go source code
- No changes to translator.go or the Graph builder
- Primitive rethink: the forEach incremental optimization doesn't enable deleting
  any kardinal reconciler — it's a pure performance improvement internal to krocodile.
  No blocked-on-krocodile issues are resolved by this upgrade.
