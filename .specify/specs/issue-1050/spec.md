# Spec: issue-1050 — Simulation Delta Feedback: COORD reads ratio and adjusts queue depth

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: "Simulation delta feedback is one-directional: SM posts it, COORDINATOR never reads it" (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1** — SM §4f writes `actual_prs_merged` and `predicted_prs_floor` to `sim-prediction.json` on `_state` after every batch. The fields must be present in `sim-prediction.json` after any SM §4f run that computed `VISION_PRS` and read `prs_next_batch_floor`.
- Violation: `sim-prediction.json` exists but lacks `actual_prs_merged` after a batch.

**O2** — COORD reads the last 3 `ratio_history` entries from `sim-prediction.json` and computes `ADJUSTED_SESSION_LIMIT`.
- When `ratio < 0.5` for 3 consecutive batches: `ADJUSTED_SESSION_LIMIT = max(1, SESSION_LIMIT - 2)`
- When `ratio > 1.2` for 3 consecutive batches: `ADJUSTED_SESSION_LIMIT = min(10, SESSION_LIMIT + 1)`
- Otherwise: `ADJUSTED_SESSION_LIMIT = SESSION_LIMIT` (no change)
- Violation: `ADJUSTED_SESSION_LIMIT` is not set, or it always equals `SESSION_LIMIT` regardless of ratio history.

**O3** — COORD logs the adjustment decision: `[COORD §1b-delta] ratio_history=[r1,r2,r3] → ADJUSTED_SESSION_LIMIT=N (base=M)`.
- Violation: no log line when `ADJUSTED_SESSION_LIMIT` differs from `SESSION_LIMIT`.

**O4** — The `§1f MULTI-ITEM CHECK` in standalone.md uses `ADJUSTED_SESSION_LIMIT` (not raw `SESSION_LIMIT`) when checking `ITEMS_COMPLETED`.
- Violation: standalone.md's `SESSION_LIMIT=...` block does not reference `ADJUSTED_SESSION_LIMIT`.

**O5** — SM §4f writes `ratio_history` as a list (max 5 entries, FIFO) so the history persists across sessions.
- Violation: `ratio_history` is absent or always has ≤1 entry after multiple SM runs.

**O6** — All logic is fail-open. If `sim-prediction.json` is absent or unreadable, `ADJUSTED_SESSION_LIMIT = SESSION_LIMIT` (no change, no error).
- Violation: COORD crashes or logs an error when `sim-prediction.json` is absent.

---

## Zone 2 — Implementer's judgment

- Ratio threshold 0.5 and 1.2 from issue body are accepted. These are not tunable via config in this PR.
- "3 consecutive batches" uses `ratio_history` list length — entries older than 3 are discarded before checking the threshold.
- `ratio = actual_prs_merged / predicted_prs_floor`. If `predicted_prs_floor = 0`: treat ratio as 1.0 (nominal).
- The `ratio_history` list uses the actual ratio value (float, 2 decimal places) not raw counts.
- SM writes to `sim-prediction.json` regardless of whether `prs_next_batch_floor` was already set (idempotent).

---

## Zone 3 — Scoped out

- Adjusting batch size based on calendar events, CI status, or other signals (beyond ratio).
- Making the 0.5/1.2 thresholds configurable via `otherness-config.yaml`.
- Retroactive history migration (history starts from this PR's merge, no backfill of older batches).
- Alerting on prolonged under/over-delivery beyond what the session-limit adjustment provides.
