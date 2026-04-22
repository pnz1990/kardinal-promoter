# Spec: issue-1049 — PDCA regression attribution + COORDINATOR halt gate

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: "Loop-caused journey regression has no dedicated detection or escalation path" + "PDCA failure enforcement in COORDINATOR is unimplemented" (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

1. **O1**: SM §4f-regression must detect when PDCA transitions from success to failure by parsing the last `[PDCA AUTOMATED]` comment on Issue #1. If "Status: N FAILED" → current_pdca_ok=False; if "Status: ALL PASS" → True.

2. **O2**: When `current_pdca_ok is False` AND `prev_pdca_status != 'failure'` AND `pdca_regression_posted is False`: SM §4f-regression must run `git log --oneline <last-tag>..HEAD -- pkg/ cmd/ chart/ web/src/` and post a `[PDCA REGRESSION]` comment to REPORT_ISSUE listing the attributed commits.

3. **O3**: SM §4f-regression must open a dedup-guarded `[NEEDS HUMAN] PDCA regression` issue with `needs-human,area/test,priority/high` labels. Must not open a second issue if one is already open.

4. **O4**: SM §4f-regression must write `pdca_status=failure` to `state.json` on failure, `pdca_status=success` + `pdca_regression_posted=False` on passing. Fail-open: API errors must not stop the SM phase.

5. **O5**: COORD §1c-pdca-gate must read `pdca_status` from `state.json` after fetching `_state`. If `pdca_status=failure`: skip queue generation (`TODO_COUNT` check moves inside `pdca_status != 'failure'` guard), post one COORDINATOR HALTED comment (dedup via `PDCA_HALT_POSTED` env var).

6. **O6**: COORD §1e-pdca-gate must read `pdca_status` from state.json before the `ITEM_ID=$(python3 ...)` block. If `failure`: set `ITEM_ID=""` and skip claim. The SM/PM phases still run (no `exit`).

---

## Zone 2 — Implementer's judgment

- Exactly which Issue to parse for `[PDCA AUTOMATED]` comments (hardcoded `1` — the PDCA workflow always posts to issue #1 of the repo)
- Whether `pdca_regression_posted` should reset on `pdca_status` → `success` or only on a new non-failure comment (reset on success is simpler and correct)
- Where in sm.md to insert the new section (between §4e and §4f — runs every cycle, before the SDM review post)

---

## Zone 3 — Scoped out

- Consecutive-failure escalation (3+ runs → second needs-human issue) — separate Future item
- Loop health snapshot RED color from PDCA — separate Future item
- vibe-vision-auto PDCA gate — separate Future item
- Changes to any Go source code
