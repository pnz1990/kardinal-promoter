#!/usr/bin/env bash
# sm-metrics-trend.sh — SM batch metrics trend analysis
#
# Reads docs/aide/metrics.md, computes trends over the last N rows,
# and outputs [METRICS ALERT] or [METRICS TREND] lines when delivery or
# test coverage is declining. Exits 0 always (informational output only).
#
# Called from SM §4b-metrics-trend in ~/.otherness/agents/phases/sm.md
# after the batch report is posted.
#
# Usage:
#   bash scripts/sm-metrics-trend.sh
#   bash scripts/sm-metrics-trend.sh --rows 3          # analyze last 3 rows (default: 5)
#   bash scripts/sm-metrics-trend.sh --metrics-file <path>  # override metrics file path
#
# Exits 0 on success or on missing/insufficient data (fail-open).
# Never exits non-zero — does not block the SM batch.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

set -euo pipefail

METRICS_FILE="docs/aide/metrics.md"
ROWS=5

# Parse args
while [[ $# -gt 0 ]]; do
  case "$1" in
    --rows)
      ROWS="$2"
      shift 2
      ;;
    --metrics-file)
      METRICS_FILE="$2"
      shift 2
      ;;
    *)
      echo "Unknown arg: $1" >&2
      exit 0  # fail-open
      ;;
  esac
done

# Run the trend analysis via Python (reliable table parsing)
python3 - <<PYEOF
import re, os, sys

metrics_file = '${METRICS_FILE}'
rows_limit = int('${ROWS}')

# Fail-open: missing file is a warning, not an error
if not os.path.exists(metrics_file):
    print(f'[SM §4b-metrics-trend] {metrics_file} not found — skipping trend analysis (fail-open)')
    sys.exit(0)

try:
    with open(metrics_file) as f:
        raw = f.read()
except Exception as e:
    print(f'[SM §4b-metrics-trend] Could not read {metrics_file}: {e} — skipping (fail-open)')
    sys.exit(0)

# Extract table rows (pipe-separated, skip headers and separators)
rows = []
for line in raw.splitlines():
    line = line.strip()
    if not line.startswith('|'):
        continue
    if line.startswith('| Date') or line.startswith('|---') or re.match(r'^\|[-| ]+\|$', line):
        continue
    cells = [c.strip() for c in line.split('|') if c.strip()]
    if len(cells) >= 3:
        rows.append(cells)

if len(rows) < 3:
    print(f'[SM §4b-metrics-trend] Not enough data rows ({len(rows)}) in {metrics_file} — need ≥3 for trend (fail-open)')
    sys.exit(0)

# Take last N rows
recent = rows[-rows_limit:]
print(f'[SM §4b-metrics-trend] Analyzing last {len(recent)} rows of {metrics_file}')

def parse_int(s):
    """Extract first integer from cell; return None if not parseable."""
    m = re.search(r'\d+', str(s).replace(',', ''))
    return int(m.group(0)) if m else None

# Column indices (based on metrics.md header):
# 0=date, 1=PRs merged (7d), 2=needs-human, 3=tests, 4=notes, 5=CI
prs_values = [parse_int(r[1]) for r in recent if len(r) > 1]
prs_values = [v for v in prs_values if v is not None]

test_values = [parse_int(r[3]) for r in recent if len(r) > 3]
test_values = [v for v in test_values if v is not None]

ci_values = [r[5].strip() if len(r) > 5 else '' for r in recent]

alerts = []

# Alert 1: PRs merged < 2 for 3+ consecutive batches → delivery declining
if len(prs_values) >= 3:
    low_prs_streak = sum(1 for v in prs_values[-3:] if v < 2)
    if low_prs_streak == 3:
        avg = sum(prs_values[-3:]) / 3
        alerts.append(
            f'[METRICS ALERT: delivery declining] PRs merged < 2 for 3 consecutive batches '
            f'(avg: {avg:.1f}/batch, values: {prs_values[-3:]}). '
            f'COORDINATOR must check queue depth and item quality.'
        )
        print(f'[SM §4b-metrics-trend] ALERT: PRs merged < 2 for last 3 batches: {prs_values[-3:]}')
    else:
        print(f'[SM §4b-metrics-trend] PRs OK: {prs_values[-3:] if len(prs_values)>=3 else prs_values}')

# Alert 2: test count flat or declining for 3+ batches → suggest test coverage item
if len(test_values) >= 3:
    # Skip rows with 0 test counts (missing data)
    nonzero = [v for v in test_values if v > 0]
    if len(nonzero) >= 3:
        last3 = nonzero[-3:]
        is_flat = max(last3) - min(last3) <= 5  # within ±5 = flat
        is_declining = last3[-1] < last3[0]
        direction = None
        if is_flat:
            direction = 'flat'
        elif is_declining:
            direction = 'declining'
        if direction:
            alerts.append(
                f'[METRICS TREND: test coverage {direction}] '
                f'Test count: {last3[0]} → {last3[-1]} over last 3 non-zero batches. '
                f'COORDINATOR should queue a test-coverage item.'
            )
            print(f'[SM §4b-metrics-trend] TREND: test count {direction}: {last3}')
        else:
            print(f'[SM §4b-metrics-trend] Tests OK: {nonzero[-3:]}')
    else:
        print(f'[SM §4b-metrics-trend] Not enough non-zero test rows ({len(nonzero)}) for test trend.')

# Alert 3: CI failures in 2+ of last 3 rows → CI instability
ci_failures = sum(
    1 for c in ci_values[-3:]
    if '❌' in c or 'FAIL' in c.upper() or c == '🔴' or c.strip() == 'x'
)
if ci_failures >= 2:
    alerts.append(
        f'[METRICS ALERT: CI instability] CI failures in {ci_failures}/3 recent batches. '
        f'COORDINATOR must prioritize CI fix items.'
    )
    print(f'[SM §4b-metrics-trend] ALERT: CI instability: {ci_values[-3:]}')
else:
    print(f'[SM §4b-metrics-trend] CI OK: {ci_values[-3:] if len(ci_values)>=3 else ci_values}')

# Print summary
if not alerts:
    print(f'[SM §4b-metrics-trend] No trend alerts. Delivery OK, tests OK, CI OK.')
else:
    print(f'[SM §4b-metrics-trend] {len(alerts)} alert(s):')
    for a in alerts:
        print(f'  {a}')
PYEOF

# Always exit 0 (fail-open: this script is informational, not a gate)
exit 0
