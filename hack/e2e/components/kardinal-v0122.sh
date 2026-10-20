#!/usr/bin/env bash
# hack/e2e/components/kardinal-v0122.sh
#
# Installs kardinal-promoter v0.12.2 as released, for the upgrade-v0122 suite:
# kro at the version v0.12.2 targets (hack/install-kro.sh with
# KRO_VERSION=$V0122_KRO_VERSION), then the v0.12.2 chart from the registry
# (it ships its CRDs) with its controller image, wired to the suite's git
# server the way v0.12.2's own e2e setup wired it, and downloads the v0.12.2
# CLI. The suite's test upgrades the release to this checkout, so a cluster
# serves one run: once upgraded, this script fails until the cluster is
# deleted.
#
# Env set for the tests:
#   KARDINAL_E2E_V0122_CLI  the v0.12.2 kardinal CLI
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster
# shellcheck disable=SC1091
source "$E2E_OUT/env"

chart=$("${HELM[@]}" -n "$KARDINAL_NS" list --filter "^$KARDINAL_RELEASE\$" -o json | python3 -c 'import json,sys; r=json.load(sys.stdin); print(r[0]["chart"] if r else "")')
[ -z "$chart" ] || [ "$chart" = "kardinal-promoter-$V0122_CHART_VERSION" ] ||
  die "$CTX runs $chart, not v$V0122_CHART_VERSION; delete it first: kind delete cluster --name $KIND_CLUSTER"

KUBE_CONTEXT="$CTX" KRO_VERSION="$V0122_KRO_VERSION" bash "$REPO_ROOT/hack/install-kro.sh" >/dev/null
pull_images "$V0122_CONTROLLER_IMAGE"
# The tag is pinned: the pulled image must be the released index digest.
digests=$(docker exec "$KIND_CLUSTER-control-plane" crictl inspecti -o json "$V0122_CONTROLLER_IMAGE" |
  python3 -c 'import json,sys; print(" ".join(json.load(sys.stdin)["status"].get("repoDigests", [])))')
case " $digests " in
  *"@$V0122_CONTROLLER_DIGEST "*) ;;
  *) die "$V0122_CONTROLLER_IMAGE is $digests, not the released $V0122_CONTROLLER_DIGEST" ;;
esac

args=(
  --set "image.repository=${V0122_CONTROLLER_IMAGE%:*}" --set "image.tag=${V0122_CONTROLLER_IMAGE##*:}"
  --set image.pullPolicy=IfNotPresent
  --set logLevel=debug
  --set github.secretRef.name=git-token
  --set "scm.provider=${KARDINAL_E2E_SCM_PROVIDER:?git server component must run first}"
  --set "scm.apiURL=${KARDINAL_E2E_SCM_API:-}"
)
if "${KUBECTL[@]}" -n "$KARDINAL_NS" get secret scm-webhook >/dev/null 2>&1; then
  args+=(--set webhook.secretRef.name=scm-webhook)
fi
"${HELM[@]}" upgrade --install "$KARDINAL_RELEASE" "$V0122_CHART" --version "$V0122_CHART_VERSION" \
  -n "$KARDINAL_NS" --create-namespace "${args[@]}" >/dev/null
"${KUBECTL[@]}" -n "$KARDINAL_NS" rollout status "deploy/$KARDINAL_RELEASE" --timeout=180s >/dev/null

CLI="$E2E_OUT/bin/kardinal-v$V0122_CHART_VERSION"
mkdir -p "$E2E_OUT/bin"
if ! echo "$V0122_CLI_SHA256  $CLI" | sha256sum -c --status 2>/dev/null; then
  curl -fsSL --retry 5 -o "$CLI.tmp" "$V0122_CLI_URL"
  echo "$V0122_CLI_SHA256  $CLI.tmp" | sha256sum -c --status || die "$V0122_CLI_URL: sha256 mismatch"
  chmod 0755 "$CLI.tmp" && mv "$CLI.tmp" "$CLI"
fi
env_set KARDINAL_E2E_V0122_CLI "$CLI"
log "kardinal-promoter v$V0122_CHART_VERSION ready (scm $KARDINAL_E2E_SCM_PROVIDER, CLI $CLI)"
