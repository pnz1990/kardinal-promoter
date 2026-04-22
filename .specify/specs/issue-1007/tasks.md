# Tasks: issue-1007 — feat(qa): §3b docs gate for user-visible features

## Pre-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1007 && go build ./...` — expected: 0 exit

## Implementation

- [AI] Add §41.5 to docs/design/41-published-docs-freshness.md Future section.
- [CMD] `grep -n "41.5" /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1007/docs/design/41-published-docs-freshness.md` — expected: line found

- [AI] Add docs gate step to qa.md §3b in ~/.otherness/agents/phases/qa.md.
- [CMD] `grep -n "docs gate\|user-visible\|41.5" ~/.otherness/agents/phases/qa.md` — expected: section found

- [AI] Write test_docs_gate.py with ≥6 tests.
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1007 && python3 .specify/specs/issue-1007/test_docs_gate.py` — expected: all tests pass

## Post-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1007 && go build ./...` — expected: 0 exit
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1007 && go test ./... -race -count=1 -timeout 120s 2>&1 | tail -5` — expected: ok or PASS
