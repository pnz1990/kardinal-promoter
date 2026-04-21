# Spec: PDCA coverage must never be 0/0 — flag as BROKEN when it is

## Design reference
- **Design doc**: `docs/design/13-scheduled-execution.md`
- **Section**: `§ Future`
- **Implements**: PDCA coverage must never be 0/0 — flag as BROKEN when it is (🔲 → ✅)

---

## Zone 1 — Obligations

**O1 — When TOTAL == 0, the PDCA workflow MUST post a `[PDCA BROKEN]` comment to Issue #1.**

The comment body must contain the literal string `[PDCA BROKEN` and explain that no scenarios
executed, distinguishing this case from a normal PASS=0 FAIL=0 run.

**O2 — The BROKEN case must add the `needs-human` label to the PDCA tracking issue (#413).**

Any `TOTAL == 0` run represents a workflow infrastructure failure, not a test failure.
A human must investigate. The `needs-human` label on issue #413 is the mechanism.

**O3 — The `Post PDCA evidence` step must handle `TOTAL == 0` as an error state, not a neutral state.**

The existing `STATUS="ALL PASS"` / `STATUS="${FAIL_COUNT} FAILED"` logic does not capture
the case where TOTAL is zero. A zero-TOTAL result MUST produce a distinct status string
(`BROKEN — 0 scenarios executed`), not `ALL PASS` or `0 FAILED`.

**O4 — The BROKEN guard must fire in the `Post PDCA evidence to Issue #1` step, which runs `if: always()`.**

This step already runs even when upstream steps fail (it has `if: always()`). The guard
belongs there so it fires even if the `Run PDCA Scenarios` step crashes or is skipped.

**O5 — Normal runs with TOTAL > 0 must be unaffected.**

No change to the existing behavior when at least one scenario ran. The PASS/FAIL counts,
coverage percentage, and comment format for TOTAL > 0 must remain identical to pre-fix.

---

## Zone 2 — Implementer's judgment

- The `needs-human` label is added to issue #413 (the existing PDCA tracking issue), not issue #1.
  Issue #1 is the report surface; #413 is the PDCA tracking issue referenced at the bottom of
  every PDCA comment.
- The BROKEN comment should include: the run URL, trigger type, date, and the specific message
  that no scenarios executed before the workflow reached the scenario step.
- The check for TOTAL == 0 is placed at the top of the `Post PDCA evidence` step, before the
  normal PASS/FAIL logic, so it short-circuits cleanly.
- The `[PDCA BROKEN]` prefix is chosen to be machine-parseable by the SM anchor tracking
  that looks for `[ANCHOR | kardinal-promoter |` patterns — the BROKEN prefix is distinct
  and will not accidentally match anchor tracking.

---

## Zone 3 — Scoped out

- This spec does NOT add a retry mechanism for broken PDCA runs.
- This spec does NOT change the PDCA workflow's step ordering.
- This spec does NOT add alerting beyond Issue #1 comment + #413 label.
- This spec does NOT address the case where TOTAL > 0 but ALL scenarios are ⚠️ notes
  (that is a separate quality issue, not a BROKEN state).
