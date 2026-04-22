#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# sim-adjustment-notify.sh — post a human-visible notification when the simulation
# queue-adjustment fires (COORD §1b-delta changes ADJUSTED_SESSION_LIMIT)
#
# Usage: ./scripts/sim-adjustment-notify.sh [repo] [report-issue]
#
# Implements docs/design/12-autonomous-loop-discipline.md §Future:
# "Simulation queue-adjustment is invisible to humans — ADJUSTED_SESSION_LIMIT fires silently"
#
# When ADJUSTED_SESSION_LIMIT differs from the default session_item_limit, posts:
#   [SIM ADJUSTMENT — queuing N items (default M): last 3 ratio_history: X/Y/Z]
#
# Exit code: always 0 (fail-open — never blocks the coordinator)

set -euo pipefail

REPO="${1:-${REPO:-pnz1990/kardinal-promoter}}"
REPORT_ISSUE="${2:-${REPORT_ISSUE:-892}}"
MY_SESSION_ID="${MY_SESSION_ID:-sess-unknown}"
OTHERNESS_VERSION="${OTHERNESS_VERSION:-unknown}"
STATE_FILE="${STATE_FILE:-.otherness/state.json}"
CONFIG_FILE="${CONFIG_FILE:-otherness-config.yaml}"

# Fail-open: gh not available
if ! command -v gh &>/dev/null; then
  echo "[SIM-ADJUSTMENT-NOTIFY SKIPPED — gh CLI not available]"
  exit 0
fi

# Step 1: Read default session limit from otherness-config.yaml
DEFAULT_LIMIT=$(python3 -c "
import re
section = None
try:
    for line in open('$CONFIG_FILE'):
        s = re.match(r'^(\w[\w_]*):', line)
        if s: section = s.group(1)
        if section == 'maqa':
            m = re.match(r'^\s+session_item_limit:\s*(\d+)', line)
            if m: print(m.group(1)); break
except Exception:
    pass
" 2>/dev/null || echo "")

if [ -z "$DEFAULT_LIMIT" ]; then
  echo "[SIM-ADJUSTMENT-NOTIFY SKIPPED — could not read session_item_limit from config]"
  exit 0
fi

# Step 2: Read adjusted_session_limit and ratio_history from state.json
ADJUSTED_LIMIT="${ADJUSTED_SESSION_LIMIT:-}"
RATIO_HISTORY=""

if [ -f "$STATE_FILE" ]; then
  STATE_DATA=$(python3 - <<PYEOF 2>/dev/null || echo ""
import json
try:
    with open('$STATE_FILE') as f:
        s = json.load(f)
    # Read adjusted_session_limit
    adj = s.get('adjusted_session_limit', '')
    # Read ratio_history (last 3 entries)
    rh = s.get('ratio_history', [])
    if isinstance(rh, list):
        last3 = rh[-3:]
    else:
        last3 = []
    ratio_str = '/'.join(str(round(float(x), 2)) for x in last3) if last3 else ''
    print(f"ADJ={adj}")
    print(f"RATIO={ratio_str}")
except Exception as e:
    print(f"ERROR={e}")
PYEOF
  )

  if [ -n "$ADJUSTED_LIMIT" ] && [ -z "$ADJUSTED_LIMIT" ]; then
    ADJUSTED_LIMIT=$(echo "$STATE_DATA" | grep "^ADJ=" | sed 's/^ADJ=//')
  fi
  if [ -z "$ADJUSTED_LIMIT" ]; then
    ADJUSTED_LIMIT=$(echo "$STATE_DATA" | grep "^ADJ=" | sed 's/^ADJ=//')
  fi
  RATIO_HISTORY=$(echo "$STATE_DATA" | grep "^RATIO=" | sed 's/^RATIO=//')
fi

# Step 3: Check if adjustment differs from default
if [ -z "$ADJUSTED_LIMIT" ]; then
  echo "[SIM-ADJUSTMENT-NOTIFY] No adjusted_session_limit in state — no adjustment active"
  exit 0
fi

if [ "$ADJUSTED_LIMIT" = "$DEFAULT_LIMIT" ]; then
  echo "[SIM-ADJUSTMENT-NOTIFY] adjusted=$ADJUSTED_LIMIT == default=$DEFAULT_LIMIT — no notification needed"
  exit 0
fi

echo "[SIM-ADJUSTMENT-NOTIFY] Adjustment detected: $ADJUSTED_LIMIT items (default: $DEFAULT_LIMIT)"

# Step 4: Build notification message
if [ -n "$RATIO_HISTORY" ]; then
  NOTIFICATION="[SIM ADJUSTMENT | ${MY_SESSION_ID} | otherness@${OTHERNESS_VERSION}] Queuing ${ADJUSTED_LIMIT} items (default ${DEFAULT_LIMIT}): last 3 ratio_history: ${RATIO_HISTORY}"
else
  NOTIFICATION="[SIM ADJUSTMENT | ${MY_SESSION_ID} | otherness@${OTHERNESS_VERSION}] Queuing ${ADJUSTED_LIMIT} items (default ${DEFAULT_LIMIT}): ratio_history not available"
fi

# Step 5: Dedup guard — skip if identical comment already posted
EXISTING=$(gh issue view "$REPORT_ISSUE" --repo "$REPO" \
  --json comments \
  --jq "[.comments[] | select(.body | contains(\"SIM ADJUSTMENT\") and contains(\"${ADJUSTED_LIMIT} items (default ${DEFAULT_LIMIT})\"))] | length" \
  2>/dev/null || echo "0")

if [ "${EXISTING:-0}" -gt 0 ]; then
  echo "[SIM-ADJUSTMENT-NOTIFY] Notification already posted for this adjustment — skipping dedup"
  exit 0
fi

# Step 6: Post notification
gh issue comment "$REPORT_ISSUE" --repo "$REPO" --body "$NOTIFICATION" 2>/dev/null && \
  echo "[SIM-ADJUSTMENT-NOTIFY] Notification posted" || \
  echo "[SIM-ADJUSTMENT-NOTIFY] Failed to post notification (non-fatal)"

exit 0
