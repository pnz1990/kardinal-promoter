#!/usr/bin/env bash
# hack/e2e/components/kardinal.sh
#
# Builds the controller image and the kardinal CLI from this checkout, loads
# the image into the kind node, and installs the chart wired to the suite's
# git server (KARDINAL_E2E_SCM_PROVIDER / KARDINAL_E2E_SCM_API in the env
# file, token Secret kardinal-system/git-token, webhook Secret
# kardinal-system/scm-webhook when present). Idempotent (helm upgrade).
#
# Env:
#   KARDINAL_E2E_IMAGE      image ref (default
#                           ghcr.io/pnz1990/kardinal-promoter/controller:e2e-<cluster>, so
#                           suites set up at once on one host don't load each other's build)
#   KARDINAL_E2E_BUILD      docker (default): docker build of the repo Dockerfile
#                           host: go build on the host + hack/e2e/controller.Dockerfile
#                           none: KARDINAL_E2E_IMAGE is already built (CI)
#   KARDINAL_E2E_HELM_ARGS  extra helm arguments, word-split
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster
# shellcheck disable=SC1091
source "$E2E_OUT/env"

IMAGE=${KARDINAL_E2E_IMAGE:-ghcr.io/pnz1990/kardinal-promoter/controller:e2e-$KIND_CLUSTER}
BUILD=${KARDINAL_E2E_BUILD:-docker}
BIN="$E2E_OUT/bin"
mkdir -p "$BIN"

case "$BUILD" in
  docker) docker build -q -t "$IMAGE" "$REPO_ROOT" >/dev/null ;;
  host)
    ctx=$(mktemp -d)
    (cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false \
      -ldflags="-s -w -X main.ControllerVersion=e2e" -o "$ctx/kardinal-controller" ./cmd/kardinal-controller)
    chmod 0755 "$ctx" "$ctx/kardinal-controller"
    docker build -q -t "$IMAGE" -f "$E2E_DIR/controller.Dockerfile" "$ctx" >/dev/null
    ;;
  none) docker image inspect "$IMAGE" >/dev/null || die "KARDINAL_E2E_BUILD=none but $IMAGE is not built" ;;
  *) die "KARDINAL_E2E_BUILD must be docker, host or none" ;;
esac
load_image "$IMAGE"
(cd "$REPO_ROOT" && go build -o "$BIN/kardinal" ./cmd/kardinal)
log "controller image $IMAGE ($BUILD), CLI $BIN/kardinal"

args=(
  --set "image.repository=${IMAGE%:*}" --set "image.tag=${IMAGE##*:}" --set image.pullPolicy=Never
  --set logLevel=debug
  --set github.secretRef.name=git-token
  --set "scm.provider=${KARDINAL_E2E_SCM_PROVIDER:?git server component must run first}"
  --set "scm.apiURL=${KARDINAL_E2E_SCM_API:-}"
)
if "${KUBECTL[@]}" -n "$KARDINAL_NS" get secret scm-webhook >/dev/null 2>&1; then
  args+=(--set webhook.secretRef.name=scm-webhook)
fi
# components/ciapi.sh
if "${KUBECTL[@]}" -n "$KARDINAL_NS" get secret bundle-api-token >/dev/null 2>&1; then
  args+=(--set bundleAPI.tokenSecretRef.name=bundle-api-token)
fi
# shellcheck disable=SC2206
args+=(${KARDINAL_E2E_HELM_ARGS:-})

# helm upgrade never updates the chart's crds/: apply them, so a reused
# cluster runs this checkout's CRDs too.
"${KUBECTL[@]}" apply --server-side --force-conflicts -f "$REPO_ROOT/chart/kardinal-promoter/crds/" >/dev/null
"${HELM[@]}" upgrade --install "$KARDINAL_RELEASE" "$REPO_ROOT/chart/kardinal-promoter" \
  -n "$KARDINAL_NS" --create-namespace "${args[@]}" --wait --timeout 5m >/dev/null
# A rebuilt image under the same tag needs a restart to be picked up.
"${KUBECTL[@]}" -n "$KARDINAL_NS" rollout restart "deploy/$KARDINAL_RELEASE" >/dev/null
"${KUBECTL[@]}" -n "$KARDINAL_NS" rollout status "deploy/$KARDINAL_RELEASE" --timeout=180s >/dev/null

env_set KARDINAL_E2E_CLI "$BIN/kardinal"
log "controller ready (scm.provider=$KARDINAL_E2E_SCM_PROVIDER)"
