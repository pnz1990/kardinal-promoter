#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# validate-workflow-bash-syntax.sh — bash -n CI guard for workflow run: blocks
#
# Usage: ./scripts/validate-workflow-bash-syntax.sh [file ...]
# Default: every .github/workflows/*.yml and .github/actions/*/action.yml
#
# Parses each GitHub Actions workflow or composite action, extracts every
# bash run: block, and runs 'bash -n' on it. Exits non-zero if any block has
# a syntax error. Run by the docs-lint job in .github/workflows/ci.yml.

set -euo pipefail

if [ $# -eq 0 ]; then
  set -- .github/workflows/*.yml .github/actions/*/action.yml
fi

python3 - "$@" <<'PYEOF'
import yaml, subprocess, sys, tempfile, os

def steps_of(doc):
    for job_name, job in (doc.get('jobs') or {}).items():
        default_shell = ((job.get('defaults') or {}).get('run') or {}).get('shell') \
            or ((doc.get('defaults') or {}).get('run') or {}).get('shell') or 'bash'
        for step in (job.get('steps') or []):
            yield job_name, default_shell, step
    runs = doc.get('runs') or {}
    for step in (runs.get('steps') or []):
        yield 'composite', 'bash', step

failures = []
checked = 0
for workflow_file in sys.argv[1:]:
    if not os.path.isfile(workflow_file):
        continue
    with open(workflow_file) as f:
        wf = yaml.safe_load(f) or {}
    for job_name, default_shell, step in steps_of(wf):
        script = step.get('run')
        if not script:
            continue
        if not str(step.get('shell', default_shell)).startswith('bash'):
            continue
        checked += 1
        step_name = step.get('name', f'unnamed-step-{checked}')
        with tempfile.NamedTemporaryFile(mode='w', suffix='.sh', delete=False) as tf:
            tf.write(script)
            tf_path = tf.name
        result = subprocess.run(['bash', '-n', tf_path], capture_output=True, text=True)
        os.unlink(tf_path)
        if result.returncode != 0:
            failures.append(f'  {workflow_file} job={job_name} step="{step_name}": {result.stderr.strip()}')

print(f'[bash-n-check] Checked {checked} run: blocks in {len(sys.argv) - 1} file(s)')
if failures:
    print('[bash-n-check] FAIL — bash syntax errors found:')
    for err in failures:
        print(err)
    sys.exit(1)
print('[bash-n-check] All run: blocks are syntactically valid')
PYEOF
