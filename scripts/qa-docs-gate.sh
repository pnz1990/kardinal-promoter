#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# qa-docs-gate.sh — QA §3b-docs-gate: verify docs/ updated when PR promotes a feature
#
# Usage: ./scripts/qa-docs-gate.sh [pr-number] [repo]
#    OR: PR_NUM=<N> REPO=<owner/repo> ./scripts/qa-docs-gate.sh
#
# Implements docs/design/41-published-docs-freshness.md §Future:
# "QA docs gate — QA §3b-docs-gate: when a PR moves a Future item to ✅ Present for a
#  user-visible feature (CLI, CRD, UI), verify docs/ files were updated or the feature
#  is Layer 1 auto-documented. If neither: WRONG finding blocks approval."
#
# Exit code:
#   0 — all checks pass (or script is skipping due to missing inputs/unavailable gh)
#   1 — WRONG finding: user-visible feature promoted without docs/ update

set -euo pipefail

PR_NUM="${1:-${PR_NUM:-}}"
REPO="${2:-${REPO:-pnz1990/kardinal-promoter}}"

# ── Fail-open guard: missing PR number ────────────────────────────────────────
if [ -z "$PR_NUM" ]; then
  echo "[QA §3b-docs-gate] SKIPPED — no PR_NUM provided (pass as \$1 or PR_NUM env var)"
  exit 0
fi

# ── Fail-open guard: gh CLI not available ─────────────────────────────────────
if ! command -v gh &>/dev/null; then
  echo "[QA §3b-docs-gate] SKIPPED — gh CLI not available"
  exit 0
fi

# ── Fail-open guard: python3 not available ────────────────────────────────────
if ! command -v python3 &>/dev/null; then
  echo "[QA §3b-docs-gate] SKIPPED — python3 not available"
  exit 0
fi

echo "[QA §3b-docs-gate] Checking PR #${PR_NUM} on ${REPO}..."

# ── Step 1: Fetch PR diff ─────────────────────────────────────────────────────
PR_DIFF=$(gh pr diff "$PR_NUM" --repo "$REPO" 2>/dev/null) || {
  echo "[QA §3b-docs-gate] SKIPPED — could not fetch diff for PR #${PR_NUM} (network or permission error)"
  exit 0
}

if [ -z "$PR_DIFF" ]; then
  echo "[QA §3b-docs-gate] SKIPPED — empty diff for PR #${PR_NUM}"
  exit 0
fi

# ── Step 2: Parse diff with Python ───────────────────────────────────────────
RESULT=$(python3 - "$PR_NUM" "$REPO" <<'PYEOF'
import sys
import re
import os

pr_num = sys.argv[1] if len(sys.argv) > 1 else ""
repo = sys.argv[2] if len(sys.argv) > 2 else ""

diff_text = sys.stdin.read()

# ── Detect Future (🔲) → Present (✅) transitions ───────────────────────────
# In a unified diff, removed lines start with '-' and added lines start with '+'.
# We track per-file transitions: a 🔲 item removed AND a ✅ item added.
#
# Pattern: in docs/design/*.md files, lines like:
#   -  - 🔲 Some feature — description
#   +  - ✅ Some feature — description (PR #N, date)

DESIGN_DOC_PATTERN = re.compile(
    r'^diff --git a/(docs/design/[^\s]+\.md)'
)
REMOVED_FUTURE = re.compile(r'^-\s*-\s*🔲\s*(.+)', re.UNICODE)
ADDED_PRESENT  = re.compile(r'^\+\s*-\s*✅\s*(.+)', re.UNICODE)

# User-visible feature keywords — if any of these appear in the item description,
# the feature is classified as user-visible and requires a docs/ update.
USER_VISIBLE_KEYWORDS = [
    r'\bcli\b', r'\bcrd\b', r'\bui\b', r'\bapi\b',
    r'\bcommand\b', r'\bflag\b', r'\bendpoint\b',
    r'spec\.', r'status\.', r'\bdashboard\b', r'\bweb\b',
    r'\bkubectl\b', r'\bkardinal\b',
]
USER_VISIBLE_RE = re.compile('|'.join(USER_VISIBLE_KEYWORDS), re.IGNORECASE)

# Layer 1 auto-documented exemption keywords
LAYER1_KEYWORDS = re.compile(r'layer 1|auto-documented|layer-1', re.IGNORECASE)

# Collect promoted items: {description: is_user_visible}
promoted_items = []

current_file = None
removed_futures = {}   # desc -> raw line
added_presents = {}    # desc -> raw line

def norm(s):
    """Normalize description for matching: strip PR ref, lowercase, strip whitespace."""
    s = re.sub(r'\s*\(PR #\d+.*?\)', '', s)
    s = re.sub(r'\s*—.*$', '', s)          # strip em-dash suffix
    s = re.sub(r'\*+', '', s)               # strip markdown bold
    return s.strip().lower()

for line in diff_text.splitlines():
    # Track current file
    file_m = DESIGN_DOC_PATTERN.match(line)
    if file_m:
        current_file = file_m.group(1)
        removed_futures = {}
        added_presents = {}
        continue

    if current_file is None:
        continue

    # Only process diff lines inside a design doc
    rm = REMOVED_FUTURE.match(line)
    if rm:
        desc = rm.group(1).strip()
        key = norm(desc)
        removed_futures[key] = desc
        continue

    am = ADDED_PRESENT.match(line)
    if am:
        desc = am.group(1).strip()
        key = norm(desc)
        added_presents[key] = desc
        continue

    # When we hit the next file, reconcile pending transitions
    if line.startswith('diff --git') and current_file:
        for key, future_desc in removed_futures.items():
            if key in added_presents:
                present_desc = added_presents[key]
                is_layer1 = bool(LAYER1_KEYWORDS.search(future_desc + ' ' + present_desc))
                is_user_visible = bool(USER_VISIBLE_RE.search(future_desc + ' ' + present_desc))
                promoted_items.append({
                    'desc': future_desc,
                    'is_user_visible': is_user_visible,
                    'is_layer1': is_layer1,
                    'file': current_file,
                })
        removed_futures = {}
        added_presents = {}

        # Track new file
        file_m = DESIGN_DOC_PATTERN.match(line)
        if file_m:
            current_file = file_m.group(1)
        else:
            current_file = None
        continue

# Reconcile the last file's transitions
if current_file:
    for key, future_desc in removed_futures.items():
        if key in added_presents:
            present_desc = added_presents[key]
            is_layer1 = bool(LAYER1_KEYWORDS.search(future_desc + ' ' + present_desc))
            is_user_visible = bool(USER_VISIBLE_RE.search(future_desc + ' ' + present_desc))
            promoted_items.append({
                'desc': future_desc,
                'is_user_visible': is_user_visible,
                'is_layer1': is_layer1,
                'file': current_file,
            })

if not promoted_items:
    print("NO_TRANSITIONS")
    sys.exit(0)

# ── Check docs/ changes in PR diff ──────────────────────────────────────────
# Look for any +/- lines in files under docs/ (excluding docs/design/)
DOCS_FILE_PATTERN = re.compile(r'^diff --git a/(docs/(?!design/)[^\s]+)')
docs_files_changed = set()

for line in diff_text.splitlines():
    m = DOCS_FILE_PATTERN.match(line)
    if m:
        docs_files_changed.add(m.group(1))

# ── Evaluate each promoted item ──────────────────────────────────────────────
wrong_items = []
pass_items = []

for item in promoted_items:
    desc = item['desc']
    # Truncate for display
    short_desc = desc[:80] + ('...' if len(desc) > 80 else '')

    if not item['is_user_visible']:
        pass_items.append(f"  OK (infra-only): {short_desc}")
        continue

    if item['is_layer1']:
        pass_items.append(f"  OK (Layer 1 auto-documented): {short_desc}")
        continue

    if docs_files_changed:
        pass_items.append(f"  OK (docs/ updated: {', '.join(sorted(docs_files_changed)[:2])}): {short_desc}")
        continue

    # WRONG: user-visible, not Layer 1, no docs/ changes
    wrong_items.append(short_desc)

# ── Output ───────────────────────────────────────────────────────────────────
total = len(promoted_items)
docs_count = len(docs_files_changed)

if wrong_items:
    for item_desc in wrong_items:
        print(f"WRONG: {item_desc}")
    print(f"WRONG_COUNT:{len(wrong_items)}")
    print(f"TOTAL:{total}")
    print(f"DOCS:{docs_count}")
else:
    print(f"PASS_COUNT:{len(pass_items)}")
    print(f"TOTAL:{total}")
    print(f"DOCS:{docs_count}")
    for item in pass_items:
        print(f"OK:{item}")

PYEOF
)

# ── Step 3: Interpret result ──────────────────────────────────────────────────
if echo "$RESULT" | grep -q "^NO_TRANSITIONS"; then
  echo "[QA §3b-docs-gate] PASS — no Future→Present transitions detected in design docs."
  exit 0
fi

if echo "$RESULT" | grep -q "^WRONG:"; then
  WRONG_COUNT=$(echo "$RESULT" | grep "^WRONG_COUNT:" | cut -d: -f2 || echo "?")
  TOTAL=$(echo "$RESULT" | grep "^TOTAL:" | cut -d: -f2 || echo "?")
  DOCS=$(echo "$RESULT" | grep "^DOCS:" | cut -d: -f2 || echo "0")

  echo ""
  echo "[QA §3b-docs-gate] ❌ WRONG — ${WRONG_COUNT} user-visible feature(s) promoted to"
  echo "  ✅ Present with no docs/ file updated."
  echo ""
  echo "  Affected items:"
  while IFS= read -r line; do
    if echo "$line" | grep -q "^WRONG:"; then
      desc="${line#WRONG: }"
      echo "  • $desc"
    fi
  done <<< "$RESULT"
  echo ""
  echo "  Required action: add or update a docs/ page for each affected feature."
  echo "  (docs/design/ changes do not count — the check looks for docs/*.md changes)"
  echo ""
  echo "  Total transitions: $TOTAL | docs/ files changed: $DOCS"
  echo ""
  echo "  Exemptions: Layer 1 auto-documented features (add 'Layer 1' to item description)"
  echo "  or infra-only features (no CLI/CRD/UI keywords in item description)."
  exit 1
fi

if echo "$RESULT" | grep -q "^PASS_COUNT:"; then
  PASS_COUNT=$(echo "$RESULT" | grep "^PASS_COUNT:" | cut -d: -f2 || echo "?")
  TOTAL=$(echo "$RESULT" | grep "^TOTAL:" | cut -d: -f2 || echo "?")
  DOCS=$(echo "$RESULT" | grep "^DOCS:" | cut -d: -f2 || echo "0")
  echo "[QA §3b-docs-gate] ✅ PASS — ${TOTAL} Present item(s) checked, ${DOCS} docs/ file(s) verified."
  exit 0
fi

# Fallback: unexpected output — fail-open
echo "[QA §3b-docs-gate] SKIPPED — unexpected script output (fail-open)"
echo "  Raw result: $RESULT"
exit 0
