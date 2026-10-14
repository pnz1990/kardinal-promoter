#!/usr/bin/env bash
# hack/e2e/sample.sh [-cycle] [DATE]
#
# Prints the nightly sample for DATE (YYYY-MM-DD, UTC; default today): the
# live e2e jobs the scheduled e2e-live.yml run adds to hack/e2e/matrix.txt's,
# one "id suite k8s shard count" line each, in the format hack/e2e/all.sh
# reads (a shard or count of - is the whole suite or the run's COUNT). The
# sample is a function of the date alone, so a night's jobs can be re-run
# locally:
#
#   SAMPLE=2026-10-14 SAMPLE_ONLY=1 make e2e-all
#
# Two jobs a night, each covering what matrix.txt never runs:
#
#   minor  one suite on a Kubernetes minor matrix.txt does not run it on: a
#          pair (suite, minor) of the suites that run on one minor only
#          (every suite but core, the upgrade suites, scale, github, whose
#          token is for one job, and multi-cluster, whose spoke boots the
#          kind-config image) and the KIND_NODE_1_* minors of
#          hack/tool-versions.env they don't run on. The pairs are sorted,
#          and night N (days since 1970-01-01) runs pair N mod their count,
#          so every pair runs within that many nights.
#   flake  a slice of the core suite on the newest minor, SHARD i/SLICES
#          (default 9: about 17 min of tests), each test repeated COUNT=3,
#          i = N mod SLICES + 1: every core test runs three times in a row
#          within SLICES nights, where the weekly run repeats each one once
#          a week.
#
#   -cycle  print the number of nights after which both rotations repeat
#           ("<pairs> <slices>") and exit
#
# Env: SLICES (default 9).
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$E2E_DIR/../.." && pwd)"
die() {
  echo "sample.sh: $*" >&2
  exit 1
}

cycle=
if [ "${1:-}" = -cycle ]; then
  cycle=1
  shift
fi
[ $# -le 1 ] || die "usage: $0 [-cycle] [YYYY-MM-DD]"
day=${1:-$(date -u +%Y-%m-%d)}
[[ "$day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || die "date $day: want YYYY-MM-DD"
secs=$(date -u -d "$day" +%s 2>/dev/null) || die "date $day: not a date"
night=$((secs / 86400))
SLICES=${SLICES:-9}
[[ "$SLICES" =~ ^[1-9][0-9]*$ ]] || die "SLICES=$SLICES: want a positive number"

# The minors every suite can boot: KIND_NODE_1_<minor>.
mapfile -t minors < <(sed -n 's/^KIND_NODE_1_\([0-9]*\)=.*/1.\1/p' "$REPO_ROOT/hack/tool-versions.env" | sort -V)
[ "${#minors[@]}" -gt 0 ] || die "hack/tool-versions.env has no KIND_NODE_1_* image"
newest=${minors[${#minors[@]} - 1]}

# suite -> the minors matrix.txt runs it on.
declare -A runs_on=()
while read -r suite k8s _ || [ -n "$suite" ]; do
  case "$suite" in '' | '#'*) continue ;; esac
  runs_on[$suite]+=" $k8s "
done <"$E2E_DIR/matrix.txt"

pairs=()
for suite in $(printf '%s\n' "${!runs_on[@]}" | sort); do
  case "$suite" in core | scale | github | multi-cluster | upgrade*) continue ;; esac
  for m in "${minors[@]}"; do
    [[ "${runs_on[$suite]}" == *" $m "* ]] || pairs+=("$suite $m")
  done
done
[ "${#pairs[@]}" -gt 0 ] || die "no (suite, minor) pair left to sample"

if [ -n "$cycle" ]; then
  echo "${#pairs[@]} $SLICES"
  exit 0
fi

read -r suite m <<<"${pairs[night % ${#pairs[@]}]}"
echo "sample-$suite-${m//./} $suite $m - -"
i=$((night % SLICES + 1))
echo "sample-core-${newest//./}-${i}of${SLICES}x3 core $newest $i/$SLICES 3"
