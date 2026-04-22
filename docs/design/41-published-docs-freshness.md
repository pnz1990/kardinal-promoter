# 41: Published Docs Freshness

> Status: Active | Created: 2026-04-22
> Applies to: kardinal-promoter PM phase (pm.md §5n)

---

## What this does

Ensures that `docs/comparison.md` (and other published documentation) stays
accurate as features are implemented. The PM phase scans for rows in the
comparison table that claim kardinal does NOT have a feature (❌), and checks
whether that feature has since been implemented (appears as ✅ Present in a
design doc). If a mismatch is detected, a `kind/docs` issue is opened for a
human to review and flip the row.

This closes the gap between "feature is implemented" and "marketing/comparison
docs reflect the implementation" — a competitive accuracy problem.

---

## §41.4 Comparison doc accuracy

**Trigger**: PM §5n (runs every N_PM_CYCLES, same cadence as §5f, §5g, §5h, §5i).

**Algorithm**:
1. Read `docs/comparison.md`. Find rows with `❌` in the kardinal column.
2. Extract the feature name from the first cell of each such row.
3. For each feature name: search all `docs/design/*/## Present` sections for
   a ✅ Present item whose description contains the feature name (case-insensitive
   substring, first 60 chars).
4. If match found: open a `kind/docs` GitHub issue:
   `docs: comparison.md row may be stale — <feature> now in ✅ Present`.
5. Dedup: skip if a similar issue is already open.
6. Post summary to REPORT_ISSUE.

**Design rationale**: The comparison table is a competitive artifact — it is
read by potential adopters deciding whether to use kardinal. A false ❌ is worse
than a missing row because it actively misleads. The PM phase is the right place
for this check because PM owns the roadmap and competitive accuracy. The check
opens issues for human review rather than auto-updating, because the comparison
table requires judgment (a feature may be partially implemented, or implemented
differently than the competitor's version).

---

## Present (✅)

- ✅ **Comparison doc accuracy — PM §5n** checks ❌ rows in `docs/comparison.md` against design doc ✅ Present items, opens `kind/docs` issues for stale rows. Fail-open. Runs every N_PM_CYCLES. (PR #1006, 2026-04-22)
- ✅ **Version string freshness — PM §5j** `scripts/version-staleness-check.sh` scans `README.md` and `docs/comparison.md` for hardcoded version strings, compares against latest git tag. If stale by ≥1 minor version: opens `kind/docs priority/high` issue. Dedup guard. Fail-open. (PR #1005-impl, 2026-04-22)

## Future (🔲)

- 🔲 QA docs gate — QA §3b-docs-gate: when a PR moves a Future item to ✅ Present for a user-visible feature (CLI, CRD, UI), verify docs/ files were updated or the feature is Layer 1 auto-documented. If neither: WRONG finding blocks approval.

- 🔲 Changelog completeness — every git tag must have a corresponding entry in CHANGELOG.md. PM §5n-changelog: scan git tags, compare against CHANGELOG.md `## [vX.Y.Z]` headers. For each missing entry: open `kind/docs` issue.
