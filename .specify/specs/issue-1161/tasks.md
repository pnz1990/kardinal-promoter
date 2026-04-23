# Tasks: issue-1161

## Pre-implementation
- [CMD] `go build ./... 2>&1 | tail -3` — expected: no errors (baseline)
- [CMD] `go vet ./... 2>&1 | tail -3` — expected: no errors (baseline)

## Implementation
- [AI] Read ci.yml to find where to insert the new step
- [AI] Write Python script logic to extract run: blocks from otherness-scheduled.yml and run bash -n
- [CMD] Add new step to ci.yml in the appropriate job
- [CMD] Test the script manually against otherness-scheduled.yml (expect 0 exit code)
- [AI] Update docs/design/12-autonomous-loop-discipline.md (🔲 → ✅)

## Post-implementation
- [CMD] `go build ./... 2>&1 | tail -3` — expected: no errors
- [CMD] `go test ./... -race -count=1 -timeout 120s 2>&1 | tail -5` — expected: PASS
- [CMD] `go vet ./... 2>&1 | tail -3` — expected: no errors
