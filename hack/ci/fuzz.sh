#!/usr/bin/env bash
# hack/ci/fuzz.sh PKG TARGET FUZZTIME [JSON]
#
# Fuzzes TARGET in PKG for FUZZTIME (go test -fuzz, 4 workers), as CI's fuzz
# job and the release gate's fuzz-* steps do. With JSON, go test writes
# -json output there; without, text to stdout.
#
# Go's fuzz coordinator can fail a run that found nothing with "context
# deadline exceeded" when FUZZTIME ends (#2078): when the deadline fires,
# context closes the coordinator's Done channel before it cancels the
# workers' derived context, and the coordinator compares the error of the
# one with the other in between (internal/fuzz/fuzz.go stop, context.go
# cancelCtx.cancel). Such a run, whose only failure is the target's own
# "context deadline exceeded" and which wrote no failing input under
# testdata/fuzz/TARGET, counts as no finding: the target's seed corpus is run
# again as a plain test, and its result is the run's. Any other failure,
# and any failing input written, fails.
set -uo pipefail
if [ $# -lt 3 ]; then
  echo "usage: $0 PKG TARGET FUZZTIME [JSON]" >&2
  exit 2
fi
pkg=$1 target=$2 fuzztime=$3 json=${4:-}
corpus="${pkg#./}/testdata/fuzz/$target"

listing() { find "$corpus" -mindepth 1 -maxdepth 1 -printf '%f\n' 2>/dev/null | sort; }
before=$(listing)

out=$(mktemp)
trap 'rm -f "$out"' EXIT
if [ -n "$json" ]; then
  go test "$pkg" -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" -parallel 4 -json > "$json"
  rc=$?
  cp "$json" "$out"
else
  go test "$pkg" -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" -parallel 4 2>&1 | tee "$out"
  rc=${PIPESTATUS[0]}
fi
[ "$rc" -eq 0 ] && exit 0

new=$(comm -13 <(echo "$before") <(listing) | grep -v '^$' || true)
fails=$(grep -o -- '--- FAIL: [A-Za-z0-9_/]*' "$out" | sort -u)
if [ -z "$new" ] && [ "$fails" = "--- FAIL: $target" ] &&
  grep -q "context deadline exceeded" "$out" && ! grep -q "Failing input written" "$out"; then
  echo "fuzz.sh: $target found nothing in $fuzztime; go test reported Go's fuzzing deadline race (\"context deadline exceeded\", #2078) and wrote no failing input: running its seed corpus as the result"
  if [ -n "$json" ]; then
    go test "$pkg" -run "^${target}\$" -count=1 -json > "$json"
  else
    go test "$pkg" -run "^${target}\$" -count=1
  fi
  exit $?
fi
if [ -n "$new" ]; then
  echo "fuzz.sh: $target wrote failing input under $corpus: $new" >&2
fi
exit "$rc"
