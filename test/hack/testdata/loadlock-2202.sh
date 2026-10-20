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
# <class>-<nanoseconds>-<pid>: only the waiter whose ticket sorts first
# tries the lock, so they are served in order, and a release gate's tickets
# (-p release, class 0) before everyone else's (class 1). hack/release-gate
# queues the same way (-priority). A waiter wakes on the queue's changes
# (inotifywait, when installed) or every RETRY seconds (10). A ticket, or a
# lock whose owner says "pid N", of a process that is gone is removed, so a
# killed run does not block the queue; a lock without a pid is never taken
# over.
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

tag="$owner pid $$ $(date -u +%Y-%m-%dT%H:%M:%SZ)"
queue="$lock.queue"
mkdir -p "$queue"
ticket=$(printf '%s-%019d-%d' "$class" "$(date +%s%N)" "$$")
printf '%s\n' "$tag" >"$queue/.$ticket" && mv "$queue/.$ticket" "$queue/$ticket"

held=
child=
release() {
  rm -f "$queue/$ticket"
  [ -n "$held" ] || return 0
  if [ "$(cat "$lock/owner" 2>/dev/null)" = "$tag" ]; then
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

alive() { [ "$1" -gt 0 ] 2>/dev/null && [ -d "/proc/$1" ]; }

# reap removes the tickets of waiters that are gone, and the lock of a
# holder that is gone: renamed aside first, and put back when the owner read
# then is not the dead one's (a new holder took the lock meanwhile).
reap() {
  local t pid holder again aside
  for t in "$queue"/*; do
    [ -e "$t" ] || continue
    pid=${t##*-}
    alive "$pid" || { rm -f "$t" && echo "loadlock: removed the ticket ${t##*/} of a waiter that is gone" >&2; }
  done
  holder=$(cat "$lock/owner" 2>/dev/null) || return 0
  [[ $holder =~ (^|[[:space:]])pid\ ([0-9]+)([[:space:]]|$) ]] || return 0
  alive "${BASH_REMATCH[2]}" && return 0
  aside="$lock.stale-$$-$(date +%s%N)"
  mv -T "$lock" "$aside" 2>/dev/null || return 0
  again=$(cat "$aside/owner" 2>/dev/null)
  if [ "$again" != "$holder" ]; then
    mv -T "$aside" "$lock" 2>/dev/null
    return 0
  fi
  rm -r "$aside"
  echo "loadlock: removed $lock of a holder that is gone: $holder" >&2
}

said=
while :; do
  reap
  first=$(find "$queue" -mindepth 1 -maxdepth 1 -name '[0-9]-*' -printf '%f\n' | LC_ALL=C sort | head -n 1)
  if [ "$first" = "$ticket" ] && mkdir "$lock" 2>/dev/null; then
    break
  fi
  why="$lock is held by $(cat "$lock/owner" 2>/dev/null || echo '?')"
  [ "$first" = "$ticket" ] || why="$why; ticket $ticket waits behind $first"
  [ "$why" = "$said" ] || echo "loadlock: $why; checking again on a change or in ${retry}s" >&2
  said=$why
  if [[ $retry =~ ^[1-9][0-9]*$ ]] && command -v inotifywait >/dev/null; then
    inotifywait -qq -t "$retry" -e create,delete,move "$queue" "$(dirname "$lock")" 2>/dev/null || true
  else
    sleep "$retry"
  fi
done
echo "$tag" >"$lock/owner"
held=1
rm -f "$queue/$ticket"

"$@" &
child=$!
wait "$child"
