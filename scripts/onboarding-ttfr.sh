#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# onboarding-ttfr.sh — track and publish onboarding time-to-first-run metric
#
# Usage:
#   ./scripts/onboarding-ttfr.sh              # Record onboard_started_at if not set
#   ./scripts/onboarding-ttfr.sh --mark-success  # Record first_run_succeeded_at if not set
#   ./scripts/onboarding-ttfr.sh --sm-report     # Output time_to_first_run for SM batch report
#
# Implements docs/design/12-autonomous-loop-discipline.md §Future:
# "Onboarding time-to-first-run metric: track and publish the setup duration"
#
# Writes onboarding: section to otherness-config.yaml.
# Only outputs time_to_first_run in first 3 batches (batch_count <= 3).
#
# Exit code: always 0 (fail-open — never blocks the SM)

set -euo pipefail

CONFIG_FILE="${CONFIG_FILE:-otherness-config.yaml}"
STATE_FILE="${STATE_FILE:-.otherness/state.json}"

MODE="${1:-}"  # --mark-success | --sm-report | (empty = record start)

# Fail-open: config file must exist
if [ ! -f "$CONFIG_FILE" ]; then
  echo "[ONBOARDING-TTFR SKIPPED — $CONFIG_FILE not found]"
  exit 0
fi

# Helper: get current UTC timestamp in ISO 8601
_now_iso() {
  python3 -c "import datetime; print(datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'))" 2>/dev/null || \
  date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || \
  echo ""
}

# Helper: read a value from otherness-config.yaml onboarding section
_read_config() {
  local key="$1"
  python3 - <<PYEOF 2>/dev/null || echo ""
import re
try:
    in_onboarding = False
    for line in open('$CONFIG_FILE'):
        s = re.match(r'^(\w[\w_]*):', line)
        if s: in_onboarding = (s.group(1) == 'onboarding')
        if in_onboarding:
            m = re.match(r'^\s+$key:\s*["\']?([^"\'#\n]+)["\']?', line)
            if m: print(m.group(1).strip()); break
except Exception:
    pass
PYEOF
}

# Helper: write a key/value to the onboarding section of otherness-config.yaml
# Creates the section if it doesn't exist; does NOT overwrite existing values.
_write_config_if_absent() {
  local key="$1"
  local value="$2"

  python3 - <<PYEOF 2>/dev/null
import re, os
key = '$key'
value = '$value'
config_path = '$CONFIG_FILE'

try:
    content = open(config_path).read()

    # Check if key already exists under onboarding:
    in_onboarding = False
    found = False
    for line in content.splitlines():
        s = re.match(r'^(\w[\w_]*):', line)
        if s: in_onboarding = (s.group(1) == 'onboarding')
        if in_onboarding:
            m = re.match(r'^\s+' + re.escape(key) + r':\s*\S', line)
            if m: found = True; break

    if found:
        print(f'[ONBOARDING-TTFR] {key} already set — skipping')
        exit(0)

    # Check if onboarding: section exists
    if 'onboarding:' in content:
        # Append after the onboarding: header line
        content = re.sub(
            r'(^onboarding:[ \t]*\n)',
            f'\\1  {key}: {value}\n',
            content, count=1, flags=re.MULTILINE)
    else:
        # Append new section at end
        content += f'\n# ── Onboarding metrics ─────────────────────────────────────────────────────────\nonboarding:\n  {key}: {value}\n'

    with open(config_path, 'w') as f:
        f.write(content)
    print(f'[ONBOARDING-TTFR] Wrote {key}={value}')
except Exception as e:
    print(f'[ONBOARDING-TTFR] Write error (non-fatal): {e}')
PYEOF
}

# Helper: read batch_count from state.json
_batch_count() {
  python3 -c "
import json
try:
    s = json.load(open('$STATE_FILE'))
    print(s.get('batch_count', 0))
except:
    print(0)
" 2>/dev/null || echo "0"
}

# Helper: compute minutes between two ISO 8601 timestamps
_diff_minutes() {
  local start="$1"
  local end="$2"
  python3 -c "
import datetime
try:
    fmt = '%Y-%m-%dT%H:%M:%SZ'
    t1 = datetime.datetime.strptime('$start', fmt).replace(tzinfo=datetime.timezone.utc)
    t2 = datetime.datetime.strptime('$end', fmt).replace(tzinfo=datetime.timezone.utc)
    mins = int((t2 - t1).total_seconds() / 60)
    print(max(0, mins))
except Exception:
    print(-1)
" 2>/dev/null || echo "-1"
}

# ── Mode: record onboard_started_at ─────────────────────────────────────────
if [ -z "$MODE" ]; then
  NOW=$(_now_iso)
  if [ -z "$NOW" ]; then
    echo "[ONBOARDING-TTFR SKIPPED — could not get current time]"
    exit 0
  fi
  _write_config_if_absent "onboard_started_at" "$NOW"
  exit 0
fi

# ── Mode: --mark-success ─────────────────────────────────────────────────────
if [ "$MODE" = "--mark-success" ]; then
  NOW=$(_now_iso)
  if [ -z "$NOW" ]; then
    echo "[ONBOARDING-TTFR SKIPPED — could not get current time]"
    exit 0
  fi
  _write_config_if_absent "first_run_succeeded_at" "$NOW"
  exit 0
fi

# ── Mode: --sm-report ────────────────────────────────────────────────────────
if [ "$MODE" = "--sm-report" ]; then
  # Only report in first 3 batches
  BATCH_COUNT=$(_batch_count)
  if [ "${BATCH_COUNT:-0}" -gt 3 ]; then
    exit 0  # Metric has served its purpose
  fi

  STARTED=$(_read_config "onboard_started_at")
  SUCCEEDED=$(_read_config "first_run_succeeded_at")

  if [ -z "$STARTED" ]; then
    echo "time_to_first_run: not measured (onboard_started_at not set)"
    exit 0
  fi

  if [ -z "$SUCCEEDED" ]; then
    echo "time_to_first_run: pending (first_run_succeeded_at not set)"
    exit 0
  fi

  MINS=$(_diff_minutes "$STARTED" "$SUCCEEDED")
  if [ "${MINS:-0}" -lt 0 ]; then
    echo "time_to_first_run: calculation error"
  else
    echo "time_to_first_run: ${MINS}min (onboarded: $STARTED → first run: $SUCCEEDED)"
  fi
  exit 0
fi

echo "[ONBOARDING-TTFR] Unknown mode: $MODE"
exit 0
