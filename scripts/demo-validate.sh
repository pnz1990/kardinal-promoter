#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# scripts/demo-validate.sh — validate all health adapter code paths
#
# Runs the full pkg/health unit test suite with race detection.
# All 5 adapters (resource, argocd, flux, argoRollouts, flagger) are exercised.
#
# Usage:
#   bash scripts/demo-validate.sh           # run all adapter tests
#   bash scripts/demo-validate.sh -v        # verbose output
#   bash scripts/demo-validate.sh -run Flux # run only Flux tests

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

VERBOSE=""
RUN_FILTER=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    -v)
      VERBOSE="-v"
      shift
      ;;
    -run)
      [[ -n "${2:-}" ]] || {
        echo "usage: $0 [-v] [-run <regex>]" >&2
        exit 2
      }
      RUN_FILTER=(-run "$2")
      shift 2
      ;;
    *)
      echo "usage: $0 [-v] [-run <regex>]" >&2
      exit 2
      ;;
  esac
done

echo "================================================================"
echo "kardinal-promoter demo-validate: health adapter coverage check"
echo "================================================================"
echo ""

# ---------------------------------------------------------------------------
# 1. Build check — must compile before running tests
# ---------------------------------------------------------------------------
echo "[1/3] Build check..."
cd "$REPO_ROOT"
go build ./... 2>&1
echo "      ✅ Build passed"
echo ""

# ---------------------------------------------------------------------------
# 2. Run health adapter unit tests
# ---------------------------------------------------------------------------
echo "[2/3] Running pkg/health tests (race, count=1)..."
echo ""

# shellcheck disable=SC2086
go test ./pkg/health/... \
  -race \
  -count=1 \
  -timeout 60s \
  ${VERBOSE} \
  ${RUN_FILTER[@]+"${RUN_FILTER[@]}"} \
  2>&1

echo ""
echo "      ✅ All health adapter tests passed"
echo ""

# ---------------------------------------------------------------------------
# 3. Adapter coverage summary
# ---------------------------------------------------------------------------
echo "[3/3] Adapter coverage summary:"
echo ""
echo "  Adapter        | Test count | GVR"
echo "  --------------|------------|--------------------------------------------"

# Count tests per adapter by grepping test names across pkg/health's test files.
# grep -c prints 0 (and exits 1) on no match, so `|| true` keeps a single "0".
count_tests() {
  cat "$REPO_ROOT"/pkg/health/*_test.go | grep -c "func $1" || true
}
RESOURCE_COUNT=$(count_tests TestDeploymentAdapter_)
ARGOCD_COUNT=$(count_tests TestArgoCDAdapter_)
FLUX_COUNT=$(count_tests TestFluxAdapter_)
ROLLOUTS_COUNT=$(count_tests TestArgoRolloutsAdapter_)
FLAGGER_COUNT=$(count_tests TestFlaggerAdapter_)

printf "  %-14s | %-10s | %s\n" "resource"      "$RESOURCE_COUNT"  "apps/v1 Deployment"
printf "  %-14s | %-10s | %s\n" "argocd"        "$ARGOCD_COUNT"   "argoproj.io/v1alpha1 Application"
printf "  %-14s | %-10s | %s\n" "flux"          "$FLUX_COUNT"     "kustomize.toolkit.fluxcd.io/v1 Kustomization"
printf "  %-14s | %-10s | %s\n" "argoRollouts"  "$ROLLOUTS_COUNT" "argoproj.io/v1alpha1 Rollout"
printf "  %-14s | %-10s | %s\n" "flagger"       "$FLAGGER_COUNT"  "flagger.app/v1beta1 Canary"
echo ""

TOTAL=$((RESOURCE_COUNT + ARGOCD_COUNT + FLUX_COUNT + ROLLOUTS_COUNT + FLAGGER_COUNT))
echo "  Total: $TOTAL adapter tests"
echo ""

# Require all adapters have at least 3 tests
PASS=true
for COUNT_NAME in "resource:$RESOURCE_COUNT" "argocd:$ARGOCD_COUNT" "flux:$FLUX_COUNT" "argoRollouts:$ROLLOUTS_COUNT" "flagger:$FLAGGER_COUNT"; do
  NAME="${COUNT_NAME%%:*}"
  COUNT="${COUNT_NAME##*:}"
  if [[ "$COUNT" -lt 3 ]]; then
    echo "  ❌ $NAME adapter has only $COUNT tests — minimum 3 required"
    PASS=false
  fi
done

if [[ "$PASS" != "true" ]]; then
  echo "  ❌ adapter coverage check failed"
  exit 1
fi
echo "  ✅ All adapters have ≥3 tests"

echo ""
echo "================================================================"
echo "Demo validation complete."
echo ""
echo "For live cluster validation, run a live e2e suite (test/e2e/README.md):"
echo "  make e2e-up SUITE=core && make test-e2e-live SUITE=core"
echo "================================================================"
