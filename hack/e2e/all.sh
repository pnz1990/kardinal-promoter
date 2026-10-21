#!/usr/bin/env bash
# hack/e2e/all.sh [-list | -matrix]
#
# Runs every live e2e job in hack/e2e/matrix.txt on this host, JOBS at a
# time, each on a kind cluster of its own (up.sh, then run.sh), deletes the
# clusters, and proves the coverage with test/e2e/proof over all of their
# results. Pull requests don't run the live suites in CI, so this is the full
# suite before a merge. Fails when a job fails, or the proof does.
#
# The github job runs only when KARDINAL_E2E_GITHUB_TOKEN_FILE or
# DEMO_GITHUB_TOKEN is set (components/github.sh); without one it is listed
# as not run, and your gh login is never used. The scheduled CI runs (nightly, weekly) cover it.
# No other job sees the token.
#
#   -list    print the jobs this run would start and exit
#   -matrix  print every job (of SUITES, when set) as the GitHub Actions
#            matrix of e2e-live.yml
#
# Env:
#   JOBS        jobs at once (default 4; each cluster takes 2-3 GB of
#               memory, gitlab's about 7 GB; multi-cluster's job has two)
#   SUITES      only these suites, e.g. 'core gitea' (default every suite)
#   MINORS      run every suite on these Kubernetes minors, e.g. '1.35 1.37',
#               instead of matrix.txt's (each suite keeps its shards); a
#               suite matrix.txt pins runs only on those of its own minors
#   SAMPLE      a date (YYYY-MM-DD, or today): add that night's sample jobs
#               (hack/e2e/sample.sh), as the scheduled e2e-live.yml runs do
#   SAMPLE_ONLY set with SAMPLE to run the sample jobs alone
#   COUNT       go test -count (default 1; the upgrade jobs always 1: the
#               test upgrades its cluster; the scale job always 1). RUN is ignored: every job runs
#               its whole suite (for some tests, use make test-e2e-live RUN=)
#   KEEP        set to keep the clusters (default: each is deleted when its
#               job ends)
#   ALL_PREFIX  cluster name prefix (default kardinal-e2e-all); two runs on
#               one host need different prefixes
#   COMPLETE    set to also fail when a coverage row is todo or did not run
#               (proof -complete); it fails until every todo row in
#               test/e2e/coverage.tsv has a live test
#   plus up.sh's and components/kardinal.sh's, e.g. KARDINAL_E2E_BUILD=host
#   where docker build can't download Go modules
#
# Results in test/e2e/results/all-<UTC time>/ (with ALL_PREFIX, the prefix
# without kardinal-e2e- instead of all): <job>.log, <job>/ (the job's
# E2E_OUT: env, kubeconfig, test.json, summary.json, diagnostics/) and
# coverage-proof.json.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$E2E_DIR/../.." && pwd)"
MODE=${1:-}
case "$MODE" in
  '' | -list | -matrix) ;;
  *)
    echo "usage: $0 [-list | -matrix]" >&2
    exit 1
    ;;
esac

log() { printf '[e2e-all %s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() {
  log "ERROR: $*"
  exit 1
}

# One "id suite k8s shard count" entry per matrix.txt line; the id names the
# job's cluster, results and CI artifact (core-135-1of3). count - is COUNT.
matrix=()
# pinned_on[suite]: the minors of a suite matrix.txt pins to them (" 1.35 ").
declare -A pinned_on=()
if [ -z "${SAMPLE_ONLY:-}" ]; then
  while read -r suite k8s shard _ pin _ || [ -n "$suite" ]; do
    case "$suite" in '' | '#'*) continue ;; esac
    [ -n "$k8s" ] && [ -n "$shard" ] || die "matrix.txt: '$suite $k8s $shard': want suite, Kubernetes minor and shard"
    [ "${pin:-}" != pinned ] || pinned_on[$suite]+=" $k8s "
    [ "$shard" != - ] || shard=
    id="$suite-${k8s//./}${shard:+-${shard%/*}of${shard#*/}}"
    matrix+=("$id $suite $k8s ${shard:--} -")
  done <"$E2E_DIR/matrix.txt"
fi
# MINORS ("1.35 1.37"): every suite runs on each of these minors instead of
# its matrix.txt ones, with its matrix.txt shards (the release gate,
# hack/release-gate -live ci).
if [ -n "${MINORS:-}" ]; then
  read -ra minors <<<"$MINORS"
  for m in "${minors[@]}"; do
    [[ "$m" =~ ^1\.[0-9]+$ ]] || die "MINORS: $m is not a Kubernetes minor (1.N)"
  done
  declare -A shards_of=()
  order=()
  for j in "${matrix[@]}"; do
    read -r _ suite _ shard _ <<<"$j"
    [ -n "${shards_of[$suite]+x}" ] || order+=("$suite")
    case " ${shards_of[$suite]:-} " in *" $shard "*) ;; *) shards_of[$suite]="${shards_of[$suite]:-} $shard" ;; esac
  done
  matrix=()
  for suite in "${order[@]}"; do
    for m in "${minors[@]}"; do
      # A pinned suite: no job on another minor (the release gate reports it).
      if [ -n "${pinned_on[$suite]:-}" ] && [[ "${pinned_on[$suite]}" != *" $m "* ]]; then
        continue
      fi
      for shard in ${shards_of[$suite]}; do
        s=$shard
        [ "$s" != - ] || s=
        matrix+=("$suite-${m//./}${s:+-${s%/*}of${s#*/}} $suite $m $shard -")
      done
    done
  done
fi
# The night's sample (hack/e2e/sample.sh): jobs matrix.txt does not run.
if [ -n "${SAMPLE:-}" ]; then
  [ "$SAMPLE" != today ] || SAMPLE=$(date -u +%Y-%m-%d)
  sample=$(bash "$E2E_DIR/sample.sh" "$SAMPLE") || die "SAMPLE=$SAMPLE: hack/e2e/sample.sh failed"
  mapfile -t lines <<<"$sample"
  matrix+=("${lines[@]}")
elif [ -n "${SAMPLE_ONLY:-}" ]; then
  die "SAMPLE_ONLY needs SAMPLE"
fi

if [ "$MODE" = -matrix ]; then
  # SUITES (the e2e-live.yml dispatch input) keeps only those suites' jobs.
  read -ra only <<<"${SUITES:-}"
  sep=
  printf '{"include":['
  for j in "${matrix[@]}"; do
    read -r id suite k8s shard count <<<"$j"
    if [ "${#only[@]}" -gt 0 ] && ! [[ " ${only[*]} " == *" $suite "* ]]; then
      continue
    fi
    [ "$shard" != - ] || shard=
    [ "$count" != - ] || count=
    printf '%s{"id":"%s","suite":"%s","k8s":"%s","shard":"%s","count":"%s"}' "$sep" "$id" "$suite" "$k8s" "$shard" "$count"
    sep=,
  done
  printf ']}\n'
  exit 0
fi

read -ra suites <<<"${SUITES:-}"
token=${KARDINAL_E2E_GITHUB_TOKEN_FILE:-}${DEMO_GITHUB_TOKEN:-}
for s in "${suites[@]}"; do
  printf '%s\n' "${matrix[@]}" | awk -v s="$s" '$2 == s { found = 1 } END { exit !found }' ||
    die "SUITES: no suite $s in matrix.txt"
  [ "$s" != github ] || [ -n "$token" ] ||
    die "SUITES: the github suite needs KARDINAL_E2E_GITHUB_TOKEN_FILE or DEMO_GITHUB_TOKEN"
done
run=() skipped=()
for j in "${matrix[@]}"; do
  read -r id suite _ <<<"$j"
  if [ "${#suites[@]}" -gt 0 ] && ! [[ " ${suites[*]} " == *" $suite "* ]]; then
    continue
  fi
  if [ "$suite" = github ] && [ -z "$token" ]; then
    skipped+=("$id")
    continue
  fi
  run+=("$j")
done
[ "${#run[@]}" -gt 0 ] || die "no job to run"
if [ "$MODE" = -list ]; then
  printf '%s\n' "${run[@]}" | cut -d' ' -f1
  for id in "${skipped[@]}"; do echo "$id (not run: no KARDINAL_E2E_GITHUB_TOKEN_FILE or DEMO_GITHUB_TOKEN)"; done
  exit 0
fi

JOBS=${JOBS:-4}
[[ "$JOBS" =~ ^[1-9][0-9]*$ ]] || die "JOBS=$JOBS: want a positive number"
PREFIX=${ALL_PREFIX:-kardinal-e2e-all}
# Once here, so the jobs' up.sh find the pinned tools in place.
bash "$E2E_DIR/tools.sh"
[ "${KARDINAL_E2E_TOOLS:-pinned}" = path ] || PATH="$REPO_ROOT/bin/e2e:$PATH"
existing=$(kind get clusters 2>/dev/null || true)
for j in "${run[@]}"; do
  read -r id _ <<<"$j"
  for c in "$PREFIX-$id" "$PREFIX-$id-spoke"; do
    if grep -qx "$c" <<<"$existing"; then
      die "kind cluster $c exists (a KEEP run, or another run with ALL_PREFIX=$PREFIX); delete it with kind delete cluster --name $c"
    fi
  done
done
if printf '%s\n' "${run[@]}" | grep -q ' core '; then
  command -v zsh >/dev/null || die "the core suite needs zsh on PATH (TestCLI_Completion)"
fi

OUT="$REPO_ROOT/test/e2e/results/${PREFIX#kardinal-e2e-}-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$OUT"

# delete_cluster ID deletes the job's cluster and the multi-cluster suite's
# spoke (components/spoke.sh), if it has one.
delete_cluster() {
  local c
  for c in "$PREFIX-$1" "$PREFIX-$1-spoke"; do
    [ "$c" = "$PREFIX-$1" ] || kind get clusters 2>/dev/null | grep -qx "$c" || continue
    kind delete cluster --name "$c" --kubeconfig "$OUT/$1/kubeconfig" >>"$OUT/$1.log" 2>&1 ||
      log "deleting kind cluster $c failed; see $OUT/$1.log"
  done
}

# run_job ID SUITE K8S SHARD COUNT writes ID.log and ID.rc ("<exit code> <seconds>").
run_job() {
  local id=$1 suite=$2 k8s=$3 shard=$4 count=$5 start rc=0
  [ "$shard" != - ] || shard=
  [ "$count" != - ] || count=${COUNT:-1}
  start=$(date +%s)
  (
    unset RUN
    [ "$suite" = github ] || unset KARDINAL_E2E_GITHUB_TOKEN_FILE DEMO_GITHUB_TOKEN
    export KIND_CLUSTER="$PREFIX-$id" KIND_K8S="$k8s" SHARD="$shard" COUNT="$count"
    # run.sh refuses COUNT above 1 for the upgrade suite; the scale suite's
    # load and chaos tests are long, and repeat nothing a second run adds.
    case $suite in upgrade | upgrade-v090 | upgrade-v0100 | upgrade-v0110 | upgrade-v0120 | upgrade-v0121 | upgrade-v0123 | upgrade-v0130 | scale) COUNT=1 ;; esac
    export E2E_OUT="$OUT/$id" KUBECONFIG="$OUT/$id/kubeconfig"
    mkdir -p "$E2E_OUT"
    bash "$E2E_DIR/up.sh" "$suite" && bash "$E2E_DIR/run.sh" "$suite"
  ) >"$OUT/$id.log" 2>&1 || rc=$?
  [ -n "${KEEP:-}" ] || delete_cluster "$id"
  echo "$rc $(($(date +%s) - start))" >"$OUT/$id.rc"
}

# Each job is a process group of its own (set -m), so an interrupt stops
# every job's up.sh, kind and go test, then deletes the clusters. The loops
# below wait with the wait builtin, not sleep: under set -m a foreground
# sleep would take the terminal's Ctrl-C, and the trap would never run.
set -m
pids=()
stop() {
  trap - INT TERM
  log "interrupted; stopping the jobs"
  for p in "${pids[@]}" $(jobs -p); do kill -TERM -- "-$p" 2>/dev/null || true; done
  wait || true
  if [ -z "${KEEP:-}" ]; then
    for j in "${run[@]}"; do
      read -r id _ <<<"$j"
      [ ! -d "$OUT/$id" ] || delete_cluster "$id"
    done
  fi
  exit "$1"
}
trap 'stop 130' INT
trap 'stop 143' TERM

start=$(date +%s)
log "${#run[@]} jobs, $JOBS at a time; results in $OUT"
for j in "${run[@]}"; do
  while [ "$(jobs -rp | wc -l)" -ge "$JOBS" ]; do wait -n || true; done
  read -r id suite k8s shard <<<"$j"
  log "start $id (cluster $PREFIX-$id, log $OUT/$id.log)"
  # shellcheck disable=SC2086
  run_job $j &
  pids+=("$!")
done
while [ "$(jobs -rp | wc -l)" -gt 0 ]; do wait -n || true; done
wait || true
trap - INT TERM
set +m

failed=0
printf '\n%-18s %-13s %s\n' JOB RESULT MINUTES
for j in "${run[@]}"; do
  read -r id _ <<<"$j"
  read -r rc secs <"$OUT/$id.rc" || { rc=1 secs=0; }
  if [ "$rc" -eq 0 ]; then
    result=pass
  elif [ -f "$OUT/$id/summary.json" ]; then
    result=FAIL failed=1
  else
    result='SETUP FAILED' failed=1
  fi
  printf '%-18s %-13s %d\n' "$id" "$result" $(((secs + 59) / 60))
done
for id in "${skipped[@]}"; do printf '%-18s %s\n' "$id" 'not run (no token)'; done
log "jobs took $((($(date +%s) - start + 59) / 60)) min"

mapfile -t files < <(find "$OUT" -mindepth 2 -maxdepth 2 -name summary.json | sort)
if [ "${#files[@]}" -eq 0 ]; then
  log "no job wrote results; see the logs in $OUT"
  exit 1
fi
cd "$REPO_ROOT"
proof=0
go run ./test/e2e/proof ${COMPLETE:+-complete} -out "$OUT/coverage-proof.json" "${files[@]}" || proof=$?
[ "$failed" -eq 0 ] || log "a job failed; its log and diagnostics are in $OUT"
[ "$proof" -eq 0 ] || log "the coverage proof failed"
[ "$failed" -eq 0 ] && [ "$proof" -eq 0 ]
