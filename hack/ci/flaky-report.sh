#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# hack/ci/flaky-report.sh finds the tests that both failed and passed on the
# same commit in the CI runs of the last DAYS days (default 7), on main and on
# pull requests: a job that failed and passed on a re-run (red-main.sh re-runs
# a failed main run once), or in two runs of one commit. It keeps one open
# issue labelled flaky-test with the list, and closes it when a week has
# none. Run weekly by .github/workflows/ci-health.yml.
#
# Env: GH_REPO and GH_TOKEN for gh; DAYS; DRY_RUN=1 prints the report instead
# of writing the issue.
set -euo pipefail

: "${GH_REPO:?GH_REPO is owner/repo}"
DAYS=${DAYS:-7}
DRY_RUN=${DRY_RUN:-0}
LABEL=flaky-test
TITLE="Flaky tests: CI jobs that failed and passed on the same commit"
REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT
mkdir "$work/logs"

# citool runs hack/citool; CITOOL, when set, is a built one (the tests).
citool() {
  if [ -n "${CITOOL:-}" ]; then
    "$CITOOL" "$@"
  else
    (cd "$REPO_ROOT" && go run ./hack/citool "$@")
  fi
}

since=$(date -u -d "-$DAYS days" +%Y-%m-%dT%H:%M:%SZ)
gh api --paginate "repos/$GH_REPO/actions/workflows/ci.yml/runs?status=completed&per_page=100&created=%3E%3D$since" \
  --jq '.workflow_runs[] | [.id, .head_sha, .run_attempt] | @tsv' > "$work/runs.tsv"

# Only a run with several attempts, or a commit with several runs, can have
# failed and passed on one commit.
awk -F'\t' '{ n[$2]++; line[NR] = $0; sha[NR] = $2; att[NR] = $3 }
  END { for (i = 1; i <= NR; i++) if (att[i] > 1 || n[sha[i]] > 1) print line[i] }' \
  "$work/runs.tsv" > "$work/candidates.tsv"

: > "$work/records.tsv"
while IFS=$'\t' read -r id sha attempts; do
  for a in $(seq 1 "$attempts"); do
    gh api --paginate "repos/$GH_REPO/actions/runs/$id/attempts/$a/jobs?per_page=100" < /dev/null \
      --jq ".jobs[] | select(.conclusion == \"success\" or .conclusion == \"failure\")
            | [\"$sha\", \"$id\", \"$a\", .name, (.id | tostring), .conclusion] | @tsv" >> "$work/records.tsv"
  done
done < "$work/candidates.tsv"

# The logs of the failed jobs whose job passed on the same commit (an expired
# log is skipped).
awk -F'\t' '$6 == "success" { ok[$1 "\t" $4] = 1 } { r[NR] = $0; k[NR] = $1 "\t" $4; c[NR] = $6; j[NR] = $5 }
  END { for (i = 1; i <= NR; i++) if (c[i] == "failure" && ok[k[i]]) print j[i] }' "$work/records.tsv" |
  while read -r job; do
    gh api "repos/$GH_REPO/actions/jobs/$job/logs" > "$work/logs/$job.log" 2>/dev/null < /dev/null ||
      rm -f "$work/logs/$job.log"
  done

citool flaky -repo "$GH_REPO" "$work/records.tsv" "$work/logs" > "$work/table.md"
{
  echo "CI jobs that failed and then passed on the same commit in the $DAYS days to $(date -u +%F)"
  echo "($(wc -l < "$work/candidates.tsv") runs re-run or sharing a commit, of $(wc -l < "$work/runs.tsv"))."
  echo
  cat "$work/table.md"
  echo
  echo "A flaky test is a bug: fix it, or skip it with a link to its issue (CONTRIBUTING.md, \"Flaky tests\")."
  echo "Updated every week by ci-health.yml."
} > "$work/body.md"
cat "$work/body.md" >> "${GITHUB_STEP_SUMMARY:-/dev/null}"

issue=$(gh issue list --repo "$GH_REPO" --label "$LABEL" --state open --json number --jq '.[0].number // empty')
if [ "$DRY_RUN" = 1 ]; then
  echo "issue: ${issue:-none open}"
  cat "$work/body.md"
  exit 0
fi
if grep -q '^No flaky tests' "$work/table.md"; then
  if [ -n "$issue" ]; then
    gh issue close "$issue" --repo "$GH_REPO" --comment "No flaky CI job in the $DAYS days to $(date -u +%F)."
  fi
  exit 0
fi
if [ -z "$issue" ]; then
  gh label create "$LABEL" --repo "$GH_REPO" --color FBCA04 \
    --description "A test that failed and passed on the same commit (ci-health.yml)" 2>/dev/null || true
  gh issue create --repo "$GH_REPO" --title "$TITLE" --body-file "$work/body.md" \
    --label "$LABEL" --label kind/bug --label area/test --label priority/high
else
  gh issue edit "$issue" --repo "$GH_REPO" --body-file "$work/body.md"
  gh issue comment "$issue" --repo "$GH_REPO" --body "Updated for the $DAYS days to $(date -u +%F)."
fi
