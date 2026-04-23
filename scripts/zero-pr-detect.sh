#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# zero-pr-detect.sh — detect sessions where the agent ran but produced no merged PRs
#
# Usage: ./scripts/zero-pr-detect.sh [repo] [report-issue]
#
# Implements doc 12 §Future: Zero-PR session detection.
# Checks whether any PRs were merged in the last hour. If not:
#   - Posts [SESSION DRY RUN] to REPORT_ISSUE
#   - Increments dry_run_count in _state:.otherness/dry-run-state.json
#   - After 3+ consecutive dry runs: escalates to [NEEDS HUMAN]
#
# On any batch that ships PRs: resets dry_run_count to 0.
#
# Exit code: always 0 (fail-safe — never blocks the SM)

set -euo pipefail

REPO="${1:-${REPO:-pnz1990/kardinal-promoter}}"
REPORT_ISSUE="${2:-${REPORT_ISSUE:-892}}"
MY_SESSION_ID="${MY_SESSION_ID:-sess-unknown}"
OTHERNESS_VERSION="${OTHERNESS_VERSION:-unknown}"

# Fail safe: gh not available
if ! command -v gh &>/dev/null; then
  echo "[ZERO-PR DETECT SKIPPED — gh CLI not available]"
  exit 0
fi

# Compute "1 hour ago" in UTC ISO format
ONE_HOUR_AGO=$(python3 -c "
import datetime
t = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=1)
print(t.strftime('%Y-%m-%dT%H:%M:%SZ'))
" 2>/dev/null || echo "")

if [ -z "$ONE_HOUR_AGO" ]; then
  echo "[ZERO-PR DETECT SKIPPED — could not compute timestamp]"
  exit 0
fi

# Count PRs merged in the last hour
MERGED_COUNT=$(gh pr list \
  --repo "$REPO" \
  --state merged \
  --limit 20 \
  --json number,mergedAt \
  --jq "[.[] | select(.mergedAt >= \"$ONE_HOUR_AGO\")] | length" 2>/dev/null || echo "-1")

if [ "$MERGED_COUNT" = "-1" ]; then
  echo "[ZERO-PR DETECT SKIPPED — could not query merged PRs]"
  exit 0
fi

echo "[ZERO-PR DETECT] Merged PRs in last 1h: $MERGED_COUNT"

# Read/write dry-run state from _state branch
DRY_RUN_COUNT=0
STATE_WT=$(python3 -c "import tempfile, os; print(os.path.join(tempfile.gettempdir(), 'otherness-dryrun-' + str(os.getpid())))" 2>/dev/null)
STATE_PATH="$STATE_WT/.otherness/dry-run-state.json"

# Try to read existing dry-run count
if git worktree add --no-checkout "$STATE_WT" origin/_state 2>/dev/null; then
  git -C "$STATE_WT" checkout _state -- .otherness/dry-run-state.json 2>/dev/null || true
  if [ -f "$STATE_PATH" ]; then
    DRY_RUN_COUNT=$(python3 -c "
import json
try:
    d = json.load(open('$STATE_PATH'))
    print(d.get('count', 0))
except:
    print(0)
" 2>/dev/null || echo "0")
  fi
  git worktree remove "$STATE_WT" --force 2>/dev/null || true
fi
git worktree prune 2>/dev/null || true

echo "[ZERO-PR DETECT] Previous consecutive dry run count: $DRY_RUN_COUNT"

if [ "$MERGED_COUNT" -gt 0 ]; then
  # PRs were shipped — reset counter
  NEW_COUNT=0
  echo "[ZERO-PR DETECT] $MERGED_COUNT PRs merged — resetting dry run count to 0"

  # Persist reset to _state
  python3 - <<PYEOF 2>/dev/null || true
import json, subprocess, os, tempfile, datetime

state_wt = os.path.join(tempfile.gettempdir(), 'otherness-dryreset-' + str(os.getpid()))
try:
    if os.path.exists(state_wt):
        subprocess.run(['git','worktree','remove',state_wt,'--force'], capture_output=True)
    subprocess.run(['git','worktree','add','--no-checkout',state_wt,'origin/_state'],
                   capture_output=True, check=True)
    target = os.path.join(state_wt, '.otherness', 'dry-run-state.json')
    os.makedirs(os.path.dirname(target), exist_ok=True)
    # Checkout BOTH files to preserve state.json in the commit tree.
    # Checking out only dry-run-state.json would clobber state.json on push.
    subprocess.run(['git','-C',state_wt,'checkout','_state','--','.otherness/dry-run-state.json'],
                   capture_output=True)
    subprocess.run(['git','-C',state_wt,'checkout','_state','--','.otherness/state.json'],
                   capture_output=True)  # preserve state.json — do not remove it from tree
    state = {'count': 0, 'updated_at': datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'), 'last_merged_count': int('$MERGED_COUNT')}
    json.dump(state, open(target, 'w'), indent=2)
    subprocess.run(['git','-C',state_wt,'add',target], capture_output=True)
    subprocess.run(['git','-C',state_wt,'commit','-m','sm: dry_run_count reset to 0'], capture_output=True)
    subprocess.run(['git','-C',state_wt,'push','origin','HEAD:_state'], capture_output=True)
    print('[ZERO-PR DETECT] dry_run_count=0 persisted to _state')
except Exception as e:
    print(f'[ZERO-PR DETECT] state persist error (non-fatal): {e}')
finally:
    try:
        subprocess.run(['git','worktree','remove',state_wt,'--force'], capture_output=True)
    except: pass
subprocess.run(['git','worktree','prune'], capture_output=True)
PYEOF
else
  # No PRs merged — increment counter
  NEW_COUNT=$((DRY_RUN_COUNT + 1))
  echo "[ZERO-PR DETECT] Zero PRs merged — dry run count: $NEW_COUNT"

  # Dedup guard: check if we already posted this exact message recently
  ALREADY_POSTED=$(gh issue view "$REPORT_ISSUE" --repo "$REPO" \
    --json comments \
    --jq "[.comments[-5:][].body | select(contains(\"SESSION DRY RUN\") and contains(\"$NEW_COUNT consecutive\"))] | length" \
    2>/dev/null || echo "0")

  if [ "${ALREADY_POSTED:-0}" -eq 0 ]; then
    # Post DRY RUN comment
    gh issue comment "$REPORT_ISSUE" --repo "$REPO" \
      --body "[SESSION DRY RUN | $MY_SESSION_ID | otherness@$OTHERNESS_VERSION] Agent ran but shipped 0 PRs in this batch. Consecutive dry runs: $NEW_COUNT. Possible causes: all items failed CI, all PRs rejected by merge protocol, coordinator generated queue but no engineer claimed work." \
      2>/dev/null || echo "[ZERO-PR DETECT] Could not post DRY RUN comment (non-fatal)"
    echo "[ZERO-PR DETECT] DRY RUN comment posted (count=$NEW_COUNT)"
  else
    echo "[ZERO-PR DETECT] DRY RUN comment already posted — skipping dedup"
  fi

  # Escalate after 3+ consecutive dry runs
  if [ "$NEW_COUNT" -ge 3 ]; then
    ALREADY_ESCALATED=$(gh issue list --repo "$REPO" --state open \
      --label "needs-human" \
      --search "3+ consecutive dry runs" \
      --json number --jq 'length' 2>/dev/null || echo "0")

    if [ "${ALREADY_ESCALATED:-0}" -eq 0 ]; then
      gh issue comment "$REPORT_ISSUE" --repo "$REPO" \
        --body "[NEEDS HUMAN: 3+ consecutive dry runs | $MY_SESSION_ID | otherness@$OTHERNESS_VERSION] Agent has run $NEW_COUNT consecutive batches with 0 merged PRs. Queue or merge protocol may be stuck. Manual investigation required." \
        2>/dev/null || true

      gh issue create --repo "$REPO" \
        --title "[NEEDS HUMAN] 3+ consecutive dry runs — agent shipped 0 PRs" \
        --label "needs-human,priority/high,kind/chore" \
        --body "## Zero-PR session escalation

The agent has completed **$NEW_COUNT consecutive batches** with 0 merged PRs.

### What to investigate
1. Is the queue empty? Check state.json on _state branch.
2. Is CI red? Check recent workflow runs.
3. Are all PRs being rejected by the merge protocol? Check needs-human issues.
4. Is the coordinator generating a queue but no engineer claiming work?

### How to resolve
1. Fix the underlying cause (empty queue, red CI, or merge blockage)
2. Close this issue after the next successful batch

Reported by: scripts/zero-pr-detect.sh | $MY_SESSION_ID | otherness@$OTHERNESS_VERSION" \
        2>/dev/null || true
      echo "[ZERO-PR DETECT] Escalated: $NEW_COUNT consecutive dry runs — [NEEDS HUMAN] opened"
    else
      echo "[ZERO-PR DETECT] Already escalated — skipping duplicate needs-human issue"
    fi
  fi

  # Persist new count to _state
  python3 - <<PYEOF 2>/dev/null || true
import json, subprocess, os, tempfile, datetime

state_wt = os.path.join(tempfile.gettempdir(), 'otherness-drycount-' + str(os.getpid()))
new_count = int('$NEW_COUNT')
try:
    if os.path.exists(state_wt):
        subprocess.run(['git','worktree','remove',state_wt,'--force'], capture_output=True)
    subprocess.run(['git','worktree','add','--no-checkout',state_wt,'origin/_state'],
                   capture_output=True, check=True)
    target = os.path.join(state_wt, '.otherness', 'dry-run-state.json')
    os.makedirs(os.path.dirname(target), exist_ok=True)
    # Checkout BOTH files to preserve state.json in the commit tree.
    # Checking out only dry-run-state.json would clobber state.json on push.
    subprocess.run(['git','-C',state_wt,'checkout','_state','--','.otherness/dry-run-state.json'],
                   capture_output=True)
    subprocess.run(['git','-C',state_wt,'checkout','_state','--','.otherness/state.json'],
                   capture_output=True)  # preserve state.json — do not remove it from tree
    state = {'count': new_count, 'updated_at': datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'), 'last_merged_count': 0}
    json.dump(state, open(target, 'w'), indent=2)
    subprocess.run(['git','-C',state_wt,'add',target], capture_output=True)
    subprocess.run(['git','-C',state_wt,'commit','-m',f'sm: dry_run_count={new_count}'], capture_output=True)
    subprocess.run(['git','-C',state_wt,'push','origin','HEAD:_state'], capture_output=True)
    print(f'[ZERO-PR DETECT] dry_run_count={new_count} persisted to _state')
except Exception as e:
    print(f'[ZERO-PR DETECT] state persist error (non-fatal): {e}')
finally:
    try:
        subprocess.run(['git','worktree','remove',state_wt,'--force'], capture_output=True)
    except: pass
subprocess.run(['git','worktree','prune'], capture_output=True)
PYEOF
fi

echo "[ZERO-PR DETECT] Done. merged_count=$MERGED_COUNT dry_run_count=$NEW_COUNT"
