# Spec: issue-1163

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: Metrics collected but not acted on: SM must read `docs/aide/metrics.md` and change queue priority (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: A script `scripts/sm-metrics-trend.sh` exists that reads `docs/aide/metrics.md` and computes trends.
Verification: `test -f scripts/sm-metrics-trend.sh`

**O2**: The script reads the last N rows (configurable, default 5) of the metrics table.
Verification: `bash scripts/sm-metrics-trend.sh --rows 3` parses 3 rows without error.

**O3**: The script outputs `[METRICS ALERT: delivery declining]` when PRs merged < 2 for 3 consecutive rows.
Verification: Unit test with synthetic data confirms alert fires correctly.

**O4**: The script outputs `[METRICS TREND: test coverage flat]` when test count does not grow over 3 rows (within ±5 tests).
Verification: Unit test confirms flat-test-count detection.

**O5**: The script outputs `[METRICS ALERT: CI instability]` when CI column shows failure in 2+ of last 3 rows.
Verification: Unit test confirms CI failure detection.

**O6**: The script exits 0 even when no alerts fire (informational output only).
Verification: `bash scripts/sm-metrics-trend.sh && echo "OK"` outputs "OK" on clean metrics.

**O7**: The script exits 0 and logs a warning when `docs/aide/metrics.md` is not found (fail-open).
Verification: `bash scripts/sm-metrics-trend.sh --metrics-file /nonexistent && echo "OK"` outputs "OK".

**O8**: `sm.md §4b-metrics-trend` in `~/.otherness/agents/phases/sm.md` calls this script or contains equivalent inline Python after this PR.
Verification: The sm.md change is applied in the otherness fork (tracked separately in `feat/issue-1163-sm-metrics-trend` branch).

---

## Zone 2 — Implementer's judgment

- The script uses Python for reliable YAML table parsing (same approach as existing scripts).
- "Flat" test count is defined as max-min ≤ 5 over the window (not strict zero change, to avoid noise).
- The `--rows` flag defaults to 5 (last 5 batches = approximately one week of 1h runs × 24h).
- The script posts nothing to GitHub — it only prints to stdout. The SM phase calls it and decides what to post (separation of concerns).
- The script is idempotent and safe to run multiple times.

---

## Zone 3 — Scoped out

- Automatic COORDINATOR queue reprioritization (the SM posts the alert; COORDINATOR reads it on next cycle — this is the separation of concerns by design)
- Trend analysis for needs-human column (currently the SM already tracks this via health_check)
- Historical trend beyond 5 rows (sufficient signal for week-over-week detection)
- Integration with external metrics systems (Prometheus, DataDog) — future work
