# SM Health State Thresholds

> Machine-checkable thresholds for the autonomous loop health signal.
> Referenced by: `docs/design/12-autonomous-loop-discipline.md §Present`
> Used by: SM `§4b` batch report generation
>
> **Note**: `docs/aide/team.yml` is agent-immutable (per AGENTS.md). These thresholds
> live here and are referenced by the SM agent file. The SM enforces them in `§4b`.

---

## Thresholds

### GREEN — Loop is healthy and productive

```yaml
state: GREEN
condition: >
  At least 1 PR with type feat/fix/test/docs was merged in the current session
  AND the last CI run on main branch did not fail.
checkable_command: |
  MEANINGFUL=$(gh pr list --repo $REPO --state merged \
    --search "merged:>$(date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-2H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null)" \
    --json title --jq '[.[] | select(.title | test("^(feat|fix|test|docs)\\b"))] | length' 2>/dev/null || echo "0")
  CI_RED=$(gh run list --repo $REPO --branch main --limit 1 \
    --json conclusion --jq 'if .[0].conclusion == "failure" then "YES" else "NO" end' 2>/dev/null || echo "NO")
  [ "$MEANINGFUL" -gt "0" ] && [ "$CI_RED" = "NO" ]
consequence: No action required. Log GREEN in batch report.
```

### AMBER — Loop running but not productive

```yaml
state: AMBER
condition: >
  Any of:
  - 0 feat/fix/test/docs PRs merged in this session (session produced no vision work)
  - At least 1 open issue labeled needs-human
  - needs-human issues exist but no agent-blocking CI failure
checkable_command: |
  MEANINGFUL=$(gh pr list --repo $REPO --state merged \
    --search "merged:>$(date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-2H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null)" \
    --json title --jq '[.[] | select(.title | test("^(feat|fix|test|docs)\\b"))] | length' 2>/dev/null || echo "0")
  NEEDS_HUMAN=$(gh issue list --repo $REPO --state open --label needs-human \
    --json number --jq 'length' 2>/dev/null || echo "0")
  [ "$MEANINGFUL" -eq "0" ] || [ "$NEEDS_HUMAN" -gt "0" ]
consequence: >
  SM posts AMBER signal. COORDINATOR queues at least one feat item next batch.
  If needs-human count > 3, post escalation comment.
```

### RED — Loop is broken (no agent work possible)

```yaml
state: RED
condition: >
  Any of:
  - Last CI run on main branch has conclusion=failure (blocking all merges)
  - The scheduled workflow itself failed to run (no run in >12h)
checkable_command: |
  CI_RED=$(gh run list --repo $REPO --branch main --limit 1 \
    --json conclusion --jq 'if .[0].conclusion == "failure" then "YES" else "NO" end' 2>/dev/null || echo "NO")
  LAST_SCHEDULED=$(gh run list --repo $REPO \
    --workflow otherness-scheduled.yml --limit 1 \
    --json createdAt --jq '.[0].createdAt' 2>/dev/null || echo "")
  HOURS_SINCE=$(python3 -c "
  import datetime, sys
  t = '$LAST_SCHEDULED'.strip()
  if not t or t == 'null': print(999); exit()
  dt = datetime.datetime.fromisoformat(t.replace('Z', '+00:00'))
  print(int((datetime.datetime.now(datetime.timezone.utc) - dt).total_seconds() / 3600))
  " 2>/dev/null || echo "0")
  [ "$CI_RED" = "YES" ] || [ "${HOURS_SINCE:-0}" -gt 12 ]
consequence: >
  SM posts RED signal and opens/updates [NEEDS HUMAN] issue.
  COORDINATOR does NOT generate new queue items until RED is resolved.
  Human must fix the blocking CI failure before the loop can resume.
```

### STALL — Loop running but systematically under-delivering

```yaml
state: STALL
condition: >
  3 or more consecutive sessions where the only merged PRs touched exclusively:
    - .otherness/ files
    - docs/aide/ files
    - .github/state/ files
    - Vision/queue/metrics docs (vision(auto):, chore(coord):, state:)
  (i.e., no feat/fix/test/docs PRs that touch pkg/, cmd/, web/src/, chart/, scripts/, docs/ outside docs/aide/)
checkable_command: |
  # Check state.json chore_only_guard_count
  STALL_COUNT=$(python3 -c "
  import json
  try:
      s = json.load(open('.otherness/state.json'))
      print(s.get('chore_only_guard_count', 0))
  except: print(0)
  " 2>/dev/null || echo "0")
  [ "${STALL_COUNT:-0}" -ge 3 ]
consequence: >
  SM detects STALL and posts [STALL DETECTED — N consecutive housekeeping-only sessions].
  COORDINATOR must queue a feat item from docs/design/15-production-readiness.md
  (NOT from meta-system docs 12/13) in the next batch.
  If STALL persists >5 sessions: post [NEEDS HUMAN: chronic STALL].
```

---

## Priority ordering

When multiple states are detected simultaneously, the highest-severity state wins:

```
RED > STALL > AMBER > GREEN
```

---

## State tracking in state.json

The SM writes these fields to `state.json` after each batch:

| Field | Type | Description |
|---|---|---|
| `last_health_state` | string | Last computed health state (GREEN/AMBER/RED/STALL) |
| `chore_only_guard_count` | int | Consecutive sessions with 0 substantive PRs |
| `last_meaningful_pr_at` | ISO8601 | Timestamp of last feat/fix/test/docs PR merge |
| `consecutive_red_runs` | int | Consecutive RED states |

Reset rules:
- `chore_only_guard_count` resets to 0 when a substantive PR merges
- `consecutive_red_runs` resets to 0 when GREEN state is reached

---

## STALL detection: substantive PR definition

A PR is **substantive** if it changes at least one file matching:
- `pkg/**`
- `cmd/**`
- `web/src/**`
- `chart/**`
- `scripts/**` (excluding `scripts/metrics-*`)
- `docs/**` (excluding `docs/aide/**`)
- `api/**`
- `config/**`

A PR is **non-substantive** (housekeeping) if ALL changed files are in:
- `.otherness/**`
- `docs/aide/**`
- `.github/workflows/` (workflow-only changes)
- titles starting with: `vision(auto):`, `chore(coord):`, `state:`, `fix(workflow):`
