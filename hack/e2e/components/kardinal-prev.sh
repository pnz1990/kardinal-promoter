#!/usr/bin/env bash
# hack/e2e/components/kardinal-prev.sh
#
# Installs kardinal-promoter as an earlier commit builds it, for the
# upgrade-agents suite (#2071): the commit's tree (git archive, so no
# worktree is registered) in ${TMPDIR:-/tmp}/kardinal-e2e-prev-<cluster>
# (outside the checkout, whose tests walk its tree), built and installed by that
# commit's own components/kardinal.sh (host build, two controller replicas,
# so a rolling upgrade runs an older and a newer replica at once), with its
# CLI kept as $E2E_OUT/bin/kardinal-prev. The suite's test upgrades the
# release to this checkout, so a cluster serves one run.
#
# The commit is KARDINAL_E2E_PREV_REF, else the v0.13.0 tag once it exists,
# else the commit this checkout leaves origin/main at (git merge-base): the
# release before this checkout's changes. On a shallow checkout (CI) the
# history and tags are fetched first.
#
# Env set for the tests:
#   KARDINAL_E2E_PREV_CLI    the earlier commit's kardinal CLI
#   KARDINAL_E2E_PREV_IMAGE  its controller image
#   KARDINAL_E2E_PREV_REF    the commit (a full SHA)
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster
# shellcheck disable=SC1091
source "$E2E_OUT/env"

git=(git -C "$REPO_ROOT")
ref=${KARDINAL_E2E_PREV_REF:-}
if [ -z "$ref" ]; then
  if [ "$("${git[@]}" rev-parse --is-shallow-repository)" = true ]; then
    "${git[@]}" fetch -q --unshallow --tags origin
  fi
  "${git[@]}" fetch -q --tags origin main
  if "${git[@]}" rev-parse -q --verify "refs/tags/v0.13.0^{commit}" >/dev/null; then
    ref=v0.13.0
  else
    ref=$("${git[@]}" merge-base HEAD FETCH_HEAD)
  fi
fi
sha=$("${git[@]}" rev-parse --verify "$ref^{commit}") || die "KARDINAL_E2E_PREV_REF=$ref is not a commit"
[ "$sha" != "$("${git[@]}" rev-parse HEAD)" ] || die "the earlier commit $ref is this checkout ($sha): nothing to upgrade from"

image=ghcr.io/pnz1990/kardinal-promoter/controller:e2e-prev-$KIND_CLUSTER
render=ghcr.io/pnz1990/kardinal-promoter/render:e2e-prev-$KIND_CLUSTER
running=$("${KUBECTL[@]}" -n "$KARDINAL_NS" get "deploy/$KARDINAL_RELEASE" --ignore-not-found \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="controller")].image}')
[ -z "$running" ] || [ "$running" = "$image" ] ||
  die "$CTX's controller runs $running, not $image: the suite's test upgraded it; delete it first: kind delete cluster --name $KIND_CLUSTER"

src="${TMPDIR:-/tmp}/kardinal-e2e-prev-$KIND_CLUSTER"
if [ "$(cat "$src/.kardinal-prev-ref" 2>/dev/null)" != "$sha" ]; then
  rm -rf "$src"
  mkdir -p "$src"
  "${git[@]}" archive "$sha" | tar -x -C "$src"
  echo "$sha" >"$src/.kardinal-prev-ref"
fi
# The pinned tools this checkout's tools.sh installed (lib.sh puts
# $REPO_ROOT/bin/e2e first on PATH).
mkdir -p "$src/bin"
ln -sfn "$REPO_ROOT/bin/e2e" "$src/bin/e2e"

log "installing kardinal-promoter as $ref ($sha) builds it: $image, two replicas"
E2E_OUT="$E2E_OUT" KARDINAL_E2E_INSTALL=1 KARDINAL_E2E_BUILD=host KARDINAL_E2E_RACE=0 \
  KARDINAL_E2E_IMAGE="$image" KARDINAL_E2E_RENDER_IMAGE="$render" \
  KARDINAL_E2E_HELM_ARGS="--set replicaCount=2 ${KARDINAL_E2E_PREV_HELM_ARGS:-}" \
  bash "$src/hack/e2e/components/kardinal.sh"
cp "$E2E_OUT/bin/kardinal" "$E2E_OUT/bin/kardinal-prev"
# This checkout's kardinal.sh, which runs next, sources the env file: drop
# what the earlier commit's wrote about its own build, or this checkout's
# would be built under the earlier image's name.
(umask 077 && grep -vE '^export KARDINAL_E2E_(IMAGE|RENDER_IMAGE|CHART|CLI|RACE)=' "$E2E_OUT/env" >"$E2E_OUT/env.tmp" || true)
mv "$E2E_OUT/env.tmp" "$E2E_OUT/env"

env_set KARDINAL_E2E_PREV_CLI "$E2E_OUT/bin/kardinal-prev"
env_set KARDINAL_E2E_PREV_IMAGE "$image"
env_set KARDINAL_E2E_PREV_REF "$sha"
log "kardinal-promoter $ref ($sha) ready, CLI $E2E_OUT/bin/kardinal-prev"
