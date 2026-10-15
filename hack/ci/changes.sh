#!/usr/bin/env bash
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
#
# hack/ci/changes.sh reads the paths a pull request changes, one per line on
# stdin, and prints which areas of CI they touch, as GitHub Actions outputs:
#
#   go=true|false        Go code, tests and what they read (examples, demo,
#                        hack/, test/, Makefile, the embedded web/dist and its embed.go), the
#                        lint and git configs, Dockerfiles, and shell scripts
#                        (shellcheck runs in the e2e stubs job)
#   web=true|false       the UI (web/), its token generator (scripts/)
#   chart=true|false     the Helm chart and the CRDs it ships
#   docs=true|false      the docs site (docs/, mkdocs.yml, overrides/, README.md)
#   workflows=true|false .github/ (workflows, composite actions)
#
# ci.yml's changes job runs it on the pull request's diff and runs each job
# only when its area changed (#1903). A change to .github/, go.mod or go.sum,
# or an empty diff, turns every area on: CI itself or the dependencies
# changed, or the diff is unknown. ALL=1 turns every area on (pushes to main).
set -euo pipefail

go=false web=false chart=false docs=false workflows=false
all=${ALL:-0}
n=0
while IFS= read -r f || [ -n "$f" ]; do
  [ -n "$f" ] || continue
  n=$((n + 1))
  case "$f" in
    .github/*) workflows=true all=1 ;;
    go.mod | go.sum) go=true all=1 ;;
  esac
  case "$f" in
    *.go | *.sh | Makefile | hack/* | test/* | examples/* | demo/* | api/* | cmd/* | pkg/* | web/dist/*) go=true ;;
    .golangci.yml | .gitignore | .gitattributes | .dockerignore | Dockerfile | *.Dockerfile) go=true ;;
  esac
  case "$f" in
    web/* | scripts/*) web=true ;;
  esac
  case "$f" in
    chart/* | config/*) chart=true ;;
  esac
  case "$f" in
    docs/* | mkdocs.yml | overrides/* | README.md) docs=true ;;
  esac
done
[ "$n" -gt 0 ] || all=1
if [ "$all" = 1 ]; then
  go=true web=true chart=true docs=true workflows=true
fi
printf 'go=%s\nweb=%s\nchart=%s\ndocs=%s\nworkflows=%s\n' "$go" "$web" "$chart" "$docs" "$workflows"
