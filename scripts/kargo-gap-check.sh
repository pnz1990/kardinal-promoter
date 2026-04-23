#!/usr/bin/env bash
# kargo-gap-check.sh — automated competitive gap detection vs Kargo
#
# Fetches the 20 newest open Kargo issues labeled kind/enhancement or kind/feature,
# cross-references against existing 🔲 items in docs/design/15-production-readiness.md,
# and outputs any Kargo request with >5 thumbsup reactions that has no matching
# keyword coverage in doc-15. The PM reviews output and decides whether to add a gap.
#
# Usage:
#   bash scripts/kargo-gap-check.sh
#   bash scripts/kargo-gap-check.sh --min-reactions 0  # show all with 0+ reactions
#   bash scripts/kargo-gap-check.sh --json             # machine-readable output
#
# Exits 0 on success (even with no gaps found).
# Exits 1 on API or IO error.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

set -euo pipefail

KARGO_REPO="akuity/kargo"
DOC15="docs/design/15-production-readiness.md"
MIN_REACTIONS=5
JSON_OUTPUT=false

# Parse args
while [[ $# -gt 0 ]]; do
  case "$1" in
    --min-reactions)
      MIN_REACTIONS="$2"
      shift 2
      ;;
    --json)
      JSON_OUTPUT=true
      shift
      ;;
    *)
      echo "Unknown arg: $1" >&2
      exit 1
      ;;
  esac
done

# Verify doc-15 exists
if [ ! -f "$DOC15" ]; then
  echo "ERROR: $DOC15 not found. Run from repo root." >&2
  exit 1
fi

# Verify gh CLI is available
if ! command -v gh &>/dev/null; then
  echo "ERROR: gh CLI not found." >&2
  exit 1
fi

echo "=== Kargo Competitive Gap Check ==="
echo "Source: https://github.com/${KARGO_REPO}/issues"
echo "Cross-referencing: ${DOC15}"
echo "Min reactions: ${MIN_REACTIONS}"
echo ""

# Read doc-15 content for cross-reference (lowercase for matching)
DOC15_CONTENT=$(tr '[:upper:]' '[:lower:]' < "$DOC15")

# Fetch top 20 open Kargo issues with kind/enhancement label
KARGO_ISSUES=$(gh api \
  "repos/${KARGO_REPO}/issues?state=open&labels=kind%2Fenhancement&per_page=20&sort=created&direction=desc" \
  --jq '[.[] | {
    number: .number,
    title: .title,
    url: .html_url,
    reactions: .reactions."+1",
    body_excerpt: (.body // "" | .[0:200])
  }]' 2>/dev/null) || {
  echo "ERROR: Failed to fetch Kargo issues. Check gh auth status." >&2
  exit 1
}

ISSUE_COUNT=$(echo "$KARGO_ISSUES" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
echo "Fetched ${ISSUE_COUNT} open kind/enhancement issues from ${KARGO_REPO}"
echo ""

# Cross-reference each issue against doc-15 content
GAP_COUNT=0
GAPS_JSON="[]"

while IFS= read -r issue_json; do
  number=$(echo "$issue_json" | python3 -c "import json,sys; d=json.loads(sys.stdin.read()); print(d['number'])" 2>/dev/null)
  title=$(echo "$issue_json" | python3 -c "import json,sys; d=json.loads(sys.stdin.read()); print(d['title'])" 2>/dev/null)
  url=$(echo "$issue_json" | python3 -c "import json,sys; d=json.loads(sys.stdin.read()); print(d['url'])" 2>/dev/null)
  reactions=$(echo "$issue_json" | python3 -c "import json,sys; d=json.loads(sys.stdin.read()); print(d['reactions'] or 0)" 2>/dev/null)

  # Skip if below reaction threshold
  if [ "${reactions}" -lt "${MIN_REACTIONS}" ] 2>/dev/null; then
    continue
  fi

  # Extract keywords from title (words > 4 chars, lowercased, no common words)
  STOPWORDS="with from this that will have been would could should their there which"
  KEYWORDS=$(echo "$title" | tr '[:upper:]' '[:lower:]' | \
    tr -cs 'a-z0-9' ' ' | \
    tr ' ' '\n' | \
    awk 'length > 4' | \
    grep -vwE "$(echo $STOPWORDS | tr ' ' '|')" | \
    sort -u | head -10)

  # Check if any keyword appears in doc-15
  FOUND=false
  for kw in $KEYWORDS; do
    if echo "$DOC15_CONTENT" | grep -qF "$kw" 2>/dev/null; then
      FOUND=true
      break
    fi
  done

  if [ "$FOUND" = "false" ]; then
    GAP_COUNT=$((GAP_COUNT + 1))
    if [ "$JSON_OUTPUT" = "false" ]; then
      echo "GAP #${number} (${reactions}👍): ${title}"
      echo "  ${url}"
      echo ""
    else
      GAPS_JSON=$(echo "$GAPS_JSON" | python3 -c "
import json, sys
gaps = json.load(sys.stdin)
gaps.append({'number': ${number}, 'reactions': ${reactions}, 'title': '${title//\'/\\\'}', 'url': '${url}'})
print(json.dumps(gaps))
" 2>/dev/null || echo "$GAPS_JSON")
    fi
  fi
done < <(echo "$KARGO_ISSUES" | python3 -c "
import json, sys
issues = json.load(sys.stdin)
for issue in issues:
    print(json.dumps(issue))
" 2>/dev/null)

if [ "$JSON_OUTPUT" = "true" ]; then
  echo "$GAPS_JSON"
else
  if [ "$GAP_COUNT" -eq 0 ]; then
    echo "✅ No untracked Kargo enhancement gaps found (${ISSUE_COUNT} issues checked, threshold: ${MIN_REACTIONS} reactions)."
  else
    echo "Found ${GAP_COUNT} potential gap(s) not covered in ${DOC15}."
    echo ""
    echo "PM action: review the gaps above and add relevant ones as '- 🔲 ⚠️ Inferred: ...' items"
    echo "in docs/design/15-production-readiness.md under the appropriate Lens."
  fi
fi

exit 0
