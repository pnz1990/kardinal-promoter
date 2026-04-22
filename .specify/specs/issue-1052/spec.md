# Spec: issue-1052 — Loop honesty signal: housekeeping-only PR detection

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: "Loop honesty signal: housekeeping-only PR detection" (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

1. **O1**: SM §4f-housekeeping-streak must scan PRs merged in the last 2 hours (via `gh pr list --state merged --limit 20 --json number,title,mergedAt,files`) and count those matching: title starts with feat/fix/refactor/test AND NOT matching housekeeping prefixes AND has at least one file in `pkg/`, `cmd/`, `web/src/`, or `chart/`.

2. **O2**: When `substantive_count == 0`: SM must increment `housekeeping_streak` in `state.json`. When `substantive_count > 0`: reset `housekeeping_streak` to 0.

3. **O3**: When `housekeeping_streak > 3`: SM must post `[LOOP STALL]` comment to REPORT_ISSUE with the streak count. Must be dedup-guarded (skip if `[LOOP STALL]` appears in last 6 comments on REPORT_ISSUE).

4. **O4**: COORD §1b-preflight already reads `housekeeping_streak` and sets `COORD_ACTION=vision-first` when ≥ 3. No change needed to coord.md (already implemented).

5. **O5**: Fail-open: if the PR list API call fails, set `substantive_count = 1` to avoid false stall detection.

---

## Zone 2 — Implementer's judgment

- Whether to use `--json files` (requires gh API call per PR) or just title-based heuristic
- Exact regex patterns for "housekeeping" vs "substantive" titles
- The dedup window (last 6 comments)

---

## Zone 3 — Scoped out

- Changes to coord.md (§1b-preflight already handles vision-first trigger from housekeeping_streak)
- Consecutive-failure escalation (separate Future item)
- Changes to any Go source code
