# Tasks: issue-1005 — PM §5j version staleness check

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1005 && go build ./...` — expected: exit 0

## Implementation
- [AI] Create `scripts/version-staleness-check.sh` with Apache 2.0 header, version
  extraction, README/comparison.md scanning, staleness detection, issue creation with dedup.
- [CMD] `cd ../kardinal-promoter.issue-1005 && bash -n scripts/version-staleness-check.sh` — expected: exit 0 (bash syntax valid)
- [AI] Update `docs/design/41-published-docs-freshness.md` — move version string freshness
  🔲 Future item to ✅ Present with PR reference placeholder.
- [CMD] `cd ../kardinal-promoter.issue-1005 && git diff --name-only HEAD` — expected: shows version-staleness-check.sh and doc changes

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1005 && go build ./...` — expected: exit 0
- [CMD] `cd ../kardinal-promoter.issue-1005 && go test ./... -race -count=1 -timeout 120s` — expected: all pass
- [CMD] `cd ../kardinal-promoter.issue-1005 && go vet ./...` — expected: exit 0
