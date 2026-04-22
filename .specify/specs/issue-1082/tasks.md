# Tasks: issue-1082 — Onboarding time-to-first-run metric

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1082 && go build ./...` — expected: exit 0

## Implementation
- [AI] Create `scripts/onboarding-ttfr.sh` with Apache 2.0 header, timestamp recording,
  computation, and SM report output.
- [CMD] `cd ../kardinal-promoter.issue-1082 && bash -n scripts/onboarding-ttfr.sh` — expected: exit 0 (syntax valid)
- [AI] Update `docs/design/12-autonomous-loop-discipline.md` — move 🔲 Future item to ✅ Present.
- [CMD] `cd ../kardinal-promoter.issue-1082 && git diff --name-only HEAD` — expected: shows script and doc changes

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1082 && go build ./...` — expected: exit 0
- [CMD] `cd ../kardinal-promoter.issue-1082 && go test ./... -race -count=1 -timeout 120s` — expected: all pass
- [CMD] `cd ../kardinal-promoter.issue-1082 && go vet ./...` — expected: exit 0
