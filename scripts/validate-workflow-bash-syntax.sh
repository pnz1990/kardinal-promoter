#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# validate-workflow-bash-syntax.sh — bash -n CI guard for workflow run: blocks
#
# Usage: ./scripts/validate-workflow-bash-syntax.sh [workflow-file]
# Default: .github/workflows/otherness-scheduled.yml
#
# Parses a GitHub Actions workflow YAML file, extracts each run: script block,
# and runs 'bash -n' to validate bash syntax. Exits non-zero if any block has
# a syntax error.
#
# Design ref: docs/design/12-autonomous-loop-discipline.md
# Integrated in CI via .github/workflows/ci.yml docs-lint job.

set -euo pipefail

WORKFLOW_FILE="${1:-.github/workflows/otherness-scheduled.yml}"

if [ ! -f "$WORKFLOW_FILE" ]; then
  echo "[bash-n-check] $WORKFLOW_FILE not found — skipping"
  exit 0
fi

python3 - "$WORKFLOW_FILE" <<'PYEOF'
import yaml, subprocess, sys, tempfile, os

workflow_file = sys.argv[1]

with open(workflow_file) as f:
    wf = yaml.safe_load(f)

failures = []
checked = 0
for job_name, job in (wf.get('jobs') or {}).items():
    for step in (job.get('steps') or []):
        script = step.get('run')
        if not script:
            continue
        checked += 1
        step_name = step.get('name', f'unnamed-step-{checked}')
        with tempfile.NamedTemporaryFile(mode='w', suffix='.sh', delete=False) as tf:
            tf.write(script)
            tf_path = tf.name
        result = subprocess.run(['bash', '-n', tf_path], capture_output=True, text=True)
        os.unlink(tf_path)
        if result.returncode != 0:
            failures.append(f'  job={job_name} step="{step_name}": {result.stderr.strip()}')

print(f'[bash-n-check] Checked {checked} run: blocks in {workflow_file}')
if failures:
    print('[bash-n-check] FAIL — bash syntax errors found:')
    for err in failures:
        print(err)
    sys.exit(1)
print('[bash-n-check] All run: blocks are syntactically valid')
PYEOF
