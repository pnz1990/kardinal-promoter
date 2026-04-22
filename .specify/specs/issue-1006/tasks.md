# Tasks: issue-1006 — feat(pm): §5j comparison doc accuracy — flip rows when gaps are closed

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1006 && go build ./...` — expected: 0 exit (no Go changes, but verify clean build)

## Implementation

- [AI] Create `docs/design/41-published-docs-freshness.md` with §Present (empty), §Future containing this item as 🔲, and §41.4 description.
- [CMD] `ls ../kardinal-promoter.issue-1006/docs/design/41-published-docs-freshness.md` — expected: file exists

- [AI] Add §5n "Comparison doc accuracy check" section to `~/.otherness/agents/phases/pm.md` after §5m, implementing the comparison ❌ row scan logic.
- [CMD] `grep -n "5n" ~/.otherness/agents/phases/pm.md` — expected: section header found

- [AI] Write `../kardinal-promoter.issue-1006/.specify/specs/issue-1006/test_comparison_check.py` unit test for the parsing logic.
- [CMD] `cd ../kardinal-promoter.issue-1006 && python3 .specify/specs/issue-1006/test_comparison_check.py` — expected: all tests pass

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1006 && go build ./...` — expected: 0 exit
- [CMD] `cd ../kardinal-promoter.issue-1006 && go test ./... -race -count=1 -timeout 120s 2>&1 | tail -5` — expected: ok or PASS
