#!/usr/bin/env bash
# hack/e2e/local-live.sh [-pr N] [-shard i/n] [-reuse CLUSTER] [-badge B] [-o OWNER] [-n] [-no-lock] SUITE [MINOR]
#
# Runs one live e2e suite on this host as its CI job does (e2e-live.yml:
# hack/e2e/tools.sh's pinned kind, kubectl and helm, a kind cluster on
# MINOR's pinned node image, up.sh, then run.sh), on a cluster of its own,
# and prints a pass/fail summary. It is the pre-merge live proof when
# GitHub's runners are throttled: with -pr N it posts the summary as a
# comment on pull request N.
#
# A shared host: every run takes a cluster slot (DIR/1..SLOT_MAX, taken with
# mkdir, as hack/release-gate -slots does; DIR from $RELEASE_GATE_SLOTS;
# the multi-cluster suite takes two), and before it creates its cluster waits
# until the host's one-minute load is under 48 (headroom under the gate's 64
# for the clusters already running) and fs.inotify.max_user_instances leaves
# room for another cluster's node. Only the heavy suites (HEAVY_SUITES below)
# also take the heavy-load lock first (hack/loadlock.sh, LOCK from
# $LOADLOCK): an ordinary live suite does not need it. All are released, and
# the cluster (and the multi-cluster suite's spoke) deleted, when the run
# ends, however it ends. -no-lock runs without the lock, a slot or the gate
# (a host of your own).
#
# A cluster's state (up.sh's E2E_OUT: its kubeconfig, the env file the
# tests source, the git server's and Argo CD's generated secrets) lives in
# STATE/<cluster>, not in the worktree that created it, so a run from any
# worktree can reuse a cluster another one set up (and kept): -reuse
# CLUSTER rebuilds and reinstalls only the controller from this checkout
# (up.sh KARDINAL_E2E_REUSE) and runs the suite. Each run's log, report and
# comment are kept in STATE/<cluster>/runs/<time>.
#
#   SUITE      a suite of hack/e2e/matrix.txt (up.sh lists them)
#   MINOR      the Kubernetes minor, e.g. 1.37 (default: the suite's first
#              minor in matrix.txt); it must have a KIND_NODE_1_<minor> pin
#              in hack/tool-versions.env
#   -pr N      post the summary on pull request N (gh pr comment; the
#              repository is $GH_REPO, default pnz1990/kardinal-promoter)
#   -shard i/n run only the ith of n parts of the suite (run.sh SHARD);
#              default the whole suite
#   -badge B   the first line of the comment, e.g. "[🔨 ENGINEER-4]"
#   -o OWNER   who holds the lock and slot, and names the cluster (OWNER)
#              (default $AGENT_BADGE)
#   -reuse C   run on kind cluster C, set up for SUITE by an earlier run
#              (KEEP) and still there; it is deleted at the end unless KEEP
#              is set again. MINOR defaults to the one C was created on
#   -n         print what would run and exit
#   -no-lock   take neither the lock nor a slot
#
# Env:
#   KARDINAL_E2E_BUILD  default host: the controller is built with the host's
#                Go and module cache (where docker build can't download
#                Go modules); docker builds it as CI does
#   COUNT        go test -count (default 1)
#   KIND_CLUSTER cluster name (default kardinal-e2e-ll-<owner>-SUITE-<minor>,
#                <owner> $AGENT_ID, else OWNER's first word, lowercased, so
#                two agents' runs of one suite do not share a name); it
#                must not exist: this run deletes it at the end
#   KEEP         set to keep the cluster, its state and its slot(s), for a
#                later -reuse
#   KARDINAL_E2E_STATE  STATE (default: e2e-state next to $LOADLOCK, the
#                host's shared directory; test/e2e/results with -no-lock)
#   SLOT_MAX     the host's cluster slots (default 4)
#   OWNER        who holds the lock and slot (default $AGENT_ID or $USER)
#   RETRY        seconds between tries for a slot, and the longest a lock
#                waiter sleeps (default 10; hack/loadlock.sh wakes sooner)
#
# Exits with the suite's status (the comment, when asked for, is posted
# either way).
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

# HEAVY_SUITES take the heavy-load lock as well as a cluster slot: the scale
# suite (its full and soak profiles too) and multi-cluster's two clusters.
HEAVY_SUITES="scale multi-cluster"
# The load gate: a cluster starts only while the one-minute load is under
# MAX_LOAD, checked every LOAD_RETRY seconds.
MAX_LOAD=48
LOAD_RETRY=30

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SELF="$HERE/$(basename "${BASH_SOURCE[0]}")"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
LOADLOCK_SH=$REPO_ROOT/hack/loadlock.sh
# A copy outside a checkout (the host's shared one, next to the shared
# loadlock.sh) runs the checkout of the directory it is run from: its
# matrix, pins, up.sh and run.sh, and the loadlock.sh next to it.
if [ ! -f "$REPO_ROOT/hack/e2e/matrix.txt" ]; then
  REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null) && [ -f "$REPO_ROOT/hack/e2e/matrix.txt" ] ||
    { echo "local-live: run it from a kardinal-promoter checkout" >&2; exit 2; }
  LOADLOCK_SH=$REPO_ROOT/hack/loadlock.sh
  [ ! -f "$HERE/loadlock.sh" ] || LOADLOCK_SH=$HERE/loadlock.sh
fi
E2E_DIR=${LOCAL_LIVE_E2E_DIR:-$REPO_ROOT/hack/e2e}
ARGS=("$@")

die() {
  echo "local-live: $*" >&2
  exit 2
}
log() { printf '[local-live %s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
usage() { die "usage: $0 [-pr N] [-shard i/n] [-reuse CLUSTER] [-badge B] [-o OWNER] [-n] [-no-lock] SUITE [MINOR]"; }

pr='' shard='' badge=${AGENT_BADGE:-} dry='' nolock='' reuse=''
while [ $# -gt 0 ]; do
  case $1 in
    -pr | --pr) [ $# -ge 2 ] || usage; pr=$2; shift 2 ;;
    -shard | --shard) [ $# -ge 2 ] || usage; shard=$2; shift 2 ;;
    -reuse | --reuse) [ $# -ge 2 ] || usage; reuse=$2; shift 2 ;;
    -badge | --badge) [ $# -ge 2 ] || usage; badge=$2; shift 2 ;;
    -o) [ $# -ge 2 ] || usage; OWNER=$2; shift 2 ;;
    -n) dry=1; shift ;;
    -no-lock | --no-lock) nolock=1; shift ;;
    --) shift; break ;;
    -*) usage ;;
    *) break ;;
  esac
done
[ $# -ge 1 ] && [ $# -le 2 ] || usage
SUITE=$1 MINOR=${2:-}
[ -z "$pr" ] || [[ "$pr" =~ ^[1-9][0-9]*$ ]] || die "-pr $pr: want a pull request number"
[ -z "$shard" ] || [[ "$shard" =~ ^[1-9][0-9]*/[1-9][0-9]*$ ]] || die "-shard $shard: want i/n"

# The suite and its minors come from matrix.txt, the pins from
# tool-versions.env: the same files CI's matrix and tools come from.
minors=$(awk -v s="$SUITE" '!/^#/ && $1 == s { print $2 }' "$REPO_ROOT/hack/e2e/matrix.txt" | uniq)
[ -n "$minors" ] || die "$SUITE is not a suite of hack/e2e/matrix.txt"
LOCK=${LOADLOCK:-}
heavy=''
[[ " $HEAVY_SUITES " == *" $SUITE "* ]] && heavy=1
if [ -n "${KARDINAL_E2E_STATE:-}" ]; then
  STATE=$KARDINAL_E2E_STATE
elif [ -z "$nolock" ] && [ -n "$LOCK" ]; then
  STATE=$(dirname "$LOCK")/e2e-state
elif [ -z "$nolock" ] && [ -n "${RELEASE_GATE_SLOTS:-}" ]; then
  STATE=$(dirname "$RELEASE_GATE_SLOTS")/e2e-state
else
  STATE=$REPO_ROOT/test/e2e/results
fi
if [ -n "$reuse" ]; then
  [ -z "${KIND_CLUSTER:-}" ] || [ "$KIND_CLUSTER" = "$reuse" ] || die "-reuse $reuse and KIND_CLUSTER=$KIND_CLUSTER disagree"
  KIND_CLUSTER=$reuse
  [ -f "$STATE/$reuse/env" ] || die "-reuse $reuse: no $STATE/$reuse/env (a cluster this script set up and kept)"
  grep -qx "export KARDINAL_E2E_SUITE=$SUITE" "$STATE/$reuse/env" || die "-reuse $reuse: it was set up for another suite than $SUITE ($STATE/$reuse/env)"
  [ -n "$MINOR" ] || MINOR=$(cat "$STATE/$reuse/minor" 2>/dev/null || true)
fi
MINOR=${MINOR:-$(head -1 <<<"$minors")}
[[ "$MINOR" =~ ^1\.[0-9]+$ ]] || die "$MINOR is not a Kubernetes minor (1.N)"
# shellcheck source=hack/tool-versions.env
source "$REPO_ROOT/hack/tool-versions.env"
pin="KIND_NODE_1_${MINOR#1.}"
NODE_IMAGE=${!pin:-}
[ -n "$NODE_IMAGE" ] || die "no $pin in hack/tool-versions.env: the pinned minors are $(grep -o '^KIND_NODE_1_[0-9]*' "$REPO_ROOT/hack/tool-versions.env" | sed 's/KIND_NODE_1_/1./' | tr '\n' ' ')"

export KARDINAL_E2E_BUILD=${KARDINAL_E2E_BUILD:-host}
OWNER=${OWNER:-${AGENT_ID:-${USER:-unknown}}}
# The owner's part of the default cluster name: AGENT_ID, else OWNER's first
# word, as a DNS label part (lowercase letters, digits and dashes, 16 at most).
owner_part=${AGENT_ID:-${OWNER%% *}}
owner_part=$(printf '%s' "$owner_part" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9-' '-' | tr -s '-' | cut -c1-16 | sed 's/^-*//; s/-*$//')
[ -n "$owner_part" ] || owner_part=unknown
export KIND_CLUSTER=${KIND_CLUSTER:-kardinal-e2e-ll-$owner_part-$SUITE-${MINOR#1.}}
case $KIND_CLUSTER in kardinal-e2e-*) ;; *) die "KIND_CLUSTER=$KIND_CLUSTER must start with kardinal-e2e-" ;; esac
COUNT=${COUNT:-1}
RETRY=${RETRY:-10}
SLOT_MAX=${SLOT_MAX:-4}
SLOTS_DIR=${RELEASE_GATE_SLOTS:-}
need_slots=1
[ "$SUITE" != multi-cluster ] || need_slots=2

if [ -z "$nolock" ]; then
  [ -z "$heavy" ] || [ -n "$LOCK" ] || die "LOADLOCK is not set: the heavy-load lock the $SUITE suite takes (or -no-lock on a host of your own)"
  [ -n "$SLOTS_DIR" ] || die "RELEASE_GATE_SLOTS is not set: the host's cluster slots directory (or -no-lock on a host of your own)"
fi

if [ -n "$dry" ]; then
  lock_d="$LOCK" slots_d="$need_slots of $SLOTS_DIR/1..$SLOT_MAX" pr_d=none
  [ -n "$heavy" ] || lock_d="none (not a heavy suite)"
  [ -z "$nolock" ] || lock_d=none slots_d=none
  [ -z "$pr" ] || pr_d="pull request $pr"
  cat <<EOF
suite    $SUITE${shard:+ shard $shard}
minor    $MINOR ($NODE_IMAGE)
cluster  $KIND_CLUSTER${reuse:+ (reused: only the controller is rebuilt)}
state    $STATE/$KIND_CLUSTER
build    $KARDINAL_E2E_BUILD
count    $COUNT
lock     $lock_d
slots    $slots_d
comment  $pr_d
EOF
  exit 0
fi

# A heavy suite takes the lock first, by running this script again under
# hack/loadlock.sh.
if [ -z "$nolock" ] && [ -n "$heavy" ] && [ -z "${_LOCAL_LIVE_LOCKED:-}" ]; then
  exec bash "$LOADLOCK_SH" -l "$LOCK" -o "$OWNER local-live $SUITE $MINOR" -r "$RETRY" -- \
    env _LOCAL_LIVE_LOCKED=1 bash "$SELF" "${ARGS[@]}"
fi

stamp=$(date -u +%Y%m%dT%H%M%SZ)
export E2E_OUT=$STATE/$KIND_CLUSTER
# Every kubectl, helm and kind of the run talks to this cluster alone:
# never to the user's kubeconfig.
export KUBECONFIG=$E2E_OUT/kubeconfig
RUN_DIR=$E2E_OUT/runs/$stamp
mkdir -p "$RUN_DIR"
LOG=$RUN_DIR/run.log
slot_tag="$OWNER local-live $SUITE $MINOR pid $$ $stamp"
taken=()

release_slots() {
  local d
  for d in "${taken[@]}"; do
    if [ "$(cat "$d/owner" 2>/dev/null)" = "$slot_tag" ]; then
      if [ -n "${KEEP:-}" ] && [ -n "$cluster_ours" ]; then
        log "KEEP: cluster slot $d stays held for $KIND_CLUSTER"
      else
        rm -r "$d"
      fi
    else
      log "cluster slot $d is not ours any more; left as it is"
    fi
  done
  taken=()
}

take_slots() {
  local i d
  # A kept cluster's slots name it: a -reuse run takes them over.
  for ((i = 1; i <= SLOT_MAX; i++)); do
    d=$SLOTS_DIR/$i
    if [ "$(cat "$d/cluster" 2>/dev/null)" = "$KIND_CLUSTER" ] && [ "${#taken[@]}" -lt "$need_slots" ]; then
      echo "$slot_tag" >"$d/owner"
      taken+=("$d")
    fi
  done
  while :; do
    for ((i = 1; i <= SLOT_MAX && ${#taken[@]} < need_slots; i++)); do
      d=$SLOTS_DIR/$i
      mkdir "$d" 2>/dev/null || continue
      echo "$slot_tag" >"$d/owner"
      echo "$KIND_CLUSTER" >"$d/cluster"
      taken+=("$d")
    done
    [ "${#taken[@]}" -lt "$need_slots" ] || return 0
    release_slots
    log "fewer than $need_slots of the $SLOT_MAX cluster slots in $SLOTS_DIR are free; retrying in ${RETRY}s"
    sleep "$RETRY" &
    wait $! || true
  done
}

# room says why another cluster may not start now, or nothing when it may:
# the one-minute load is at MAX_LOAD or more, or fs.inotify.max_user_instances
# is under 128 per kind node running and the one to come (kind nodes hang at
# boot without them). LOCAL_LIVE_TEST=1 lets tests set the load and the
# limit (LOCAL_LIVE_TEST_LOAD, LOCAL_LIVE_TEST_INOTIFY).
room() {
  local load max nodes
  load=$(cut -d' ' -f1 /proc/loadavg)
  max=$(cat /proc/sys/fs/inotify/max_user_instances 2>/dev/null || echo 0)
  if [ "${LOCAL_LIVE_TEST:-}" = 1 ]; then
    load=${LOCAL_LIVE_TEST_LOAD:-$load}
    max=${LOCAL_LIVE_TEST_INOTIFY:-$max}
  fi
  nodes=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -c -- '-control-plane$' || true)
  if ! awk -v l="$load" -v m="$MAX_LOAD" 'BEGIN { exit !(l < m) }'; then
    echo "the one-minute load is $load, not under $MAX_LOAD"
  elif [ "$max" -lt $((128 * (nodes + need_slots))) ]; then
    echo "fs.inotify.max_user_instances is $max, under 128 for each of $nodes kind nodes and $need_slots more (sudo sysctl -w fs.inotify.max_user_instances=8192)"
  fi
}

# wait_for_room waits until another cluster may start.
wait_for_room() {
  local why said=
  while why=$(room) && [ -n "$why" ]; do
    [ "$why" = "$said" ] || log "waiting to create $KIND_CLUSTER: $why; checking every ${LOAD_RETRY}s"
    said=$why
    sleep "$LOAD_RETRY" &
    napping=$!
    wait "$napping" || true
    napping=''
  done
}

# forget_state drops a deleted cluster's state but its runs' logs: its
# kubeconfig, env file and generated credentials are of no use any more.
forget_state() {
  rm -f "$E2E_OUT/env" "$E2E_OUT/kubeconfig" "$E2E_OUT/minor"
  [ ! -d "$E2E_OUT/secrets" ] || rm -r "$E2E_OUT/secrets"
}

# shellcheck disable=SC2329 # cleanup (the EXIT trap) runs it
delete_cluster() {
  if [ -n "${KEEP:-}" ]; then
    log "KEEP: kept kind cluster $KIND_CLUSTER; reuse it from any worktree: hack/e2e/local-live.sh -reuse $KIND_CLUSTER $SUITE"
    return 0
  fi
  local c
  for c in "$KIND_CLUSTER" "$KIND_CLUSTER-spoke"; do
    kind get clusters 2>/dev/null | grep -qx "$c" || continue
    log "deleting kind cluster $c"
    kind delete cluster --name "$c" --kubeconfig "$KUBECONFIG" >>"$LOG" 2>&1 || log "deleting kind cluster $c failed; see $LOG"
  done
  forget_state
}

suite_pid=''
# shellcheck disable=SC2329 # the EXIT trap
cleanup() {
  local rc=$?
  trap - EXIT INT TERM
  [ -z "${napping:-}" ] || kill "$napping" 2>/dev/null || true
  if [ -n "$suite_pid" ] && kill -0 "$suite_pid" 2>/dev/null; then
    kill -TERM -- "-$suite_pid" 2>/dev/null || kill -TERM "$suite_pid" 2>/dev/null || true
    wait "$suite_pid" 2>/dev/null || true
  fi
  [ -z "$cluster_ours" ] || delete_cluster
  release_slots
  exit "$rc"
}
cluster_ours=''
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [ -z "$nolock" ]; then
  mkdir -p "$SLOTS_DIR"
  take_slots
  log "holding cluster slot(s) ${taken[*]}"
fi

bash "$E2E_DIR/tools.sh"
[ "${KARDINAL_E2E_TOOLS:-pinned}" = path ] || PATH="$REPO_ROOT/bin/e2e:$PATH"
if [ -n "$reuse" ]; then
  kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER" || die "-reuse $KIND_CLUSTER: no such kind cluster"
else
  for c in "$KIND_CLUSTER" "$KIND_CLUSTER-spoke"; do
    if kind get clusters 2>/dev/null | grep -qx "$c"; then
      die "kind cluster $c exists: another run's, or a KEEP run's (reuse it with -reuse $KIND_CLUSTER); delete it, or set KIND_CLUSTER"
    fi
  done
  [ -n "$nolock" ] || wait_for_room
  # A deleted cluster's leftover state would point up.sh at its old secrets.
  forget_state
  echo "$MINOR" >"$E2E_OUT/minor"
fi
rm -f "$E2E_OUT/test.json" "$E2E_OUT/summary.json" "$E2E_OUT/summary.md"
[ "$SUITE" != core ] || command -v zsh >/dev/null || die "the core suite needs zsh on PATH (TestCLI_Completion)"

head_sha=$(git -C "$REPO_ROOT" rev-parse HEAD)
dirty=$(git -C "$REPO_ROOT" status --porcelain --untracked-files=no | head -1)
start=$(date +%s)
log "suite $SUITE${shard:+ shard $shard} on Kubernetes $MINOR, cluster $KIND_CLUSTER, build $KARDINAL_E2E_BUILD; log $LOG"

# The suite runs in a process group of its own (set -m), so a signal stops
# up.sh's kind and run.sh's go test with it.
cluster_ours=1
rc=0
set -m
(
  [ "$SUITE" = github ] || unset KARDINAL_E2E_GITHUB_TOKEN_FILE DEMO_GITHUB_TOKEN
  unset RUN
  export KIND_K8S=$MINOR SHARD=$shard COUNT
  [ -z "$reuse" ] || export KARDINAL_E2E_REUSE=1
  # run.sh's report writes its Markdown summary here, as CI's job summary.
  export GITHUB_STEP_SUMMARY=$E2E_OUT/summary.md
  bash "$E2E_DIR/up.sh" "$SUITE" && bash "$E2E_DIR/run.sh" "$SUITE"
) >"$LOG" 2>&1 &
suite_pid=$!
set +m
wait "$suite_pid" || rc=$?
suite_pid=''
mins=$((($(date +%s) - start + 59) / 60))

for f in test.json summary.json summary.md; do
  [ ! -f "$E2E_OUT/$f" ] || cp "$E2E_OUT/$f" "$RUN_DIR/$f"
done
result=PASS
[ "$rc" -eq 0 ] || result=FAIL
tests=''
if [ -f "$E2E_OUT/summary.json" ] && command -v jq >/dev/null; then
  tests=$(jq -r '[.results[] | .action] | group_by(.) | map("\(length) \(.[0])") | join(", ")' "$E2E_OUT/summary.json" 2>/dev/null || true)
fi
body=$RUN_DIR/comment.md
{
  [ -z "$badge" ] || echo "$badge"
  echo "## local live: $SUITE${shard:+ shard $shard} on Kubernetes $MINOR: $result"
  echo
  echo "| | |"
  echo "|---|---|"
  echo "| commit | \`$head_sha\`${dirty:+ plus uncommitted changes (not a proof of this commit)} |"
  echo "| result | $result (exit $rc)${tests:+, tests: $tests} |"
  echo "| time | $mins min |"
  echo "| node | \`$NODE_IMAGE\` |"
  echo "| tools | kind $KIND_VERSION, kubectl=$KUBECTL_VERSION, helm=$HELM_VERSION (hack/tool-versions.env) |"
  echo "| build | KARDINAL_E2E_BUILD=$KARDINAL_E2E_BUILD, COUNT=$COUNT${reuse:+, on reused cluster $KIND_CLUSTER (controller rebuilt)} |"
  echo "| host | $(hostname -s 2>/dev/null || echo ?), load $(cut -d' ' -f1-3 /proc/loadavg 2>/dev/null || echo ?) |"
  echo
  echo "Re-run: \`hack/e2e/local-live.sh${shard:+ -shard $shard} $SUITE $MINOR\`"
  if [ -s "$E2E_OUT/summary.md" ]; then
    echo
    echo "<details><summary>run.sh's report</summary>"
    echo
    cat "$E2E_OUT/summary.md"
    echo
    echo "</details>"
  elif [ "$rc" -ne 0 ]; then
    echo
    echo "The suite did not report (setup failed?). The last lines of the log:"
    echo
    echo '```'
    tail -n 30 "$LOG"
    echo '```'
  fi
} >"$body"

printf '\n'
cat "$body"
printf '\nlocal-live: %s %s (log %s, results %s)\n' "$result" "$SUITE" "$LOG" "$RUN_DIR"

if [ -n "$pr" ]; then
  if gh pr comment "$pr" --repo "${GH_REPO:-pnz1990/kardinal-promoter}" --body-file "$body" >>"$LOG" 2>&1; then
    log "posted the summary on pull request $pr"
  else
    log "posting the summary on pull request $pr failed (see $LOG); it is in $body"
  fi
fi
exit "$rc"
