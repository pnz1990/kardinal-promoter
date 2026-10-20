#!/usr/bin/env bash
# hack/loadlock.sh [-l LOCK] [-o OWNER] [-p release|normal] [-x] [-r RETRY] -- CMD [ARG...]
#
# Runs CMD holding the shared host's heavy-load lock. When CMD ends, however
# it ends (an error, a signal), its hold is given up only if its owner record
# is still this run's. Exits with CMD's status.
#
# The lock counts: up to 2 hold it while the host's one-minute load is under
# 32, one at or above it. Each holder has a slot directory LOCK.d/<n> with
# its owner record (mkdir is atomic); LOCK exists while anyone holds it, with
# LOCK/owner listing every holder, one per line, and LOCK/.counting marking
# it as that view. An older client's mkdir LOCK still waits for it, and a
# LOCK made by an older client (no marker) holds it alone.
#
# The lock is fair. Waiters queue in LOCK.queue, one ticket file each,
# <class>-<nanoseconds>-<pid>: release gates' tickets (-p release, class 0)
# before everyone else's (class 1), then the oldest. hack/release-gate
# (-priority) follows the same protocol (hack/release-gate/fairlock.go):
#   - Only the waiter whose ticket sorts first takes the lock. Every waiter
#     reclaims a slot (or an older client's LOCK) of a holder that is gone,
#     under flock(1) on LOCK.queue/.reclaim, and removes it outright: an
#     older client at the head cannot judge the counting lock's holders.
#   - Records (the owner file, tickets) are "<owner> pid <N> <time>" plus
#     boot=<boot id> start=<the process's start time, /proc/<pid>/stat field
#     22> (and ticket=<name> in the owner file). One is dead when its boot id
#     is not this boot's, its pid is gone, or the pid's start time differs
#     (a reused pid). A record without those keys (an older client's) is dead
#     when its pid is gone or started after the record's time. A lock without
#     a pid is never taken over, unless it has had no owner file for a minute.
#   - A waiter whose ticket is gone writes it again under its name.
#   - Each waiter touches its ticket on every poll (a heartbeat, beat=1); a
#     ticket untouched for 10 minutes (30 for an older client's, which
#     touches none) is a stopped, hung or stray waiter's, and any waiter
#     removes it whatever its pid. A ticket that cannot be read is judged by
#     its name's pid and time.
#   - A normal ticket that has waited 2 hours sorts as a release one, and
#     -p release needs RELEASE_GATE_PRIORITY_TOKEN matching the token file
#     next to LOCK (DIR/.release-token, mode 600): release tickets never
#     starve the others, and no one else jumps the queue. Where every agent
#     runs as one user the token says intent, not authority: the record's
#     priority= makes a misuse visible.
#   - A waiter wakes on the queue's changes (inotifywait, when installed) or
#     every RETRY seconds (10).
# Clients that take the lock without a ticket (code from before the queue)
# are not ordered by it; a waiter says so when one holds the lock.
#
# LOCK defaults to $LOADLOCK; one of the two is required.
# OWNER (for example "ENGINEER-4 scale full run") is written with this
# run's pid and start time, as hack/release-gate's -owner is.
set -uo pipefail

usage() {
  echo "usage: $0 [-l LOCK] [-o OWNER] [-p release|normal] [-x] [-r RETRY] -- CMD [ARG...]" >&2
  exit 2
}
lock=${LOADLOCK:-}
owner=${USER:-unknown}
retry=10
class=1
alone=''
while [ $# -gt 0 ]; do
  case $1 in
    -l) lock=$2; shift 2 ;;
    -o) owner=$2; shift 2 ;;
    -r) retry=$2; shift 2 ;;
    -x) alone=1; shift ;;
    -p)
      case $2 in release) class=0 ;; normal) class=1 ;; *) usage ;; esac
      shift 2 ;;
    --) shift; break ;;
    -*) usage ;;
    *) break ;;
  esac
done
[ $# -gt 0 ] && [ -n "$lock" ] || usage
# A release ticket (class 0) is a release run's: -p release needs
# RELEASE_GATE_PRIORITY_TOKEN to match the token file the coordinator keeps
# next to the lock (DIR/.release-token, this user's, mode 600), or the run
# refuses (#2282 QA: no one else jumps the queue by saying so).
prio=normal
if [ "$class" = 0 ]; then
  prio=release
  case $lock in */*) token_file=${lock%/*}/.release-token ;; *) token_file=.release-token ;; esac
  want=''
  { read -r want <"$token_file"; } 2>/dev/null || want=''
  mode=$(stat -c %a "$token_file" 2>/dev/null) || mode=''
  if [ -z "$want" ] || [ "$mode" != 600 ] || [ ! -O "$token_file" ] || [ "${RELEASE_GATE_PRIORITY_TOKEN:-}" != "$want" ]; then
    echo "loadlock: -p release needs RELEASE_GATE_PRIORITY_TOKEN matching $token_file (mode 600): refusing to jump the queue" >&2
    exit 2
  fi
fi

proc=/proc
# LOADLOCK_TEST_PROC (tests only, with LOADLOCK_TEST=1): pids the test makes
# (directories <pid>, with a stat or without); a pid there is judged from it,
# every other from /proc.
test_proc=
[ "${LOADLOCK_TEST:-}" != 1 ] || test_proc=${LOADLOCK_TEST_PROC:-}
# pid_dir PID sets pid_dir_v to the directory PID is judged from.
pid_dir() {
  pid_dir_v=$proc/$1
  [ -z "$test_proc" ] || [ ! -e "$test_proc/$1" ] || pid_dir_v=$test_proc/$1
}
# Every decision on the lock's state below uses shell builtins only (read,
# case, [[ ]], parameter expansion; no $(...), cat, grep, cut, awk, sed or
# find): on a busy host a fork can fail, and a failed $(cat ...) once made a
# live holder look gone (#2242 stress at load 300, #2242 QA). Where a file is
# there but cannot be read, the answer is the safe one: its holder is alive,
# exclusive, the host busy. The commands that change the lock (mkdir, rm,
# flock) fail toward not taking it.
this_boot=
{ read -r this_boot <"$proc/sys/kernel/random/boot_id"; } 2>/dev/null || this_boot=
# read_file FILE sets file_v to FILE's text, trailing newlines dropped; it
# fails when FILE cannot be opened (a missing file, or a read that failed).
read_file() {
  local opened=
  file_v=
  { { opened=1; IFS= read -r -d '' file_v; } <"$1"; } 2>/dev/null
  [ -n "$opened" ] || return 1
  while [[ $file_v == *$'\n' ]]; do file_v=${file_v%$'\n'}; done
  return 0
}
# now_v is the time, in seconds since the epoch (a builtin printf).
now() { printf -v now_v '%(%s)T' -1; }
# read_start PID sets start_v to PID's start time (stat field 22, counted
# after the command's last ')'), or fails when PID does not run.
read_start() {
  local stat rest f
  start_v=
  pid_dir "$1"
  { IFS= read -r stat <"$pid_dir_v/stat"; } 2>/dev/null || return 1
  rest=${stat##*)}
  read -ra f <<<"$rest"
  [ -n "${f[19]:-}" ] || return 1
  start_v=${f[19]}
}
# record_dead RECORD [EPOCH] succeeds when RECORD names a process that is
# gone; EPOCH is the record's time when the text has none (a ticket's).
record_dead() {
  local r=$1 at=${2:-} pid start btime hz
  [[ $r =~ (^|[[:space:]])pid\ ([0-9]+)([[:space:]]|$) ]] || return 1
  pid=${BASH_REMATCH[2]}
  if [[ $r =~ (^|[[:space:]])boot=([^[:space:]]+) ]] && [ -n "$this_boot" ] && [ "${BASH_REMATCH[2]}" != "$this_boot" ]; then
    return 0
  fi
  if ! read_start "$pid"; then
    # Its stat could not be opened: gone, unless $proc/<pid> is there (a
    # full file table fails the open as a failed fork does).
    pid_dir "$pid"
    [ ! -d "$pid_dir_v" ] || return 1
    return 0
  fi
  start=$start_v
  # A holder that could not read its own start time: boot id and pid only.
  [[ $r =~ (^|[[:space:]])start=unknown([[:space:]]|$) ]] && return 1
  if [[ $r =~ (^|[[:space:]])start=([0-9]+) ]]; then
    [ "${BASH_REMATCH[2]}" != "$start" ]
    return
  fi
  if [[ $r =~ pid\ [0-9]+\ ([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]+Z) ]]; then
    at=$(date -u -d "${BASH_REMATCH[1]}" +%s 2>/dev/null) || at=
  fi
  [[ $at =~ ^[0-9]+$ ]] || return 1
  btime=
  while read -r key val; do
    [ "$key" != btime ] || { btime=$val; break; }
  done <"$proc/stat" 2>/dev/null
  hz=100 # USER_HZ, stat's start time unit on Linux (hack/release-gate's clockTicks)
  [[ $btime =~ ^[0-9]+$ ]] || return 1
  [ $((btime + start / hz)) -gt $((at + 1)) ]
}

# Never start=0: a holder whose start time is unknown says so, and the others
# judge it by its boot id and pid only.
read_start $$ && self_start=$start_v || self_start=unknown
# priority= says the ticket's class; beat=1 that its waiter touches it on
# every poll (a heartbeat: one untouched for STALE_BEAT seconds is a stopped or
# hung waiter's, and any waiter removes it; its waiter queues it again in its
# place when it resumes).
tag="$owner pid $$ $(date -u +%Y-%m-%dT%H:%M:%SZ) boot=$this_boot start=$self_start priority=$prio beat=1"
# -x: hold the lock alone (a heavy live suite): this run takes a slot only when
# none is held, and while its record (exclusive=1) holds one no one takes the
# other.
[ -z "$alone" ] || tag="$tag exclusive=1"
queue="$lock.queue"
mkdir -p "$queue"
# The ticket's time is EPOCHREALTIME's (a builtin: a failed date never
# writes a 0 that jumps the queue, #2242 QA), date's on an older bash
# (LOADLOCK_TEST_NO_EPOCHREALTIME, tests only, with LOADLOCK_TEST=1: as if).
epoch=${EPOCHREALTIME:-}
[ "${LOADLOCK_TEST:-}" != 1 ] || [ -z "${LOADLOCK_TEST_NO_EPOCHREALTIME:-}" ] || epoch=''
if [[ $epoch =~ ^([0-9]+)[.,]([0-9]{6})$ ]]; then
  ticket_ns=${BASH_REMATCH[1]}${BASH_REMATCH[2]}000
else
  ticket_ns=$(date +%s%N) && [[ $ticket_ns =~ ^[1-9][0-9]+$ ]] || {
    echo "loadlock: cannot read the time for a ticket" >&2
    exit 1
  }
fi
printf -v ticket '%s-%019d-%d' "$class" "$((10#$ticket_ns))" "$$"
# write_ticket (re)writes this run's ticket, the queue directory too when it
# was removed; a failure ends the run, as hack/release-gate's does.
write_ticket() {
  for _ in 1 2 3; do
    mkdir -p "$queue" && printf '%s\n' "$tag" >"$queue/.$ticket" 2>/dev/null &&
      mv "$queue/.$ticket" "$queue/$ticket" 2>/dev/null && return 0
  done
  echo "loadlock: cannot write ticket $queue/$ticket" >&2
  exit 1
}
write_ticket
record="$tag ticket=$ticket"

slots="$lock.d"
mkdir -p "$slots"
slot=

# with_flock CMD... runs CMD under flock on queue/.reclaim, which serializes
# every change to the lock, its slots and its view. CMD never runs without
# the flock: when flock fails (a fork that failed), with_flock fails.
# It waits for the flock FLOCK_WAIT seconds (60) at most: a waiter stopped
# (Ctrl-Z) while it held it would wedge every other one, so they say so and
# try again. LOADLOCK_TEST_FLOCK_WAIT sets it, in tests.
FLOCK_WAIT=60
if [ "${LOADLOCK_TEST:-}" = 1 ] && [[ ${LOADLOCK_TEST_FLOCK_WAIT:-} =~ ^[0-9]+$ ]]; then
  FLOCK_WAIT=$LOADLOCK_TEST_FLOCK_WAIT
fi
with_flock() {
  local fd rc
  exec {fd}>"$queue/.reclaim" || return 1
  if ! flock -x -w "$FLOCK_WAIT" "$fd"; then
    exec {fd}>&-
    echo "loadlock: no flock on $queue/.reclaim in ${FLOCK_WAIT}s: a waiter stopped (Ctrl-Z) or hung while it held it? Resume or end it" >&2
    return 1
  fi
  "$@"
  rc=$?
  flock -u "$fd"
  exec {fd}>&-
  return "$rc"
}

# count_holders sets held_n to how many slots are held, or to "exclusive"
# when LOCK is an older client's (no view marker); held_x is set when a
# holder holds it alone (exclusive=1), or its record cannot be read.
count_holders() {
  local d
  held_n=0 held_x=
  if [ -d "$lock" ] && [ ! -e "$lock/.counting" ]; then
    held_n=exclusive
    return
  fi
  for d in "$slots"/*; do
    [ -e "$d/owner" ] || continue
    held_n=$((held_n + 1))
    if ! read_file "$d/owner" || [[ $file_v == *exclusive=1* ]]; then
      held_x=1
    fi
  done
}

# write_view makes LOCK the view of the slots: with the marker and LOCK/owner
# listing every holder while any is held, gone when none is. Under the flock.
# It fails (3) when LOCK is an older client's: one made since holders_count
# looked is never taken over (a plain mkdir, #2242 QA). The holder lines have
# "pid N" written "pid:N": older clients judge LOCK/owner by its first
# "pid N", and would remove the view while another slot's holder runs.
write_view() {
  local d lines='' line
  count_holders
  [ "$held_n" != exclusive ] || return 3
  for d in "$slots"/*; do
    [ -e "$d/owner" ] || continue
    if read_file "$d/owner"; then
      line=$file_v
      while [[ $line =~ (^|[[:space:]])pid\ ([0-9]+) ]]; do
        line=${line/"${BASH_REMATCH[0]}"/"${BASH_REMATCH[1]}pid:${BASH_REMATCH[2]}"}
      done
    else
      line="a holder whose record cannot be read: ${d##*/}"
    fi
    lines="$lines$line"$'\n'
  done
  if [ -z "$lines" ]; then
    # Only the view goes, and only once its marker has: removing the marker
    # is the test (#2242 QA), so a LOCK an older client made since
    # holders_count looked (no marker) is never removed. LOCK stays there
    # until the rmdir, so no one makes another meanwhile.
    if rm "$lock/.counting" 2>/dev/null; then
      rm -f "$lock/owner" "$lock/.owner.tmp"
      rmdir "$lock" 2>/dev/null || echo "loadlock: cannot remove the view $lock; it is left with no owner, and goes in a minute" >&2
    fi
    return 0
  fi
  if mkdir "$lock" 2>/dev/null; then
    : >"$lock/.counting"
  elif [ ! -e "$lock/.counting" ]; then
    return 3
  fi
  printf '%s' "$lines" >"$lock/.owner.tmp" && mv "$lock/.owner.tmp" "$lock/owner"
}

held=
child=
release_slot() {
  if read_file "$slot/owner" && [ "$file_v" = "$record" ]; then
    rm -r "$slot"
    write_view || true
  else
    echo "loadlock: $slot is not ours any more; left as it is" >&2
  fi
}
release() {
  rm -f "$queue/$ticket"
  [ -n "$held" ] || return 0
  with_flock release_slot
}
trap release EXIT
# A signal stops CMD first, then releases the lock (EXIT).
stop() {
  [ -n "$child" ] && kill -TERM "$child" 2>/dev/null && wait "$child" 2>/dev/null
  exit "$1"
}
trap 'stop 130' INT
trap 'stop 143' TERM

# A ticket untouched for STALE_BEAT seconds (one with beat=1) or STALE_OLD
# (an older client's, which touches none) is a stopped, hung or stray
# waiter's: removed, whatever its pid (#2282 QA: a head that does not move
# wedged everyone). LOADLOCK_TEST_STALE sets both, in tests.
STALE_BEAT=600
STALE_OLD=1800
if [ "${LOADLOCK_TEST:-}" = 1 ] && [[ ${LOADLOCK_TEST_STALE:-} =~ ^[0-9]+$ ]]; then
  STALE_BEAT=$LOADLOCK_TEST_STALE STALE_OLD=$LOADLOCK_TEST_STALE
fi

# reap_tickets removes the tickets of waiters that are gone, and stale ones.
# A ticket that cannot be read is judged by its name's pid and time.
reap_tickets() {
  local t name text ns mtime limit now=${EPOCHSECONDS:-}
  for t in "$queue"/[0-9]-*; do
    [ -e "$t" ] || continue
    name=${t##*/}
    [ "$name" != "$ticket" ] || continue
    if read_file "$t"; then text=$file_v; else text='(a ticket that cannot be read)'; fi
    # stat is the one fork here: when it fails, the ticket is not stale.
    if [[ $now =~ ^[0-9]+$ ]] && mtime=$(stat -c %Y "$t" 2>/dev/null) && [[ $mtime =~ ^[0-9]+$ ]]; then
      limit=$STALE_OLD
      [[ $text != *beat=1* ]] || limit=$STALE_BEAT
      if ((now - mtime > limit)); then
        rm -f "$t" && echo "loadlock: removed the ticket $name, untouched for $((now - mtime))s (a waiter that is stopped, hung or gone; it queues again when it resumes)" >&2
        continue
      fi
    fi
    if [[ $text != *boot=* ]] && [[ " $warned " != *" $name "* ]]; then
      warned="$warned $name"
      echo "loadlock: warning: ticket $name is a waiter on code from before #2226 ($text); it can move the lock from under its holder: rebase it onto main" >&2
    fi
    [[ $text =~ (^|[[:space:]])pid\ [0-9]+ ]] || text="$text pid ${name##*-}"
    ns=${name#*-}
    ns=${ns%-*}
    if record_dead "$text" "$((10#$ns / 1000000000))"; then
      rm -f "$t" && echo "loadlock: removed the ticket $name of a waiter that is gone" >&2
    fi
  done
}

# The lock counts: up to 2 hold it at once while the host's one-minute load
# is under 32, one at or above it (LOADLOCK_TEST_LOAD overrides the load,
# for tests only: it is honoured only with LOADLOCK_TEST=1). Each holder has
# a slot LOCK.d/<n> with its owner record; LOCK
# exists while anyone holds it (an older client's mkdir LOCK still waits),
# LOCK/owner listing every holder and LOCK/.counting marking it as this
# view. hack/release-gate/fairlock.go counts the same way.
# It sets cap_v; a load it cannot read or parse is a busy host's (1).
# LOADLOCK_TEST_LOADAVG (tests, with LOADLOCK_TEST=1) is the file to read.
capacity() {
  local load='' loadavg=$proc/loadavg
  [ "${LOADLOCK_TEST:-}" != 1 ] || loadavg=${LOADLOCK_TEST_LOADAVG:-$loadavg}
  { read -r load _ <"$loadavg"; } 2>/dev/null
  [ "${LOADLOCK_TEST:-}" != 1 ] || load=${LOADLOCK_TEST_LOAD:-$load}
  cap_v=1
  if [[ $load =~ ^([0-9]+)(\.[0-9]*)?$ ]] && [ "$((10#${BASH_REMATCH[1]}))" -lt 32 ]; then
    cap_v=2
  fi
}

# dead_dir DIR succeeds when DIR's owner is gone, or DIR has had no owner
# file for a minute; it sets why_v. An owner file that is there but cannot
# be read is a live holder's.
dead_dir() {
  local mtime
  why_v=
  if [ -e "$1/owner" ]; then
    read_file "$1/owner" || return 1
    record_dead "$file_v" || return 1
    why_v=" of a holder that is gone: $file_v"
    return 0
  fi
  [ -d "$1" ] || return 1
  # stat is the one fork here: when it fails, the directory is not old.
  mtime=$(stat -c %Y "$1" 2>/dev/null) && [[ $mtime =~ ^[0-9]+$ ]] || return 1
  now
  [[ $now_v =~ ^[0-9]+$ ]] && [ $((now_v - mtime)) -ge 60 ] || return 1
  # Looked again: an owner written meanwhile is a holder's.
  [ ! -e "$1/owner" ] || return 1
  why_v=", which has had no owner for a minute"
}

# take_turn is the head waiter's turn, under the flock: it removes the slots
# (and an older client's exclusive lock) of holders that are gone, then takes
# a free slot when fewer than the capacity hold the lock. It succeeds when
# this run holds a slot.
# before_beat is a test's pause between the ticket's check and its heartbeat
# (LOADLOCK_TEST_BEFORE_BEAT=FILE, tests only, with LOADLOCK_TEST=1): the
# first time, it writes FILE.parked and waits, 10s at most, for FILE.go, so
# a test can remove the ticket there, as a stale reaper would.
before_beat() {
  local f=${LOADLOCK_TEST_BEFORE_BEAT:-} i
  [ "${LOADLOCK_TEST:-}" = 1 ] && [ -n "$f" ] && [ ! -e "$f.parked" ] || return 0
  : >"$f.parked"
  for ((i = 0; i < 100; i++)); do
    [ ! -e "$f.go" ] || return 0
    sleep 0.1
  done
}

# before_rm is a test's pause between judging an older client's lock dead
# and reading its owner again (LOADLOCK_TEST_BEFORE_RM=FILE, tests only,
# with LOADLOCK_TEST=1): it writes FILE.judged and waits, 10s at most, for
# FILE.go, so the test can put another LOCK there meanwhile.
# shellcheck disable=SC2329 # called by reclaim_turn, through with_flock
before_rm() {
  local f=${LOADLOCK_TEST_BEFORE_RM:-} i
  [ "${LOADLOCK_TEST:-}" = 1 ] && [ -n "$f" ] || return 0
  : >"$f.judged"
  for ((i = 0; i < 100; i++)); do
    [ ! -e "$f.go" ] || return 0
    sleep 0.1
  done
}

# reclaim_turn, under the flock, removes the slots (and an older client's
# exclusive lock) of holders that are gone, and the view when no slot is
# left. Any waiter does it, not only the head (#2242 QA: an older client at
# the head cannot judge the view's holders, so a new holder that died
# without its EXIT trap would wedge the lock for everyone); only the head
# takes a slot. An older client's lock is removed only while its owner is
# still the dead one judged (read again just before).
# shellcheck disable=SC2329 # called through with_flock
reclaim_turn() {
  local d judged reclaimed=
  count_holders
  if [ "$held_n" = exclusive ] && dead_dir "$lock"; then
    judged=$why_v
    before_rm
    if [ -e "$lock/owner" ]; then
      read_file "$lock/owner" && [ " of a holder that is gone: $file_v" = "$judged" ] || judged=''
    fi
    [ -z "$judged" ] || { rm -r "$lock" && echo "loadlock: removed $lock$judged" >&2; }
  fi
  for d in "$slots"/*; do
    [ -d "$d" ] || continue
    if dead_dir "$d"; then
      rm -r "$d" && reclaimed=1 && echo "loadlock: removed lock slot $d$why_v" >&2
    fi
  done
  # The view changes only with the slots (#2242 QA).
  [ -z "$reclaimed" ] || write_view || true
}

take_turn() {
  local d n
  # Still the head? A release ticket (or an aged one) that came since the
  # check outside the flock goes first (#2282 QA).
  first_ticket
  [ "$first" = "$ticket" ] || return 1
  reclaim_turn
  count_holders
  capacity
  [ "$held_n" != exclusive ] && [ "$held_n" -lt "$cap_v" ] || return 1
  [ -z "$alone" ] || [ "$held_n" -eq 0 ] || return 1
  [ -z "$held_x" ] || return 1
  for n in 1 2; do
    mkdir "$slots/$n" 2>/dev/null || continue
    if ! echo "$record" >"$slots/$n/owner"; then
      rmdir "$slots/$n"
      echo "loadlock: cannot write $slots/$n/owner; gave the lock up" >&2
      return 2
    fi
    if ! write_view; then
      rm -r "${slots:?}/$n"
      echo "loadlock: an older client took $lock meanwhile; gave slot $n back" >&2
      return 1
    fi
    # Held from here: a signal from now on gives the slot up (release).
    slot="$slots/$n"
    held=1
    return 0
  done
  return 1
}

# reclaim_due succeeds when a holder looks gone (judged again under the
# flock): only then does a waiter that is not the head take the flock, so
# a waiter stopped while it waits seldom holds it.
reclaim_due() {
  local d
  count_holders
  if [ "$held_n" = exclusive ]; then
    dead_dir "$lock"
    return
  fi
  for d in "$slots"/*; do
    [ -d "$d" ] || continue
    dead_dir "$d" && return 0
  done
  return 1
}

# may_take succeeds when the head may find a slot it can take (judged again
# under the flock): a holder looks gone, or fewer than the capacity hold it.
may_take() {
  reclaim_due && return 0
  count_holders
  capacity
  [ "$held_n" != exclusive ] && [ "$held_n" -lt "$cap_v" ] || return 1
  [ -z "$alone" ] || [ "$held_n" -eq 0 ] || return 1
  [ -z "$held_x" ]
}

# head_turn runs take_turn under the flock; a failed owner write ends the run.
head_turn() {
  local rc=0
  mkdir -p "$slots"
  with_flock take_turn || rc=$?
  [ "$rc" -ne 2 ] || exit 1
  return "$rc"
}

# first_ticket sets first to the ticket that sorts first (bytewise), a
# normal ticket that has waited AGE_PROMOTE seconds (2h) sorting as a release
# one (#2282 QA: release tickets never starve the others).
AGE_PROMOTE=7200
first_ticket() {
  local t name key first_key='' rest ns now_ns='' LC_ALL=C
  [[ ${EPOCHREALTIME:-} =~ ^([0-9]+)[.,]([0-9]{6})$ ]] && now_ns=${BASH_REMATCH[1]}${BASH_REMATCH[2]}000
  first=
  for t in "$queue"/[0-9]-*; do
    [ -e "$t" ] || continue
    name=${t##*/} key=${t##*/}
    rest=${name#*-} ns=${rest%%-*}
    if [ "${name%%-*}" = 1 ] && [ -n "$now_ns" ] && [[ $ns =~ ^[0-9]+$ ]] && ((10#$now_ns - 10#$ns > AGE_PROMOTE * 1000000000)); then
      key=0-$rest
    fi
    if [ -z "$first" ] || [[ $key < $first_key ]]; then first=$name first_key=$key; fi
  done
}

said=
warned=
while :; do
  if [ ! -e "$queue/$ticket" ]; then
    write_ticket
    echo "loadlock: ticket $ticket was gone; queued it again in its place" >&2
  else
    before_beat
    # The heartbeat (a builtin write: its time is the ticket's mtime). A
    # ticket removed since the check above is written again by it, under its
    # name: the waiter keeps its place.
    printf '%s\n' "$tag" >"$queue/$ticket" 2>/dev/null || true
  fi
  reap_tickets
  first_ticket
  # LOADLOCK_TEST_AFTER_HEAD (tests only, with LOADLOCK_TEST=1) widens the
  # gap between the head check and the turn, seconds.
  if [ "${LOADLOCK_TEST:-}" = 1 ] && [ -n "${LOADLOCK_TEST_AFTER_HEAD:-}" ]; then sleep "$LOADLOCK_TEST_AFTER_HEAD"; fi
  if [ "$first" = "$ticket" ]; then
    ! may_take || ! head_turn || break
  elif reclaim_due; then
    with_flock reclaim_turn || true
  fi
  holder='?'
  ! read_file "$lock/owner" || holder=$file_v
  why="$lock is held by $holder"
  [ "$holder" = '?' ] || [[ $holder == *ticket=* ]] ||
    why="$why (a holder that took it without a ticket: the queue does not order it)"
  [ "$first" = "$ticket" ] || why="$why; ticket $ticket waits behind $first"
  [ "$why" = "$said" ] || echo "loadlock: $why; checking again on a change or in ${retry}s" >&2
  said=$why
  if [[ $retry =~ ^[1-9][0-9]*$ ]] && command -v inotifywait >/dev/null; then
    inotifywait -qq -t "$retry" -e create,delete,move "$queue" "$(dirname "$lock")" "$slots" 2>/dev/null || true
  else
    sleep "$retry"
  fi
done
held=1
rm -f "$queue/$ticket"

"$@" &
child=$!
wait "$child"
