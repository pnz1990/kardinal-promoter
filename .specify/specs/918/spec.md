# Spec: kardinal get subscriptions + subscription visibility in get pipelines

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Lens 5: Adoption`
- **Implements**: "Warehouse-equivalent: no automatic discovery mode" (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

O1: `kardinal get subscriptions` returns a table with columns: NAME, TYPE, PIPELINE, PHASE, LAST-CHECK, LAST-BUNDLE, AGE.
  - Violation: command doesn't exist or table is missing any of these columns.

O2: `kardinal get subscriptions [name]` with an optional name arg filters to that one Subscription.
  - Violation: a positional arg is not respected as a name filter.

O3: `kardinal get subscriptions` supports `--all-namespaces` / `-A` flag; when set, adds NAMESPACE as first column.
  - Violation: flag not present or NAMESPACE column absent when flag used.

O4: `kardinal get subscriptions` supports `-o json` and `-o yaml` output formats via existing `OutputFormat()` mechanism.
  - Violation: json/yaml flags don't work or produce malformed output.

O5: `kardinal get pipelines` output includes a SUB column indicating how many active Subscriptions target each pipeline.
  - "active" means Subscription.Status.Phase == "Watching".
  - "0" (or "-") when no Subscriptions target the pipeline.
  - Violation: column is absent, or shows wrong count.

O6: The `get` subcommand registers `get subscriptions` (aliases: `subscription`, `sub`).
  - Violation: `kardinal get sub` or `kardinal get subscription` doesn't work.

O7: `kardinal get subscriptions` respects the current namespace (from kubeconfig or `--namespace` flag) when `--all-namespaces` is not set.

O8: All new .go files carry the Apache 2.0 copyright header.

O9: `go test ./...` passes with the new tests (table-driven, `testify`).

---

## Zone 2 — Implementer's judgment

- Column widths and formatting details are left to the implementer.
- Whether to show "0" or "-" for zero SUB count is the implementer's choice.
- The SUB column may be placed after existing environment columns in `get pipelines`.
- LAST-CHECK may be shown as a human-readable duration (e.g. "5m ago") or absolute time.
- No watch mode is required for `get subscriptions` in this iteration.

---

## Zone 3 — Scoped out

- No UI changes (web/) in this PR.
- No changes to the Subscription reconciler.
- No new API endpoints.
- No changes to how Bundles are created.
