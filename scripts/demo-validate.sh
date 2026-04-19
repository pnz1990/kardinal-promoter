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

VERBOSE=${1:-}
RUN_FILTER=""
if [[ "${1:-}" == "-v" ]]; then
  VERBOSE="-v"
  shift || true
fi
if [[ "${1:-}" == "-run" && -n "${2:-}" ]]; then
  RUN_FILTER="-run $2"
  shift 2 || true
fi

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
  ${RUN_FILTER} \
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

# Count tests per adapter by grepping test names
RESOURCE_COUNT=$(grep -c "func TestDeploymentAdapter_" "$REPO_ROOT/pkg/health/health_test.go" 2>/dev/null || echo 0)
ARGOCD_COUNT=$(grep -c "func TestArgoCDAdapter_" "$REPO_ROOT/pkg/health/health_test.go" 2>/dev/null || echo 0)
FLUX_COUNT=$(grep -c "func TestFluxAdapter_" "$REPO_ROOT/pkg/health/health_test.go" 2>/dev/null || echo 0)
ROLLOUTS_COUNT=$(grep -c "func TestArgoRolloutsAdapter_" "$REPO_ROOT/pkg/health/health_test.go" 2>/dev/null || echo 0)
FLAGGER_COUNT=$(grep -c "func TestFlaggerAdapter_" "$REPO_ROOT/pkg/health/health_test.go" 2>/dev/null || echo 0)

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

if [[ "$PASS" == "true" ]]; then
  echo "  ✅ All adapters have ≥3 tests"
fi

echo ""
echo "================================================================"
echo "Demo validation complete."
echo ""
echo "For live cluster validation, see AGENTS.md §Product Validation Scenarios"
echo "or run: make setup-e2e-env"
echo "================================================================"
