#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# hack/ci/refresh-durations.sh measures each Go package's test time from the
# go test shard jobs of the last RUNS (default 5) green CI runs on main, and
# when the committed hack/ci/test-durations.txt splits them more than 15% off
# the ideal (or a package came or went), pushes the measured file to the
# branch bot/test-durations and opens or updates an issue (label
# test-durations) linking the pull request to open. GitHub Actions may not
# open pull requests in this repository, and a pull request its token opened
# would not run CI anyway. Run weekly by .github/workflows/ci-health.yml.
#
# Env: GH_REPO and GH_TOKEN for gh; RUNS; JOBS, the job name prefix of the
# shards ("go test ("); DRY_RUN=1 prints the result and pushes nothing.
set -euo pipefail

: "${GH_REPO:?GH_REPO is owner/repo}"
RUNS=${RUNS:-5}
JOBS=${JOBS:-go test (}
DRY_RUN=${DRY_RUN:-0}
BRANCH=bot/test-durations
LABEL=test-durations
TITLE="Refresh hack/ci/test-durations.txt: the go test shards are unbalanced"
REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
FILE=hack/ci/test-durations.txt
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT
cd "$REPO_ROOT"

# citool runs hack/citool; CITOOL, when set, is a built one (the tests).
citool() {
  if [ -n "${CITOOL:-}" ]; then
    "$CITOOL" "$@"
  else
    (cd "$REPO_ROOT" && go run ./hack/citool "$@")
  fi
}

shards=$(sed -n 's/^ *SHARDS: "\([0-9]*\)"$/\1/p' .github/workflows/ci.yml)
[ -n "$shards" ] || { echo "no SHARDS in ci.yml" >&2; exit 1; }

gh api "repos/$GH_REPO/actions/workflows/ci.yml/runs?branch=main&event=push&status=success&per_page=$RUNS" \
  --jq '.workflow_runs[].id' > "$work/runs"
n=0
while read -r run; do
  gh api --paginate "repos/$GH_REPO/actions/runs/$run/jobs?per_page=100" < /dev/null \
    --jq ".jobs[] | select(.name | startswith(\"$JOBS\")) | .id" |
    while read -r job; do
      gh api "repos/$GH_REPO/actions/jobs/$job/logs" > "$work/$job.log" 2>/dev/null < /dev/null ||
        rm -f "$work/$job.log"
    done
  n=$((n + 1))
done < "$work/runs"
logs=("$work"/*.log)
if [ ! -e "${logs[0]}" ]; then
  echo "no go test shard logs in the last $RUNS green main runs"
  exit 0
fi

citool durations "${logs[@]}" > "$work/measured.txt"
skew=$(citool skew "$shards" "$FILE" "$work/measured.txt")
pkgs() { grep -v '^#' "$1" | awk 'NF { print $1 }' | sort; }
added=$(comm -13 <(pkgs "$FILE") <(pkgs "$work/measured.txt") | wc -l)
removed=$(comm -23 <(pkgs "$FILE") <(pkgs "$work/measured.txt") | wc -l)
summary="From the go test shards of $n green main runs: the committed durations split them with the slowest shard at ${skew}x the ideal; $added packages not in the file, $removed in it that no longer run."
echo "$summary" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"

if awk -v s="$skew" 'BEGIN { exit !(s <= 1.15) }' && [ "$added" = 0 ] && [ "$removed" = 0 ]; then
  echo "balanced: nothing to refresh"
  exit 0
fi
if [ "$DRY_RUN" = 1 ]; then
  diff "$FILE" "$work/measured.txt" || true
  exit 0
fi

git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git switch -C "$BRANCH"
cp "$work/measured.txt" "$FILE"
git add "$FILE"
git commit -q -m "chore(ci): refresh hack/ci/test-durations.txt" -m "$summary"
git push -f origin "$BRANCH"

body="$summary

[Open the pull request](https://github.com/$GH_REPO/compare/main...$BRANCH?expand=1) for the branch \`$BRANCH\`, which ci-health.yml refreshes every week. It closes this issue when it merges, with \"Closes #<this issue>\" in its body."
issue=$(gh issue list --repo "$GH_REPO" --label "$LABEL" --state open --json number --jq '.[0].number // empty')
if [ -z "$issue" ]; then
  gh label create "$LABEL" --repo "$GH_REPO" --color C5DEF5 \
    --description "hack/ci/test-durations.txt needs a refresh (ci-health.yml)" 2>/dev/null || true
  gh issue create --repo "$GH_REPO" --title "$TITLE" --body "$body" \
    --label "$LABEL" --label kind/chore --label area/test --label priority/low
else
  gh issue edit "$issue" --repo "$GH_REPO" --body "$body"
fi
