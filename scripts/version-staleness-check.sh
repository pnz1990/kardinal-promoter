#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# version-staleness-check.sh — PM §5j: scan README and comparison docs for stale version strings
#
# Usage: ./scripts/version-staleness-check.sh [repo] [report-issue]
#
# Implements docs/design/41-published-docs-freshness.md §Future:
# "Version string freshness — PM §5j version staleness check: scan README.md and
#  comparison.md for hardcoded version strings (e.g. v0.6.0) and compare against
#  latest git tag. If stale by ≥1 minor version, open kind/docs issue. Dedup guard."
#
# Exit code: always 0 (fail-open — never blocks the SM)

set -euo pipefail

REPO="${1:-${REPO:-pnz1990/kardinal-promoter}}"
REPORT_ISSUE="${2:-${REPORT_ISSUE:-892}}"
MY_SESSION_ID="${MY_SESSION_ID:-sess-unknown}"
OTHERNESS_VERSION="${OTHERNESS_VERSION:-unknown}"

# Fail-open: gh not available
if ! command -v gh &>/dev/null; then
  echo "[VERSION-STALENESS SKIPPED — gh CLI not available]"
  exit 0
fi

# Step 1: Get latest git tag
LATEST_TAG=$(git tag --list 'v*' 2>/dev/null | sort -V | tail -1 || echo "")
if [ -z "$LATEST_TAG" ]; then
  echo "[VERSION-STALENESS SKIPPED — no git tags found]"
  exit 0
fi

echo "[VERSION-STALENESS] Latest tag: $LATEST_TAG"

# Parse latest version components
LATEST_MAJOR=$(echo "$LATEST_TAG" | python3 -c "import sys,re; m=re.match(r'v(\d+)\.(\d+)\.(\d+)',sys.stdin.read().strip()); print(m.group(1)) if m else print('0')" 2>/dev/null || echo "0")
LATEST_MINOR=$(echo "$LATEST_TAG" | python3 -c "import sys,re; m=re.match(r'v(\d+)\.(\d+)\.(\d+)',sys.stdin.read().strip()); print(m.group(2)) if m else print('0')" 2>/dev/null || echo "0")

# Step 2: Scan files for version strings
FILES_TO_SCAN="README.md docs/comparison.md"
ISSUES_OPENED=0

for SCAN_FILE in $FILES_TO_SCAN; do
  if [ ! -f "$SCAN_FILE" ]; then
    echo "[VERSION-STALENESS] $SCAN_FILE not found — skipping"
    continue
  fi

  echo "[VERSION-STALENESS] Scanning $SCAN_FILE..."

  # Extract all version strings matching vMAJOR.MINOR.PATCH
  FOUND_VERSIONS=$(grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' "$SCAN_FILE" 2>/dev/null | sort -uV || echo "")

  if [ -z "$FOUND_VERSIONS" ]; then
    echo "[VERSION-STALENESS] No version strings found in $SCAN_FILE"
    continue
  fi

  while IFS= read -r FOUND_VER; do
    [ -z "$FOUND_VER" ] && continue

    # Parse found version
    FOUND_MAJOR=$(echo "$FOUND_VER" | python3 -c "import sys,re; m=re.match(r'v(\d+)\.(\d+)\.(\d+)',sys.stdin.read().strip()); print(m.group(1)) if m else print('0')" 2>/dev/null || echo "0")
    FOUND_MINOR=$(echo "$FOUND_VER" | python3 -c "import sys,re; m=re.match(r'v(\d+)\.(\d+)\.(\d+)',sys.stdin.read().strip()); print(m.group(2)) if m else print('0')" 2>/dev/null || echo "0")

    # Staleness check: stale if found version is older by ≥1 minor version
    STALE=$(python3 -c "
found_major, found_minor = int('$FOUND_MAJOR'), int('$FOUND_MINOR')
latest_major, latest_minor = int('$LATEST_MAJOR'), int('$LATEST_MINOR')
# Stale if found < latest by ≥1 minor (same major), or found major < latest major
if found_major < latest_major:
    print('true')
elif found_major == latest_major and found_minor < latest_minor:
    print('true')
else:
    print('false')
" 2>/dev/null || echo "false")

    if [ "$STALE" = "true" ]; then
      echo "[VERSION-STALENESS] STALE: $FOUND_VER in $SCAN_FILE (latest: $LATEST_TAG)"

      # Build issue title
      ISSUE_TITLE="docs: stale version string in ${SCAN_FILE}: found ${FOUND_VER}, latest is ${LATEST_TAG}"

      # Dedup: skip if issue already open
      EXISTING=$(gh issue list \
        --repo "$REPO" \
        --state open \
        --search "$ISSUE_TITLE" \
        --json number \
        --jq 'length' 2>/dev/null || echo "1")

      if [ "${EXISTING:-1}" -gt 0 ]; then
        echo "[VERSION-STALENESS] Issue already open for $FOUND_VER in $SCAN_FILE — skipping"
        continue
      fi

      # Open issue
      ISSUE_BODY="## Version string staleness detected

**File**: \`${SCAN_FILE}\`
**Found version**: \`${FOUND_VER}\`
**Latest tag**: \`${LATEST_TAG}\`

The file contains a hardcoded version string that is behind the latest release by ≥1 minor version.

## What to do

Update \`${SCAN_FILE}\` to reference \`${LATEST_TAG}\` (or remove the hardcoded version if it is part of a badge/shield that auto-updates).

## Design reference

- **Design doc**: \`docs/design/41-published-docs-freshness.md\`
- **Section**: Version string freshness — PM §5j

Detected by: PM §5j version-staleness-check.sh | ${MY_SESSION_ID} | otherness@${OTHERNESS_VERSION}"

      CREATE_RESULT=$(gh issue create \
        --repo "$REPO" \
        --title "$ISSUE_TITLE" \
        --label "kind/docs,priority/high" \
        --body "$ISSUE_BODY" 2>/dev/null || echo "")

      if [ -n "$CREATE_RESULT" ]; then
        echo "[VERSION-STALENESS] Opened issue: $CREATE_RESULT"
        ISSUES_OPENED=$((ISSUES_OPENED + 1))
      else
        echo "[VERSION-STALENESS] Failed to open issue for $FOUND_VER in $SCAN_FILE (non-fatal)"
      fi
    else
      echo "[VERSION-STALENESS] OK: $FOUND_VER in $SCAN_FILE (latest: $LATEST_TAG)"
    fi
  done <<< "$FOUND_VERSIONS"
done

# Step 3: Post summary to REPORT_ISSUE
if [ "$ISSUES_OPENED" -gt 0 ]; then
  SUMMARY="[📋 PM §5j | ${MY_SESSION_ID} | otherness@${OTHERNESS_VERSION}] Version staleness check complete. ${ISSUES_OPENED} stale version issue(s) opened. Latest tag: ${LATEST_TAG}."
else
  SUMMARY="[📋 PM §5j | ${MY_SESSION_ID} | otherness@${OTHERNESS_VERSION}] Version staleness check complete. No stale versions found. Latest tag: ${LATEST_TAG}."
fi

gh issue comment "$REPORT_ISSUE" --repo "$REPO" --body "$SUMMARY" 2>/dev/null || true

echo "[VERSION-STALENESS] Done. Issues opened: $ISSUES_OPENED"
exit 0
