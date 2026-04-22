# Tasks: issue-1046 — Single-page health dashboard

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1046 && go build ./... 2>&1 | head -3` — expected: clean build

## Implementation
- [AI] Add `§4f-health-snapshot` step to sm.md, after the health computation block and before the batch report post. Find existing sentinel comment on REPORT_ISSUE; edit if found, create if not.
- [CMD] `grep -n "health-snapshot\|HEALTH SNAPSHOT\|otherness-health-snapshot" ~/.otherness/agents/phases/sm.md` — expected: at least 3 lines
- [AI] Update design doc 13: move 🔲 Single-page health dashboard to ✅ Present.
- [CMD] `grep -c "✅.*Single-page health dashboard" ../kardinal-promoter.issue-1046/docs/design/13-scheduled-execution.md` — expected: 1

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1046 && go build ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-1046 && go test ./... -race -count=1 -timeout 120s 2>&1 | tail -5` — expected: PASS
