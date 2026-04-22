# Tasks: issue-1078 — Otherness onboarding quality gate

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1078 && go build ./...` — expected: exit 0

## Implementation
- [AI] Create scripts/onboard-smoke-test.sh with the 4 validation checks
- [CMD] `bash -n ../kardinal-promoter.issue-1078/scripts/onboard-smoke-test.sh` — expected: exit 0
- [AI] Add "Verify your setup" section to docs/quickstart.md
- [CMD] `grep -c "onboard-smoke-test" ../kardinal-promoter.issue-1078/docs/quickstart.md` — expected: >= 1

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1078 && go build ./...` — expected: exit 0
- [CMD] `cd ../kardinal-promoter.issue-1078 && go vet ./...` — expected: exit 0
