# Spec: issue-1073 — Simulation queue-adjustment visibility

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future — Simulation queue-adjustment is invisible to humans`
- **Implements**: Simulation queue-adjustment visibility — post notification when session limit is adjusted (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable, must satisfy)

1. **O1 — Script exists**: `scripts/sim-adjustment-notify.sh` is present, executable, and
   has the Apache 2.0 copyright header.

2. **O2 — Ratio history read**: The script reads `ratio_history` from
   `.otherness/state.json` (the last 3 entries, matching the COORD §1b-delta pattern).
   If state.json is absent or ratio_history is empty, the script exits 0 (fail-open).

3. **O3 — Session limit comparison**: The script compares `adjusted_session_limit` (from
   state.json or env) against `default_session_limit` (from `otherness-config.yaml`
   `session_item_limit` field). If they differ, a notification is posted.

4. **O4 — Notification format**: When an adjustment is detected, the script posts to
   REPORT_ISSUE:
   `[SIM ADJUSTMENT — queuing N items (default M): last 3 ratio_history: X/Y/Z]`
   This is exactly the format specified in the design doc Future item.

5. **O5 — Dedup guard**: Does not post the same adjustment notification twice for the
   same batch (checks if an identical comment exists on REPORT_ISSUE before posting).

6. **O6 — Fail-open**: Any error (gh unavailable, state unreadable, missing fields)
   causes the script to exit 0 with a skip message.

7. **O7 — Idempotent**: Running the script multiple times in the same batch without a
   limit change does not create duplicate comments.

---

## Zone 2 — Implementer's judgment

- The `adjusted_session_limit` may not be in state.json yet (it's set by COORD §1b-delta
  in the otherness agents). The script reads it from state.json but also accepts it as
  an environment variable `ADJUSTED_SESSION_LIMIT` for direct testing.
- `default_session_limit` is read from `otherness-config.yaml` `session_item_limit` field.
- If `adjusted_session_limit` == `default_session_limit` or `adjusted_session_limit` is
  absent: no notification needed.

---

## Zone 3 — Scoped out

- Does NOT modify coord.md (CRITICAL-A tier, would require self-review protocol)
- Does NOT modify the COORD §1b-delta logic itself
- Does NOT modify any Go code
- The actual COORD §1b-delta adjustment logic remains in the otherness agents
