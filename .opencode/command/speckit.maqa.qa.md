---
description: QA agent. Continuous PR watcher. Reviews every PR with the project
  label against spec, user docs, examples, and journey definition. Re-reviews after
  every engineer fix. Loops until project complete.
---

<!-- Extension: maqa -->

## Step 0 — Read project config

```bash
REPO=$(git remote get-url origin 2>/dev/null | sed 's|.*github.com[:/]||;s|\.git$||')
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
LINT_COMMAND=$(python3 -c "
import re
for line in open('AGENTS.md'):
    m = re.match(r'^LINT_COMMAND:\s*(.+)', line.strip())
    if m: print(m.group(1).strip('\"').strip(\"'\")); break
" 2>/dev/null)

# Read project-specific QA rules from AGENTS.md
# (copyright format, banned filenames, anti-patterns, code standards)
echo "REPO=$REPO  REPORT_ISSUE=$REPORT_ISSUE  PR_LABEL=$PR_LABEL"
cat AGENTS.md | grep -A 20 "Go Standards\|Anti-Patterns\|Banned Filenames\|Code Standards" 2>/dev/null || true
```

Read `.specify/memory/sdlc.md` section "QA Loop" for the authoritative process.

## Step 1 — Poll for open PRs

```bash
git pull origin main
cat .specify/memory/sdlc.md
cat AGENTS.md | grep -A 5 "Anti-Patterns"
cat docs/aide/definition-of-done.md | head -50

while true; do
  OPEN_PRS=$(gh pr list \
    --repo $REPO \
    --label "$PR_LABEL" \
    --state open \
    --json number,title,headRefName,updatedAt \
    2>/dev/null)

  COUNT=$(echo "$OPEN_PRS" | python3 -c "import json,sys; prs=json.load(sys.stdin); print(len(prs))" 2>/dev/null || echo "0")

  if [ "$COUNT" -gt 0 ]; then
    echo "Found $COUNT open PRs. Reviewing..."
    echo "$OPEN_PRS"
    break
  fi

  if gh issue view $REPORT_ISSUE --repo $REPO --comments 2>/dev/null | grep -q "\[PROJECT COMPLETE\]"; then
    echo "Project complete. QA session ending."
    exit 0
  fi

  echo "[🔍 QA] No open PRs. Polling in 2 minutes..."
  sleep 120
done
```

## Step 2 — Review each PR

For each open PR:

```bash
PR_NUM=<number from Step 1>

# Read full diff and PR body
gh pr diff $PR_NUM --repo $REPO
gh pr view $PR_NUM --repo $REPO --json body,title,headRefName

# Identify item from branch name
BRANCH=$(gh pr view $PR_NUM --repo $REPO --json headRefName -q '.headRefName')
ITEM_ID=$(echo $BRANCH | sed 's/[^0-9]*\([0-9]*-[^/]*\).*/\1/')

# Read spec and item
cat .specify/specs/*/spec.md 2>/dev/null | head -100
cat docs/aide/items/${ITEM_ID}*.md 2>/dev/null

export SPECIFY_FEATURE="$ITEM_ID"
# /speckit.verify
```

**Full checklist** — read project-specific rules from AGENTS.md, then check:

```
□ 1. Every Given/When/Then from spec.md has a real implementation (not a stub)
□ 2. Every FR-NNN in spec.md has corresponding code
□ 3. PR body includes journey validation output
□ 4. PR body includes /speckit.verify-tasks.run output (zero phantom completions)
□ 5. CI is green: gh pr checks $PR_NUM --repo $REPO
□ 6. All project code standards from AGENTS.md satisfied (copyright, error handling, logging)
□ 7. No banned filenames from AGENTS.md in the diff
□ 8. No forbidden imports/patterns from AGENTS.md anti-patterns list
□ 9. User docs consistent with implementation (if user-facing feature)
□ 10. examples/ YAML applies cleanly (if relevant)
□ 11. Feature advances at least one journey in definition-of-done.md
```

```bash
# Check banned filenames (read list from AGENTS.md)
BANNED=$(python3 -c "
import re
lines = open('AGENTS.md').read()
m = re.search(r'Banned Filenames.*?\n(.*?)\n\n', lines, re.DOTALL)
if m:
    for f in re.findall(r'\`([^\']+\.go)\`', m.group(1)): print(f)
" 2>/dev/null)
for BANNED_FILE in $BANNED; do
  gh pr diff $PR_NUM --repo $REPO | grep "^+++ b/" | grep "$BANNED_FILE" && \
    echo "BANNED FILENAME: $BANNED_FILE"
done
```

## Step 3 — Post review

**If all checks pass:**
```bash
gh pr review $PR_NUM --repo $REPO --approve \
  --body "[🔍 QA] LGTM. All acceptance criteria satisfied. CI green."
```

**If any check fails:**
```bash
gh pr review $PR_NUM --repo $REPO --request-changes \
  --body "[🔍 QA] ## Changes Required

<for each issue: file:line — exact description>"
```

## Step 4 — Wait for engineer fix and re-review

```bash
LAST_COMMIT=$(gh pr view $PR_NUM --repo $REPO --json commits -q '.commits[-1].oid')
while true; do
  CURRENT=$(gh pr view $PR_NUM --repo $REPO --json commits -q '.commits[-1].oid')
  if [ "$CURRENT" != "$LAST_COMMIT" ]; then
    echo "New commit. Re-reviewing full diff..."
    LAST_COMMIT=$CURRENT
    break  # go back to Step 2
  fi
  STATE=$(gh pr view $PR_NUM --repo $REPO --json state -q '.state')
  [ "$STATE" = "CLOSED" ] && echo "PR closed." && break
  sleep 300
done
```

## Step 5 — Escalate if needed

If the same issue appears 3+ times:

```bash
gh issue create --repo $REPO \
  --title "QA Finding: <description>" \
  --body "[🔍 QA] ## [QA FINDING]
Severity: medium | PR: #$PR_NUM | Item: $ITEM_ID
File: path/to/file:<line>
Finding: <exact description — appeared 3+ times>
Recommended action: <what human should decide>" \
  --label "needs-human"

gh issue comment $REPORT_ISSUE --repo $REPO \
  --body "[🔍 QA] ## [QA FINDING] $ITEM_ID PR#$PR_NUM — same issue 3x. Human review required."
```

## Step 6 — Loop

Go to Step 1.
