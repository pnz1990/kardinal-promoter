#!/usr/bin/env bash
# hack/loadlock.sh [-l LOCK] [-o OWNER] [-p release|normal] [-r RETRY] -- CMD [ARG...]
#
# Runs CMD holding the shared host's heavy-load lock: a directory that only
# one holder can create (mkdir is atomic), with an owner file naming who
# holds it. When CMD ends, however it ends (an error, a signal), the lock is
# removed only if its owner file is still this run's: a lock another holder
# created, or put back, is never removed. Exits with CMD's status.
#
# The lock is fair. Waiters queue in LOCK.queue, one ticket file each,
# <class>-<nanoseconds>-<pid>: release gates' tickets (-p release, class 0)
# before everyone else's (class 1), then the oldest. hack/release-gate
# (-priority) follows the same protocol (hack/release-gate/fairlock.go):
#   - Only the waiter whose ticket sorts first tries the lock, and only it may
#     reclaim the lock of a holder that is gone, under flock(1) on
#     LOCK.queue/.reclaim; it removes such a lock outright.
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
  echo "usage: $0 [-l LOCK] [-o OWNER] [-p release|normal] [-r RETRY] -- CMD [ARG...]" >&2
  exit 2
}
lock=${LOADLOCK:-}
owner=${USER:-unknown}
retry=10
class=1
while [ $# -gt 0 ]; do
  case $1 in
    -l) lock=$2; shift 2 ;;
    -o) owner=$2; shift 2 ;;
    -r) retry=$2; shift 2 ;;
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

held=
child=
release() {
  rm -f "$queue/$ticket"
  [ -n "$held" ] || return 0
  if [ "$(cat "$lock/owner" 2>/dev/null)" = "$record" ]; then
    rm -r "$lock"
  else
    echo "loadlock: $lock is not ours any more; left as it is" >&2
  fi
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

# head_turn is the head waiter's turn, under flock on queue/.reclaim: it
# removes the lock of a holder that is gone (or a lock with no owner file for
# a minute), then tries mkdir. It succeeds when this run holds the lock.
head_turn() {
  local fd holder rc=1
  exec {fd}>"$queue/.reclaim" || return 1
  flock -x "$fd"
  if holder=$(cat "$lock/owner" 2>/dev/null); then
    if record_dead "$holder"; then
      rm -r "$lock" && echo "loadlock: removed $lock of a holder that is gone: $holder" >&2
    fi
  elif [ -d "$lock" ] && [ $(($(date +%s) - $(stat -c %Y "$lock" 2>/dev/null || date +%s))) -ge 60 ]; then
    rm -r "$lock" && echo "loadlock: removed $lock, which has had no owner for a minute" >&2
  fi
  if mkdir "$lock" 2>/dev/null; then
    if echo "$record" >"$lock/owner"; then
      # Held from here: a signal from now on releases the lock.
      held=1
      rc=0
    else
      rmdir "$lock"
      echo "loadlock: cannot write $lock/owner; gave the lock up" >&2
      flock -u "$fd"
      exit 1
    fi
  fi
  flock -u "$fd"
  exec {fd}>&-
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
    inotifywait -qq -t "$retry" -e create,delete,move "$queue" "$(dirname "$lock")" 2>/dev/null || true
  else
    sleep "$retry"
  fi
done
held=1
rm -f "$queue/$ticket"

"$@" &
child=$!
wait "$child"
