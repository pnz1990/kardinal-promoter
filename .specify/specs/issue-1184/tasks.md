# Tasks: issue-1184

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1184 && go build ./... 2>&1 | tail -3` — expected: PASSED (baseline)
- [CMD] `cd ../kardinal-promoter.issue-1184 && go vet ./... 2>&1 | tail -3` — expected: PASSED (baseline)

## Implementation
- [AI] Read spec Zone 1 obligations O1-O6 and implement minimum required changes
- [CMD] Create `scripts/gitops-promoter-gap-check.sh` following `scripts/kargo-gap-check.sh` pattern
- [CMD] Verify: `test -f scripts/gitops-promoter-gap-check.sh && head -3 scripts/gitops-promoter-gap-check.sh | grep -q '#!/usr/bin/env bash'`
- [CMD] Verify O2: `grep -q 'argoproj-labs/gitops-promoter' scripts/gitops-promoter-gap-check.sh`
- [CMD] Verify O3: `grep -qE '\-\-min-reactions|\-\-json' scripts/gitops-promoter-gap-check.sh`
- [CMD] Verify O6: `grep -q 'Copyright 2026 The kardinal-promoter Authors' scripts/gitops-promoter-gap-check.sh`
- [AI] Flip 🔲 to ✅ in docs/design/15-production-readiness.md for the GitOps Promoter gap item
- [CMD] Verify O5: `grep -q 'gitops-promoter-gap-check' docs/design/15-production-readiness.md`

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1184 && go build ./... 2>&1 | tail -3` — expected: PASSED
- [CMD] `cd ../kardinal-promoter.issue-1184 && go vet ./... 2>&1 | tail -3` — expected: PASSED
- [CMD] `bash -n scripts/gitops-promoter-gap-check.sh` — expected: no syntax errors
