#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# check-pressure-context-drift.sh — detect unauthorized pressure context changes
#
# Usage: ./scripts/check-pressure-context-drift.sh [workflow-file]
# Default: .github/workflows/otherness-scheduled.yml
#
# Checks whether the "Context for this vision scan:" block in the otherness
# scheduled workflow has changed in the latest commit (HEAD~ vs HEAD). If it
# has, outputs:
#   [HUMAN REVIEW REQUIRED: pressure context changed]
# and exits non-zero — causing CI to fail and requiring a human to approve
# the change before it can be merged.
#
# Rationale: the pressure context defines the bar against which agents are
# evaluated. Agents can (and do) rewrite it. An agent that lowers the pressure
# bar before executing is a conflict of interest. This check ensures any change
# to the pressure context is explicitly visible in CI and requires human sign-off.
#
# Design ref: docs/design/12-autonomous-loop-discipline.md
# Integrated in: .github/workflows/ci.yml docs-lint job

set -euo pipefail

WORKFLOW_FILE="${WORKFLOW_FILE:-${1:-.github/workflows/otherness-scheduled.yml}}"

# Fail-safe: exit 0 if workflow file doesn't exist
if [ ! -f "$WORKFLOW_FILE" ]; then
  echo "[pressure-check] $WORKFLOW_FILE not found — skipping"
  exit 0
fi

# Fail-safe: exit 0 if no git history (first commit)
if ! git rev-parse HEAD~ >/dev/null 2>&1; then
  echo "[pressure-check] No prior commit — skipping (first commit)"
  exit 0
fi

# Check if the "Context for this vision scan:" line was modified in the last commit
CHANGED=$(git diff HEAD~ HEAD -- "$WORKFLOW_FILE" 2>/dev/null | \
  grep "^+" | grep -v "^+++" | grep "Context for this vision scan:" || true)

if [ -z "$CHANGED" ]; then
  echo "[pressure-check] Pressure context unchanged — OK"
  exit 0
fi

echo ""
echo "[HUMAN REVIEW REQUIRED: pressure context changed]"
echo ""
echo "The 'Context for this vision scan:' block in $WORKFLOW_FILE was modified."
echo "This block defines the evaluation bar for the autonomous agent."
echo "Changes to it must be reviewed and approved by a human before merge."
echo ""
echo "Changed lines:"
echo "$CHANGED"
echo ""
echo "If this change is intentional, a human must:"
echo "  1. Review the new pressure context"
echo "  2. Approve the PR with a comment explaining why the change is appropriate"
echo "  3. Merge the PR manually (CI failure will not block human merge)"
exit 1
