#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# adversary-check.sh — adversarial architecture review before queue inclusion
#
# Usage: ./scripts/adversary-check.sh <issue-number> [repo]
#
# Implements doc 12 §Future: Monoculture break — adversarial agent role.
# Evaluates a proposed queue item against three failure-mode lenses:
#   (a) Exact mechanism — forces naming the API, CRD field, or function
#   (b) Blast radius — forces enumerating what breaks if this is wrong
#   (c) Competing approach — forces comparison against an external reference
#
# Output: structured [🔴 ADVERSARY] block with VERDICT: PROCEED or CHALLENGE
# Exit code: always 0 (fail-safe — never blocks queue generation on own failure)
#
# Integration: otherness-config.yaml adversary.enabled=true activates this check
# in COORDINATOR §1c before an item is added to the queue.

set -euo pipefail

ISSUE_NUM="${1:-}"
REPO="${2:-${REPO:-pnz1990/kardinal-promoter}}"

# Fail safe: missing issue number
if [ -z "$ISSUE_NUM" ]; then
  echo "[ADVERSARY SKIPPED — no issue number provided]"
  exit 0
fi

# Fail safe: gh not available
if ! command -v gh &>/dev/null; then
  echo "[ADVERSARY SKIPPED — gh CLI not available]"
  exit 0
fi

# Fetch issue title and body
ISSUE_DATA=$(gh issue view "$ISSUE_NUM" --repo "$REPO" \
  --json title,body,labels \
  --jq '{title: .title, body: .body, labels: [.labels[].name]}' 2>/dev/null) || {
  echo "[ADVERSARY SKIPPED — could not fetch issue #$ISSUE_NUM from $REPO]"
  exit 0
}

TITLE=$(echo "$ISSUE_DATA" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('title',''))" 2>/dev/null || echo "")
BODY=$(echo "$ISSUE_DATA" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('body',''))" 2>/dev/null || echo "")
LABELS=$(echo "$ISSUE_DATA" | python3 -c "import json,sys; d=json.load(sys.stdin); print(','.join(d.get('labels',[])))" 2>/dev/null || echo "")

# Classify the item kind
KIND="enhancement"
if echo "$LABELS" | grep -q "kind/chore\|kind/docs"; then
  KIND="housekeeping"
fi
if echo "$LABELS" | grep -q "kind/bug"; then
  KIND="bugfix"
fi

# Housekeeping and bugfixes pass automatically — adversary only challenges enhancements
if [ "$KIND" = "housekeeping" ]; then
  echo "[🔴 ADVERSARY | issue-#$ISSUE_NUM]"
  echo "Title: $TITLE"
  echo "Kind: $KIND (housekeeping auto-pass)"
  echo "WHAT WOULD BREAK THIS: N/A — housekeeping item has no architectural surface"
  echo "BLAST RADIUS: Minimal — no new behavior, bounded change"
  echo "COMPETING APPROACH: N/A"
  echo "VERDICT: PROCEED"
  exit 0
fi

# Extract design doc reference if present
DESIGN_DOC=$(echo "$BODY" | grep -oP '(?<=Design doc\*\*: `)docs/design/[^`]+(?=`)' | head -1 || echo "")

# Lens (a): Exact mechanism check
# Does the issue body name a specific function, CRD field, API, or config key?
MECHANISM_SCORE=0
MECHANISM_NOTE=""
if echo "$BODY" | grep -qiE '`[a-z][A-Za-z0-9_/.-]+`|func |\.spec\.|\.status\.|pkg/|cmd/|api/'; then
  MECHANISM_SCORE=1
  MECHANISM_NOTE="Issue references specific code artifact"
else
  MECHANISM_NOTE="Issue does not name a specific function, CRD field, or package path — mechanism is abstract"
fi

# Lens (b): Blast radius check
# Does the item touch known high-risk areas?
BLAST_SCORE=0
BLAST_NOTE=""
BLAST_AREAS=""
if echo "$TITLE$BODY" | grep -qiE 'reconciler|controller|crd|graph|krocodile|cel|schedule|policygate'; then
  BLAST_SCORE=1
  BLAST_AREAS="controller/reconciler path"
fi
if echo "$TITLE$BODY" | grep -qiE 'merge|branch.protect|workflow|ci|yaml'; then
  BLAST_SCORE=1
  BLAST_AREAS="${BLAST_AREAS:+$BLAST_AREAS, }CI/workflow"
fi
if echo "$TITLE$BODY" | grep -qiE 'state\.json|_state|queue|session'; then
  BLAST_SCORE=1
  BLAST_AREAS="${BLAST_AREAS:+$BLAST_AREAS, }agent state"
fi
if [ "$BLAST_SCORE" -eq 0 ]; then
  BLAST_NOTE="No high-risk areas detected — bounded change"
else
  BLAST_NOTE="Touches: ${BLAST_AREAS:-unknown}. Review idempotency and failure modes."
fi

# Lens (c): Competing approach
# Does the issue reference any external project (Kargo, GitOps Promoter, Flux, etc.)?
COMPETING_NOTE=""
COMPETING_REF=0
if echo "$TITLE$BODY" | grep -qiE 'kargo|gitops.promoter|flux|argo.?rollout|competitor'; then
  COMPETING_NOTE="Issue references a competing design as context"
  COMPETING_REF=1
else
  # Check if the item is in a domain with known external references
  if echo "$TITLE$BODY" | grep -qiE 'promotion|pipeline|bundle|policygate|health.check'; then
    COMPETING_NOTE="Domain has competing implementations (Kargo/GitOps Promoter) — no explicit comparison found"
    COMPETING_REF=0
  else
    COMPETING_NOTE="No competing approach reference required for this domain"
    COMPETING_REF=1  # Pass by default for non-product areas
  fi
fi

# Determine verdict
# CHALLENGE if: mechanism is abstract AND touches high-risk area
# PROCEED if: mechanism named OR low blast radius OR competing ref present
VERDICT="PROCEED"
CHALLENGE_REASONS=""

if [ "$MECHANISM_SCORE" -eq 0 ] && [ "$BLAST_SCORE" -eq 1 ]; then
  VERDICT="CHALLENGE"
  CHALLENGE_REASONS="Mechanism is abstract AND touches high-risk area ($BLAST_AREAS)"
fi

if [ "$COMPETING_REF" -eq 0 ] && [ "$BLAST_SCORE" -eq 1 ]; then
  # Escalate to CHALLENGE if both mechanism and competing ref are missing
  if [ "$MECHANISM_SCORE" -eq 0 ]; then
    VERDICT="CHALLENGE"
    CHALLENGE_REASONS="${CHALLENGE_REASONS:+$CHALLENGE_REASONS; }No competing reference for product-domain item"
  fi
fi

# Print structured output
echo "[🔴 ADVERSARY | issue-#$ISSUE_NUM | $(date -u '+%Y-%m-%dT%H:%M:%SZ')]"
echo "Title: $TITLE"
echo "Kind: $KIND"
if [ -n "$DESIGN_DOC" ]; then
  echo "Design doc: $DESIGN_DOC"
fi
echo ""
echo "WHAT WOULD BREAK THIS:"
echo "  (a) Mechanism — $MECHANISM_NOTE"
echo "  (b) Blast radius — $BLAST_NOTE"
echo "  (c) Competing approach — $COMPETING_NOTE"
echo ""
if [ "$VERDICT" = "CHALLENGE" ]; then
  echo "CHALLENGE REASONS: $CHALLENGE_REASONS"
  echo ""
  echo "VERDICT: CHALLENGE"
  echo "  Recommended action: add specific mechanism details or competing design comparison"
  echo "  before queuing. The COORDINATOR may proceed anyway with this log as the record."
else
  echo "VERDICT: PROCEED"
fi
