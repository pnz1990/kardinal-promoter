# Spec: issue-1162

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: SM health signal says GREEN but product is not advancing: distinguish "healthy loop" from "fast loop" (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: `sm.md §4b` batch report includes a `velocity` dimension alongside the existing
`loop` (GREEN/AMBER/RED) health signal. The velocity value is one of:
- `FAST`: at least one item from `docs/design/15-production-readiness.md ## Future`
  merged in the last 3 batches (checked via merged PR titles cross-referenced against
  doc-15 future items)
- `SLOW`: 3–6 batches since last production-readiness item merged
- `STALL`: 6+ batches since last production-readiness item merged

Falsified by: `grep -q 'velocity=' ~/.otherness/agents/phases/sm.md` returning non-zero.

**O2**: The batch report comment includes `velocity=FAST|SLOW|STALL` in the health line.
When velocity=SLOW or velocity=STALL, the batch report includes a note directing the
COORDINATOR to queue a doc-15 item in the next batch.

Falsified by: the SM batch report not containing the word "velocity" in the health output.

**O3**: `docs/design/12-autonomous-loop-discipline.md` is updated: the
'SM health signal says GREEN' item is flipped from 🔲 to ✅ with a PR reference.

Falsified by: `grep -q '✅.*SM health signal says GREEN' docs/design/12-autonomous-loop-discipline.md` returning non-zero.

**O4**: Fail-open behavior: if `docs/aide/metrics.md` is absent or has fewer than 3 rows,
the velocity metric is computed as `UNKNOWN` and does not block the batch report.

Falsified by: the SM crashing when metrics.md is empty.

---

## Zone 2 — Implementer's judgment

- Batch count detection: use metrics.md rows as a proxy for batch number (count of data rows).
- Doc-15 PR detection: cross-reference merged PR titles (last 20) against doc-15 future item
  descriptions (first 40 chars). A PR title containing a doc-15 item description is counted
  as a "production-readiness PR".
- The 3-batch/6-batch thresholds are configurable via the doc_priority pattern; hardcode
  for now as 3/6 batches (the design spec values).

---

## Zone 3 — Scoped out

- COORDINATOR queuing logic is not changed (that's handled by issue-1173 round-robin fix).
- This spec only adds the velocity metric to the SM report; it does not force the COORDINATOR.
- Historical backfill of metrics.md is not required.

---

## Verification note

Verified by: `grep -q 'velocity=' ~/.otherness/agents/phases/sm.md` — confirms the
velocity variable is written in the SM batch-report step. This is a pure process
change to the agent loop; no Go/UI code changes.
