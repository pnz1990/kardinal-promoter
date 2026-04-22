# Tasks: issue-1049 — PDCA regression attribution + COORDINATOR halt gate

## Pre-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1049 && go build ./... 2>&1 | tail -3` — expected: 0 exit

## Implementation

- [AI] Add SM §4f-regression section to ~/.otherness/agents/phases/sm.md between §4e and §4f
- [CMD] `grep -n "4f-regression\|PDCA REGRESSION" ~/.otherness/agents/phases/sm.md | head -5` — expected: contains "4f-regression"
- [AI] Add COORD §1c-pdca-gate to ~/.otherness/agents/phases/coord.md before TODO_COUNT check
- [CMD] `grep -n "pdca-gate\|pdca_status" ~/.otherness/agents/phases/coord.md | head -5` — expected: contains "1c-pdca-gate"
- [AI] Add COORD §1e-pdca-gate to ~/.otherness/agents/phases/coord.md in §1e before ITEM_ID assignment
- [CMD] `grep -n "1e-pdca-gate" ~/.otherness/agents/phases/coord.md | head -3` — expected: contains "1e-pdca-gate"
- [AI] Update docs/design/12-autonomous-loop-discipline.md: move 🔲 items to ✅ Present

## Post-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1049 && go build ./... 2>&1 | tail -3` — expected: 0 exit
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1049 && go test ./... -count=1 -timeout 60s 2>&1 | tail -5` — expected: PASS or ok
