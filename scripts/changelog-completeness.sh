#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# changelog-completeness.sh — PM §5n: verify every git tag has a CHANGELOG.md entry
#
# Usage: ./scripts/changelog-completeness.sh [repo] [report-issue]
#
# Implements docs/design/41-published-docs-freshness.md §Future:
# "Changelog completeness — every git tag must have a corresponding entry in CHANGELOG.md.
#  PM §5n-changelog: scan git tags, compare against CHANGELOG.md ## [vX.Y.Z] headers.
#  For each missing entry: open kind/docs issue."
#
# Exit code: always 0 (fail-open — never blocks the SM)

set -euo pipefail

REPO="${1:-${REPO:-pnz1990/kardinal-promoter}}"
REPORT_ISSUE="${2:-${REPORT_ISSUE:-892}}"
MY_SESSION_ID="${MY_SESSION_ID:-sess-unknown}"
OTHERNESS_VERSION="${OTHERNESS_VERSION:-unknown}"

# Fail-open: gh not available
if ! command -v gh &>/dev/null; then
  echo "[CHANGELOG-COMPLETENESS SKIPPED — gh CLI not available]"
  exit 0
fi

# Fail-open: CHANGELOG.md not present
if [ ! -f "CHANGELOG.md" ]; then
  echo "[CHANGELOG-COMPLETENESS SKIPPED — CHANGELOG.md not found]"
  exit 0
fi

# Step 1: Get all git tags matching vMAJOR.MINOR.PATCH
GIT_TAGS=$(git tag --list 'v*' 2>/dev/null | sort -V || echo "")
if [ -z "$GIT_TAGS" ]; then
  echo "[CHANGELOG-COMPLETENESS SKIPPED — no git tags found]"
  exit 0
fi

echo "[CHANGELOG-COMPLETENESS] Found tags: $(echo "$GIT_TAGS" | tr '\n' ' ')"

# Step 2: Extract existing version headers from CHANGELOG.md
# Matches lines like: ## [v0.8.1] — 2026-04-17  or  ## [v0.8.1]
CHANGELOG_VERSIONS=$(grep -oE '\[v[0-9]+\.[0-9]+\.[0-9]+\]' CHANGELOG.md 2>/dev/null \
  | tr -d '[]' \
  | sort -V \
  || echo "")

echo "[CHANGELOG-COMPLETENESS] CHANGELOG.md versions: $(echo "$CHANGELOG_VERSIONS" | tr '\n' ' ')"

# Step 3: For each git tag, check if there is a CHANGELOG entry
ISSUES_OPENED=0
MISSING_TAGS=()

while IFS= read -r TAG; do
  [ -z "$TAG" ] && continue
  # Only check semver tags (vMAJOR.MINOR.PATCH)
  if ! echo "$TAG" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    continue
  fi

  if echo "$CHANGELOG_VERSIONS" | grep -qF "$TAG"; then
    echo "[CHANGELOG-COMPLETENESS] OK: $TAG is in CHANGELOG.md"
  else
    echo "[CHANGELOG-COMPLETENESS] MISSING: $TAG has no CHANGELOG.md entry"
    MISSING_TAGS+=("$TAG")
  fi
done <<< "$GIT_TAGS"

# Step 4: Open issues for missing tags (dedup guard)
for MISSING_TAG in "${MISSING_TAGS[@]:-}"; do
  [ -z "$MISSING_TAG" ] && continue

  ISSUE_TITLE="docs(changelog): missing CHANGELOG.md entry for ${MISSING_TAG}"

  # Dedup: skip if issue already open
  EXISTING=$(gh issue list \
    --repo "$REPO" \
    --state open \
    --search "$ISSUE_TITLE" \
    --json number \
    --jq 'length' 2>/dev/null || echo "1")

  if [ "${EXISTING:-1}" -gt 0 ]; then
    echo "[CHANGELOG-COMPLETENESS] Issue already open for $MISSING_TAG — skipping"
    continue
  fi

  # Get the release date for this tag from git log
  TAG_DATE=$(git log -1 --format="%as" "$MISSING_TAG" 2>/dev/null || echo "unknown")

  # Get the PR list between this tag and the previous tag (for changelog content)
  PREV_TAG=$(git tag --list 'v*' 2>/dev/null | sort -V | grep -B1 "^${MISSING_TAG}$" | head -1 || echo "")
  if [ -z "$PREV_TAG" ] || [ "$PREV_TAG" = "$MISSING_TAG" ]; then
    COMMITS_SINCE="(first release)"
  else
    COMMITS_SINCE=$(git log "${PREV_TAG}..${MISSING_TAG}" --oneline --no-walk=unsorted 2>/dev/null \
      | grep -E "^[a-f0-9]+ (feat|fix|docs|chore|refactor|perf|test|ci)" \
      | head -20 \
      || echo "(no conventional commits found)")
  fi

  ISSUE_BODY="## Missing CHANGELOG.md entry

**Tag**: \`${MISSING_TAG}\`
**Release date**: ${TAG_DATE}

The git tag \`${MISSING_TAG}\` has no corresponding entry in \`CHANGELOG.md\`.

## What to do

Add a section to \`CHANGELOG.md\` with the format:

\`\`\`markdown
## [${MISSING_TAG}] — ${TAG_DATE}

<list of notable changes>
\`\`\`

## Commits in this release

\`\`\`
${COMMITS_SINCE}
\`\`\`

## Design reference

- **Design doc**: \`docs/design/41-published-docs-freshness.md\`
- **Section**: Changelog completeness — PM §5n-changelog

Detected by: PM §5n-changelog changelog-completeness.sh | ${MY_SESSION_ID} | otherness@${OTHERNESS_VERSION}"

  CREATE_RESULT=$(gh issue create \
    --repo "$REPO" \
    --title "$ISSUE_TITLE" \
    --label "kind/docs,area/docs,priority/medium,size/xs" \
    --body "$ISSUE_BODY" 2>/dev/null || echo "")

  if [ -n "$CREATE_RESULT" ]; then
    echo "[CHANGELOG-COMPLETENESS] Opened issue: $CREATE_RESULT"
    ISSUES_OPENED=$((ISSUES_OPENED + 1))
  else
    echo "[CHANGELOG-COMPLETENESS] Failed to open issue for $MISSING_TAG (non-fatal)"
  fi
done

# Step 5: Post summary to REPORT_ISSUE
MISSING_COUNT="${#MISSING_TAGS[@]}"
if [ "$ISSUES_OPENED" -gt 0 ]; then
  SUMMARY="[📋 PM §5n-changelog | ${MY_SESSION_ID} | otherness@${OTHERNESS_VERSION}] Changelog completeness check: ${MISSING_COUNT} missing tag(s). Opened ${ISSUES_OPENED} issue(s)."
else
  SUMMARY="[📋 PM §5n-changelog | ${MY_SESSION_ID} | otherness@${OTHERNESS_VERSION}] Changelog completeness check: all $(echo "$GIT_TAGS" | wc -l | tr -d ' ') tags have CHANGELOG.md entries. No issues needed."
fi

gh issue comment "$REPORT_ISSUE" --repo "$REPO" --body "$SUMMARY" 2>/dev/null || true

echo "[CHANGELOG-COMPLETENESS] Done. Missing: ${MISSING_COUNT}. Issues opened: $ISSUES_OPENED"
exit 0
