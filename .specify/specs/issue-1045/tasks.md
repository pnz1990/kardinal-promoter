# Tasks: issue-1045 — SM PDCA workflow result check

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1045 && go build ./... 2>&1 | head -5` — expected: 0 exit (clean build)

## Implementation
- [AI] Insert PDCA_STATUS check into ~/.otherness/agents/phases/sm.md §4f, immediately after `[ "$CI_STATUS" = "failure" ] && HEALTH="AMBER"` and before NEEDS_HUMAN_COUNT. Set HEALTH=RED on failure. Fail-open if workflow not found.
- [CMD] `grep -n "PDCA_STATUS" ~/.otherness/agents/phases/sm.md` — expected: at least 3 matching lines (variable assignment, condition check, log line)
- [AI] Add `pdca_status` write to state.json in SM §4f so COORD can read it next session.
- [CMD] `grep -c "pdca_status" ~/.otherness/agents/phases/sm.md` — expected: ≥1
- [AI] Open dedup-guarded [PDCA FAILING] issue block after PDCA check.
- [CMD] `grep -n "PDCA FAILING" ~/.otherness/agents/phases/sm.md` — expected: at least 1 line
- [AI] Update design doc 12 in worktree: move 🔲 item to ✅ Present.
- [CMD] `grep -c "✅.*PDCA workflow result" ../kardinal-promoter.issue-1045/docs/design/12-autonomous-loop-discipline.md` — expected: 1

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1045 && go build ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-1045 && go test ./... -race -count=1 -timeout 120s 2>&1 | tail -10` — expected: ok / PASS
