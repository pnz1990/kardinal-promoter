# Spec: issue-1082 — Onboarding time-to-first-run metric

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future — Onboarding time-to-first-run metric`
- **Implements**: Onboarding time-to-first-run metric: track and publish the setup duration (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable, must satisfy)

1. **O1 — Script exists**: `scripts/onboarding-ttfr.sh` is present, executable, and has
   the Apache 2.0 copyright header.

2. **O2 — onboard_started_at recording**: The script writes `onboard_started_at` (ISO 8601
   UTC timestamp) to `otherness-config.yaml` under an `onboarding:` section if not already
   set. Idempotent: a second call does not overwrite an existing timestamp.

3. **O3 — first_run_succeeded_at recording**: The script, when called with `--mark-success`,
   writes `first_run_succeeded_at` (ISO 8601 UTC timestamp) to `otherness-config.yaml` under
   the `onboarding:` section if not already set. Idempotent.

4. **O4 — time_to_first_run computation**: When both timestamps are present, the script
   computes the difference in minutes and prints `time_to_first_run: <N>min`.

5. **O5 — SM batch report integration**: When called with `--sm-report`, the script
   outputs the `time_to_first_run: <N>min` line (or a "not yet measured" placeholder)
   suitable for inclusion in SM batch reports.

6. **O6 — Fail-open**: Any error exits 0 with a skip message. Never blocks the SM.

7. **O7 — Only reports in first 3 batches**: The script reads `batch_count` from
   `.otherness/state.json` and only outputs `time_to_first_run` when `batch_count <= 3`.
   After that, it outputs nothing (metric has served its purpose).

---

## Zone 2 — Implementer's judgment

- Format for the timestamps in `otherness-config.yaml`: use YAML comments or a dedicated
  `onboarding:` section. Adding a structured section is cleaner.
- The script should be safe to call from the SM batch report without crashing.
- The `--mark-success` flag should be called once by the SM when `batch_count == 1`
  (first successful run).

---

## Zone 3 — Scoped out

- Does NOT modify any agent phase files (sm.md, coord.md, etc.)
- Does NOT require changes to Go code
- The onboard_started_at timestamp should be set by calling the script from the
  onboarding flow (otherness.onboard.md) — out of scope for this item
  (this item implements the script and the SM batch report integration)
