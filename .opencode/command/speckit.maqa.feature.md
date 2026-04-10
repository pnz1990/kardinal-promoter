---
description: Engineer agent. Owns one feature end-to-end from assignment through
  merged PR. TDD, self-validation against journeys, CI monitoring, QA response,
  merge, smoke test. Loops continuously until no more work.
---

<!-- Extension: maqa -->

## Step 0 — Read project config

```bash
REPO=$(git remote get-url origin 2>/dev/null | sed 's|.*github.com[:/]||;s|\.git$||')
REPO_NAME=$(basename $(git rev-parse --show-toplevel))
REPORT_ISSUE=$(python3 -c "
import re
for line in open('AGENTS.md'):
    m = re.match(r'^REPORT_ISSUE:\s*(\S+)', line.strip())
    if m: print(m.group(1)); break
" 2>/dev/null || echo "1")
PR_LABEL=$(python3 -c "
import re
for line in open('AGENTS.md'):
    m = re.match(r'^PR_LABEL:\s*(\S+)', line.strip())
    if m: print(m.group(1)); break
" 2>/dev/null || echo "")
BUILD_COMMAND=$(python3 -c "
import re
for line in open('AGENTS.md'):
    m = re.match(r'^BUILD_COMMAND:\s*(.+)', line.strip())
    if m: print(m.group(1).strip('\"').strip(\"'\")); break
" 2>/dev/null)
TEST_COMMAND=$(python3 -c "
import re
for line in open('maqa-config.yml'):
    m = re.match(r'^test_command:\s*[\"\']?([^\"\'#\n]+)[\"\']?', line.strip())
    if m: print(m.group(1).strip()); break
" 2>/dev/null)
LINT_COMMAND=$(python3 -c "
import re
for line in open('AGENTS.md'):
    m = re.match(r'^LINT_COMMAND:\s*(.+)', line.strip())
    if m: print(m.group(1).strip('\"').strip(\"'\")); break
" 2>/dev/null)
echo "REPO=$REPO  REPORT_ISSUE=$REPORT_ISSUE  PR_LABEL=$PR_LABEL"
```

Read and follow `$AGENTS_PATH/engineer.md` (agents_path from maqa-config.yml).
This command provides the shell-executable implementation of that agent's loop.

## Step 1 — Pick up assignment via CLAIM file

```bash
# Find CLAIM file written by coordinator
CLAIM_FILE=$(ls ../${REPO_NAME}.*/CLAIM 2>/dev/null | head -1)
if [ -z "$CLAIM_FILE" ]; then
  echo "No CLAIM file found. No valid assignment. Idling."
  gh issue comment $REPORT_ISSUE --repo $REPO \
    --body "[🔨 ENGINEER] Started session but found no CLAIM file. Idling."
  exit 0
fi

AGENT_ID=$(grep AGENT_ID $CLAIM_FILE | cut -d= -f2)
ITEM_ID=$(grep ITEM_ID $CLAIM_FILE | cut -d= -f2)
WORKTREE=$(dirname $CLAIM_FILE)
echo "Identity: $AGENT_ID | Item: $ITEM_ID | Worktree: $WORKTREE"

# Claim-check: verify state.json agrees
python3 - <<EOF
import json, sys
s = json.load(open('.maqa/state.json'))
item = s['features'].get('$ITEM_ID', {})
if item.get('assigned_to') != '$AGENT_ID':
    print(f"CONFLICT: {item.get('assigned_to')} owns this item, not $AGENT_ID. STOPPING.")
    sys.exit(1)
print(f"CLAIM VALID: state={item['state']}")
EOF
```

Once validated:
```bash
cd "$WORKTREE"
git pull origin main 2>/dev/null || true

# Read item spec
cat ITEM.md 2>/dev/null || cat "../${REPO_NAME}/docs/aide/items/${ITEM_ID}"*.md

# Confirm pickup
cd "../${REPO_NAME}"
python3 -c "
import json, os
s = json.load(open('.maqa/state.json'))
s['features']['$ITEM_ID']['state'] = 'in_progress'
import os; tmp='.maqa/state.json.tmp'; json.dump(s,open(tmp,'w'),indent=2); os.rename(tmp,'.maqa/state.json')
"
gh issue comment $REPORT_ISSUE --repo $REPO \
  --body "[$AGENT_ID] Confirmed pickup of $ITEM_ID. Starting implementation."
cd "$WORKTREE"
```

## Step 2 — Implement (TDD)

Write the test file FIRST for every implementation task:
```bash
cd "$WORKTREE"
# Write test, then implement, then verify
eval "$TEST_COMMAND" 2>&1 | tail -5
eval "$LINT_COMMAND" 2>&1
```

## Step 3 — Self-validate (mandatory)

```bash
cd "$WORKTREE"
eval "$BUILD_COMMAND" && echo "BUILD: PASS" || echo "BUILD: FAIL"
eval "$TEST_COMMAND" && echo "TESTS: PASS" || echo "TESTS: FAIL"
eval "$LINT_COMMAND" && echo "LINT: PASS" || echo "LINT: FAIL"

cd "../${REPO_NAME}"
export SPECIFY_FEATURE="$ITEM_ID"
# /speckit.verify-tasks.run
# /speckit.verify
cd "$WORKTREE"
```

## Step 4 — Push PR

```bash
cd "$WORKTREE"
git add -A
git commit -m "feat(${ITEM_ID}): <description from item file>"
git push -u origin HEAD

PR_NUM=$(gh pr create \
  --repo $REPO \
  --title "feat(${ITEM_ID}): <description>" \
  --body "$(cat ../\${REPO_NAME}/docs/aide/pr-template.md 2>/dev/null)" \
  --label "$PR_LABEL" \
  --json number -q '.number' 2>/dev/null || \
  gh pr list --repo $REPO --head "$(git branch --show-current)" --json number -q '.[0].number')

cd "../${REPO_NAME}"
python3 -c "
import json, os
s = json.load(open('.maqa/state.json'))
s['features']['$ITEM_ID']['state'] = 'in_review'
s['features']['$ITEM_ID']['pr_number'] = $PR_NUM
tmp='.maqa/state.json.tmp'; json.dump(s,open(tmp,'w'),indent=2); os.rename(tmp,'.maqa/state.json')
"
cd "$WORKTREE"
```

## Step 5 — Monitor CI

```bash
while true; do
  STATUS=$(gh pr checks $PR_NUM --repo $REPO 2>&1)
  if echo "$STATUS" | grep -q "fail"; then
    echo "CI RED. Reading failure..."
    gh run view --log-failed --repo $REPO 2>&1 | head -50
    git add -A && git commit -m "fix(${ITEM_ID}): fix CI failure" && git push
    sleep 60
  elif echo "$STATUS" | grep -qE "pass|success"; then
    echo "CI GREEN."; break
  else
    echo "CI pending..."; sleep 180
  fi
done
```

## Step 6 — Wait for QA and respond

```bash
while true; do
  REVIEW=$(gh pr view $PR_NUM --repo $REPO --json reviews -q '.reviews[-1].state' 2>/dev/null)
  if [ "$REVIEW" = "APPROVED" ]; then
    echo "QA APPROVED."; break
  elif [ "$REVIEW" = "CHANGES_REQUESTED" ]; then
    echo "QA requested changes."
    gh pr view $PR_NUM --repo $REPO --json reviews -q '.reviews[-1].body'
    gh pr diff $PR_NUM --repo $REPO | head -100
    git add -A && git commit -m "fix(${ITEM_ID}): address QA review" && git push
  else
    echo "Waiting for QA... ($REVIEW)"; sleep 300
  fi
done
```

## Step 7 — Merge

```bash
cd "$WORKTREE"
gh pr merge $PR_NUM --repo $REPO --squash --delete-branch

cd "../${REPO_NAME}"
git worktree remove "$WORKTREE" --force 2>/dev/null || true

python3 -c "
import json, os
s = json.load(open('.maqa/state.json'))
s['features']['$ITEM_ID']['state'] = 'done'
s['features']['$ITEM_ID']['pr_merged'] = True
s['engineer_slots']['$AGENT_ID'] = None
tmp='.maqa/state.json.tmp'; json.dump(s,open(tmp,'w'),indent=2); os.rename(tmp,'.maqa/state.json')
"

ISSUE_NUM=$(gh issue list --repo $REPO --search "$ITEM_ID" --json number -q '.[0].number' 2>/dev/null)
[ -n "$ISSUE_NUM" ] && gh issue comment $ISSUE_NUM --repo $REPO \
  --body "[$AGENT_ID] Merged PR #$PR_NUM. Feature complete." && \
  gh issue close $ISSUE_NUM --repo $REPO
```

## Step 8 — Smoke test on main

```bash
git checkout main && git pull
eval "$BUILD_COMMAND" && echo "SMOKE TEST: PASS" || {
  echo "SMOKE TEST: FAIL"
  gh issue create --repo $REPO \
    --title "hotfix: build failed after merging $ITEM_ID" \
    --body "[$AGENT_ID] Build broke after merging PR #$PR_NUM for $ITEM_ID." \
    --label "needs-human"
}
```

## Step 9 — Loop

Go to Step 1 and pick up the next assignment.

## Escalation

If blocked after 2 retries:
```bash
cd "../${REPO_NAME}"
python3 -c "
import json, os
s = json.load(open('.maqa/state.json'))
s['features']['$ITEM_ID']['state'] = 'blocked'
s['engineer_slots']['$AGENT_ID'] = None
tmp='.maqa/state.json.tmp'; json.dump(s,open(tmp,'w'),indent=2); os.rename(tmp,'.maqa/state.json')
"
ISSUE_NUM=$(gh issue list --repo $REPO --search "$ITEM_ID" --json number -q '.[0].number' 2>/dev/null)
[ -n "$ISSUE_NUM" ] && \
  gh issue comment $ISSUE_NUM --repo $REPO \
    --body "[$AGENT_ID] BLOCKED after 2 retries. <exact reason>. Decision needed: <exact question>." && \
  gh issue edit $ISSUE_NUM --repo $REPO --add-label "needs-human"
```
