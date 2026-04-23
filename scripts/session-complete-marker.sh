#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# session-complete-marker.sh — write and check session-end markers on the _state branch
#
# Usage:
#   ./scripts/session-complete-marker.sh write [session-id]   # called at end of a successful session
#   ./scripts/session-complete-marker.sh check [session-id]   # called at start to detect truncation
#
# Implements doc 12 §Future: Session token exhaustion is invisible: no detection when
# agent exits mid-queue due to budget.
#
# Design:
#   write: writes session_end={session-id,timestamp,status=COMPLETE} to _state branch.
#   check: reads _state, compares stored session_id against prior; if different session
#          AND no session_end marker was written for it, reports [SESSION TRUNCATED].
#
# Integration:
#   - Call `write` at the LAST step of standalone.md loop (after SM/PM phase completes)
#   - Call `check` at startup in coord.md §1a before claiming any work
#   - Both calls are no-ops when _state branch does not exist (first-run safety)
#
# Exit code: always 0 (fail-safe — never blocks the coordinator on own failure)

set -euo pipefail

COMMAND="${1:-check}"
SESSION_ID="${2:-${MY_SESSION_ID:-sess-unknown}}"
REPO="${REPO:-pnz1990/kardinal-promoter}"
REPORT_ISSUE="${REPORT_ISSUE:-892}"
OTHERNESS_VERSION="${OTHERNESS_VERSION:-unknown}"

# Fail-safe: exit cleanly if git not available
if ! command -v git &>/dev/null; then
  echo "[SESSION-MARKER SKIPPED — git not available]"
  exit 0
fi

# Fail-safe: exit cleanly if _state branch does not exist (first-run)
if ! git ls-remote --heads origin _state 2>/dev/null | grep -q '_state'; then
  echo "[SESSION-MARKER SKIPPED — _state branch not found (first-run)]"
  exit 0
fi

case "$COMMAND" in
  write)
    _write_marker() {
      local tmp_wt
      tmp_wt=$(mktemp -d -t session-marker-XXXX)
      trap 'git worktree remove "$tmp_wt" --force 2>/dev/null; rm -rf "$tmp_wt"' EXIT

      git worktree add --no-checkout "$tmp_wt" origin/_state 2>/dev/null || {
        echo "[SESSION-MARKER] Could not create worktree — skipping write"
        return
      }

      git -C "$tmp_wt" checkout _state -- '.otherness/state.json' 2>/dev/null || true

      local state_file="$tmp_wt/.otherness/state.json"
      mkdir -p "$(dirname "$state_file")"

      local timestamp
      timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "")

      # Read and update state
      python3 - <<PYEOF
import json, os, sys

state_file = '${state_file}'
session_id = '${SESSION_ID}'
timestamp = '${timestamp}'

try:
    with open(state_file) as f:
        s = json.load(f)
except Exception:
    s = {}

s['session_end'] = {
    'session_id': session_id,
    'timestamp': timestamp,
    'status': 'COMPLETE',
}

with open(state_file, 'w') as f:
    json.dump(s, f, indent=2)
print(f"[SESSION-MARKER] session_end marker written for {session_id}")
PYEOF

      git -C "$tmp_wt" add '.otherness/state.json' 2>/dev/null
      git -C "$tmp_wt" commit -m "session: ${SESSION_ID} completed at ${timestamp}" 2>/dev/null || true

      local result
      result=$(git -C "$tmp_wt" push origin HEAD:_state 2>&1)
      if echo "$result" | grep -q "error\|rejected"; then
        echo "[SESSION-MARKER] Push failed (non-fatal): $result"
      else
        echo "[SESSION-MARKER] session_end marker persisted to _state"
      fi

      git worktree remove "$tmp_wt" --force 2>/dev/null || true
      trap - EXIT
    }
    _write_marker
    ;;

  check)
    _check_marker() {
      # Fetch latest _state
      git fetch origin _state --quiet 2>/dev/null || true

      local state_json
      state_json=$(git show origin/_state:.otherness/state.json 2>/dev/null) || {
        echo "[SESSION-MARKER] Could not read _state/.otherness/state.json — skipping check"
        return
      }

      python3 - <<PYEOF
import json, sys, subprocess, os

state_json = '''${state_json}'''
current_session = '${SESSION_ID}'
repo = '${REPO}'
report_issue = '${REPORT_ISSUE}'
otherness_version = '${OTHERNESS_VERSION}'

try:
    s = json.loads(state_json)
except Exception as e:
    print(f"[SESSION-MARKER] Could not parse state.json: {e} — skipping check")
    sys.exit(0)

session_end = s.get('session_end', {})
last_session_id = session_end.get('session_id', '')
last_status = session_end.get('status', '')
last_timestamp = session_end.get('timestamp', 'unknown')

# Compute last session from heartbeats (most recent heartbeat that isn't ours)
heartbeats = s.get('session_heartbeats', {})
other_sessions = {sid: hb for sid, hb in heartbeats.items() if sid != current_session}

if not other_sessions:
    print('[SESSION-MARKER] No prior sessions in heartbeats — skipping truncation check')
    sys.exit(0)

# Find most recent prior session
import datetime
def parse_ts(ts):
    try:
        return datetime.datetime.fromisoformat(ts.replace('Z', '+00:00'))
    except Exception:
        return datetime.datetime.min.replace(tzinfo=datetime.timezone.utc)

last_hb_session = max(other_sessions.items(),
    key=lambda x: parse_ts(x[1].get('last_seen', '')))[0]
last_hb_ts = other_sessions[last_hb_session].get('last_seen', 'unknown')

# Check: did the last heartbeat session write a COMPLETE marker?
if last_session_id == last_hb_session and last_status == 'COMPLETE':
    print(f'[SESSION-MARKER] Prior session {last_hb_session} completed normally at {last_timestamp}')
    sys.exit(0)

# Truncation detected: last heartbeat session != session that wrote COMPLETE
now = datetime.datetime.now(datetime.timezone.utc)
hb_age = (now - parse_ts(last_hb_ts)).total_seconds() / 3600

if hb_age < 2:
    # Recent heartbeat — prior session may still be running (not truncated)
    print(f'[SESSION-MARKER] Prior session {last_hb_session} heartbeat {hb_age:.1f}h ago — may still be running')
    sys.exit(0)

# Stale heartbeat with no COMPLETE marker = truncation
msg = (f'[SESSION TRUNCATED | {current_session} | otherness@{otherness_version}] '
       f'Prior session {last_hb_session} (last heartbeat: {last_hb_ts}) did not write a '
       f'session_end=COMPLETE marker. The session likely ran out of token budget mid-queue. '
       f'Items claimed by that session may be in limbo. '
       f'Check for stale in_progress items in _state:.otherness/state.json.')
print(msg)

# Post to report issue (non-blocking)
try:
    r = subprocess.run(['gh', 'issue', 'comment', report_issue, '--repo', repo,
                        '--body', msg],
                       capture_output=True, text=True, timeout=15)
    if r.returncode == 0:
        print(f'[SESSION-MARKER] Truncation reported to issue #{report_issue}')
except Exception as e:
    print(f'[SESSION-MARKER] Could not post truncation comment (non-fatal): {e}')
PYEOF
    }
    _check_marker
    ;;

  *)
    echo "Usage: $0 <write|check> [session-id]"
    echo "  write: write session_end=COMPLETE marker to _state branch"
    echo "  check: detect if prior session ended without COMPLETE marker"
    exit 0
    ;;
esac
