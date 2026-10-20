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
#   - Only the waiter whose ticket sorts first tries the lock, and only it may
#     reclaim a slot (or an older client's LOCK) of a holder that is gone,
#     under flock(1) on LOCK.queue/.reclaim; it removes it outright.
#   - Records (the owner file, tickets) are "<owner> pid <N> <time>" plus
#     boot=<boot id> start=<the process's start time, /proc/<pid>/stat field
#     22> (and ticket=<name> in the owner file). One is dead when its boot id
#     is not this boot's, its pid is gone, or the pid's start time differs
#     (a reused pid). A record without those keys (an older client's) is dead
#     when its pid is gone or started after the record's time. A lock without
#     a pid is never taken over, unless it has had no owner file for a minute.
#   - A waiter whose ticket is gone writes it again under its name.
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

proc=/proc
boot_id() { cat "$proc/sys/kernel/random/boot_id" 2>/dev/null; }
# start_of PID prints the start time of PID (stat field 22, counted after the
# command's last ')'), or fails when PID does not run.
start_of() {
  local stat rest f
  stat=$(cat "$proc/$1/stat" 2>/dev/null) || return 1
  rest=${stat##*)}
  read -ra f <<<"$rest"
  [ -n "${f[19]:-}" ] || return 1
  echo "${f[19]}"
}
# record_dead RECORD [EPOCH] succeeds when RECORD names a process that is
# gone; EPOCH is the record's time when the text has none (a ticket's).
record_dead() {
  local r=$1 at=${2:-} pid start btime hz
  [[ $r =~ (^|[[:space:]])pid\ ([0-9]+)([[:space:]]|$) ]] || return 1
  pid=${BASH_REMATCH[2]}
  if [[ $r =~ (^|[[:space:]])boot=([^[:space:]]+) ]] && [ "${BASH_REMATCH[2]}" != "$(boot_id)" ]; then
    return 0
  fi
  start=$(start_of "$pid") || return 0
  # A holder that could not read its own start time: boot id and pid only.
  [[ $r =~ (^|[[:space:]])start=unknown([[:space:]]|$) ]] && return 1
  if [[ $r =~ (^|[[:space:]])start=([0-9]+) ]]; then
    [ "${BASH_REMATCH[2]}" != "$start" ]
    return
  fi
  if [[ $r =~ pid\ [0-9]+\ ([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]+Z) ]]; then
    at=$(date -u -d "${BASH_REMATCH[1]}" +%s 2>/dev/null) || at=
  fi
  [ -n "$at" ] || return 1
  btime=$(awk '$1 == "btime" {print $2}' "$proc/stat" 2>/dev/null)
  hz=$(getconf CLK_TCK)
  [ -n "$btime" ] || return 1
  [ $((btime + start / hz)) -gt $((at + 1)) ]
}

# Never start=0: a holder whose start time is unknown says so, and the others
# judge it by its boot id and pid only.
self_start=$(start_of $$) || self_start=unknown
tag="$owner pid $$ $(date -u +%Y-%m-%dT%H:%M:%SZ) boot=$(boot_id) start=$self_start"
# -x: hold the lock alone (a heavy live suite): this run takes a slot only when
# none is held, and while its record (exclusive=1) holds one no one takes the
# other.
[ -z "$alone" ] || tag="$tag exclusive=1"
queue="$lock.queue"
mkdir -p "$queue"
ticket=$(printf '%s-%019d-%d' "$class" "$(date +%s%N)" "$$")
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
# every change to the lock, its slots and its view.
with_flock() {
  local fd rc
  exec {fd}>"$queue/.reclaim" || return 1
  flock -x "$fd"
  "$@"
  rc=$?
  flock -u "$fd"
  exec {fd}>&-
  return "$rc"
}

# holders_count prints how many slots are held, or "exclusive" when LOCK is
# an older client's (no view marker).
holders_count() {
  local n=0 d
  if [ -d "$lock" ] && [ ! -e "$lock/.counting" ]; then
    echo exclusive
    return
  fi
  for d in "$slots"/*; do
    [ -f "$d/owner" ] && n=$((n + 1))
  done
  echo "$n"
}

# write_view makes LOCK the view of the slots: with the marker and LOCK/owner
# listing every holder while any is held, gone when none is. Under the flock.
# It fails (3) when LOCK is an older client's: one made since holders_count
# looked is never taken over (a plain mkdir, #2242 QA). The holder lines have
# "pid N" written "pid:N": older clients judge LOCK/owner by its first
# "pid N", and would remove the view while another slot's holder runs.
write_view() {
  local d lines=
  [ "$(holders_count)" != exclusive ] || return 3
  for d in "$slots"/*; do
    [ -f "$d/owner" ] && lines="$lines$(sed -E 's/(^|[[:space:]])pid ([0-9]+)/\1pid:\2/g' "$d/owner")"$'\n'
  done
  if [ -z "$lines" ]; then
    # Only the view goes: a LOCK an older client made since holders_count
    # looked has no marker, and is left alone.
    [ ! -e "$lock/.counting" ] || rm -r "$lock"
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
  if [ "$(cat "$slot/owner" 2>/dev/null)" = "$record" ]; then
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

# reap_tickets removes the tickets of waiters that are gone.
reap_tickets() {
  local t name text ns
  for t in "$queue"/[0-9]-*; do
    [ -e "$t" ] || continue
    name=${t##*/}
    text=$(cat "$t" 2>/dev/null) || continue
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
# for tests only: it is honoured only with LOADLOCK_TEST=1). Each holder has a slot LOCK.d/<n> with its owner record; LOCK
# exists while anyone holds it (an older client's mkdir LOCK still waits),
# LOCK/owner listing every holder and LOCK/.counting marking it as this
# view. hack/release-gate/fairlock.go counts the same way.
capacity() {
  local load
  load=$(cut -d' ' -f1 /proc/loadavg)
  [ "${LOADLOCK_TEST:-}" != 1 ] || load=${LOADLOCK_TEST_LOAD:-$load}
  if awk -v l="$load" 'BEGIN { exit !(l < 32) }'; then echo 2; else echo 1; fi
}

# dead_dir DIR succeeds when DIR's owner is gone, or DIR has had no owner
# file for a minute; it prints why.
dead_dir() {
  local holder
  if holder=$(cat "$1/owner" 2>/dev/null); then
    record_dead "$holder" && echo " of a holder that is gone: $holder"
    return
  fi
  [ -d "$1" ] && [ $(($(date +%s) - $(stat -c %Y "$1" 2>/dev/null || date +%s))) -ge 60 ] &&
    echo ", which has had no owner for a minute"
}

# take_turn is the head waiter's turn, under the flock: it removes the slots
# (and an older client's exclusive lock) of holders that are gone, then takes
# a free slot when fewer than the capacity hold the lock. It succeeds when
# this run holds a slot.
take_turn() {
  local d why held_n n
  if [ "$(holders_count)" = exclusive ]; then
    if why=$(dead_dir "$lock"); then
      rm -r "$lock" && echo "loadlock: removed $lock$why" >&2
    fi
  fi
  for d in "$slots"/*; do
    [ -d "$d" ] || continue
    if why=$(dead_dir "$d"); then
      rm -r "$d" && echo "loadlock: removed lock slot $d$why" >&2
    fi
  done
  write_view || true
  held_n=$(holders_count)
  [ "$held_n" != exclusive ] && [ "$held_n" -lt "$(capacity)" ] || return 1
  [ -z "$alone" ] || [ "$held_n" -eq 0 ] || return 1
  for d in "$slots"/*; do
    [ -f "$d/owner" ] && grep -q 'exclusive=1' "$d/owner" && return 1
  done
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

# head_turn runs take_turn under the flock; a failed owner write ends the run.
head_turn() {
  local rc=0
  mkdir -p "$slots"
  with_flock take_turn || rc=$?
  [ "$rc" -ne 2 ] || exit 1
  return "$rc"
}

said=
warned=
while :; do
  if [ ! -e "$queue/$ticket" ]; then
    write_ticket
    echo "loadlock: ticket $ticket was gone; queued it again in its place" >&2
  fi
  reap_tickets
  first=$(find "$queue" -mindepth 1 -maxdepth 1 -name '[0-9]-*' -printf '%f\n' | LC_ALL=C sort | head -n 1)
  if [ "$first" = "$ticket" ] && head_turn; then
    break
  fi
  holder=$(cat "$lock/owner" 2>/dev/null || echo '?')
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
