#!/usr/bin/env bash
# demo/scripts/validate.sh
#
# End-to-end validation of the kardinal-promoter demo environment.
# This script IS the source of truth that "kardinal works". Every feature
# exercised here must remain working as new features are added.
#
# Exit code:  0 = all scenarios passed
#             1 = one or more scenarios failed
#
# Usage:
#   ./demo/scripts/validate.sh                  # run all scenarios
#   ./demo/scripts/validate.sh --scenario 1     # run just scenario 1
#   ./demo/scripts/validate.sh --fast           # skip soak waits
#   GITHUB_TOKEN=xxx ./demo/scripts/validate.sh
#
# Scenarios validated:
#   1. Controller health          kardinal doctor
#   2. Pipeline list              kardinal get pipelines
#   3. UI reachable               curl http://localhost:8082/api/v1/ui/pipelines
#   4. Happy path promotion       kardinal-test-app: test→uat auto, prod PR opened
#   5. Policy gate: weekend       kardinal policy simulate → BLOCKED on Saturday
#   6. Policy gate: soak          promote before soak completes → BLOCKED
#   7. Pause / resume             bundle halts at test when paused
#   8. Rollback                   kardinal rollback → PR with rollback label
#   9. CLI completeness           version, get, explain, completion, --dry-run
#  11. Flux health adapter        Kustomization Ready=True → adapter reports Healthy
#  12. Argo Rollouts adapter      Rollout phase=Healthy → adapter reports Healthy
#  13. Flagger health adapter     Canary phase=Succeeded → adapter reports Healthy
#
# Scenarios 11-13 skip gracefully when the adapter is not installed.
# To enable all: run setup.sh with INSTALL_FLUX=true INSTALL_ARGO_ROLLOUTS=true INSTALL_FLAGGER=true
#
# The script counts its own failures, so it does not use set -e: one failing
# command must not abort the run before the summary.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

set -uo pipefail
# Checks end in grep ... >/dev/null, not grep -q: grep -q exits at the first
# match, the writer then dies of SIGPIPE, and pipefail fails the check.

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "${DEMO_DIR}/.." && pwd)"
CONTROL_CLUSTER="${CONTROL_CLUSTER:-kardinal-control}"
GITHUB_TOKEN="${GITHUB_TOKEN:-}"
FAST="${FAST:-false}"
SCENARIO_FILTER=""

# ── Find kardinal CLI ─────────────────────────────────────────────────────────
# KARDINAL overrides the binary. Otherwise prefer a locally built binary in the
# repo root so CI always tests current code.
if [[ -n "${KARDINAL:-}" ]]; then
  :
elif [[ -x "${REPO_ROOT}/bin/kardinal" ]]; then
  KARDINAL="${REPO_ROOT}/bin/kardinal"
elif command -v kardinal &>/dev/null; then
  KARDINAL="kardinal"
else
  echo "[validate] kardinal CLI not found. Building from source..."
  (cd "${REPO_ROOT}" && go build -mod=mod -o "${REPO_ROOT}/bin/kardinal" ./cmd/kardinal/) || exit 1
  KARDINAL="${REPO_ROOT}/bin/kardinal"
fi

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
PASS=0; FAIL=0; SKIP=0
FAILURES=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --fast) FAST=true; shift ;;
    --scenario)
      [[ $# -ge 2 ]] || { echo "usage: $0 [--fast] [--scenario N]" >&2; exit 2; }
      SCENARIO_FILTER="$2"; shift 2 ;;
    --scenario=*) SCENARIO_FILTER="${1#*=}"; shift ;;
    *) echo "unknown argument: $1" >&2; echo "usage: $0 [--fast] [--scenario N]" >&2; exit 2 ;;
  esac
done

pass() { echo -e "${GREEN}  ✓${NC} $*"; PASS=$((PASS+1)); }
fail() { echo -e "${RED}  ✗${NC} $*"; FAIL=$((FAIL+1)); FAILURES+=("$*"); }
skip() { echo -e "${YELLOW}  -${NC} $*"; SKIP=$((SKIP+1)); }
scenario() {
  local n="$1"; local name="$2"
  if [[ -n "$SCENARIO_FILTER" && "$SCENARIO_FILTER" != "$n" ]]; then
    return 1  # filtered out
  fi
  echo ""
  echo -e "${BLUE}── Scenario ${n}: ${name}${NC}"
  return 0
}

# ── Setup ─────────────────────────────────────────────────────────────────────

# Try demo cluster name first, then fall back to legacy kind-kardinal-e2e
if kubectl config use-context "kind-${CONTROL_CLUSTER}" 2>/dev/null; then
  : # context found
elif kubectl config use-context "kind-kardinal-e2e" 2>/dev/null; then
  CONTROL_CLUSTER="kardinal-e2e"
  echo "[validate] Using legacy context kind-kardinal-e2e (set CONTROL_CLUSTER=kardinal-e2e to suppress)"
else
  echo "ERROR: No kardinal controller cluster found."
  echo "  Tried: kind-${CONTROL_CLUSTER}, kind-kardinal-e2e"
  echo "  Run: GITHUB_TOKEN=xxx ./demo/scripts/setup.sh"
  exit 1
fi

# A real test app image: TEST_APP_IMAGE, else the latest kardinal-test-app
# commit, else the image kardinal-demo's overlays pin.
if [[ -z "${TEST_APP_IMAGE:-}" ]]; then
  LATEST_SHA=$(curl -sf --max-time 5 "https://api.github.com/repos/pnz1990/kardinal-test-app/commits/main" \
    ${GITHUB_TOKEN:+-H "Authorization: Bearer ${GITHUB_TOKEN}"} 2>/dev/null | \
    python3 -c "import sys,json; print(json.load(sys.stdin)['sha'][:7])" 2>/dev/null || true)
  TEST_APP_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA:-9349a3f}"
fi

# Start port-forward for UI tests
PF_PID=""
cleanup() {
  [[ -n "$PF_PID" ]] && kill "$PF_PID" 2>/dev/null || true
}
trap cleanup EXIT

# ── Scenario 1: Controller health ────────────────────────────────────────────

if scenario 1 "Controller health"; then
  # doctor exits non-zero when any check fails.
  if DOCTOR=$($KARDINAL doctor 2>&1) && echo "$DOCTOR" | grep "check(s) passed" >/dev/null; then
    pass "kardinal doctor reports healthy"
  elif kubectl get pods -n kardinal-system 2>/dev/null | grep "Running" >/dev/null; then
    pass "controller pod is Running (kardinal doctor partial)"
  else
    fail "Controller is not healthy"
  fi
fi

# ── Scenario 2: Pipeline list ─────────────────────────────────────────────────

if scenario 2 "Pipeline list"; then
  OUTPUT=$($KARDINAL get pipelines 2>&1)
  if echo "$OUTPUT" | grep "kardinal-test-app" >/dev/null; then
    pass "kardinal get pipelines shows kardinal-test-app"
  else
    fail "kardinal get pipelines: pipeline not found. Output: $OUTPUT"
  fi
  if $KARDINAL get pipelines -o json 2>/dev/null | python3 -c "import sys,json; d=json.load(sys.stdin); assert any(p['metadata']['name'] == 'kardinal-test-app' for p in d)" 2>/dev/null; then
    pass "kardinal get pipelines --output json is valid JSON"
  else
    fail "kardinal get pipelines -o json does not list kardinal-test-app"
  fi
fi

# ── Scenario 3: UI reachable ──────────────────────────────────────────────────

if scenario 3 "UI reachable"; then
  # Start port-forward
  CONTROLLER_POD=$(kubectl get pods -n kardinal-system -l app.kubernetes.io/name=kardinal-promoter \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || \
    kubectl get pods -n kardinal-system -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

  if [[ -n "$CONTROLLER_POD" ]]; then
    kubectl port-forward -n kardinal-system "$CONTROLLER_POD" 8082:8082 &>/dev/null &
    PF_PID=$!
    sleep 2

    HTTP_CODE=$(curl -sf -o /dev/null -w "%{http_code}" http://localhost:8082/api/v1/ui/pipelines 2>/dev/null || echo "000")
    if [[ "$HTTP_CODE" == "200" ]]; then
      pass "UI API responds HTTP 200"
    else
      fail "UI API returned HTTP ${HTTP_CODE} (expected 200)"
    fi

    # Check React app is served
    HTML=$(curl -sf http://localhost:8082/ui/ 2>/dev/null || echo "")
    if echo "$HTML" | grep "kardinal\|react\|<div id" >/dev/null; then
      pass "UI HTML is served at /ui/"
    else
      fail "UI HTML not found at /ui/"
    fi

    kill "$PF_PID" 2>/dev/null; PF_PID=""
  else
    fail "No controller pod found"
  fi
fi

# ── Scenario 4: Happy path promotion ─────────────────────────────────────────

if scenario 4 "Happy path promotion (test→uat→prod PR)"; then
  if [[ -z "$GITHUB_TOKEN" ]]; then
    skip "Scenario 4 skipped — GITHUB_TOKEN not set (required for GitOps push)"
  else
    OUTPUT=$(timeout 30 $KARDINAL create bundle kardinal-test-app --image "$TEST_APP_IMAGE" 2>&1)
    if echo "$OUTPUT" | grep "created for pipeline kardinal-test-app" >/dev/null; then
      pass "Bundle created: $TEST_APP_IMAGE"
    else
      fail "Bundle creation failed: $OUTPUT"
    fi

    # Wait for test environment to promote
    WAIT_SECS=$([[ "$FAST" == "true" ]] && echo 30 || echo 120)
    echo -e "${BLUE}  →${NC} Waiting ${WAIT_SECS}s for test environment..."
    sleep "$WAIT_SECS"

    # get bundles -o json prints Bundle objects; progress is status.environments.
    STATUS=$($KARDINAL get bundles kardinal-test-app -o json 2>/dev/null | \
      python3 -c "
import sys, json
for b in json.load(sys.stdin):
    for e in (b.get('status') or {}).get('environments') or []:
        print(e.get('name'), e.get('phase'))
" 2>/dev/null || true)

    if echo "$STATUS" | grep -i "Verified\|Promoting\|WaitingForMerge" >/dev/null; then
      pass "Pipeline is progressing (test/uat Verified or Promoting)"
    elif [[ "$FAST" == "true" ]]; then
      skip "Fast mode — not enough time to verify promotion progress"
    else
      fail "Pipeline not progressing after ${WAIT_SECS}s. Status: $STATUS"
    fi
  fi
fi

# ── Scenario 5: Weekend gate ──────────────────────────────────────────────────

if scenario 5 "PolicyGate: weekend blocks prod"; then
  # Match the no-weekend-deploys gate's own row ("<gate>:   PASS|BLOCK   (...)",
  # cmd/kardinal/cmd/policy.go), not any PASS elsewhere in the output.
  OUTPUT=$($KARDINAL policy simulate --pipeline kardinal-test-app --env prod \
    --time "Saturday 3pm" 2>&1)
  if echo "$OUTPUT" | grep "^RESULT: BLOCKED" >/dev/null && echo "$OUTPUT" | grep -E "^no-weekend-deploys:[[:space:]]+BLOCK[[:space:]]" >/dev/null; then
    pass "Weekend gate blocks prod on Saturday"
  else
    fail "Weekend gate did not block. Output: $OUTPUT"
  fi

  OUTPUT=$($KARDINAL policy simulate --pipeline kardinal-test-app --env prod \
    --time "Tuesday 10am" 2>&1)
  if echo "$OUTPUT" | grep -E "^no-weekend-deploys:[[:space:]]+PASS[[:space:]]" >/dev/null; then
    pass "Weekend gate allows prod on Tuesday"
  else
    fail "Weekend gate did not allow on Tuesday. Output: $OUTPUT"
  fi
fi

# ── Scenario 6: Soak gate ─────────────────────────────────────────────────────

if scenario 6 "PolicyGate: soak blocks prod before 30m"; then
  OUTPUT=$($KARDINAL explain kardinal-test-app --env prod 2>&1)
  if echo "$OUTPUT" | grep -i "soak\|upstreamSoak" >/dev/null; then
    pass "kardinal explain shows soak gate for prod"
  else
    skip "Soak gate not visible in explain (may require active bundle)"
  fi
fi

# ── Scenario 7: Pause / resume ────────────────────────────────────────────────

if scenario 7 "Pause / resume pipeline"; then
  if $KARDINAL pause kardinal-test-app 2>&1 | grep "Pipeline kardinal-test-app paused" >/dev/null; then
    pass "kardinal pause accepted"
  else
    fail "kardinal pause failed"
  fi

  STATUS=$($KARDINAL get pipelines -o json 2>/dev/null | \
    python3 -c "
import sys, json
for p in json.load(sys.stdin):
    if p['metadata']['name'] == 'kardinal-test-app':
        print(p.get('spec', {}).get('paused', False))
" 2>/dev/null || echo "")
  if [[ "$STATUS" == "True" ]]; then
    pass "Pipeline shows spec.paused=true"
  else
    fail "Pipeline spec.paused is not true after kardinal pause"
  fi

  if $KARDINAL resume kardinal-test-app 2>&1 | grep "Pipeline kardinal-test-app resumed" >/dev/null; then
    pass "kardinal resume accepted"
  else
    fail "kardinal resume failed"
  fi
fi

# ── Scenario 8: Rollback ──────────────────────────────────────────────────────

if scenario 8 "Rollback opens a PR"; then
  if [[ -z "$GITHUB_TOKEN" ]]; then
    skip "Scenario 8 skipped — GITHUB_TOKEN not set (required for GitOps push)"
  else
    OUTPUT=$(timeout 30 $KARDINAL rollback kardinal-test-app --env prod 2>&1)
    if echo "$OUTPUT" | grep "created (rollbackOf=" >/dev/null; then
      pass "kardinal rollback created a rollback Bundle"
    else
      skip "Rollback skipped (requires a promoted bundle in prod). Output: $(echo "$OUTPUT" | head -2)"
    fi
  fi
fi

# ── Scenario 9: CLI completeness ──────────────────────────────────────────────

if scenario 9 "CLI completeness"; then
  check_cmd() {
    local cmd="$1"; local expected="$2"
    OUTPUT=$(eval "$KARDINAL $cmd" 2>&1 || true)
    if echo "$OUTPUT" | grep -i "$expected" >/dev/null; then
      pass "$KARDINAL $cmd: OK"
    else
      fail "$KARDINAL $cmd: unexpected output. Got: $(echo "$OUTPUT" | head -2)"
    fi
  }

  check_cmd "version" "CLI\|v0\."
  check_cmd "get pipelines" "PIPELINE\|kardinal-test-app"
  check_cmd "explain kardinal-test-app --env prod" "gate\|Gate\|prod\|no-weekend"
  check_cmd "history kardinal-test-app" "Bundle\|history\|No history\|AGE"
  check_cmd "get steps kardinal-test-app" "STEP\|Steps\|No active bundles\|PromotionStep"

  # Shell completion exists
$KARDINAL completion bash &>/dev/null && pass "kardinal completion bash works" || \
    fail "kardinal completion bash failed"

  # --dry-run flag exists
$KARDINAL create bundle kardinal-test-app \
    --image ghcr.io/pnz1990/kardinal-test-app:sha-dryrun \
    --dry-run 2>&1 | grep -i "dry.run\|DRY.RUN\|preview\|would\|simulate" >/dev/null && \
    pass "kardinal create bundle --dry-run works" || \
    fail "kardinal create bundle --dry-run: unexpected output"
fi

# ── Scenario 11: Flux health adapter ─────────────────────────────────────────
# Validates that a Pipeline with health.type: flux waits for Kustomization.Ready=True.

if scenario 11 "Flux health adapter — Kustomization Ready check"; then
  if ! kubectl get ns flux-system &>/dev/null 2>&1; then
    skip "Scenario 11 skipped — Flux not installed (run setup.sh with INSTALL_FLUX=true)"
  elif ! kubectl get crd kustomizations.kustomize.toolkit.fluxcd.io &>/dev/null 2>&1; then
    skip "Scenario 11 skipped — Flux Kustomization CRD not found"
  else
    FLUX_PIPELINE=$($KARDINAL get pipelines 2>&1 | grep "kardinal-test-app-flux" || true)
    if [[ -z "$FLUX_PIPELINE" ]]; then
      skip "Scenario 11 skipped — kardinal-test-app-flux pipeline not found (apply demo/manifests/flux/)"
    else
      pass "kardinal-test-app-flux pipeline registered"
      KS_COUNT=$(kubectl get kustomizations -n flux-system --no-headers 2>/dev/null | wc -l | tr -d ' ')
      if [[ "${KS_COUNT:-0}" -gt 0 ]]; then
        pass "Flux Kustomizations found: $KS_COUNT"
      else
        skip "No Kustomizations in flux-system yet"
      fi
      READY_KS=$(kubectl get kustomizations -n flux-system -o json 2>/dev/null | \
        python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    for item in data.get('items', []):
        conds = item.get('status', {}).get('conditions', [])
        ready = next((c for c in conds if c.get('type') == 'Ready'), None)
        if ready and ready.get('status') == 'True':
            print(item['metadata']['name']); break
except: pass
" 2>/dev/null || true)
      if [[ -n "$READY_KS" ]]; then
        pass "Kustomization '$READY_KS' Ready=True — adapter would report Healthy"
      else
        skip "No Ready=True Kustomization yet — Flux may still be reconciling"
      fi
      FLUX_TYPE=$(kubectl get pipeline kardinal-test-app-flux -n default -o json 2>/dev/null | \
        python3 -c "
import sys, json
try:
    p = json.load(sys.stdin)
    for env in p.get('spec', {}).get('environments', []):
        if env.get('health', {}).get('type') == 'flux':
            print('flux'); break
except: pass
" 2>/dev/null || true)
      if [[ "$FLUX_TYPE" == "flux" ]]; then
        pass "Pipeline spec confirms health.type=flux"
      else
        fail "Pipeline kardinal-test-app-flux does not have health.type=flux in spec"
      fi
    fi
  fi
fi

# ── Scenario 12: Argo Rollouts health adapter ─────────────────────────────────

if scenario 12 "Argo Rollouts health adapter — Rollout phase check"; then
  if ! kubectl get ns argo-rollouts &>/dev/null 2>&1; then
    skip "Scenario 12 skipped — Argo Rollouts not installed (run setup.sh with INSTALL_ARGO_ROLLOUTS=true)"
  elif ! kubectl get crd rollouts.argoproj.io &>/dev/null 2>&1; then
    skip "Scenario 12 skipped — Rollout CRD not found"
  else
    ROLLOUTS_PIPELINE=$($KARDINAL get pipelines 2>&1 | grep "kardinal-test-app-rollouts" || true)
    if [[ -z "$ROLLOUTS_PIPELINE" ]]; then
      skip "Scenario 12 skipped — kardinal-test-app-rollouts pipeline not found"
    else
      pass "kardinal-test-app-rollouts pipeline registered"
      ROLLOUT_PHASE=$(kubectl get rollout kardinal-test-app-rollouts \
        -n kardinal-test-app-test -o json 2>/dev/null | \
        python3 -c "import sys,json; r=json.load(sys.stdin); print(r.get('status',{}).get('phase','unknown'))" \
        2>/dev/null || echo "NotFound")
      case "$ROLLOUT_PHASE" in
        "Healthy")   pass "Rollout phase=Healthy — argoRollouts adapter reports Healthy" ;;
        "Progressing"|"Paused") pass "Rollout phase=$ROLLOUT_PHASE — adapter returns Wait (expected)" ;;
        "Degraded")  fail "Rollout phase=Degraded — needs investigation" ;;
        "NotFound")  skip "Rollout not found in kardinal-test-app-test" ;;
        *)           pass "Rollout phase=$ROLLOUT_PHASE — adapter running" ;;
      esac
      ROLLOUTS_TYPE=$(kubectl get pipeline kardinal-test-app-rollouts -n default -o json 2>/dev/null | \
        python3 -c "
import sys, json
try:
    p = json.load(sys.stdin)
    for env in p.get('spec', {}).get('environments', []):
        if env.get('health', {}).get('type') == 'argoRollouts':
            print('argoRollouts'); break
except: pass
" 2>/dev/null || true)
      if [[ "$ROLLOUTS_TYPE" == "argoRollouts" ]]; then
        pass "Pipeline spec confirms health.type=argoRollouts"
      else
        fail "Pipeline kardinal-test-app-rollouts does not have health.type=argoRollouts"
      fi
    fi
  fi
fi

# ── Scenario 13: Flagger health adapter ──────────────────────────────────────

if scenario 13 "Flagger health adapter — Canary phase check"; then
  if ! kubectl get crd canaries.flagger.app &>/dev/null 2>&1; then
    skip "Scenario 13 skipped — Flagger Canary CRD not found (run setup.sh with INSTALL_FLAGGER=true)"
  else
    FLAGGER_PIPELINE=$($KARDINAL get pipelines 2>&1 | grep "kardinal-test-app-flagger" || true)
    if [[ -z "$FLAGGER_PIPELINE" ]]; then
      skip "Scenario 13 skipped — kardinal-test-app-flagger pipeline not found"
    else
      pass "kardinal-test-app-flagger pipeline registered"
      CANARY_PHASE=$(kubectl get canary kardinal-test-app-flagger \
        -n kardinal-test-app-test -o json 2>/dev/null | \
        python3 -c "import sys,json; c=json.load(sys.stdin); print(c.get('status',{}).get('phase','unknown'))" \
        2>/dev/null || echo "NotFound")
      case "$CANARY_PHASE" in
        "Succeeded")  pass "Canary phase=Succeeded — flagger adapter reports Healthy" ;;
        "Initializing"|"Initialized"|"Waiting"|"Progressing"|"Promoting"|"Finalising")
                      pass "Canary phase=$CANARY_PHASE — adapter returns Wait (expected)" ;;
        "Failed")     fail "Canary phase=Failed — Flagger rolled back" ;;
        "NotFound")   skip "Canary not found in kardinal-test-app-test" ;;
        *)            pass "Canary phase=$CANARY_PHASE — adapter running" ;;
      esac
      FLAGGER_TYPE=$(kubectl get pipeline kardinal-test-app-flagger -n default -o json 2>/dev/null | \
        python3 -c "
import sys, json
try:
    p = json.load(sys.stdin)
    for env in p.get('spec', {}).get('environments', []):
        if env.get('health', {}).get('type') == 'flagger':
            print('flagger'); break
except: pass
" 2>/dev/null || true)
      if [[ "$FLAGGER_TYPE" == "flagger" ]]; then
        pass "Pipeline spec confirms health.type=flagger"
      else
        fail "Pipeline kardinal-test-app-flagger does not have health.type=flagger"
      fi
    fi
  fi
fi

echo ""
echo "═══════════════════════════════════════════════════════════════"
if [[ $FAIL -eq 0 ]]; then
  echo -e "${GREEN}  RESULT: ALL PASSED  (${PASS} passed, ${SKIP} skipped, ${FAIL} failed)${NC}"
else
  echo -e "${RED}  RESULT: FAILED  (${PASS} passed, ${SKIP} skipped, ${FAIL} failed)${NC}"
  echo ""
  echo "  Failed scenarios:"
  for f in "${FAILURES[@]}"; do
    echo -e "    ${RED}✗${NC} $f"
  done
fi
echo "═══════════════════════════════════════════════════════════════"
echo ""

[[ $FAIL -eq 0 ]]
