# Spec: kardinal logs — per-step status.steps[] rendering

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Observability`
- **Implements**: `kardinal logs` surfaces static snapshot only — per-step granularity missing (🔲 → ✅)

## Zone 1 — Obligations

1. `logsFn` MUST render `status.steps[]` entries as a table below the top-level PromotionStep header.
   - Each row: step name, state, duration (e.g. "1.2s" or "-" if not completed), message (truncated to 80 chars).
2. Steps table MUST include a header row: `STEP`, `STATE`, `DURATION`, `MESSAGE`.
3. If `status.steps[]` is empty or nil, the steps table MUST be omitted (no empty table rendered).
4. Duration MUST be formatted as seconds with 1 decimal place (e.g. `1.2s`) when `DurationMs > 0`, and `-` when zero.
5. Step state MUST be shown verbatim from the `StepExecutionState` field.
6. The existing top-level fields (message, pr_url, outputs, conditions) MUST still be rendered.
7. All new code MUST have unit test coverage in `logs_test.go`.

## Zone 2 — Implementer's judgment

- Table alignment: use tabwriter (already in use in logs.go).
- Message column truncation length: 80 chars (balance readability vs. long kustomize stderr).
- Steps section header: use indented "  steps:" prefix consistent with existing output.
- Ordering: render steps in slice order (already ordered by reconciler).

## Zone 3 — Scoped out

- `--follow` streaming mode (separate issue).
- Showing stdout/stderr verbatim (not captured in current StepStatus struct).
- Filtering steps by state.
