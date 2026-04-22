#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# Otherness onboarding smoke test — run after /otherness.onboard to verify setup.
# Checks that the generated files are valid before the first scheduled run.
#
# Usage:
#   ./scripts/onboard-smoke-test.sh [project-root]
#
# Exit codes:
#   0 = all checks passed
#   1 = one or more checks failed
#
# Design ref: docs/design/12-autonomous-loop-discipline.md
# §Future: Otherness onboarding quality gate (🔲 → ✅)

set -euo pipefail

PROJECT_ROOT="${1:-$(git rev-parse --show-toplevel 2>/dev/null || echo "$PWD")}"
cd "$PROJECT_ROOT"

PASS=0
FAIL=0
GAPS=()

# ── Helper: report check result ──────────────────────────────────────────────
check_pass() { PASS=$((PASS + 1)); echo "  ✓ $1"; }
check_fail() { FAIL=$((FAIL + 1)); GAPS+=("$1"); echo "  ✗ $1"; }

echo "[ONBOARD SMOKE TEST] Project root: $PROJECT_ROOT"
echo ""

# ── Check 1: Workflow YAML structure ────────────────────────────────────────
echo "Check 1: Workflow YAML structure"
WORKFLOW=".github/workflows/otherness-scheduled.yml"
if [ ! -f "$WORKFLOW" ]; then
  check_fail "YAML structure: $WORKFLOW not found — run /otherness.onboard first"
elif python3 -c "import yaml; yaml.safe_load(open('$WORKFLOW'))" 2>/dev/null; then
  check_pass "YAML structure: $WORKFLOW is valid YAML"
else
  check_fail "YAML structure: $WORKFLOW has YAML syntax errors (run: python3 -c \"import yaml; yaml.safe_load(open('$WORKFLOW'))\")"
fi

# ── Check 2: Bash syntax in workflow run: blocks ─────────────────────────────
echo "Check 2: Bash syntax in workflow run: blocks"
if [ ! -f "$WORKFLOW" ]; then
  check_fail "Bash syntax: $WORKFLOW not found — skipping"
elif ! python3 -c "import yaml" 2>/dev/null; then
  check_fail "Bash syntax: PyYAML not installed — install with: pip3 install pyyaml"
else
  BASH_ERRORS=$(python3 - <<'PYEOF'
import yaml, subprocess, sys, tempfile, os

workflow_file = '.github/workflows/otherness-scheduled.yml'
try:
    with open(workflow_file) as f:
        wf = yaml.safe_load(f)
except Exception as e:
    print(f"Cannot parse workflow: {e}")
    sys.exit(1)

errors = []
jobs = wf.get('jobs', {})
for job_name, job in jobs.items():
    for step in job.get('steps', []):
        run_script = step.get('run', '')
        if not run_script:
            continue
        step_name = step.get('name', 'unnamed step')
        with tempfile.NamedTemporaryFile(mode='w', suffix='.sh', delete=False) as tmp:
            tmp.write(run_script)
            tmp_path = tmp.name
        try:
            result = subprocess.run(['bash', '-n', tmp_path], capture_output=True, text=True)
            if result.returncode != 0:
                errors.append(f"step '{step_name}': {result.stderr.strip()}")
        finally:
            os.unlink(tmp_path)

if errors:
    for e in errors:
        print(e)
    sys.exit(1)
PYEOF
  )
  if [ $? -eq 0 ]; then
    check_pass "Bash syntax: all run: blocks in $WORKFLOW are syntactically valid"
  else
    check_fail "Bash syntax: $WORKFLOW has bash syntax errors — $(echo "$BASH_ERRORS" | head -1)"
  fi
fi

# ── Check 3: otherness-config.yaml structure ─────────────────────────────────
echo "Check 3: otherness-config.yaml structure"
CONFIG="otherness-config.yaml"
if [ ! -f "$CONFIG" ]; then
  check_fail "Config structure: $CONFIG not found — run /otherness.onboard first"
elif python3 -c "import yaml; yaml.safe_load(open('$CONFIG'))" 2>/dev/null; then
  check_pass "Config structure: $CONFIG is valid YAML"
else
  check_fail "Config structure: $CONFIG has YAML syntax errors"
fi

# ── Check 4: Required secrets present ────────────────────────────────────────
echo "Check 4: Required secrets (AWS_ROLE_ARN, GH_TOKEN)"
REPO=$(python3 -c "
import re
for line in open('$CONFIG'):
    m = re.match(r'^\s+repo:\s*(\S+)', line)
    if m: print(m.group(1)); break
" 2>/dev/null || echo "")

if [ -z "$REPO" ]; then
  check_fail "Secrets: cannot read 'project.repo' from $CONFIG"
else
  # Try to list secrets using GH_TOKEN or GITHUB_TOKEN
  EFFECTIVE_TOKEN="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
  if [ -z "$EFFECTIVE_TOKEN" ]; then
    echo "  ⚠ Secrets check: no GH_TOKEN or GITHUB_TOKEN env var — skipping (manual check needed)"
    echo "    Required secrets: AWS_ROLE_ARN, GH_TOKEN (verify at: https://github.com/$REPO/settings/secrets/actions)"
  else
    SECRETS_RAW=$(GH_TOKEN="$EFFECTIVE_TOKEN" gh api "repos/$REPO/actions/secrets" \
      --jq '[.secrets[].name]' 2>&1) || true
    SECRETS_EXIT=$?
    # Handle API auth errors gracefully (403 = App token can't list secrets)
    if [ $SECRETS_EXIT -ne 0 ] || echo "$SECRETS_RAW" | grep -q '"message"'; then
      echo "  ⚠ Secrets check: API returned error (token may not have secrets:read scope) — skipping"
      echo "    Required secrets: AWS_ROLE_ARN, GH_TOKEN (verify manually at: https://github.com/$REPO/settings/secrets/actions)"
    else
      SECRETS="$SECRETS_RAW"
      MISSING=()
      for SECRET in "AWS_ROLE_ARN" "GH_TOKEN"; do
        if ! echo "$SECRETS" | python3 -c "import json,sys; s=json.load(sys.stdin); exit(0 if '$SECRET' in s else 1)" 2>/dev/null; then
          MISSING+=("$SECRET")
        fi
      done
      if [ ${#MISSING[@]} -eq 0 ]; then
        check_pass "Secrets: AWS_ROLE_ARN and GH_TOKEN are present in $REPO"
      else
        check_fail "Secrets: missing in $REPO: ${MISSING[*]} — add at https://github.com/$REPO/settings/secrets/actions"
      fi
    fi
  fi
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
TOTAL=$((PASS + FAIL))
echo "[ONBOARD SMOKE TEST: $PASS/$TOTAL checks passed]"
echo ""

if [ ${#GAPS[@]} -gt 0 ]; then
  echo "Gaps found — fix these before your first scheduled run:"
  for GAP in "${GAPS[@]}"; do
    echo "  [ONBOARD GAP]: $GAP"
  done
  echo ""
  exit 1
else
  echo "All checks passed. Your project is ready for /otherness.run."
  echo ""
fi
