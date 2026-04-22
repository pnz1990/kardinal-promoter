# Spec: issue-1054 — Loop prediction → behavior feedback loop

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: "Loop prediction → behavior feedback loop" (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

1. **O1**: SM §4f-pred-delta must read `prs_next_batch_floor` and `prs_next_batch_ceiling` from `_state:sim-prediction.json` (via `git show origin/_state:.otherness/sim-prediction.json`).

2. **O2**: Must compute `ratio = actual_merged / floor` where `actual_merged = MERGED` env var.

3. **O3**: SM batch report `<details>` block must include: `Sim delta: predicted N-M items, actual X (ratio: X/N)`.

4. **O4**: If `ratio < 0.5`: append ` ⚠️ Underdelivery (ratio=X < 0.5)` to the line.

5. **O5**: Fail-open: if `sim-prediction.json` is missing or unreadable, show `?` values without error.

---

## Zone 2 — Implementer's judgment

- Whether to use floor or midpoint as the "predicted N" value (use floor — matches SM §4e divergence check convention)
- Format of the ratio (2 decimal places)

---

## Zone 3 — Scoped out

- COORDINATOR reading the delta and adjusting queue depth (separate Future item: "Simulation delta feedback is one-directional")
- Persistent underdelivery escalation (SM §4e already handles this)
- Changes to any Go source code
