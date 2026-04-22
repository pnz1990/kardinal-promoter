# Spec: Zero-PR session detection

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: Zero-PR session detection: agent ran but produced no mergeable content (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

1. **O1 — Script exists and is executable**: `scripts/zero-pr-detect.sh` exists, is `chmod +x`,
   and runs without error.
   *Violation*: script missing, not executable, or exits non-zero on valid input.

2. **O2 — Detects zero-PR batch**: when `gh pr list --merged --search "created:>1h ago"` returns
   0 items, the script emits `[SESSION DRY RUN — agent ran but shipped 0 PRs]` and posts it to
   REPORT_ISSUE.
   *Violation*: zero-PR batch is not detected or comment is not posted.

3. **O3 — Tracks consecutive dry runs**: the script reads/writes `dry_run_count` to
   `_state:.otherness/dry-run-state.json`. It increments on each dry-run batch, resets on any
   non-zero batch.
   *Violation*: count is not persisted; reset does not happen when PRs are shipped.

4. **O4 — Escalates after 3 consecutive dry runs**: when `dry_run_count >= 3`, the script posts
   `[NEEDS HUMAN: 3+ consecutive dry runs]` to REPORT_ISSUE and opens a `needs-human` issue.
   *Violation*: no escalation after 3+ consecutive batches.

5. **O5 — Fail-safe**: when `gh` is unavailable or _state branch cannot be reached, the script
   exits 0 and emits a `[ZERO-PR DETECT SKIPPED — <reason>]` message.
   *Violation*: script exits non-zero on infrastructure failure.

6. **O6 — Idempotent**: running the script twice in the same batch does not post duplicate
   comments. It uses a dedup guard (check if comment was already posted in last N comments).
   *Violation*: duplicate `[SESSION DRY RUN]` comments for the same batch.

---

## Zone 2 — Implementer's judgment

- How "1h ago" is computed (exact UTC, or approximate)
- The format of the dry-run-state.json schema
- Whether the dedup guard checks the last 5 or 10 comments
- Whether the script is invoked from SM or as a standalone cron

---

## Zone 3 — Scoped out

- Detecting the root cause of the dry run (queue empty? CI red? PR rejected?)
- Automatically recovering from dry runs
- Integration with the simulation calibration
- Housekeeping-only PR detection (that is a separate feature: loop honesty signal)
