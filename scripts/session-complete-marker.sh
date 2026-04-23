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
#   write: writes session_end={session-id,timestamp,status=COMPLETE} to _state branch state.json.
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
      trap 'git worktree remove "$tmp_wt" --force 2>/dev/null || true; rm -rf "$tmp_wt" 2>/dev/null || true' EXIT

      git worktree add --no-checkout "$tmp_wt" origin/_state 2>/dev/null || {
        echo "[SESSION-MARKER] Could not create worktree — skipping write"
        return 0
      }

      # Checkout both files to preserve full _state commit tree
      git -C "$tmp_wt" checkout _state -- '.otherness/state.json' 2>/dev/null || true

      local state_file="$tmp_wt/.otherness/state.json"
      mkdir -p "$(dirname "$state_file")"

      local timestamp
      timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "unknown")

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
      git -C "$tmp_wt" commit -m "session: ${SESSION_ID} completed at ${timestamp}" 2>/dev/null || {
        echo "[SESSION-MARKER] Nothing to commit (already marked)"
        return 0
      }

      for attempt in 1 2 3; do
        if git -C "$tmp_wt" push origin HEAD:_state 2>/dev/null; then
          echo "[SESSION-MARKER] session_end written to _state (${SESSION_ID})"
          return 0
        fi
        echo "[SESSION-MARKER] Push conflict attempt ${attempt}/3 — retrying..."
        git -C "$tmp_wt" fetch origin _state --quiet 2>/dev/null || true
        git -C "$tmp_wt" reset --hard origin/_state 2>/dev/null || true
        # Re-apply the session_end field
        python3 - <<PYEOF2
import json, os
state_file = '${state_file}'
try:
    with open(state_file) as f: s = json.load(f)
except Exception: s = {}
s['session_end'] = {'session_id': '${SESSION_ID}', 'timestamp': '${timestamp}', 'status': 'COMPLETE'}
with open(state_file, 'w') as f: json.dump(s, f, indent=2)
PYEOF2
        git -C "$tmp_wt" add '.otherness/state.json' 2>/dev/null
        git -C "$tmp_wt" commit -m "session: ${SESSION_ID} completed at ${timestamp} (retry ${attempt})" 2>/dev/null || true
        sleep $(( attempt * 2 ))
      done
      echo "[SESSION-MARKER] Push failed after 3 attempts — skipping (non-fatal)"
    }
    _write_marker
    git worktree prune 2>/dev/null || true
    ;;

  check)
    _check_marker() {
      local tmp_wt
      tmp_wt=$(mktemp -d -t session-marker-check-XXXX)
      trap 'git worktree remove "$tmp_wt" --force 2>/dev/null || true; rm -rf "$tmp_wt" 2>/dev/null || true' EXIT

      git worktree add --no-checkout "$tmp_wt" origin/_state 2>/dev/null || {
        echo "[SESSION-MARKER CHECK SKIPPED — could not read _state]"
        return 0
      }

      git -C "$tmp_wt" checkout _state -- '.otherness/state.json' 2>/dev/null || {
        echo "[SESSION-MARKER CHECK] No state.json on _state — first run"
        return 0
      }

      python3 - <<PYEOF
import json, os, sys

state_file = '${tmp_wt}/.otherness/state.json'
current_session = '${SESSION_ID}'
REPO = '${REPO}'
REPORT_ISSUE = '${REPORT_ISSUE}'
MY_SESSION_ID = '${SESSION_ID}'
OTHERNESS_VERSION = '${OTHERNESS_VERSION}'

try:
    with open(state_file) as f:
        s = json.load(f)
except Exception as e:
    print(f"[SESSION-MARKER CHECK] Could not read state.json: {e} — skipping")
    sys.exit(0)

session_end = s.get('session_end', {})
stored_session = session_end.get('session_id', '')
stored_status = session_end.get('status', '')
stored_timestamp = session_end.get('timestamp', '')

if not stored_session:
    # No prior session recorded — clean start
    print("[SESSION-MARKER CHECK] No prior session_end marker — clean start")
    sys.exit(0)

if stored_session == current_session:
    # Same session continuing (unlikely but safe to handle)
    print(f"[SESSION-MARKER CHECK] Same session resumed: {current_session}")
    sys.exit(0)

if stored_status == 'COMPLETE':
    print(f"[SESSION-MARKER CHECK] Prior session {stored_session} completed cleanly at {stored_timestamp}")
    sys.exit(0)

# Different session, no COMPLETE marker — prior session was truncated
msg = (
    f"[SESSION TRUNCATED | {MY_SESSION_ID} | otherness@{OTHERNESS_VERSION}] "
    f"Prior session {stored_session} did not write a session_end=COMPLETE marker. "
    f"Last recorded status: {stored_status!r}. "
    f"Possible cause: token budget exhaustion mid-batch. "
    f"Check PR history for partially-claimed items."
)
print(msg)

import subprocess
try:
    subprocess.run(
        ['gh', 'issue', 'comment', REPORT_ISSUE, '--repo', REPO, '--body', msg],
        capture_output=True, timeout=15)
    print(f"[SESSION-MARKER CHECK] Truncation warning posted to issue #{REPORT_ISSUE}")
except Exception as e:
    print(f"[SESSION-MARKER CHECK] Could not post warning (non-fatal): {e}")
PYEOF
    }
    _check_marker
    git worktree prune 2>/dev/null || true
    ;;

  *)
    echo "Usage: $0 write|check [session-id]" >&2
    exit 0
    ;;
esac
