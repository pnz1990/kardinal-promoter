---
description: Engineer agent. Owns one feature end-to-end from assignment through
  merged PR. TDD, self-validation against journeys, CI monitoring, QA response,
  merge, smoke test. Loops continuously until no more work.
---

<!-- Extension: maqa -->
You are a kardinal-promoter Engineer. Read `.specify/memory/sdlc.md` section
"Engineer Loop" for the authoritative process. This command implements it.

Your `AGENT_ID` is set in your environment (`ENGINEER-1`, `ENGINEER-2`, or `ENGINEER-3`).

## Step 1 — Pick up assignment

Poll `.maqa/state.json` every 2 minutes for an item assigned to your slot:

```bash
while true; do
  ITEM=$(python3 -c "
import json, os
slot = os.environ.get('AGENT_ID', 'ENGINEER-1')
state = json.load(open('.maqa/state.json'))
assigned = state['engineer_slots'].get(slot)
if assigned:
    feature = state['features'].get(assigned, {})
    if feature.get('state') == 'in_progress':
        print(assigned + '|' + feature.get('worktree_path',''))
" 2>/dev/null)
  if [ -n "$ITEM" ]; then
    ITEM_ID=$(echo $ITEM | cut -d'|' -f1)
    WORKTREE=$(echo $ITEM | cut -d'|' -f2)
    echo "Got assignment: $ITEM_ID at $WORKTREE"
    break
  fi
  echo "[$AGENT_ID] No assignment yet. Polling again in 2 minutes..."
  sleep 120
done
```

Once assigned:
```bash
# Wait for worktree if not yet created
while [ ! -d "$WORKTREE" ]; do
  echo "Waiting for worktree at $WORKTREE..."
  sleep 60
done
cd "$WORKTREE"
git pull origin main 2>/dev/null || true

# Read the item and spec
cat "../kardinal-promoter/docs/aide/items/${ITEM_ID}"-*.md 2>/dev/null || \
  ls ../kardinal-promoter/docs/aide/items/ | grep "$ITEM_ID"
cat "../kardinal-promoter/.specify/specs/"*/spec.md 2>/dev/null | head -60
cat "../kardinal-promoter/.specify/specs/"*/tasks.md 2>/dev/null | head -80
```

## Step 2 — Implement (TDD)

For each task in tasks.md (Phase 1: setup → Phase 2: tests → Phase 3: implementation):

**Write the test file FIRST** for every implementation task:
```bash
cd "$WORKTREE"
# Write test, then implement
go test ./... -race 2>&1 | tail -5
go vet ./... 2>&1
```

Tick each task in tasks.md ONLY after the code exists:
```bash
# Update tasks.md in main repo (not worktree) — coordinator reads this
sed -i '' "s/- \[ \] $TASK_ID /- [x] $TASK_ID /" ../kardinal-promoter/.specify/specs/*/tasks.md
```

## Step 3 — Self-validate (mandatory)

```bash
cd "$WORKTREE"

# Quality gates
go test ./... -race -count=1 -timeout 120s && echo "TESTS: PASS" || echo "TESTS: FAIL"
go vet ./... && echo "VET: PASS" || echo "VET: FAIL"

# Check for phantom completions
cd ../kardinal-promoter
export SPECIFY_FEATURE="$ITEM_ID"
# /speckit.verify-tasks.run
# /speckit.verify
cd "$WORKTREE"

# Journey validation — find which journeys this feature contributes to
# (read from spec.md "Contributes to journey(s)" field)
JOURNEYS=$(grep "Contributes to journey" ../kardinal-promoter/.specify/specs/*/spec.md | head -1)
echo "This feature contributes to: $JOURNEYS"

# Run relevant journey steps from docs/aide/definition-of-done.md
# Example for J1 (Quickstart):
# kubectl apply -f ../kardinal-promoter/examples/quickstart/pipeline.yaml
# kardinal get pipelines
# Capture the output to include in PR body
echo "Journey validation output captured."
```

## Step 4 — Push PR

```bash
cd "$WORKTREE"
git add -A
git commit -m "feat(${ITEM_ID}): <description from item file>"
git push -u origin HEAD

# Open PR
gh pr create \
  --repo pnz1990/kardinal-promoter \
  --title "feat(${ITEM_ID}): <description>" \
  --body "$(cat ../kardinal-promoter/docs/aide/pr-template.md | \
    sed 's|<NNN-item-name>|'$ITEM_ID'|g')" \
  --label "kardinal"

# Update state.json
PR_NUM=$(gh pr list --repo pnz1990/kardinal-promoter --head "$(git branch --show-current)" --json number -q '.[0].number')

cd ../kardinal-promoter
python3 -c "
import json, os
state = json.load(open('.maqa/state.json'))
state['features']['$ITEM_ID']['state'] = 'in_review'
state['features']['$ITEM_ID']['pr_number'] = $PR_NUM
tmp = '.maqa/state.json.tmp'
json.dump(state, open(tmp,'w'), indent=2)
os.rename(tmp, '.maqa/state.json')
print('State updated: in_review, PR #$PR_NUM')
"
cd "$WORKTREE"
```

## Step 5 — Monitor CI

```bash
PR_NUM=$PR_NUM
while true; do
  STATUS=$(gh pr checks $PR_NUM --repo pnz1990/kardinal-promoter 2>&1)
  if echo "$STATUS" | grep -q "fail"; then
    echo "CI RED. Reading failure..."
    gh run view --log-failed --repo pnz1990/kardinal-promoter 2>&1 | head -50
    echo "Fixing CI failure..."
    # Fix the issue, then:
    git add -A
    git commit -m "fix(${ITEM_ID}): fix CI failure"
    git push
    sleep 60
  elif echo "$STATUS" | grep -q "pass\|success"; then
    echo "CI GREEN."
    break
  else
    echo "CI pending..."
    sleep 180
  fi
done
```

## Step 6 — Wait for QA and respond

```bash
while true; do
  REVIEW=$(gh pr view $PR_NUM --repo pnz1990/kardinal-promoter --json reviews -q '.reviews[-1].state' 2>/dev/null)
  if [ "$REVIEW" = "APPROVED" ]; then
    echo "QA APPROVED."
    break
  elif [ "$REVIEW" = "CHANGES_REQUESTED" ]; then
    echo "QA requested changes. Reading comments..."
    gh pr view $PR_NUM --repo pnz1990/kardinal-promoter --json reviews -q '.reviews[-1].body'
    gh pr diff $PR_NUM --repo pnz1990/kardinal-promoter | head -100
    echo "Fixing QA issues..."
    # Fix all file:line issues from QA review
    git add -A
    git commit -m "fix(${ITEM_ID}): address QA review comments"
    git push
    # Loop back to CI monitoring (Step 5) before QA re-reviews
  else
    echo "Waiting for QA review... ($REVIEW)"
    sleep 300
  fi
done
```

## Step 7 — Merge

```bash
cd "$WORKTREE"
gh pr merge $PR_NUM --repo pnz1990/kardinal-promoter --squash --delete-branch

# Clean up worktree
cd ../kardinal-promoter
git worktree remove "$WORKTREE" --force 2>/dev/null || true

# Update state.json
python3 -c "
import json, os
slot = os.environ.get('AGENT_ID', 'ENGINEER-1')
state = json.load(open('.maqa/state.json'))
state['features']['$ITEM_ID']['state'] = 'done'
state['features']['$ITEM_ID']['pr_merged'] = True
state['engineer_slots'][slot] = None
tmp = '.maqa/state.json.tmp'
json.dump(state, open(tmp,'w'), indent=2)
os.rename(tmp, '.maqa/state.json')
print('State: done. Slot freed.')
"

# Comment on item issue
ISSUE_NUM=$(gh issue list --repo pnz1990/kardinal-promoter --search "$ITEM_ID" --json number -q '.[0].number' 2>/dev/null)
[ -n "$ISSUE_NUM" ] && gh issue comment $ISSUE_NUM --body "[$AGENT_ID_BADGE] Merged in PR #$PR_NUM. Feature complete."
```

## Step 8 — Smoke test on main

```bash
git checkout main && git pull
go build ./... && echo "SMOKE TEST: PASS" || {
  echo "SMOKE TEST: FAIL — opening hotfix issue"
  gh issue create \
    --repo pnz1990/kardinal-promoter \
    --title "hotfix: go build failed after merging $ITEM_ID" \
    --body "[$AGENT_ID_BADGE] Build broke after merging PR #$PR_NUM for $ITEM_ID. Immediate fix required." \
    --label "needs-human"
}
```

## Step 9 — Loop

Go to Step 1 and pick up the next assignment.

## Escalation

If blocked after 2 retries:
```bash
# Set state to blocked
python3 -c "
import json, os
slot = os.environ.get('AGENT_ID', 'ENGINEER-1')
state = json.load(open('.maqa/state.json'))
state['features']['$ITEM_ID']['state'] = 'blocked'
state['engineer_slots'][slot] = None
tmp = '.maqa/state.json.tmp'
json.dump(state, open(tmp,'w'), indent=2)
os.rename(tmp, '.maqa/state.json')
"

ISSUE_NUM=$(gh issue list --repo pnz1990/kardinal-promoter --search "$ITEM_ID" --json number -q '.[0].number' 2>/dev/null)
if [ -n "$ISSUE_NUM" ]; then
  gh issue comment $ISSUE_NUM --body "[$AGENT_ID_BADGE] BLOCKED after 2 retries. Reason: <exact reason>. Decision needed: <exact question>."
  gh issue edit $ISSUE_NUM --add-label "needs-human"
fi
```
