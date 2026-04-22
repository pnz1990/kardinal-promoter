# Tasks: issue-1050 — Simulation Delta Feedback Loop (COORD reads ratio, adjusts session limit)

## Pre-implementation
- [CMD] `cd ../kardinal-promoter.issue-1050 && go build ./... 2>&1 | tail -3` — expected: no errors

## Implementation

### Part 1: SM §4f — write actual_prs_merged, predicted_prs_floor, and ratio_history to sim-prediction.json
- [AI] In `~/.otherness/agents/phases/sm.md`, after §4f computes VISION_PRS and reads prs_next_batch_floor (around line 3504 SIM_DELTA section), add a block that:
  1. Reads current `ratio_history` from `sim-prediction.json` (max 5 entries)
  2. Appends new entry: `{actual: VISION_PRS, floor: prs_next_batch_floor, ratio: actual/floor}`
  3. Trims to last 5 entries
  4. Writes `actual_prs_merged`, `predicted_prs_floor`, `ratio_history` back to `sim-prediction.json`
- [CMD] `grep -n "ratio_history" ~/.otherness/agents/phases/sm.md | head -5` — expected: lines found

### Part 2: COORD §1b-delta — read ratio_history and compute ADJUSTED_SESSION_LIMIT
- [AI] In `~/.otherness/agents/phases/coord.md`, after the `§1b-sim` section (around line 310), add new section `§1b-delta`:
  1. Read `ratio_history` from `_state:sim-prediction.json` (last 3 entries)
  2. Check if all 3 entries have ratio < 0.5 → set ADJUSTED_SESSION_LIMIT = max(1, SESSION_LIMIT - 2)
  3. Check if all 3 entries have ratio > 1.2 → set ADJUSTED_SESSION_LIMIT = min(10, SESSION_LIMIT + 1)
  4. Otherwise ADJUSTED_SESSION_LIMIT = SESSION_LIMIT
  5. Log the decision
- [CMD] `grep -n "ADJUSTED_SESSION_LIMIT\|1b-delta" ~/.otherness/agents/phases/coord.md | head -5` — expected: lines found

### Part 3: standalone.md §1f MULTI-ITEM CHECK — use ADJUSTED_SESSION_LIMIT
- [AI] In `~/.otherness/agents/standalone.md`, in the `§1f GATE — MULTI-ITEM CHECK` section, change the SESSION_LIMIT comparison to use ADJUSTED_SESSION_LIMIT if set
- [CMD] `grep -n "ADJUSTED_SESSION_LIMIT\|SESSION_LIMIT" ~/.otherness/agents/standalone.md | head -10` — expected: ADJUSTED_SESSION_LIMIT referenced

## Post-implementation
- [CMD] `cd ../kardinal-promoter.issue-1050 && go build ./... 2>&1 | tail -3` — expected: no errors
- [CMD] `cd ../kardinal-promoter.issue-1050 && go test ./... -race -count=1 -timeout 120s 2>&1 | tail -5` — expected: PASS
