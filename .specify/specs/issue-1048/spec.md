# Spec: issue-1048 — Surface dependsOn validation errors in CLI output

## Design reference
- **Design doc**: `docs/design/39-demo-e2e-reliability.md`
- **Section**: `§ Future`
- **Implements**: Graph builder `dependsOn` validation error is silent in CLI output (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: When `kardinal get pipelines` is invoked and a Bundle exists with
`status.phase == "Failed"` for a pipeline in scope, the CLI output MUST include a
visible error notice containing the pipeline name and the failing condition message.

Violation: output shows only the tabular row with no indication of why promotion failed.

**O2**: The error notice MUST be printed after the table, on its own line, prefixed with
`ERROR:`. The message MUST include the pipeline name and the bundle condition message
(from `status.conditions` where `reason` is `TranslationError` or `InvalidSpec`).

Violation: error buried inside the table column, or not printed at all.

**O3**: `getPipelinesOnce` MUST fetch the Bundle list from the cluster in the same
namespace scope as PromotionSteps. On fetch error, fall back gracefully (no error
notice, table still renders).

Violation: CLI crashes or returns error when Bundle list fetch fails.

**O4**: `FormatPipelineTableFull` signature is unchanged. Error surfacing is handled
as a post-table section printed by `getPipelinesOnce`. The formatting function for
the error block is exported as `FormatBundleErrors` for testability.

Violation: `FormatPipelineTableFull` signature changes, breaking callers.

**O5**: When no bundles are in `Failed` phase, nothing extra is printed (no empty section).

Violation: empty "ERROR:" section printed when all promotions are healthy.

---

## Zone 2 — Implementer's judgment

- Bundle fetch is added to `getPipelinesOnce`, non-fatal on error (same pattern as
  PromotionStep fetch).
- Error notice format: `ERROR: pipeline <name>: <condition-message>\n`. Simple text,
  no tabwriter (error messages can be long).
- Only `Failed` phase bundles are surfaced; `Superseded` and other phases are ignored.
- For `Failed` bundles, search `status.conditions` for any condition with
  `status=True` and `reason` in `{TranslationError, CircularDependency}`. If none
  found, fall back to the raw phase message.

---

## Zone 3 — Scoped out

- Surfacing errors in `--output json` and `--output yaml` modes (those already include
  full Bundle status).
- Showing error inline in the table row (would break column alignment).
- Surfacing `PromotionStep`-level errors (separate concern from graph build errors).
