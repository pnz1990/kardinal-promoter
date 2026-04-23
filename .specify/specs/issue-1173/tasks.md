# Tasks: issue-1173

## Pre-implementation
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1173 && go build ./... 2>&1 | tail -3` — expected: no output (success)
- [CMD] `ls ~/.otherness/agents/phases/coord.md` — expected: file exists

## Implementation
- [AI] Read spec Zone 1 obligations
- [AI] Locate the two `sorted(os.listdir(design_dir))` calls in coord.md §1c
- [AI] Replace the `PYEOF` display block to use priority-weighted round-robin ordering
- [AI] Replace the `ISSUE_GEN` block to use priority-weighted round-robin with doc-15 boost
- [CMD] Verify O1: `grep -q 'doc_priority' ~/.otherness/agents/phases/coord.md && echo PASS || echo FAIL`
- [CMD] Verify O2: `grep -q 'round-robin' ~/.otherness/agents/phases/coord.md && echo PASS || echo FAIL`

## Post-implementation
- [CMD] Verify design doc updated: `grep -q 'COORDINATOR alphabetic doc ordering.*✅\|✅.*COORDINATOR alphabetic doc ordering' /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1173/docs/design/12-autonomous-loop-discipline.md && echo PASS || echo FAIL`
- [CMD] `cd /home/runner/work/kardinal-promoter/kardinal-promoter.issue-1173 && go build ./... 2>&1 | tail -3` — expected: success
