# Tasks: issue-1188

## Pre-implementation
- [CMD] Verify bug exists: `grep -c 'dry-run-state.json' scripts/zero-pr-detect.sh` — see 2+ checkout lines with only dry-run-state.json

## Implementation
- [AI] In the reset block (lines ~100): add `.otherness/state.json` to checkout and add commands
- [AI] In the increment block (lines ~187): same change
- [AI] Verify only dry-run-state.json content is modified; state.json is preserved unchanged

## Post-implementation
- [CMD] `grep -c "checkout.*state.json" scripts/zero-pr-detect.sh` — expected: ≥2
- [CMD] `grep -c "add.*state.json" scripts/zero-pr-detect.sh` — expected: ≥2
- [CMD] `bash -n scripts/zero-pr-detect.sh` — expected: no syntax errors
