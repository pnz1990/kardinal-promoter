#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# check-backlog-overflow.sh — SM backlog overflow alarm
#
# Usage: ./scripts/check-backlog-overflow.sh [threshold]
# Default threshold: 40 items
#
# Counts all 🔲 (U+1F7B2) Future items across docs/design/*.md.
# If count exceeds the threshold, outputs:
#   [BACKLOG OVERFLOW — N unqueued items]
# and posts a warning (but does NOT exit non-zero — this is an alarm, not a gate).
#
# Design ref: docs/design/12-autonomous-loop-discipline.md
# Called from SM §4b batch report to detect backlog accumulation.

set -euo pipefail

THRESHOLD="${1:-${BACKLOG_OVERFLOW_THRESHOLD:-40}}"
DESIGN_DIR="${DESIGN_DIR:-docs/design}"

if [ ! -d "$DESIGN_DIR" ]; then
  echo "[backlog-check] $DESIGN_DIR not found — skipping"
  exit 0
fi

# Count 🔲 items (unqueued future items)
# grep -rc returns "file:count" lines; awk sums the counts
BACKLOG=$(grep -rc "🔲" "$DESIGN_DIR" 2>/dev/null | \
  awk -F: '{sum+=$2} END{print sum}' 2>/dev/null || echo "0")

echo "[backlog-check] Current backlog: ${BACKLOG} unqueued 🔲 items (threshold: ${THRESHOLD})"

if [ "${BACKLOG:-0}" -gt "${THRESHOLD}" ]; then
  echo "[BACKLOG OVERFLOW — ${BACKLOG} unqueued items]"
  echo "[backlog-check] COORDINATOR should spend next batch exclusively burning down backlog."
else
  echo "[backlog-check] Backlog within threshold (${BACKLOG} ≤ ${THRESHOLD})."
fi

# Always exit 0 — this is an alarm, not a gate
exit 0
