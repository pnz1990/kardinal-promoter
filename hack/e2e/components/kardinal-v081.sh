#!/usr/bin/env bash
# hack/e2e/components/kardinal-v081.sh
#
# Installs kardinal-promoter v0.8.1 as released, for the upgrade suite: its
# CRDs from the v0.8.1 tag, then its chart (the controller in kardinal-system
# and the bundled krocodile Graph controller in kro-system), wired to the
# suite's git server, and downloads the v0.8.1 CLI. The suite's test upgrades
# the release to this checkout, so a cluster serves one run: once upgraded,
# this script fails until the cluster is deleted.
#
# v0.8.1 needs four settings its chart doesn't make:
#   - its CRDs, applied first: the chart ships none, and its ScheduleClock
#     object fails to install without them;
#   - validatingAdmissionPolicy.enabled=false: its Pipeline policy requires an
#     environment field gitRepo that the Pipeline CRD doesn't have, so it
#     rejects every Pipeline;
#   - runAsUser 65532 on krocodile: its image's user is the name "nonroot",
#     which the pod's runAsNonRoot refuses;
#   - KARDINAL_SCM_PROVIDER and KARDINAL_SCM_API_URL on the controller: the
#     chart has no SCM settings (it only knows GitHub).
#
# Env set for the tests:
#   KARDINAL_E2E_V081_CLI          the v0.8.1 kardinal CLI
#   KARDINAL_E2E_PROMETHEUS_IMAGE  PROMETHEUS_IMAGE, pulled into the node
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster
# shellcheck disable=SC1091
source "$E2E_OUT/env"

if "${KUBECTL[@]}" get crd graphs.kro.run >/dev/null 2>&1; then
  die "$CTX was upgraded past v0.8.1 (CRD graphs.kro.run exists); delete it first: kind delete cluster --name $KIND_CLUSTER"
fi
chart=$("${HELM[@]}" -n "$KARDINAL_NS" list --filter "^$KARDINAL_RELEASE\$" -o json | python3 -c 'import json,sys; r=json.load(sys.stdin); print(r[0]["chart"] if r else "")')
[ -z "$chart" ] || [ "$chart" = "kardinal-promoter-$V081_CHART_VERSION" ] ||
  die "$CTX runs $chart, not v$V081_CHART_VERSION; delete it first: kind delete cluster --name $KIND_CLUSTER"

pull_images "$V081_CONTROLLER_IMAGE" "$V081_KROCODILE_IMAGE" "$PROMETHEUS_IMAGE"
env_set KARDINAL_E2E_PROMETHEUS_IMAGE "$PROMETHEUS_IMAGE"

CRDS="$E2E_OUT/v081-crds"
mkdir -p "$CRDS"
for r in auditevents bundles changewindows metricchecks pipelines policygates promotionsteps prstatuses \
  rollbackpolicies scheduleclocks subscriptions; do
  f="$CRDS/kardinal.io_$r.yaml"
  [ -s "$f" ] || curl -fsSL --retry 5 -o "$f" "$V081_CRD_BASE/kardinal.io_$r.yaml"
done
# Client-side, as a v0.8.1 user applied them from config/crd/bases.
"${KUBECTL[@]}" apply -f "$CRDS" >/dev/null

"${HELM[@]}" upgrade --install "$KARDINAL_RELEASE" "$V081_CHART" --version "$V081_CHART_VERSION" \
  -n "$KARDINAL_NS" --create-namespace \
  --set github.secretRef.name=git-token \
  --set validatingAdmissionPolicy.enabled=false >/dev/null
"${KUBECTL[@]}" -n kro-system patch deployment graph-controller --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/securityContext/runAsUser","value":65532}]' >/dev/null
"${KUBECTL[@]}" -n "$KARDINAL_NS" set env "deploy/$KARDINAL_RELEASE" \
  "KARDINAL_SCM_PROVIDER=${KARDINAL_E2E_SCM_PROVIDER:?git server component must run first}" \
  "KARDINAL_SCM_API_URL=${KARDINAL_E2E_SCM_API:-}" >/dev/null
"${KUBECTL[@]}" -n kro-system rollout status deploy/graph-controller --timeout=180s >/dev/null
"${KUBECTL[@]}" -n "$KARDINAL_NS" rollout status "deploy/$KARDINAL_RELEASE" --timeout=180s >/dev/null

CLI="$E2E_OUT/bin/kardinal-v$V081_CHART_VERSION"
mkdir -p "$E2E_OUT/bin"
if ! echo "$V081_CLI_SHA256  $CLI" | sha256sum -c --status 2>/dev/null; then
  curl -fsSL --retry 5 -o "$CLI.tmp" "$V081_CLI_URL"
  echo "$V081_CLI_SHA256  $CLI.tmp" | sha256sum -c --status || die "$V081_CLI_URL: sha256 mismatch"
  chmod 0755 "$CLI.tmp" && mv "$CLI.tmp" "$CLI"
fi
env_set KARDINAL_E2E_V081_CLI "$CLI"
log "kardinal-promoter v$V081_CHART_VERSION ready (scm $KARDINAL_E2E_SCM_PROVIDER, CLI $CLI)"
