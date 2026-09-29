# Spec: chore(graph): Graph controller fork upgrade 3376810→d6cbc54

## Design reference
- **Design doc**: `docs/design/01-graph-integration.md`
- **Section**: `§ Present` (Graph controller upgrade cadence)
- **Implements**: Graph controller upgrade cadence check — 5 commits behind threshold reached

## Zone 1 — Obligations

O1. The fork install script MUST have its pinned commit updated from `3376810` to `d6cbc54`.

O2. `chart/kardinal-promoter/values.yaml` MUST have the fork's pinned-commit value updated to `"d6cbc54"`.

O3. `chart/kardinal-promoter/Chart.yaml` MUST have the fork's commit annotation updated to `"d6cbc54"`.

O4. All existing tests MUST continue to pass after the upgrade.

O5. No kardinal source files need changes — the 5 new fork commits are additive performance
    improvements to forEach that don't break the kardinal integration.

## Zone 2 — Implementer's judgment

- Whether to update any comment text referencing the old commit hash.

## Zone 3 — Scoped out

- No changes to kardinal Go source code
- No changes to translator.go or the Graph builder
- Primitive rethink: the forEach incremental optimization doesn't enable deleting
  any kardinal reconciler — it's a pure performance improvement internal to the Graph controller.
  No blocked-on-upstream issues are resolved by this upgrade.
