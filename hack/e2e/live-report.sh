#!/usr/bin/env bash
# hack/e2e/live-report.sh JOBS_TSV
#
# The failure issues of a scheduled e2e-live.yml run (its report job).
# JOBS_TSV has one "<job name>\t<conclusion>" line per job of the run. The
# sample jobs (hack/e2e/sample.sh), whose names start with their id
# ("sample-gitea-135: ..."), each get an issue of their own,
# "e2e live sample failed: <id>": a failed sample job opens it (or comments
# on it) with the commands that run the job again, and only that job
# passing again closes it, so a pair that runs once every 14 nights is not
# closed by the nights in between. Every other job shares
# "e2e live failed on main (<KIND>)", opened when one of them fails and
# closed by a run where none failed.
#
# Env: KIND (the run's label), RUN_URL, SHA, SAMPLE (the night's date),
# E2E_LIVE (the conclusion of the "e2e live" job), GH_REPO; gh on PATH.
# The "e2e live" job fails when any job does: it counts against the shared
# issue only when no job failed (the coverage proof failed on its own).
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[ $# -eq 1 ] || {
  echo "usage: $0 JOBS_TSV" >&2
  exit 1
}
jobs_tsv=$1
: "${KIND:?}" "${RUN_URL:?}" "${SHA:?}" "${E2E_LIVE:?}"

failed() { [ "$1" != success ] && [ "$1" != skipped ]; }

# open_issues TITLE prints the numbers of the open issues titled TITLE.
open_issues() {
  gh issue list --state open --limit 200 --json number,title |
    jq -r --arg t "$1" '.[] | select(.title == $t) | .number'
}

# report TITLE BODY_FILE comments on the open issue titled TITLE, or opens one.
report() {
  local n
  n=$(open_issues "$1" | head -1)
  if [ -n "$n" ]; then
    gh issue comment "$n" --body-file "$2"
  else
    gh issue create --title "$1" --body-file "$2" --label kind/bug --label area/test
  fi
}

# resolve TITLE closes the open issues titled TITLE.
resolve() {
  local n
  for n in $(open_issues "$1"); do
    gh issue close "$n" --comment "Passed on ${SHA::8}: $RUN_URL"
  done
}

main_failed=() any_failed=
while IFS=$'\t' read -r name result || [ -n "$name" ]; do
  [ -n "$name" ] || continue
  if [[ "$name" == sample-* ]]; then
    id=${name%%:*}
    title="e2e live sample failed: $id"
    if failed "$result"; then
      any_failed=1
      suite=$(bash "$E2E_DIR/sample.sh" "${SAMPLE:?}" | awk -v id="$id" '$1 == id { print $2 }')
      {
        echo "The sample job \`$id\` of the scheduled e2e live run on ${SHA::8} (sample of $SAMPLE) failed ($result): $RUN_URL"
        echo
        echo "It runs again in the schedule once every \`hack/e2e/sample.sh -cycle\` nights; this issue closes when it passes. Run it now:"
        echo
        echo '```'
        echo "SAMPLE=$SAMPLE SAMPLE_ONLY=1 SUITES=${suite:-<suite>} make e2e-all"
        echo "gh workflow run e2e-live.yml --ref main -f suites=${suite:-<suite>} -f sample=$SAMPLE"
        echo '```'
        echo
        echo "Diagnostics are in the run's e2e-live-$id-* artifact."
      } >body.md
      report "$title" body.md
    elif [ "$result" = success ]; then
      resolve "$title"
    fi
    continue
  fi
  [ "$name" != "e2e live" ] || continue
  if failed "$result"; then
    any_failed=1
    main_failed+=("- $name ($result)")
  fi
done <"$jobs_tsv"
if [ -z "$any_failed" ] && failed "$E2E_LIVE"; then
  main_failed+=("- e2e live ($E2E_LIVE)")
fi

title="e2e live failed on main ($KIND)"
if [ "${#main_failed[@]}" -gt 0 ]; then
  {
    echo "The scheduled e2e live run on ${SHA::8} failed: $RUN_URL"
    echo
    echo "Failed jobs:"
    printf '%s\n' "${main_failed[@]}"
    echo
    echo "Diagnostics are in the run's e2e-live-* artifacts; the coverage proof in coverage-proof."
  } >body.md
  report "$title" body.md
else
  resolve "$title"
fi
