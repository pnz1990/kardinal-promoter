---
description: Coordinator agent. Reads the roadmap and progress, generates work queues,
  assigns items to engineers, runs batch audits, spawns Scrum Master and PM after
  each batch, and updates the GitHub Projects board. Run once — loops until all
  journeys complete.
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
VULN_COMMAND=$(python3 -c "
import re
for line in open('AGENTS.md'):
    m = re.match(r'^VULN_COMMAND:\s*(.+)', line.strip())
    if m: print(m.group(1).strip('\"').strip(\"'\")); break
" 2>/dev/null)
echo "REPO=$REPO  REPORT_ISSUE=$REPORT_ISSUE"
```

You are the Coordinator. Read `.specify/memory/sdlc.md` section
"Coordinator Loop" for the authoritative process. This command implements it.

## Step 1 — Read context

```bash
git pull origin main
cat .specify/memory/sdlc.md
cat docs/aide/team.yml
cat AGENTS.md
cat docs/aide/progress.md
cat docs/aide/roadmap.md
cat .maqa/state.json
```

Identify: current stage, items in queue (if any), engineer slot availability.

## Step 2 — Generate queue if empty

```bash
ls docs/aide/queue/ 2>/dev/null && ls docs/aide/items/ 2>/dev/null
```

If `docs/aide/queue/` is empty or has no queue file matching current stage:

Run `/speckit.aide.create-queue` to generate `docs/aide/queue/queue-NNN.md`.
Then for each item in the queue, run `/speckit.aide.create-item` to generate
`docs/aide/items/NNN-item-name.md`.

Then populate the GitHub Projects board:
```bash
cat maqa-github-projects/github-projects-config.yml
```
Run `/speckit.maqa-github-projects.populate` to sync items to the board.

## Step 3 — Validate dependencies and assign

Read `.specify/specs/<feature>/spec.md` for each queue item's `Depends on:` field.
Only assign items where all dependencies have `state: done` in `.maqa/state.json`.

For each assignable item (up to `max_parallel` from `maqa-config.yml`):

```bash
BRANCH="NNN-feature-name"
REPO_NAME=$(basename $(git rev-parse --show-toplevel))
WORKTREE="../${REPO_NAME}.${BRANCH}"
git worktree add "$WORKTREE" -b "$BRANCH" 2>&1

# Write CLAIM file so engineer knows its identity
cat > "$WORKTREE/CLAIM" <<EOF
AGENT_ID=ENGINEER-$SLOT_NUM
ITEM_ID=$BRANCH
ASSIGNED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
COORDINATOR_CYCLE=$CYCLE
EOF

# Update state.json atomically
python3 -c "
import json, os
state = json.load(open('.maqa/state.json'))
for slot in [s for s in state['engineer_slots'] if state['engineer_slots'][s] is None]:
    state['engineer_slots'][slot] = '$BRANCH'
    state['features']['$BRANCH'] = {
        'state': 'assigned',
        'assigned_to': slot,
        'worktree_path': '$WORKTREE',
        'pr_number': None,
        'pr_merged': False
    }
    state['last_updated'] = '$(date -u +%Y-%m-%dT%H:%M:%SZ)'
    break
tmp = '.maqa/state.json.tmp'
json.dump(state, open(tmp,'w'), indent=2)
os.rename(tmp, '.maqa/state.json')
"

# Comment on GitHub Issue for this item
ISSUE_NUM=$(gh issue list --repo $REPO --search "$BRANCH" --json number -q '.[0].number' 2>/dev/null || echo "")
[ -n "$ISSUE_NUM" ] && gh issue comment $ISSUE_NUM --repo $REPO \
  --body "[🎯 COORDINATOR] Assigned to $SLOT. Worktree ready at $WORKTREE."
```

## Step 4 — Monitor state (continuous poll)

Every 2 minutes, read `.maqa/state.json` and react:

**When state = in_review:** Move board card to In Review.
**When state = done:** Move card to Done, free slot, assign next item immediately.
**When state = blocked:** Post [NEEDS HUMAN] to Issue `#$REPORT_ISSUE`, label the item issue `needs-human`.

## Step 5 — Batch complete: audit and report

When all queue items are `done` or `blocked`:

```bash
git checkout main && git pull
/speckit.analyze
/speckit.memorylint.run

eval "$BUILD_COMMAND" && echo "BUILD: OK" || echo "BUILD: FAILED"
eval "$TEST_COMMAND" && echo "TESTS: OK" || echo "TESTS: FAILED"
[ -n "$VULN_COMMAND" ] && eval "$VULN_COMMAND" && echo "VULN: OK" || echo "VULN: FOUND"

cat docs/aide/definition-of-done.md | grep "^|"
```

If any check fails:
```bash
gh issue comment $REPORT_ISSUE --repo $REPO \
  --body "[🎯 COORDINATOR] ## [BATCH QUALITY GATE FAILED] $(date -u +%Y-%m-%dT%H:%M)
Failed checks: <list>. Next queue generation is BLOCKED."
gh issue edit $REPORT_ISSUE --repo $REPO --add-label "needs-human"
```

If all pass:
```bash
DONE_COUNT=$(python3 -c "import json; s=json.load(open('.maqa/state.json')); print(sum(1 for v in s['features'].values() if v['state']=='done'))")
BLOCKED_COUNT=$(python3 -c "import json; s=json.load(open('.maqa/state.json')); print(sum(1 for v in s['features'].values() if v['state']=='blocked'))")

gh issue comment $REPORT_ISSUE --repo $REPO \
  --body "[🎯 COORDINATOR] ## [BATCH COMPLETE] $(date -u +%Y-%m-%dT%H:%M)
**Shipped** ($DONE_COUNT items) | **Blocked** ($BLOCKED_COUNT items)
**Journey status:**
$(grep "^| J" docs/aide/definition-of-done.md | head -10)
**Audit**: BUILD ✅ TESTS ✅ VULN ✅
Scrum Master and Product Manager — your review cycle is ready."
```

## Stop condition

When all journeys in `docs/aide/definition-of-done.md` are ✅:

```bash
gh issue comment $REPORT_ISSUE --repo $REPO \
  --body "[🎯 COORDINATOR] ## [PROJECT COMPLETE] $(date -u +%Y-%m-%dT%H:%M)
All journeys passing. The project is complete."
```

Exit.
