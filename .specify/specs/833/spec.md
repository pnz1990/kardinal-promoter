# Spec: chore(ci): reduce scheduled cadence to 6h (issue #833)

## Design reference
- N/A — infrastructure change with no user-visible behavior

## Zone 1 — Obligations

- O1: `.github/workflows/otherness-scheduled.yml` cron schedule changes from `0 * * * *` (hourly) to `0 */6 * * *` (every 6 hours).
- O2: The locked-cron comment block (lines 4-8) is updated to reflect the new steady-state cadence and remove the "DO NOT CHANGE" warning.
- O3: No functional behavior changes — only the schedule frequency changes.

## Zone 2 — Implementer's judgment

- The comment update should explain WHY the cadence is now 6h (project in steady-state, all journeys passing, queue empty).

## Zone 3 — Scoped out

- otherness-config.yaml does not have a cron field — no change needed there.
- No logic changes to the agent itself.
