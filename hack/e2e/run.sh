#!/usr/bin/env bash
# hack/e2e/run.sh SUITE
#
# Runs SUITE's live tests against the cluster hack/e2e/up.sh set up, with
# the env file it wrote. The output goes through test/e2e/report, which
# fails the run when a test fails or skips, or when no test ran. The raw
# `go test -json` stream is kept in test/e2e/results/<cluster>/test.json and
# the results, for test/e2e/proof, in summary.json next to it.
#
# Env:
#   COUNT         go test -count (default 1; the weekly flake job uses more;
#                 the upgrade suite runs once)
#   RUN           go test -run pattern (default: the suite's, from up.sh)
#   SHARD         i/n runs the ith of n parts of the matching tests, split by
#                 their durations in hack/e2e/durations.txt so the parts
#                 take about as long (CI splits the core suite across jobs
#                 this way)
#   KIND_CLUSTER  cluster name (default kardinal-e2e-SUITE)
#   KARDINAL_E2E_TIMEOUT   go test -timeout (default 90m per repetition; the
#                 scale suite's full and soak profiles need more, e.g. 4h)
#   KARDINAL_E2E_PARALLEL  go test -parallel (default GOMAXPROCS; the scale
#                 suite's default is 4, a CI runner's vCPUs)
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

SUITE=${1:?usage: $0 SUITE}
export KIND_CLUSTER=${KIND_CLUSTER:-kardinal-e2e-$SUITE}
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
[ -f "$E2E_OUT/env" ] || die "no $E2E_OUT/env; run make e2e-up SUITE=$SUITE first"
set -a
# shellcheck disable=SC1091
source "$E2E_OUT/env"
set +a
target_cluster
[ "$KARDINAL_E2E_CONTEXT" = "$CTX" ] || die "env file is for $KARDINAL_E2E_CONTEXT, not $CTX"

cd "$REPO_ROOT"
go build -o "$E2E_OUT/bin/report" ./test/e2e/report
RUN=${RUN:-$KARDINAL_E2E_RUN}
COUNT=${COUNT:-1}
# The upgrade test upgrades the cluster's v0.8.1 release, so it runs once.
[ "$SUITE" != upgrade ] || [ "$COUNT" = 1 ] || die "COUNT=$COUNT: the upgrade suite's cluster serves one run; use COUNT=1"
# shard_tests I N DURATIONS reads test names on stdin and prints part I of
# N. Serial tests are dealt out first, longest first, to the part with the
# least serial time; then the parallel ones, longest first, to the part
# whose estimated wall time (serial seconds plus parallel seconds / 3, the
# runners' effective parallelism) is smallest. Unknown tests cost the
# median. Ties go to the lowest part, so every job computes the same split.
shard_tests() {
  local want=$1 n=$2 durations=$3 median
  median=$(awk '$0 !~ /^#/ && NF >= 3 { print $2 }' "$durations" | sort -n |
    awk '{ v[NR] = $1 } END { print NR ? v[int((NR + 1) / 2)] : 60 }')
  # name seconds kind, serial tests first, each group longest first.
  awk -v median="$median" '
    FNR == NR { if ($0 !~ /^#/ && NF >= 3) { sec[$1] = $2; kind[$1] = $3 } next }
    { t = $1; s = (t in sec) ? sec[t] : median; k = (kind[t] == "serial") ? 0 : 1; print k, s, t }
  ' "$durations" - | LC_ALL=C sort -k1,1n -k2,2nr -k3,3 |
    awk -v want="$want" -v n="$n" '
      {
        best = 1
        for (p = 2; p <= n; p++) {
          if ($1 == 0 && serial[p] + 0 < serial[best] + 0) best = p
          if ($1 == 1 && serial[p] + parallel[p] / 3 < serial[best] + parallel[best] / 3) best = p
        }
        if ($1 == 0) serial[best] += $2; else parallel[best] += $2
        if (best == want) print $3
      }'
}

if [ -n "${SHARD:-}" ]; then
  if ! [[ "$SHARD" =~ ^([0-9]+)/([0-9]+)$ ]] || [ "${BASH_REMATCH[1]}" -lt 1 ] ||
    [ "${BASH_REMATCH[1]}" -gt "${BASH_REMATCH[2]}" ]; then
    die "SHARD=$SHARD: want i/n with 1 <= i <= n"
  fi
  i=${BASH_REMATCH[1]} n=${BASH_REMATCH[2]}
  list=$(go test -tags e2e ./test/e2e/live -list "$RUN")
  mapfile -t tests < <(grep '^Test' <<<"$list")
  mapfile -t mine < <(printf '%s\n' "${tests[@]}" | shard_tests "$i" "$n" "$(dirname "$0")/durations.txt")
  [ "${#mine[@]}" -gt 0 ] || die "SHARD=$SHARD: no tests (${#tests[@]} match $RUN)"
  # Test names are identifiers, so the exact-name pattern needs no quoting.
  RUN="^($(IFS='|'; echo "${mine[*]}"))\$"
  echo "shard $SHARD: ${#mine[@]} of ${#tests[@]} tests"
fi
# 90 minutes per repetition unless KARDINAL_E2E_TIMEOUT says.
PARALLEL=${KARDINAL_E2E_PARALLEL:-}
[ -n "$PARALLEL" ] || [ "$SUITE" != scale ] || PARALLEL=4
# report decides: go test also fails the package for an expected failure
# (scale.KnownBug), which report does not count as one.
set +o pipefail
go test -tags e2e ./test/e2e/live -run "$RUN" -count="$COUNT" -timeout "${KARDINAL_E2E_TIMEOUT:-$((90 * COUNT))m}" \
  ${PARALLEL:+-parallel "$PARALLEL"} -json 2>&1 |
  tee "$E2E_OUT/test.json" | "$E2E_OUT/bin/report" -suite "$SUITE" -out "$E2E_OUT/summary.json"
exit "${PIPESTATUS[2]}"
