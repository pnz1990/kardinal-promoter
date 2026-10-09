#!/usr/bin/env bash
# hack/e2e/components/shard.sh
#
# The shard suite's second controller: release kardinal-shard-b in namespace
# kardinal-shard-b (labelled kardinal.io/shard=b), the image and settings of
# the main release with controller.namespaceShard=b. The main release runs as
# shard "default" (up.sh HELM_ARGS). Run after components/kardinal.sh.
# Idempotent.
#
# Env file:
#   KARDINAL_E2E_SHARD_B_NS       the shard b release namespace
#   KARDINAL_E2E_SHARD_B_RELEASE  its release name
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster
# shellcheck disable=SC1091
source "$E2E_OUT/env"

NS=kardinal-shard-b
RELEASE=kardinal-shard-b
IMAGE=${KARDINAL_E2E_IMAGE:?components/kardinal.sh must run first}

"${KUBECTL[@]}" create namespace "$NS" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
"${KUBECTL[@]}" label namespace "$NS" kardinal.io/shard=b --overwrite >/dev/null
# The git token Secret the chart reads (github.secretRef), copied from the
# main release's namespace.
"${KUBECTL[@]}" -n "$KARDINAL_NS" get secret git-token -o jsonpath='{.data.token}' | base64 -d >"$E2E_OUT/.shard-token"
"${KUBECTL[@]}" -n "$NS" create secret generic git-token --from-file=token="$E2E_OUT/.shard-token" \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
rm -f "$E2E_OUT/.shard-token"

existed=false
"${HELM[@]}" -n "$NS" status "$RELEASE" >/dev/null 2>&1 && existed=true
"${HELM[@]}" upgrade --install "$RELEASE" "$REPO_ROOT/chart/kardinal-promoter" -n "$NS" \
  --set "image.repository=${IMAGE%:*}" --set "image.tag=${IMAGE##*:}" --set image.pullPolicy=Never \
  --set logLevel=debug --set github.secretRef.name=git-token \
  --set "scm.provider=${KARDINAL_E2E_SCM_PROVIDER:?}" --set "scm.apiURL=${KARDINAL_E2E_SCM_API:-}" \
  --set controller.namespaceShard=b --skip-crds --wait --timeout 5m >/dev/null
# The Deployment is named <release>-kardinal-promoter.
DEPLOY=$("${KUBECTL[@]}" -n "$NS" get deploy -l "app.kubernetes.io/instance=$RELEASE" -o name)
[ -n "$DEPLOY" ] || die "no Deployment of release $RELEASE in $NS"
if $existed; then
  "${KUBECTL[@]}" -n "$NS" rollout restart "$DEPLOY" >/dev/null
  "${KUBECTL[@]}" -n "$NS" rollout status "$DEPLOY" --timeout=180s >/dev/null
fi
env_set KARDINAL_E2E_SHARD_B_NS "$NS"
env_set KARDINAL_E2E_SHARD_B_RELEASE "$RELEASE"
log "shard b controller ready ($RELEASE in $NS); the main release is shard default"
