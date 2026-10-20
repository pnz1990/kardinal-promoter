#!/usr/bin/env bash
# hack/e2e/local-live.sh [-pr N] [-shard i/n] [-run REGEX] [-count N] [-p release|normal] [-reuse CLUSTER] [-badge B] [-o OWNER] [-n] [-no-lock] SUITE [MINOR]
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
# the multi-cluster suite takes two). Slots are first come, first served:
# waiters queue in DIR/.queue (hack/loadlock.sh's tickets; release gates'
# first), and only the first takes slots, however often the others poll.
# A slot whose owner is gone (its pid,
# boot id or start time no longer match, as hack/loadlock.sh judges) is
# reclaimed; a KEEP run's slot only when its cluster is gone. Before it
# creates its cluster a run waits until the host's one-minute load is under
# 48 (headroom under the gate's 64 for the clusters already running) and
# fs.inotify.max_user_instances leaves room for another cluster's node; the
# check and the start are serialized across runs (flock), and cluster starts
# are 60s apart, so two runs never pass the gate on the same reading.
#
# The heavy suites (host heavy in hack/e2e/matrix.txt: scale, multi-cluster)
# also take the heavy-load lock first (hack/loadlock.sh, LOCK from
# $LOADLOCK), and drain the host: a waiting heavy run writes DIR/.drain, no
# new run takes a slot while it is there (the drainer is the queue's head
# meanwhile, so it never waits for a waiter that waits for it), and the
# heavy run starts once at most one other slot is in use and the load is
# under 16. The marker stays
# until the heavy run ends, so its numbers are not shared with light runs.
#
# All are released, and the cluster (and the multi-cluster suite's spoke)
# deleted, when the run ends, however it ends. -no-lock runs without the
# lock, a slot or the gates (a host of your own).
#
# A copy outside a checkout (the host's shared one) runs the checkout it is
# run from; when that checkout's local-live.sh is at least as new
# (LOCAL_LIVE_VERSION) and keeps the run off the user's kubeconfig
# (LOCAL_LIVE_ISOLATION), the copy runs it instead.
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
#   -run R     run only the suite's tests matching go test -run R (run.sh
#              RUN): a targeted run (a single test's proof, a flake check
#              with -count). It takes the lock and slot the suite does, and
#              its summary and comment say "targeted": never a proof of the
#              whole suite
#   -count N   go test -count N (1 to 100; default $COUNT, else 1)
#   -p P       release or normal (default): a release run's cluster slot
#              ticket, and its heavy-load lock ticket (hack/loadlock.sh -p),
#              are served before every normal one, as hack/release-gate
#              -priority release's
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
#   SLOT_MAX     the host's cluster slots (default: SLOT_MAX=N in
#                $RELEASE_GATE_SLOTS/../host.env, else 2): no run starts a
#                cluster while SLOT_MAX slots are in use, counting any slot
#                directory above SLOT_MAX a run with a larger one took
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
# Never the user's kubeconfig (on a shared host, the default context can be a
# production cluster): nothing before the run's own kind kubeconfig can reach
# one. The run below points KUBECONFIG at its cluster's file, and HOME at a
# scratch directory, so no ~/.kube/config fallback exists for any child.
# The isolation covers HOME and KUBECONFIG; a child that looks the real home
# up in the password database on purpose (getent passwd, bash ~user, Go's
# os/user) still finds it. Nothing in the tree does.
export KUBECONFIG=/dev/null

# LOCAL_LIVE_VERSION orders copies of this script: the shared copy runs a
# checkout's own when it is at least as new, and only when it says
# LOCAL_LIVE_ISOLATION=kubeconfig,home (#2233, #2244 QA): a copy at 3 or
# above must keep the KUBECONFIG and HOME isolation above and below, and the
# line; one without the line (version 2's a89b89b6f had none of it) is
# never run by a copy that has it. The line must stay byte-identical: the
# hand-off matches it whole (grep -x), so any change to it refuses every
# checkout copy until the shared one is refreshed. Version 4 queues for
# cluster slots (DIR/.queue): a shared copy at 4 runs a checkout's at 3
# never, so every run it starts waits its turn in the queue.
LOCAL_LIVE_VERSION=4
LOCAL_LIVE_ISOLATION=kubeconfig,home
# The load gate: a cluster starts only while the one-minute load is under
# MAX_LOAD (HEAVY_MAX_LOAD for a heavy suite, which also needs at most
# HEAVY_OTHER_SLOTS other slots in use), checked every LOAD_RETRY seconds,
# and cluster starts are at least START_SPACING seconds apart.
MAX_LOAD=48
HEAVY_MAX_LOAD=16
HEAVY_OTHER_SLOTS=1
LOAD_RETRY=30
START_SPACING=60
# DRAIN_DEADLINE: how long a heavy suite may drain the host waiting for its
# start before it gives up (seconds; DRAIN_DEADLINE in the env overrides it).
DRAIN_DEADLINE=${DRAIN_DEADLINE:-1800}

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
  # The checkout's own copy, when it is at least as new, knows its suites
  # best: run it.
  own=$REPO_ROOT/hack/e2e/local-live.sh
  own_version=$(sed -n 's/^LOCAL_LIVE_VERSION=\([0-9]*\)$/\1/p' "$own" 2>/dev/null | head -1)
  if [ "${own_version:-0}" -ge "$LOCAL_LIVE_VERSION" ]; then
    if grep -qx "LOCAL_LIVE_ISOLATION=$LOCAL_LIVE_ISOLATION" "$own" 2>/dev/null; then
      exec bash "$own" "$@"
    fi
    echo "local-live: $own (version $own_version) does not keep the run off the user's kubeconfig (no LOCAL_LIVE_ISOLATION=$LOCAL_LIVE_ISOLATION); running this copy instead" >&2
  fi
fi
E2E_DIR=${LOCAL_LIVE_E2E_DIR:-$REPO_ROOT/hack/e2e}
ARGS=("$@")

die() {
  echo "local-live: $*" >&2
  exit 2
}
log() { printf '[local-live %s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

# The test overrides (LOCAL_LIVE_TEST_*: the load, inotify, spacing, failing
# tools, /proc) are test/hack's fake host's only: on with LOCAL_LIVE_TEST=1
# and LOCAL_LIVE_TEST_TOKEN matching the token file the harness writes in its
# fake e2e directory (E2E_DIR/.local-live-test-token). LOCAL_LIVE_TEST=1
# without it refuses to run, so a stray export never bypasses the host's
# gates (#2244 QA).
test_mode=''
if [ "${LOCAL_LIVE_TEST:-}" = 1 ]; then
  test_token=''
  { read -r test_token <"$E2E_DIR/.local-live-test-token"; } 2>/dev/null || test_token=''
  [ -n "$test_token" ] && [ "${LOCAL_LIVE_TEST_TOKEN:-}" = "$test_token" ] ||
    die "LOCAL_LIVE_TEST=1 is set without the test harness's token: refusing to run (its overrides would bypass the host's gates; unset it)"
  test_mode=1
  LOAD_RETRY=${LOCAL_LIVE_TEST_LOAD_RETRY:-$LOAD_RETRY}
fi
usage() { die "usage: $0 [-pr N] [-shard i/n] [-run REGEX] [-count N] [-p release|normal] [-reuse CLUSTER] [-badge B] [-o OWNER] [-n] [-no-lock] SUITE [MINOR]"; }

pr='' shard='' badge=${AGENT_BADGE:-} dry='' nolock='' reuse='' run_re='' count='' priority=normal
while [ $# -gt 0 ]; do
  case $1 in
    -pr | --pr) [ $# -ge 2 ] || usage; pr=$2; shift 2 ;;
    -shard | --shard) [ $# -ge 2 ] || usage; shard=$2; shift 2 ;;
    -run | --run) [ $# -ge 2 ] || usage; run_re=$2; shift 2 ;;
    -p) [ $# -ge 2 ] || usage; priority=$2; shift 2 ;;
    -count | --count) [ $# -ge 2 ] || usage; count=$2; shift 2 ;;
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
[ -z "$count" ] || { [[ $count =~ ^[1-9][0-9]?$|^100$ ]] || die "-count $count: want 1 to 100"; }
# A release run's tickets (class 0) go before the others' (1).
case $priority in release) slot_class=0 ;; normal) slot_class=1 ;; *) die "-p $priority: want release or normal" ;; esac
# A targeted run says so wherever its result is shown.
targeted=''
[ -z "$run_re" ] || targeted=" (targeted: -run $run_re)"

# The suite and its minors come from matrix.txt, the pins from
# tool-versions.env: the same files CI's matrix and tools come from.
minors=$(awk -v s="$SUITE" '!/^#/ && $1 == s { print $2 }' "$REPO_ROOT/hack/e2e/matrix.txt" | uniq)
[ -n "$minors" ] || die "$SUITE is not a suite of hack/e2e/matrix.txt"
LOCK=${LOADLOCK:-}
heavy=''
if awk -v s="$SUITE" '!/^#/ && $1 == s && $4 == "heavy" { found = 1 } END { exit !found }' "$REPO_ROOT/hack/e2e/matrix.txt"; then
  heavy=1
fi
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
COUNT=${count:-${COUNT:-1}}
[[ $COUNT =~ ^[1-9][0-9]?$|^100$ ]] || die "COUNT=$COUNT: want 1 to 100"
RETRY=${RETRY:-10}
SLOTS_DIR=${RELEASE_GATE_SLOTS:-}
# The host's cluster slots: $SLOT_MAX, else SLOT_MAX=N in the host's
# host.env next to the slots directory (DIR/../host.env, where the host's
# capacity is tuned, not in this script), else 2; 1 to 4 (MAX_SLOTS), or
# the run refuses (#2244 QA: any agent can write host.env, and a value out of
# range never means room). A host.env that cannot be read leaves 2, the
# fewest. hack/release-gate's -slot-max reads the same.
MAX_SLOTS=4
slot_max_from="\$SLOT_MAX"
if [ -z "${SLOT_MAX:-}" ]; then
  SLOT_MAX='' slot_max_from='the default'
  host_env=${SLOTS_DIR%/}
  if [[ $host_env == */* ]]; then host_env=${host_env%/*}/host.env; else host_env=host.env; fi
  if [ -n "$SLOTS_DIR" ] && [ -e "$host_env" ]; then
    if {
      while IFS= read -r line || [ -n "$line" ]; do
        line=${line%$'\r'}
        [[ $line != SLOT_MAX=* ]] || SLOT_MAX=${line#SLOT_MAX=} slot_max_from=$host_env
      done <"$host_env"
    } 2>/dev/null; then :; else
      SLOT_MAX='' slot_max_from='the default'
      log "warning: cannot read $host_env; the host has 2 cluster slots"
    fi
  fi
fi
SLOT_MAX=${SLOT_MAX:-2}
[[ $SLOT_MAX =~ ^[1-9]$ ]] && [ "$SLOT_MAX" -le "$MAX_SLOTS" ] ||
  die "SLOT_MAX=$SLOT_MAX ($slot_max_from): the host has 1 to $MAX_SLOTS cluster slots"
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
run      ${run_re:-the whole suite}${run_re:+ (targeted: not a proof of the whole suite)}
lock     $lock_d
slots    $slots_d
comment  $pr_d
EOF
  exit 0
fi

# A heavy suite takes the lock first, by running this script again under
# hack/loadlock.sh.
if [ -z "$nolock" ] && [ -n "$heavy" ] && [ -z "${_LOCAL_LIVE_LOCKED:-}" ]; then
  # A heavy suite holds the lock alone: loadlock.sh -x, where it counts
  # holders (#2242); a single-holder loadlock.sh is alone anyway.
  alone=()
  ! grep -q 'exclusive=1' "$LOADLOCK_SH" || alone=(-x)
  exec bash "$LOADLOCK_SH" -l "$LOCK" -o "$OWNER local-live $SUITE $MINOR" -p "$priority" "${alone[@]}" -r "$RETRY" -- \
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
# Children get a scratch HOME (#2233: a kubectl fell back to the host's
# ~/.kube/config): the caches and logins they need are passed by path.
real_home=$HOME
export GOPATH=${GOPATH:-$(go env GOPATH 2>/dev/null || echo "$real_home/go")}
export GOMODCACHE=${GOMODCACHE:-$(go env GOMODCACHE 2>/dev/null || echo "$GOPATH/pkg/mod")}
export GOCACHE=${GOCACHE:-$(go env GOCACHE 2>/dev/null || echo "$real_home/.cache/go-build")}
export DOCKER_CONFIG=${DOCKER_CONFIG:-$real_home/.docker}
export GH_CONFIG_DIR=${GH_CONFIG_DIR:-$real_home/.config/gh}
export NPM_CONFIG_CACHE=${NPM_CONFIG_CACHE:-$real_home/.npm}
if [ -d "$real_home/.cache/ms-playwright" ]; then
  export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-$real_home/.cache/ms-playwright}
fi
export HOME=$RUN_DIR/home
mkdir -p "$HOME"
[ "$KUBECONFIG" = "$E2E_OUT/kubeconfig" ] && [ ! -e "$HOME/.kube/config" ] ||
  die "refusing to run: KUBECONFIG is $KUBECONFIG, not the run's own $E2E_OUT/kubeconfig"
# Every decision on the host's shared state below (slots, the drain marker,
# the load gate) uses shell builtins only (read, case, [[ ]], parameter
# expansion; no $(...) of cat, grep, cut or awk), as hack/loadlock.sh does
# (#2242 QA): on a busy host a fork can fail, and a failed $(cat ...) must
# never make a live run look gone or the host look free. A file that is
# there but cannot be read is the safe answer: its run is alive, the host is
# draining, busy or full.
# LOCAL_LIVE_TEST_PROC (tests only, in the test mode): pids the test
# makes (directories <pid>, with a stat or without); a pid there is judged
# from it, every other from /proc, as hack/loadlock.sh's LOADLOCK_TEST_PROC.
test_proc=''
[ -z "$test_mode" ] || test_proc=${LOCAL_LIVE_TEST_PROC:-}
# pid_dir PID sets pid_dir_v to the directory PID is judged from.
pid_dir() {
  pid_dir_v=/proc/$1
  [ -z "$test_proc" ] || [ ! -e "$test_proc/$1" ] || pid_dir_v=$test_proc/$1
}
this_boot=
{ read -r this_boot </proc/sys/kernel/random/boot_id; } 2>/dev/null || this_boot=
# read_file FILE sets file_v to FILE's text, trailing newlines dropped; it
# fails when FILE cannot be opened (a missing file, or a read that failed).
read_file() {
  local opened=''
  file_v=''
  { { opened=1; IFS= read -r -d '' file_v; } <"$1"; } 2>/dev/null
  [ -n "$opened" ] || return 1
  while [[ $file_v == *$'\n' ]]; do file_v=${file_v%$'\n'}; done
  return 0
}
# read_start PID sets start_v to PID's start time (/proc/<pid>/stat field 22,
# counted after the command's last ')'), or fails when PID does not run.
read_start() {
  local stat rest f
  start_v=''
  pid_dir "$1"
  { IFS= read -r stat <"$pid_dir_v/stat"; } 2>/dev/null || return 1
  rest=${stat##*)}
  read -ra f <<<"$rest"
  [ -n "${f[19]:-}" ] || return 1
  start_v=${f[19]}
}
# now_ms sets now_ms_v to the time in milliseconds (bash 5's EPOCHREALTIME;
# date on an older bash), or to nothing when it cannot be read.
# shellcheck disable=SC2329 # called by room, through with_flock
now_ms() {
  local t=${EPOCHREALTIME:-}
  now_ms_v=''
  if [[ $t =~ ^([0-9]+)[.,]([0-9]{3}) ]]; then
    now_ms_v=${BASH_REMATCH[1]}${BASH_REMATCH[2]}
  else
    t=$(date +%s%3N 2>/dev/null) && [[ $t =~ ^[0-9]+$ ]] && now_ms_v=$t
  fi
  return 0
}
# record_gone RECORD succeeds when RECORD ("... pid N ..." with optional
# boot= and start= keys, as hack/loadlock.sh writes them) names a process
# that is gone. A record without a pid is never gone.
# shellcheck disable=SC2329 # called through with_flock, or the EXIT trap
record_gone() {
  local r=$1 pid
  [[ $r =~ (^|[[:space:]])pid\ ([0-9]+)([[:space:]]|$) ]] || return 1
  pid=${BASH_REMATCH[2]}
  if [[ $r =~ (^|[[:space:]])boot=([^[:space:]]+) ]] && [ -n "$this_boot" ] && [ "${BASH_REMATCH[2]}" != "$this_boot" ]; then
    return 0
  fi
  if ! read_start "$pid"; then
    # Its stat could not be opened: gone, unless /proc/<pid> is there (a
    # full file table fails the open as a failed fork does).
    pid_dir "$pid"
    [ ! -d "$pid_dir_v" ] || return 1
    return 0
  fi
  if [[ $r =~ (^|[[:space:]])start=([0-9]+)([[:space:]]|$) ]]; then
    [ "${BASH_REMATCH[2]}" != "$start_v" ]
    return
  fi
  return 1
}

read_start $$ && self_start=$start_v || self_start=unknown
slot_tag="$OWNER local-live $SUITE $MINOR pid $$ $stamp boot=$this_boot start=$self_start"
kept_tag="KEEP $KIND_CLUSTER, kept by $OWNER local-live $SUITE $MINOR at $stamp"
drain_tag="$OWNER local-live $SUITE $MINOR draining the host for a heavy suite, pid $$ $stamp boot=$this_boot start=$self_start"
taken=()
draining=''
# The cluster slots' queue (#2242's protocol, as hack/loadlock.sh's): a
# waiter's ticket SLOTS_DIR/.queue/<class>-<ns>-<pid>, release gates'
# (class 0, hack/release-gate -priority release) before everyone else's
# (1), then the oldest; only the first takes slots, so a waiter that polls
# often never overtakes one that polls seldom. A heavy run draining the
# host is the head while its drain lasts: the waiters ahead of it wait for
# the drain, and it waits only for the clusters running.
slot_queue=$SLOTS_DIR/.queue
slot_ticket=''

# with_flock NAME CMD... runs CMD under flock on SLOTS_DIR/NAME; never
# without it: when flock fails (a fork that failed), with_flock fails.
with_flock() {
  local name=$1 fd rc
  shift
  exec {fd}>"$SLOTS_DIR/$name" || return 1
  if ! flock -x "$fd"; then
    exec {fd}>&-
    why="cannot take the $name flock"
    return 1
  fi
  "$@"
  rc=$?
  flock -u "$fd"
  exec {fd}>&-
  return "$rc"
}

# shellcheck disable=SC2329 # called through with_flock, or the EXIT trap
release_slots() {
  local d
  for d in "${taken[@]}"; do
    if read_file "$d/owner" && [ "$file_v" = "$slot_tag" ]; then
      if [ -n "${KEEP:-}" ] && [ -n "$cluster_ours" ]; then
        # A kept cluster's slot has no pid: it is not reclaimed while the
        # cluster is there.
        echo "$kept_tag" >"$d/owner"
        log "KEEP: cluster slot $d stays held for $KIND_CLUSTER"
      else
        rm -r "$d"
      fi
    else
      log "cluster slot $d is not ours any more; left as it is"
    fi
  done
  taken=()
  [ -z "$slot_ticket" ] || rm -f "$slot_queue/$slot_ticket"
  slot_ticket=''
  if [ -n "$draining" ] && read_file "$SLOTS_DIR/.drain" && [ "$file_v" = "$drain_tag" ]; then
    rm -f "$SLOTS_DIR/.drain"
  fi
  draining=''
}

# reclaim_slots removes, under the .reclaim flock, the slots whose owner is
# gone, and a KEEP slot whose cluster is gone, and a .drain marker whose heavy
# run is gone. A slot owner without a pid (a person's, or an older tool's
# with none) is never reclaimed.
# shellcheck disable=SC2329 # called through with_flock, or the EXIT trap
reclaim_slots() {
  local d owner cluster docker_ok=1
  docker info >/dev/null 2>&1 || docker_ok=''
  for d in "$SLOTS_DIR"/[0-9]*; do
    [ -d "$d" ] || continue
    # An owner that is there but cannot be read is a live run's.
    read_file "$d/owner" || continue
    owner=$file_v
    [[ $owner == KEEP\ * ]] || record_gone "$owner" || continue
    # A kept cluster's slot, or a dead run's (a SIGKILLed KEEP run's cluster
    # may still run): reclaimed only when the cluster is definitely gone.
    cluster=''
    if [ -e "$d/cluster" ]; then
      if ! read_file "$d/cluster" || [ -z "$file_v" ]; then
        log "warning: cannot read cluster slot $d's cluster; the slot stays"
        continue
      fi
      cluster=$file_v
    fi
    if [ -z "$cluster" ]; then
      [[ $owner == KEEP\ * ]] || { rm -r "$d" && log "reclaimed cluster slot $d of a run that is gone: $owner"; }
      continue
    fi
    if [ -z "$docker_ok" ]; then
      log "warning: docker does not answer; kept cluster slot $d ($cluster) as it is"
      continue
    fi
    cluster_state "$cluster"
    case $state_v in
      gone) rm -r "$d" && log "reclaimed cluster slot $d: its cluster $cluster is gone ($owner)" ;;
      running) [[ $owner == KEEP\ * ]] || log "warning: cluster slot $d's run is gone but its cluster $cluster still runs; the slot stays: delete the cluster to free it" ;;
      *) log "warning: cannot tell whether cluster $cluster of slot $d is there ($state_v); the slot stays" ;;
    esac
  done
  read_file "$SLOTS_DIR/.drain" || return 0
  owner=$file_v
  if record_gone "$owner"; then
    rm -f "$SLOTS_DIR/.drain" && log "removed the drain marker of a heavy run that is gone: $owner"
  fi
}

# cluster_state sets state_v to running when CLUSTER's node is there, gone
# only on a definite "no such object" or "not found" from docker (or
# podman), and to the error otherwise (docker inspect is the one fork: when
# it fails to run, the answer is its error, never gone).
# shellcheck disable=SC2329 # called by reclaim_slots, through with_flock
cluster_state() {
  local out lower
  if out=$(docker inspect "$1-control-plane" 2>&1 >/dev/null); then
    state_v=running
    return
  fi
  lower=${out,,}
  if [[ $lower == *"no such object"* || $lower == *"not found"* || $lower == *"no such container"* ]]; then
    state_v=gone
  else
    state_v=${out%%$'\n'*}
    state_v=${state_v:-docker inspect failed}
  fi
}

# in_use sets in_use_v to how many slots other runs hold.
# shellcheck disable=SC2329 # called through with_flock, or the EXIT trap
in_use() {
  local d n=0
  for d in "$SLOTS_DIR"/[0-9]*; do
    [ -d "$d" ] && n=$((n + 1))
  done
  in_use_v=$((n - ${#taken[@]}))
}

# write_slot_ticket (re)writes this run's ticket in the slots' queue (the
# queue directory too); a failure ends the run.
write_slot_ticket() {
  local _
  for _ in 1 2 3; do
    mkdir -p "$slot_queue" && printf '%s\n' "$slot_tag" >"$slot_queue/.$slot_ticket" 2>/dev/null &&
      mv "$slot_queue/.$slot_ticket" "$slot_queue/$slot_ticket" 2>/dev/null && return 0
  done
  die "cannot write the cluster slot ticket $slot_queue/$slot_ticket"
}

# reap_slot_tickets removes the tickets of waiters that are gone (a ticket's
# text without a pid is judged by the pid in its name).
# shellcheck disable=SC2329 # called by try_slots, through with_flock
reap_slot_tickets() {
  local t name text
  for t in "$slot_queue"/[0-9]-*; do
    [ -e "$t" ] || continue
    name=${t##*/}
    read_file "$t" || continue
    text=$file_v
    [[ $text =~ (^|[[:space:]])pid\ [0-9]+ ]] || text="$text pid ${name##*-}"
    if record_gone "$text"; then
      rm -f "$t" && log "removed the cluster slot ticket $name of a waiter that is gone"
    fi
  done
}

# first_slot_ticket sets first_v to the ticket that sorts first (bytewise).
# shellcheck disable=SC2329 # called by try_slots, through with_flock
first_slot_ticket() {
  local t name LC_ALL=C
  first_v=''
  for t in "$slot_queue"/[0-9]-*; do
    [ -e "$t" ] || continue
    name=${t##*/}
    if [ -z "$first_v" ] || [[ $name < $first_v ]]; then first_v=$name; fi
  done
}

# try_slots, under the .reclaim flock and in this shell (it sets taken):
# reclaims, then takes this run's slots when it may: a light run not while a
# heavy run drains the host; a heavy run only with at most HEAVY_OTHER_SLOTS
# other slots in use. It sets why when it did not.
# shellcheck disable=SC2329 # called through with_flock, or the EXIT trap
try_slots() {
  local i d
  why=''
  reclaim_slots
  reap_slot_tickets
  if [ ! -e "$slot_queue/$slot_ticket" ]; then
    write_slot_ticket
    log "cluster slot ticket $slot_ticket was gone; queued it again in its place"
  fi
  # A marker that is there but cannot be read is a drain.
  if [ -z "$draining" ] && [ -e "$SLOTS_DIR/.drain" ]; then
    read_file "$SLOTS_DIR/.drain" || file_v='(a marker that cannot be read)'
    why="a heavy suite is draining the host: $file_v"
    return 1
  fi
  # First come, first served; the drainer is the head while it drains.
  if [ -z "$draining" ]; then
    first_slot_ticket
    if [ "$first_v" != "$slot_ticket" ]; then
      why="cluster slot ticket $slot_ticket waits behind ${first_v:-?}"
      return 1
    fi
  fi
  in_use
  if [ -n "$heavy" ] && ((in_use_v > HEAVY_OTHER_SLOTS)); then
    why="$in_use_v other cluster slots are in use; a heavy suite starts with at most $HEAVY_OTHER_SLOTS"
    return 1
  fi
  # Every slot directory counts, one above SLOT_MAX too (a run with a larger
  # SLOT_MAX took it): the host runs SLOT_MAX clusters at most.
  if ((in_use_v + need_slots > SLOT_MAX)); then
    why="$in_use_v cluster slots are in use in $SLOTS_DIR, and the host has $SLOT_MAX"
    return 1
  fi
  for ((i = 1; i <= SLOT_MAX && ${#taken[@]} < need_slots; i++)); do
    d=$SLOTS_DIR/$i
    mkdir "$d" 2>/dev/null || continue
    echo "$slot_tag" >"$d/owner"
    echo "$KIND_CLUSTER" >"$d/cluster"
    taken+=("$d")
  done
  if [ "${#taken[@]}" -ge "$need_slots" ]; then
    rm -f "$slot_queue/$slot_ticket"
    slot_ticket=''
    return 0
  fi
  for d in "${taken[@]}"; do rm -r "$d"; done
  taken=()
  why="fewer than $need_slots of the $SLOT_MAX cluster slots in $SLOTS_DIR are free"
  return 1
}

take_slots() {
  local i d said=
  # A kept cluster's slots name it: a -reuse run takes them over.
  for ((i = 1; i <= SLOT_MAX; i++)); do
    d=$SLOTS_DIR/$i
    if read_file "$d/cluster" && [ "$file_v" = "$KIND_CLUSTER" ] && [ "${#taken[@]}" -lt "$need_slots" ]; then
      echo "$slot_tag" >"$d/owner"
      taken+=("$d")
    fi
  done
  [ "${#taken[@]}" -lt "$need_slots" ] || return 0
  # In the queue: a ticket named by its class, time (ns) and pid.
  local ns
  if [[ ${EPOCHREALTIME:-} =~ ^([0-9]+)[.,]([0-9]{6})$ ]]; then
    ns=${BASH_REMATCH[1]}${BASH_REMATCH[2]}000
  else
    ns=$(date +%s%N) && [[ $ns =~ ^[0-9]+$ ]] || die "cannot read the time for a cluster slot ticket"
  fi
  printf -v slot_ticket '%s-%019d-%d' "$slot_class" "$((10#$ns))" "$$"
  write_slot_ticket
  if [ -n "$heavy" ]; then
    # Drain: no new run takes a slot from now on, for DRAIN_DEADLINE at most.
    echo "$drain_tag" >"$SLOTS_DIR/.drain"
    draining=1
    drain_since=${EPOCHSECONDS:-}
    [ -n "$drain_since" ] || printf -v drain_since '%(%s)T' -1
  fi
  while ! with_flock .reclaim try_slots; do
    drain_expired
    [ "$why" = "$said" ] || log "waiting for a cluster slot: $why; checking every ${RETRY}s"
    said=$why
    sleep "$RETRY" &
    napping=$!
    wait "$napping" || true
    napping=''
  done
}

why=''
drain_since=''

# drain_expired ends a heavy run that has drained the host for DRAIN_DEADLINE
# without starting: it says what blocked it, gives the marker and (exiting)
# the heavy-load lock back, and fails.
drain_expired() {
  local d owner cluster now=${EPOCHSECONDS:-}
  [ -n "$drain_since" ] || return 0
  [ -n "$now" ] || printf -v now '%(%s)T' -1
  [ $((now - drain_since)) -ge "$DRAIN_DEADLINE" ] || return 0
  log "the heavy suite $SUITE drained the host for ${DRAIN_DEADLINE}s without starting: $why"
  for d in "$SLOTS_DIR"/[0-9]*; do
    [ -d "$d" ] || continue
    [[ " ${taken[*]} " != *" $d "* ]] || continue
    owner=$(cat "$d/owner" 2>/dev/null || echo '?')
    cluster=$(cat "$d/cluster" 2>/dev/null || true)
    if [[ $owner == KEEP\ * ]]; then
      log "  kept cluster $cluster holds slot ${d##*/}; delete it or run with -reuse $cluster"
    else
      log "  slot ${d##*/} is held by $owner"
    fi
  done
  log "  the one-minute load is $(cut -d' ' -f1 /proc/loadavg)"
  echo "local-live: the heavy suite $SUITE gave up draining the host after ${DRAIN_DEADLINE}s; see the log above" >&2
  exit 1
}

# room says why another cluster may not start now, or nothing when it may:
# the one-minute load is at MAX_LOAD (HEAVY_MAX_LOAD for a heavy suite) or
# more, fs.inotify.max_user_instances is under 128 per kind node running and
# the one to come (kind nodes hang at boot without them), or the last cluster
# start was less than START_SPACING seconds ago (its load does not show yet).
# The test mode (test_mode) lets tests set the load, the limit and the spacing
# (LOCAL_LIVE_TEST_LOAD, LOCAL_LIVE_TEST_INOTIFY, LOCAL_LIVE_TEST_SPACING).
# shellcheck disable=SC2329 # called by start_turn, through with_flock
# It sets why; a load, an inotify limit, a node count or a last start it
# cannot read is the safe answer (no room).
room() {
  local load='' max='' names nodes=0 n limit=$MAX_LOAD spacing=$START_SPACING last=''
  why=''
  { read -r load _ </proc/loadavg; } 2>/dev/null
  { read -r max </proc/sys/fs/inotify/max_user_instances; } 2>/dev/null
  if [ -n "$test_mode" ]; then
    load=${LOCAL_LIVE_TEST_LOAD:-$load}
    max=${LOCAL_LIVE_TEST_INOTIFY:-$max}
    spacing=${LOCAL_LIVE_TEST_SPACING:-$spacing}
  fi
  [ -z "$heavy" ] || limit=$HEAVY_MAX_LOAD
  if ! [[ $load =~ ^([0-9]+)(\.[0-9]*)?$ ]] || [ "$((10#${BASH_REMATCH[1]}))" -ge "$limit" ]; then
    why="the one-minute load is ${load:-unreadable}, not under $limit"
    return
  fi
  # docker ps is the one fork: when it fails, the nodes cannot be counted.
  if ! names=$(docker ps --format '{{.Names}}' 2>/dev/null); then
    why="docker ps failed: the kind nodes running cannot be counted"
    return
  fi
  for n in $names; do [[ $n != *-control-plane ]] || nodes=$((nodes + 1)); done
  if ! [[ $max =~ ^[0-9]+$ ]] || [ "$max" -lt $((128 * (nodes + need_slots))) ]; then
    why="fs.inotify.max_user_instances is ${max:-unreadable}, under 128 for each of $nodes kind nodes and $need_slots more (sudo sysctl -w fs.inotify.max_user_instances=8192)"
    return
  fi
  now_ms
  if [ -e "$SLOTS_DIR/.last-start" ]; then
    read_file "$SLOTS_DIR/.last-start" && last=$file_v # milliseconds
    [[ $last =~ ^[0-9]+$ ]] || last=$now_ms_v          # unreadable: as if just now
  fi
  if ! [[ $now_ms_v =~ ^[0-9]+$ ]]; then
    why="the time cannot be read"
  elif [ $((now_ms_v - ${last:-0})) -lt $((spacing * 1000)) ]; then
    why="a cluster started $(((now_ms_v - last) / 1000))s ago; starts are ${spacing}s apart"
  fi
}

# start_turn, under the .start flock: when there is room, records this start
# (SLOTS_DIR/.last-start) and succeeds; otherwise it sets why.
# shellcheck disable=SC2329 # called through with_flock
start_turn() {
  room
  [ -z "$why" ] || return 1
  printf '%s\n' "$now_ms_v" >"$SLOTS_DIR/.last-start"
}

# wait_for_room waits until this run may start its cluster: the check and the
# start are one step under a flock, so two runs never pass on one reading.
wait_for_room() {
  local said=
  while ! with_flock .start start_turn; do
    drain_expired
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

# LOCAL_LIVE_TEST_FAIL_PATH (tests, in the test mode) is put in front
# of PATH while the run takes its slots and waits for room: a directory of
# tools that fail, as forks do on a busy host.
fail_path=''
[ -z "$test_mode" ] || [ -z "${LOCAL_LIVE_TEST_FAIL_PATH:-}" ] || fail_path=$LOCAL_LIVE_TEST_FAIL_PATH:
if [ -z "$nolock" ]; then
  mkdir -p "$SLOTS_DIR"
  PATH=$fail_path$PATH take_slots
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
  [ -n "$nolock" ] || PATH=$fail_path$PATH wait_for_room
  # A deleted cluster's leftover state would point up.sh at its old secrets.
  forget_state
  echo "$MINOR" >"$E2E_OUT/minor"
fi
rm -f "$E2E_OUT/test.json" "$E2E_OUT/summary.json" "$E2E_OUT/summary.md"
[ "$SUITE" != core ] || command -v zsh >/dev/null || die "the core suite needs zsh on PATH (TestCLI_Completion)"

head_sha=$(git -C "$REPO_ROOT" rev-parse HEAD)
dirty=$(git -C "$REPO_ROOT" status --porcelain --untracked-files=no | head -1)
start=$(date +%s)
log "suite $SUITE${shard:+ shard $shard}$targeted on Kubernetes $MINOR, cluster $KIND_CLUSTER, build $KARDINAL_E2E_BUILD; log $LOG"

# The suite runs in a process group of its own (set -m), so a signal stops
# up.sh's kind and run.sh's go test with it.
cluster_ours=1
rc=0
set -m
(
  [ "$SUITE" = github ] || unset KARDINAL_E2E_GITHUB_TOKEN_FILE DEMO_GITHUB_TOKEN
  unset RUN
  [ -z "$run_re" ] || export RUN=$run_re
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
  echo "## local live: $SUITE${shard:+ shard $shard}$targeted on Kubernetes $MINOR: $result"
  echo
  if [ -n "$run_re" ]; then
    echo "**Targeted run**: only the tests matching \`-run $run_re\`; this is not a proof of the whole $SUITE suite."
    echo
  fi
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
  rerun=hack/e2e/local-live.sh
  [ -z "$shard" ] || rerun="$rerun -shard $shard"
  [ -z "$run_re" ] || rerun="$rerun -run $(printf '%q' "$run_re")"
  [ "$COUNT" = 1 ] || rerun="$rerun -count $COUNT"
  echo "Re-run: \`$rerun $SUITE $MINOR\`"
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
printf '\nlocal-live: %s %s%s (log %s, results %s)\n' "$result" "$SUITE" "$targeted" "$LOG" "$RUN_DIR"

if [ -n "$pr" ]; then
  if gh pr comment "$pr" --repo "${GH_REPO:-pnz1990/kardinal-promoter}" --body-file "$body" >>"$LOG" 2>&1; then
    log "posted the summary on pull request $pr"
  else
    log "posting the summary on pull request $pr failed (see $LOG); it is in $body"
  fi
fi
exit "$rc"
