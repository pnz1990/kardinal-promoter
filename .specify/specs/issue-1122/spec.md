# Spec: issue-1122 — kardinal logs --follow streaming mode

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — kardinal logs has no --follow / streaming mode`
- **Implements**: kardinal logs --follow streaming mode (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable, must satisfy)

1. **O1 — Flag exists**: `kardinal logs <pipeline>` accepts a `--follow` (`-f`) boolean flag.
   The existing static output is unchanged when `--follow` is not set.

2. **O2 — Polling loop**: When `--follow` is set, the command polls every 2 seconds,
   re-fetching the PromotionStep list and re-rendering new `status.steps[]` entries.

3. **O3 — Incremental output**: Only newly-appeared step entries are printed on each poll
   cycle (identified by a cursor tracking the last known step count per PromotionStep).
   Previously printed lines are not reprinted.

4. **O4 — Terminal state exit**: The follow loop exits when all filtered PromotionSteps
   are in a terminal state: Verified, Failed, Superseded, AbortedByAlarm, or RollingBack
   terminal. Steps in Promoting, WaitingForMerge, HealthChecking, or Pending are non-terminal.

5. **O5 — Signal handling**: The follow loop exits cleanly on SIGINT (Ctrl+C) with no error.

6. **O6 — Test coverage**: Tests in `cmd/kardinal/cmd/logs_test.go` (or new file) verify
   that `--follow` is registered and that the incremental logic correctly identifies new steps.

---

## Zone 2 — Implementer's judgment

- Polling interval: 2 seconds (matches design doc description)
- Terminal states: Verified, Failed, Superseded, AbortedByAlarm, RollingBack (when complete)
- Cursor implementation: map[stepName]int tracking len(s.Status.Steps) per step
- SIGINT handling: `context.WithCancel` + `signal.NotifyContext` pattern

---

## Zone 3 — Scoped out

- Does NOT implement watch semantics (kubectl watch) — polling is sufficient per spec
- Does NOT stream the raw controller logs
- Does NOT handle `--bundle` + `--follow` combined edge cases with supersession
