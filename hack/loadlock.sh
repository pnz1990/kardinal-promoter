#!/usr/bin/env bash
# hack/loadlock.sh [-l LOCK] [-o OWNER] [-r RETRY] -- CMD [ARG...]
#
# Runs CMD holding the shared host's heavy-load lock: a directory that only
# one holder can create (mkdir is atomic), with an owner file naming who
# holds it. It waits, retrying every RETRY seconds (30), while another
# holder has it. When CMD ends, however it ends (an error, a signal), the
# lock is removed only if its owner file is still this run's: a lock
# another holder created, or put back, is never removed. Exits with CMD's
# status.
#
# LOCK defaults to $LOADLOCK; one of the two is required.
# OWNER (for example "ENGINEER-4 scale full run") is written with this
# run's pid and start time, as hack/release-gate's -owner is.
set -uo pipefail

lock=${LOADLOCK:-}
owner=${USER:-unknown}
retry=30
while [ $# -gt 0 ]; do
  case $1 in
    -l) lock=$2; shift 2 ;;
    -o) owner=$2; shift 2 ;;
    -r) retry=$2; shift 2 ;;
    --) shift; break ;;
    -*) echo "usage: $0 [-l LOCK] [-o OWNER] [-r RETRY] -- CMD [ARG...]" >&2; exit 2 ;;
    *) break ;;
  esac
done
if [ $# -eq 0 ] || [ -z "$lock" ]; then
  echo "usage: $0 [-l LOCK] [-o OWNER] [-r RETRY] -- CMD [ARG...]" >&2
  exit 2
fi

tag="$owner pid $$ $(date -u +%Y-%m-%dT%H:%M:%SZ)"
until mkdir "$lock" 2>/dev/null; do
  echo "loadlock: $lock is held by $(cat "$lock/owner" 2>/dev/null || echo '?'); retrying in ${retry}s" >&2
  sleep "$retry"
done
echo "$tag" > "$lock/owner"

release() {
  if [ "$(cat "$lock/owner" 2>/dev/null)" = "$tag" ]; then
    rm -r "$lock"
  else
    echo "loadlock: $lock is not ours any more; left as it is" >&2
  fi
}
trap release EXIT
# A signal stops CMD first, then releases the lock (EXIT).
child=
stop() {
  [ -n "$child" ] && kill -TERM "$child" 2>/dev/null && wait "$child" 2>/dev/null
  exit "$1"
}
trap 'stop 130' INT
trap 'stop 143' TERM

"$@" &
child=$!
wait "$child"
