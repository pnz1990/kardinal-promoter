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
#   KARDINAL_E2E_INSTALL    0: build and load the image and the CLI and apply the
#                           chart's CRDs, but install no release (the chart suite's
#                           tests install their own); build: only build and load
#                           the image and the CLI (the upgrade suite's test applies
#                           the CRDs and upgrades the v0.8.1 release); default 1
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
env_set KARDINAL_E2E_IMAGE "$IMAGE"
env_set KARDINAL_E2E_CHART "$REPO_ROOT/chart/kardinal-promoter"
env_set KARDINAL_E2E_HELM "$(command -v helm)"

if [ "${KARDINAL_E2E_INSTALL:-1}" = build ]; then
  env_set KARDINAL_E2E_CLI "$BIN/kardinal"
  log "no controller release and no CRDs (KARDINAL_E2E_INSTALL=build)"
  exit 0
fi
if [ "${KARDINAL_E2E_INSTALL:-1}" = 0 ]; then
  # Helm installs crds/ only when a CRD is missing, and parallel installs
  # would race for them; apply them once here.
  "${HELM[@]}" show crds "$REPO_ROOT/chart/kardinal-promoter" |
    "${KUBECTL[@]}" apply --server-side --force-conflicts -f - >/dev/null
  env_set KARDINAL_E2E_CLI "$BIN/kardinal"
  log "no controller release (KARDINAL_E2E_INSTALL=0); CRDs applied"
  exit 0
fi

args=(
  --set "image.repository=${IMAGE%:*}" --set "image.tag=${IMAGE##*:}" --set image.pullPolicy=Never
  --set logLevel=debug
  # Write gate status on every evaluation, so live tests can see each
  # evaluation in status.lastEvaluatedAt. TestChart_GateStatusHeartbeat covers
  # the default (10m).
  --set controller.gateStatusHeartbeat=0s
  --set github.secretRef.name=git-token
  --set "scm.provider=${KARDINAL_E2E_SCM_PROVIDER:?git server component must run first}"
  --set "scm.apiURL=${KARDINAL_E2E_SCM_API:-}"
  # Off by default; TestUI_UserRoles checks the opt-in (a binding to the
  # built-in view role grants the viewer rules). test/helm covers the default.
  --set rbac.userRoles.aggregateToDefaultRoles=true
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
existed=false
"${HELM[@]}" -n "$KARDINAL_NS" status "$KARDINAL_RELEASE" >/dev/null 2>&1 && existed=true
"${HELM[@]}" upgrade --install "$KARDINAL_RELEASE" "$REPO_ROOT/chart/kardinal-promoter" \
  -n "$KARDINAL_NS" --create-namespace "${args[@]}" --wait --timeout 5m >/dev/null
# A rebuilt image under the same tag needs a restart to be picked up; a new
# release's Pods already run it.
if $existed; then
  "${KUBECTL[@]}" -n "$KARDINAL_NS" rollout restart "deploy/$KARDINAL_RELEASE" >/dev/null
  "${KUBECTL[@]}" -n "$KARDINAL_NS" rollout status "deploy/$KARDINAL_RELEASE" --timeout=180s >/dev/null
fi
# A rollout is done while the old Pod still shuts down (the chart's
# shutdownDelaySeconds), and tests that look up the controller Pod expect
# only the running ones.
want=$("${KUBECTL[@]}" -n "$KARDINAL_NS" get "deploy/$KARDINAL_RELEASE" -o jsonpath='{.spec.replicas}')
waited=0
until [ "$("${KUBECTL[@]}" -n "$KARDINAL_NS" get pods -l app.kubernetes.io/name=kardinal-promoter -o name | wc -l)" -eq "$want" ]; do
  waited=$((waited + 2))
  [ "$waited" -ge 120 ] && die "the old controller Pods are still there after 120s (want $want)"
  sleep 2
done

env_set KARDINAL_E2E_CLI "$BIN/kardinal"
log "controller ready (scm.provider=$KARDINAL_E2E_SCM_PROVIDER)"
