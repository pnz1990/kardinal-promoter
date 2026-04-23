# Spec: issue-1171

## Design reference
- **Design doc**: `docs/design/12-autonomous-loop-discipline.md`
- **Section**: `§ Future`
- **Implements**: Session token exhaustion is invisible: no detection when agent exits mid-queue due to budget (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: `scripts/session-complete-marker.sh` exists and is executable, accepts `write` and `check` subcommands.
Verify: `test -x scripts/session-complete-marker.sh && bash -n scripts/session-complete-marker.sh`

**O2**: `write` subcommand writes `session_end.status=COMPLETE` to `.otherness/state.json` on the `_state` branch.
Verify: run `write`, then `git show origin/_state:.otherness/state.json | python3 -c "import json,sys; s=json.load(sys.stdin); print(s['session_end']['status'])"` returns `COMPLETE`.

**O3**: `check` subcommand detects when the prior session did not write a COMPLETE marker and posts `[SESSION TRUNCATED]` to the report issue.
Verify: inject a state.json with `session_end.session_id=prior-sess` and no COMPLETE status, then run `check current-sess` → output contains `SESSION TRUNCATED`.

**O4**: Both subcommands exit 0 (fail-safe) when `_state` branch does not exist.
Verify: run in a repo with no `_state` branch → script prints `SKIPPED` and returns 0.

**O5**: Script has Apache 2.0 license header.
Verify: `head -3 scripts/session-complete-marker.sh | grep -q 'Apache License'`

## Zone 2 — Implementer's judgment

- Use a git worktree (not clone) for _state writes — consistent with standalone.md pattern
- Retry loop on push conflict (3 attempts) — consistent with state writer in standalone.md
- Post truncation warning to GitHub Issue via `gh issue comment` — fail-open if gh unavailable
- Check mode reads _state without writing — safe for concurrent reads

## Zone 3 — Scoped out

- Integration with standalone.md `write`/`check` call sites (requires modifying standalone.md, which is CRITICAL tier and out of scope for this session's CODE zone)
- Automatic detection of which items were in-flight at truncation time
- Retry logic for the `check` subcommand (read-only, failure is non-blocking)
