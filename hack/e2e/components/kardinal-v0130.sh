#!/usr/bin/env bash
# hack/e2e/components/kardinal-v0130.sh
#
# Installs kardinal-promoter v0.13.0 as released, for the upgrade-v0130
# suite: kro at the version v0.13.0 targets (hack/install-kro.sh with
# KRO_VERSION=$V0130_KRO_VERSION), then the v0.13.0 chart from the registry
# (it ships its CRDs) with its controller image, wired to the suite's git
# server, with receipts signed by a static key and pushed to the suite's
# registry (lib.sh receipt_helm_args) and the fake ticketing API's CA
# (components/ticketapi.sh), and downloads the v0.13.0 CLI. Run it after
# registry.sh and ticketapi.sh. The suite's test upgrades the release to this
# checkout and rolls it back, so a cluster serves one run: once upgraded,
# this script fails until the cluster is deleted.
#
# Until v0.13.0 is released its pins in hack/e2e/versions.env are empty: this
# script then installs nothing and writes why to the env file
# (KARDINAL_E2E_V0130_UNRELEASED), and the suite's test skips with that
# reason. Once the release is published it stops until the pins are set
# (hack/release/pin-upgrade.sh 0.13.0 prints them): nothing installs v0.13.0
# unpinned.
#
# Env set for the tests:
#   KARDINAL_E2E_V0130_CLI         the v0.13.0 kardinal CLI
#   KARDINAL_E2E_V0130_UNRELEASED  why nothing was installed, while v0.13.0
#                                  is not released
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster
# shellcheck disable=SC1091
source "$E2E_OUT/env"

if [ -z "$V0130_CHART_DIGEST" ] || [ -z "$V0130_CONTROLLER_DIGEST" ] || [ -z "$V0130_CLI_SHA256" ]; then
  # The release's CLI asset is the last thing its workflow publishes.
  code=$(curl -sS -o /dev/null -w '%{http_code}' -IL --retry 3 "$V0130_CLI_URL" || true)
  case $code in
    404)
      why="kardinal-promoter v$V0130_CHART_VERSION is not released yet ($V0130_CLI_URL: HTTP 404), so there is nothing to upgrade from; once it is, set its pins in hack/e2e/versions.env (hack/release/pin-upgrade.sh $V0130_CHART_VERSION)"
      env_set KARDINAL_E2E_V0130_UNRELEASED "$why"
      log "$why"
      exit 0
      ;;
    200)
      die "kardinal-promoter v$V0130_CHART_VERSION is released: set V0130_CHART_DIGEST, V0130_CONTROLLER_DIGEST and V0130_CLI_SHA256 in hack/e2e/versions.env to what hack/release/pin-upgrade.sh $V0130_CHART_VERSION prints"
      ;;
    *) die "can't tell whether kardinal-promoter v$V0130_CHART_VERSION is released: $V0130_CLI_URL answered HTTP $code" ;;
  esac
fi
env_set KARDINAL_E2E_V0130_UNRELEASED ""

chart=$("${HELM[@]}" -n "$KARDINAL_NS" list --filter "^$KARDINAL_RELEASE\$" -o json | python3 -c 'import json,sys; r=json.load(sys.stdin); print(r[0]["chart"] if r else "")')
[ -z "$chart" ] || [ "$chart" = "kardinal-promoter-$V0130_CHART_VERSION" ] ||
  die "$CTX runs $chart, not v$V0130_CHART_VERSION; delete it first: kind delete cluster --name $KIND_CLUSTER"

KUBE_CONTEXT="$CTX" KRO_VERSION="$V0130_KRO_VERSION" bash "$REPO_ROOT/hack/install-kro.sh" >/dev/null
pull_images "$V0130_CONTROLLER_IMAGE"
# The tag is pinned: the pulled image must be the released index digest.
digests=$(docker exec "$KIND_CLUSTER-control-plane" crictl inspecti -o json "$V0130_CONTROLLER_IMAGE" |
  python3 -c 'import json,sys; print(" ".join(json.load(sys.stdin)["status"].get("repoDigests", [])))')
case " $digests " in
  *"@$V0130_CONTROLLER_DIGEST "*) ;;
  *) die "$V0130_CONTROLLER_IMAGE is $digests, not the released $V0130_CONTROLLER_DIGEST" ;;
esac

args=(
  --set "image.repository=${V0130_CONTROLLER_IMAGE%:*}" --set "image.tag=${V0130_CONTROLLER_IMAGE##*:}"
  --set image.pullPolicy=IfNotPresent
  --set logLevel=debug
  --set github.secretRef.name=git-token
  --set "scm.provider=${KARDINAL_E2E_SCM_PROVIDER:?git server component must run first}"
  --set "scm.apiURL=${KARDINAL_E2E_SCM_API:-}"
)
if "${KUBECTL[@]}" -n "$KARDINAL_NS" get secret scm-webhook >/dev/null 2>&1; then
  args+=(--set webhook.secretRef.name=scm-webhook)
fi
# v0.13.0's signed receipts, as the receipts suite has them: a static key,
# referrers in the suite's registry.
receipt_helm_args "$KARDINAL_NS" receipt-key
args+=("${RECEIPT_ARGS[@]}")
"${KUBECTL[@]}" -n "$KARDINAL_NS" get configmap change-request-ca >/dev/null 2>&1 ||
  die "no ConfigMap $KARDINAL_NS/change-request-ca: run components/ticketapi.sh first"
args+=(--set changeRequest.caBundle.configMap=change-request-ca)
"${HELM[@]}" upgrade --install "$KARDINAL_RELEASE" "$V0130_CHART:$V0130_CHART_VERSION@$V0130_CHART_DIGEST" \
  -n "$KARDINAL_NS" --create-namespace "${args[@]}" >/dev/null
"${KUBECTL[@]}" -n "$KARDINAL_NS" rollout status "deploy/$KARDINAL_RELEASE" --timeout=180s >/dev/null

CLI="$E2E_OUT/bin/kardinal-v$V0130_CHART_VERSION"
mkdir -p "$E2E_OUT/bin"
if ! echo "$V0130_CLI_SHA256  $CLI" | sha256sum -c --status 2>/dev/null; then
  curl -fsSL --retry 5 -o "$CLI.tmp" "$V0130_CLI_URL"
  echo "$V0130_CLI_SHA256  $CLI.tmp" | sha256sum -c --status || die "$V0130_CLI_URL: sha256 mismatch"
  chmod 0755 "$CLI.tmp" && mv "$CLI.tmp" "$CLI"
fi
env_set KARDINAL_E2E_V0130_CLI "$CLI"
log "kardinal-promoter v$V0130_CHART_VERSION ready (scm $KARDINAL_E2E_SCM_PROVIDER, receipts signed, CLI $CLI)"
