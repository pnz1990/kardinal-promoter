#!/usr/bin/env bash
# test.sh — unit tests for the image-parsing and JSON-building logic in action.yml
# Runs without network access. Exits 0 on pass, non-zero on fail.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

set -euo pipefail

PASS=0
FAIL=0

ok() {
  local desc="$1"
  PASS=$((PASS + 1))
  echo "  PASS: $desc"
}

fail() {
  local desc="$1" got="$2" want="$3"
  FAIL=$((FAIL + 1))
  echo "  FAIL: $desc"
  echo "        got:  $got"
  echo "        want: $want"
}

# parse_image <ref> [override_digest] — mirrors action.yml logic
parse_image() {
  local img="$1"
  local override_digest="${2:-}"
  if [ -n "$override_digest" ]; then
    local repo
    repo=$(echo "$img" | cut -d: -f1)
    echo "{\"repository\":\"$repo\",\"digest\":\"$override_digest\"}"
  elif echo "$img" | grep -q "@"; then
    local repo digest
    repo=$(echo "$img" | cut -d@ -f1)
    digest=$(echo "$img" | cut -d@ -f2)
    echo "{\"repository\":\"$repo\",\"digest\":\"$digest\"}"
  elif echo "$img" | grep -q ":"; then
    local repo tag
    repo=$(echo "$img" | rev | cut -d: -f2- | rev)
    tag=$(echo "$img" | rev | cut -d: -f1 | rev)
    echo "{\"repository\":\"$repo\",\"tag\":\"$tag\"}"
  else
    echo "{\"repository\":\"$img\"}"
  fi
}

echo ""
echo "--- Image parsing tests ---"

# T1: repo:tag
T1=$(parse_image "ghcr.io/myorg/app:v1.2.3" "")
WANT_T1='{"repository":"ghcr.io/myorg/app","tag":"v1.2.3"}'
if [ "$T1" = "$WANT_T1" ]; then ok "repo:tag parsing"; else fail "repo:tag parsing" "$T1" "$WANT_T1"; fi

# T2: repo@digest
T2=$(parse_image "ghcr.io/myorg/app@sha256:abcdef0123456789" "")
WANT_T2='{"repository":"ghcr.io/myorg/app","digest":"sha256:abcdef0123456789"}'
if [ "$T2" = "$WANT_T2" ]; then ok "repo@digest parsing"; else fail "repo@digest parsing" "$T2" "$WANT_T2"; fi

# T3: bare repo (no tag or digest)
T3=$(parse_image "ghcr.io/myorg/app" "")
WANT_T3='{"repository":"ghcr.io/myorg/app"}'
if [ "$T3" = "$WANT_T3" ]; then ok "bare repo parsing"; else fail "bare repo parsing" "$T3" "$WANT_T3"; fi

# T4: image input + override digest (the image+digest input pattern)
T4=$(parse_image "ghcr.io/myorg/app:v1.2.3" "sha256:abc123")
WANT_T4='{"repository":"ghcr.io/myorg/app","digest":"sha256:abc123"}'
if [ "$T4" = "$WANT_T4" ]; then ok "image+digest override"; else fail "image+digest override" "$T4" "$WANT_T4"; fi

# T5: multi-image list building
MULTI_INPUT="ghcr.io/myorg/app:v1.2.3
ghcr.io/myorg/sidecar@sha256:deadbeef"
ITEMS=""
while IFS= read -r img; do
  [ -z "$img" ] && continue
  ENTRY=$(parse_image "$img" "")
  if [ -z "$ITEMS" ]; then ITEMS="$ENTRY"; else ITEMS="$ITEMS,$ENTRY"; fi
done <<< "$MULTI_INPUT"
T5="[$ITEMS]"
WANT_T5='[{"repository":"ghcr.io/myorg/app","tag":"v1.2.3"},{"repository":"ghcr.io/myorg/sidecar","digest":"sha256:deadbeef"}]'
if [ "$T5" = "$WANT_T5" ]; then ok "multi-image list building"; else fail "multi-image list building" "$T5" "$WANT_T5"; fi

# T6: URL trailing slash stripping
KARDINAL_URL="https://kardinal.example.com/"
KARDINAL_URL="${KARDINAL_URL%/}"
WANT_T6="https://kardinal.example.com"
if [ "$KARDINAL_URL" = "$WANT_T6" ]; then ok "URL trailing slash strip"; else fail "URL trailing slash strip" "$KARDINAL_URL" "$WANT_T6"; fi

# T7: bundle-status-url construction
STATUS_URL="${KARDINAL_URL}/ui#pipeline=my-app"
WANT_T7="https://kardinal.example.com/ui#pipeline=my-app"
if [ "$STATUS_URL" = "$WANT_T7" ]; then ok "bundle-status-url construction"; else fail "bundle-status-url construction" "$STATUS_URL" "$WANT_T7"; fi

# T8: provenance JSON via python3
PROVENANCE_JSON=$(GITHUB_SHA="abc123" GITHUB_SERVER_URL="https://github.com" \
  GITHUB_REPOSITORY="myorg/myapp" GITHUB_RUN_ID="42" GITHUB_ACTOR="engineer" \
  python3 -c "
import json, os
print(json.dumps({
  'commitSHA': os.environ.get('GITHUB_SHA', ''),
  'ciRunURL': '{}/{}/actions/runs/{}'.format(
    os.environ.get('GITHUB_SERVER_URL', 'https://github.com'),
    os.environ.get('GITHUB_REPOSITORY', 'unknown'),
    os.environ.get('GITHUB_RUN_ID', '0')),
  'author': os.environ.get('GITHUB_ACTOR', ''),
}))
")
WANT_PROV='{"commitSHA": "abc123", "ciRunURL": "https://github.com/myorg/myapp/actions/runs/42", "author": "engineer"}'
# Compare via python3 for key-order-agnostic comparison
T8_MATCH=$(python3 -c "
import json
got = json.loads('$PROVENANCE_JSON')
want = json.loads('$WANT_PROV')
print('ok' if got == want else 'fail')
" 2>/dev/null || echo "fail")
if [ "$T8_MATCH" = "ok" ]; then ok "provenance JSON construction"; else fail "provenance JSON construction" "$PROVENANCE_JSON" "$WANT_PROV"; fi

echo ""
echo "--- Results ---"
echo "  Passed: $PASS"
echo "  Failed: $FAIL"
echo ""

if [ $FAIL -gt 0 ]; then
  echo "FAIL: $FAIL test(s) failed"
  exit 1
fi

echo "PASS: all $PASS tests passed"
