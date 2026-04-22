# Tasks: issue-979 — Community presence

## Pre-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-979 && go build ./...` — expected: 0 exit

## Implementation
- [AI] Create `CONTRIBUTING.md` at repository root
- [CMD] `ls /home/runner/work/kardinal-promoter/kardinal-promoter.issue-979/CONTRIBUTING.md` — expected: file exists
- [AI] Add "Community" section to `docs/index.md`
- [CMD] `grep -c "Community" /home/runner/work/kardinal-promoter/kardinal-promoter.issue-979/docs/index.md` — expected: ≥1
- [AI] Update design doc (🔲 → ✅ with note about admin requirement for Discussions)
- [CMD] `grep -c "community presence" /home/runner/work/kardinal-promoter/kardinal-promoter.issue-979/docs/design/15-production-readiness.md` — expected: ≥1

## Post-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-979 && go build ./...` — expected: 0 exit
