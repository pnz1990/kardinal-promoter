# Spec: issue-1006 — feat(pm): §5j comparison doc accuracy — flip rows when gaps are closed

## Design reference
- **Design doc**: `docs/design/41-published-docs-freshness.md`
- **Section**: `§41.4 Comparison doc accuracy`
- **Implements**: 🔲 Comparison doc accuracy — PM §5n checks ❌ rows against design doc ✅ Present (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

1. `docs/design/41-published-docs-freshness.md` exists and contains a `§ Future` section with this item marked 🔲, and a `§ Present` section.

2. `pm.md` contains a new `## 5n.` section (after `## 5m.`) titled "Comparison doc accuracy check".

3. The §5n section runs under `N_PM_CYCLES` guard: `if [ $((${PM_CYCLE:-0} % ${N_PM_CYCLES:-3})) -eq 0 ]`.

4. The §5n implementation:
   a. Reads `docs/comparison.md` (fails gracefully if absent — skip silently).
   b. Finds rows containing `❌` in the feature matrix table.
   c. For each ❌ row: extracts the feature name (first cell of the table row).
   d. Searches all `docs/design/` files for ✅ Present items matching the feature name (case-insensitive substring match on first 60 chars of item description).
   e. If a match is found: opens a GitHub issue titled `docs: comparison.md row may be stale — <feature> now in ✅ Present` (deduplication: skips if similar issue already open).
   f. Posts summary comment to REPORT_ISSUE with count of mismatches found.

5. All GitHub API calls in §5n are wrapped in try/except with non-fatal error handling (fail-open).

6. The implementation includes a test: `python3 .specify/specs/issue-1006/test_comparison_check.py` that verifies the parsing logic against a fixture comparison table.

## Zone 2 — Implementer's judgment

- The exact matching algorithm (feature name to ✅ Present item) uses heuristic substring matching. False positives are acceptable — the goal is to surface candidates for human review, not to automatically flip rows.
- The §5n section number. If §5n conflicts with an existing section, use the next available letter.
- Whether to also scan for `No` cells (not just ❌). Conservative: only ❌ for now.

## Zone 3 — Scoped out

- Automatically updating comparison.md (human must update it).
- Parsing non-table content in comparison.md.
- Matching ✅ Present items to Kargo/GitOps Promoter columns (only kardinal column checked).
- Semantic similarity matching (exact substring only).
