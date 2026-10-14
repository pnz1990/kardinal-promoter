#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# hack/ci/red-main.sh keeps one open issue labelled red-main in step with CI
# on main (#1836), run by .github/workflows/red-main.yml after every CI run on
# main:
#
#   - The newest finished main run failed on its first attempt: re-run its
#     failed jobs once and wait, so a flaky test does not page anyone (the
#     weekly flaky-test report in ci-health.yml lists it).
#   - It (still) fails: open the red-main issue, or update it, with the
#     failing jobs and tests and the merges since the last green main run.
#   - It passed: close the issue.
#
# It always acts on the newest finished run, not the one that triggered it,
# so it is safe to run twice or out of order.
#
# Env: GH_REPO (owner/repo) and GH_TOKEN for gh; DRY_RUN=1 prints the issue
# instead of writing it and does not re-run anything; RERUN=0 skips the
# re-run; RUN_ID reports that main run instead of the newest (to try it on an
# old failure with DRY_RUN=1).
set -euo pipefail

: "${GH_REPO:?GH_REPO is owner/repo}"
DRY_RUN=${DRY_RUN:-0}
RERUN=${RERUN:-1}
LABEL=red-main
TITLE="CI is red on main"
SERVER=https://github.com/$GH_REPO
REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

# citool runs hack/citool; CITOOL, when set, is a built one (the tests).
citool() {
  if [ -n "${CITOOL:-}" ]; then
    "$CITOOL" "$@"
  else
    (cd "$REPO_ROOT" && go run ./hack/citool "$@")
  fi
}

# The last 100 finished CI runs of pushes to main that passed or failed
# (cancelled ones say nothing), newest first: id, sha, conclusion, attempt,
# created.
main_runs() {
  gh api "repos/$GH_REPO/actions/workflows/ci.yml/runs?branch=main&event=push&status=completed&per_page=100" \
    --jq '.workflow_runs[] | select(.conclusion == "success" or .conclusion == "failure")
          | [.id, .head_sha, .conclusion, .run_attempt, .created_at] | @tsv'
}

main_runs > "$work/runs.tsv"
if [ ! -s "$work/runs.tsv" ]; then
  echo "no finished CI run on main"
  exit 0
fi
if [ -n "${RUN_ID:-}" ]; then
  awk -F'\t' -v id="$RUN_ID" '$1 == id' "$work/runs.tsv" > "$work/pick.tsv"
  [ -s "$work/pick.tsv" ] || { echo "run $RUN_ID is not a finished main run" >&2; exit 1; }
else
  head -n 1 "$work/runs.tsv" > "$work/pick.tsv"
fi
IFS=$'\t' read -r id sha conclusion attempt created < "$work/pick.tsv"

if [ "$conclusion" = failure ] && [ "$attempt" = 1 ] && [ "$RERUN" = 1 ] && [ "$DRY_RUN" != 1 ]; then
  echo "run $id on ${sha::8} failed on attempt 1: re-running its failed jobs once"
  if gh run rerun "$id" --failed --repo "$GH_REPO"; then
    # Wait for the re-run to start, then to finish (40 minutes at most).
    for _ in $(seq 1 30); do
      [ "$(gh api "repos/$GH_REPO/actions/runs/$id" --jq .status)" != completed ] && break
      sleep 2
    done
    for _ in $(seq 1 80); do
      [ "$(gh api "repos/$GH_REPO/actions/runs/$id" --jq .status)" = completed ] && break
      sleep 30
    done
    main_runs > "$work/runs.tsv"
    IFS=$'\t' read -r id sha conclusion attempt created < "$work/runs.tsv"
  else
    echo "::warning::could not re-run run $id; reporting it as it is"
  fi
fi

issue=$(gh issue list --repo "$GH_REPO" --label "$LABEL" --state open --json number --jq '.[0].number // empty')
run_url="$SERVER/actions/runs/$id/attempts/$attempt"

if [ "$conclusion" = success ]; then
  echo "main is green at ${sha::8} (run $id)"
  [ -n "$issue" ] || exit 0
  msg="main is green again at [${sha::8}]($SERVER/commit/$sha): $run_url"
  if [ "$attempt" != 1 ]; then
    msg+=$'\n\n'"It passed on attempt $attempt: what failed before is flaky, and the weekly flaky-test report (ci-health.yml) lists it."
  fi
  if [ "$DRY_RUN" = 1 ]; then
    printf 'would close #%s:\n%s\n' "$issue" "$msg"
  else
    gh issue close "$issue" --repo "$GH_REPO" --comment "$msg"
  fi
  exit 0
fi

# Failing jobs, their failed steps, and the tests their logs name.
gh api --paginate "repos/$GH_REPO/actions/runs/$id/attempts/$attempt/jobs?per_page=100" \
  --jq '.jobs[] | select(.conclusion == "failure")
        | [.id, .name, .html_url, ([.steps[]? | select(.conclusion == "failure") | .name] | join(", "))] | @tsv' \
  > "$work/jobs.tsv"
{
  echo "<!-- red-main run=$id attempt=$attempt -->"
  echo "CI is red on main at [${sha::8}]($SERVER/commit/$sha): $run_url (attempt $attempt)."
  echo
  echo "### Failing jobs"
  echo
  while IFS=$'\t' read -r job_id job_name job_url steps; do
    [ "$job_name" = build ] && continue # the shard collector: it fails when a shard does
    echo "- [$job_name]($job_url)${steps:+: $steps}"
    if gh api "repos/$GH_REPO/actions/jobs/$job_id/logs" > "$work/$job_id.log" 2>/dev/null < /dev/null; then
      citool failures < "$work/$job_id.log" |
        awk -F'\t' 'NR <= 30 { sub("^github.com/kardinal-promoter/kardinal-promoter/", "", $1); printf "  - `%s` `%s`\n", $1, $2 }'
    fi
  done < "$work/jobs.tsv"
  echo
  # The merges since the last green run before this one.
  green=$(awk -F'\t' -v c="$created" '$3 == "success" && $5 < c { print $2; exit }' "$work/runs.tsv")
  if [ -n "$green" ]; then
    echo "### Merges since the last green main run ([${green::8}]($SERVER/commit/$green))"
    echo
    gh api "repos/$GH_REPO/compare/$green...$sha" < /dev/null \
      --jq '.commits[] | "- \(.sha[:8]) \(.commit.message | split("\n")[0]) (@\(.author.login // .commit.author.name))"'
  else
    echo "### Merges"
    echo
    echo "No green main run in the last 100: see the commit history."
  fi
  echo
  echo "### What to do"
  echo
  echo "Fix forward within 30 minutes, or revert the merge that broke main (CONTRIBUTING.md, \"When main is red\")."
  echo "Do not merge anything else until main is green. This issue closes itself on the next green main run."
} > "$work/body.md"

if [ "$DRY_RUN" = 1 ]; then
  echo "would ${issue:+update #$issue}${issue:-open an issue}:"
  cat "$work/body.md"
  exit 0
fi
if [ -z "$issue" ]; then
  gh label create "$LABEL" --repo "$GH_REPO" --color B60205 \
    --description "CI on main is failing (opened and closed by red-main.yml)" 2>/dev/null || true
  gh issue create --repo "$GH_REPO" --title "$TITLE" --body-file "$work/body.md" \
    --label "$LABEL" --label kind/bug --label area/test --label priority/critical
  exit 0
fi
previous=$(gh issue view "$issue" --repo "$GH_REPO" --json body --jq '.body | split("\n")[0]')
gh issue edit "$issue" --repo "$GH_REPO" --body-file "$work/body.md"
if [ "$previous" != "<!-- red-main run=$id attempt=$attempt -->" ]; then
  gh issue comment "$issue" --repo "$GH_REPO" --body "Still red at [${sha::8}]($SERVER/commit/$sha): $run_url"
fi
