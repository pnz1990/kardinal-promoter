# Tasks: issue-1134 — QA docs gate

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1134 && go build ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-1134 && go vet ./...` — expected: 0 exit

## Implementation
- [AI] Create `scripts/qa-docs-gate.sh` with copyright header, PR number input, gh PR diff reading
- [CMD] `cd ../kardinal-promoter.issue-1134 && bash -n scripts/qa-docs-gate.sh` — expected: 0 exit (no syntax errors)
- [AI] Add Future-to-Present detection logic (regex on diff lines)
- [AI] Add user-visible feature classification (keyword matching)
- [AI] Add docs/ update check and Layer 1 exemption
- [AI] Add WRONG/PASS output and exit codes
- [AI] Update `docs/design/41-published-docs-freshness.md` to move QA docs gate from 🔲 to ✅
- [CMD] `cd ../kardinal-promoter.issue-1134 && bash scripts/qa-docs-gate.sh 2>&1 | head -3` — expected: skip message (no PR_NUM)
- [CMD] `cd ../kardinal-promoter.issue-1134 && echo 'no pr' | bash -c 'PR_NUM="" bash scripts/qa-docs-gate.sh'` — expected: exit 0

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1134 && go build ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-1134 && go vet ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-1134 && bash -n scripts/qa-docs-gate.sh` — expected: 0 exit
