# Tasks: issue-1079 — SM health state definition

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1079 && go build ./...` — expected: exit 0

## Implementation
- [AI] Create docs/aide/health-thresholds.md with GREEN/AMBER/RED/STALL definitions
- [CMD] `ls ../kardinal-promoter.issue-1079/docs/aide/health-thresholds.md` — expected: file exists
- [AI] Update docs/design/12-autonomous-loop-discipline.md (🔲 → ✅)
- [CMD] `grep "health-thresholds" ../kardinal-promoter.issue-1079/docs/design/12-autonomous-loop-discipline.md` — expected: reference present

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1079 && go build ./...` — expected: exit 0
- [CMD] `cd ../kardinal-promoter.issue-1079 && go vet ./...` — expected: exit 0
