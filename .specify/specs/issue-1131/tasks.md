# Tasks: issue-1131 — Automated Kargo community issue monitoring

## Pre-implementation
- [CMD] `gh api repos/akuity/kargo/issues --jq 'length'` — expected: >0 (API accessible)

## Implementation
- [AI] Write scripts/kargo-gap-check.sh with the cross-reference logic
- [CMD] `chmod +x /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1131/scripts/kargo-gap-check.sh && bash /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1131/scripts/kargo-gap-check.sh` — expected: exit 0

## Post-implementation
- [CMD] `bash /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1131/scripts/kargo-gap-check.sh 2>&1 | head -5` — expected: script runs and produces output
