# Tasks: issue-1051 — Skills Inventory Proactive Discovery

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1051 && go build ./... 2>&1 | tail -3` — expected: no errors

## Implementation

### Part 1: Create docs/aide/skills-inventory.md
- [AI] Create `docs/aide/skills-inventory.md` with a markdown table listing all skills in `~/.otherness/agents/skills/` (excluding PROVENANCE.md, README.md). Each row: Name | File | Keywords | Description
- [CMD] `test -f ../kardinal-promoter.issue-1051/docs/aide/skills-inventory.md && echo FOUND || echo MISSING` — expected: FOUND
- [CMD] `grep -c "^|" ../kardinal-promoter.issue-1051/docs/aide/skills-inventory.md` — expected: ≥10 (one header row + 9+ skill rows)

### Part 2: COORD §1b-skills — read inventory and emit suggestions
- [AI] In `~/.otherness/agents/phases/coord.md`, add new section `§1b-skills` after `§1b-delta`:
  1. Read `docs/aide/skills-inventory.md` — fail-open if absent
  2. Read top 5 todo items from state.json titles
  3. For each skill row: if any keyword matches any item title → emit `[SKILL SUGGESTED: <name> — relevant to <area>]`
  4. Limit to 5 suggestions
- [CMD] `grep -n "1b-skills\|SKILL SUGGESTED" ~/.otherness/agents/phases/coord.md | head -5` — expected: lines found

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1051 && go build ./... 2>&1 | tail -3` — expected: no errors
- [CMD] `cd ../kardinal-promoter.issue-1051 && go test ./... -race -count=1 -timeout 60s 2>&1 | tail -5` — expected: ok/PASS
