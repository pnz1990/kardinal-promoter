# Spec: issue-1170

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: Backlog overflow alarm: SM flags when backlog > 40 items (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: `scripts/check-backlog-overflow.sh` exists and counts 🔲 items in docs/design/.
- Verification: `test -f scripts/check-backlog-overflow.sh && bash scripts/check-backlog-overflow.sh`

**O2**: The script outputs `[BACKLOG OVERFLOW — N unqueued items]` when count > 40.
- Verification: inject 41+ 🔲 items in a test dir, run the script, expect the alarm string.

**O3**: The script exits 0 (non-blocking by design — it is an alarm, not a gate).
- Verification: `bash scripts/check-backlog-overflow.sh; echo "exit: $?"` → exit: 0

**O4**: The design doc item is flipped from 🔲 to ✅.
- Verification: `grep -q '✅.*[Bb]acklog.*[Oo]verflow\|✅.*[Bb]acklog.*[Aa]larm' docs/design/12-autonomous-loop-discipline.md`

---

## Zone 2 — Implementer's judgment

- The threshold (40 items) is taken from the design doc. Configurable via env var.
- Output format: `[BACKLOG OVERFLOW — N unqueued items]` when N > threshold.
- The script can be called from sm.md §4b batch report as an [AI-STEP] reference.

---

## Zone 3 — Scoped out

- Integration into sm.md phase file (separate CRITICAL-A PR if needed).
- The sm.md phase file update requires CRITICAL-A self-review protocol.
- This PR delivers the script and design doc update only.
