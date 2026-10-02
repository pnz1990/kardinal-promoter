#!/usr/bin/env bash
# hack/e2e/tools.sh
#
# Installs the kind, kubectl and helm of hack/tool-versions.env into bin/e2e,
# each download checked against the sha256 there; a tool already there at
# its version is kept. lib.sh puts bin/e2e first on PATH, so the e2e scripts
# and the tests run.sh starts use the tools CI does (the e2e-live workflow
# runs this script too). up.sh and all.sh run it; make e2e-tools does too,
# and then the chart tests in test/helm use bin/e2e/helm.
#
# The sums are linux-amd64's. On another platform, or with
# KARDINAL_E2E_TOOLS=path, nothing is installed, lib.sh leaves PATH alone,
# and this script only warns about a tool on PATH at another version.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=hack/tool-versions.env
source "$REPO_ROOT/hack/tool-versions.env"
BIN="$REPO_ROOT/bin/e2e"

log() { printf '[e2e %s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

# version TOOL prints the version of TOOL (a path or a name on PATH), or
# nothing when it is missing or does not run.
version() {
  case "${1##*/}" in
    kind) "$1" version -q 2>/dev/null | sed 's/^/v/' ;;
    kubectl) "$1" version --client -o json 2>/dev/null | sed -n 's/.*"gitVersion": "\(.*\)".*/\1/p' ;;
    helm) "$1" version --template '{{.Version}}' 2>/dev/null ;;
  esac || true
}

case "${KARDINAL_E2E_TOOLS:-pinned}" in
  pinned | path) ;;
  *)
    log "ERROR: KARDINAL_E2E_TOOLS=$KARDINAL_E2E_TOOLS: want pinned or path"
    exit 1
    ;;
esac
if [ "${KARDINAL_E2E_TOOLS:-pinned}" = path ] || [ "$(uname -s)/$(uname -m)" != Linux/x86_64 ]; then
  for t in kind:$KIND_VERSION kubectl:$KUBECTL_VERSION helm:$HELM_VERSION; do
    have=$(version "${t%%:*}")
    [ "$have" = "${t#*:}" ] || log "WARNING: ${t%%:*} on PATH is ${have:-missing}, CI runs ${t#*:} (hack/tool-versions.env)"
  done
  exit 0
fi

mkdir -p "$BIN"
tmp=$(mktemp -d "$BIN/.download.XXXXXX")
trap 'rm -r "$tmp"' EXIT
# Each tool is checked, then moved into place in one rename, so a run that
# starts while another downloads never finds half a binary.
if [ "$(version "$BIN/kind")" != "$KIND_VERSION" ]; then
  curl -fsSLo "$tmp/kind" "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64"
  echo "${KIND_SHA256}  $tmp/kind" | sha256sum -c --quiet -
  chmod +x "$tmp/kind" && mv -f "$tmp/kind" "$BIN/kind"
  log "installed $BIN/kind $KIND_VERSION"
fi
if [ "$(version "$BIN/kubectl")" != "$KUBECTL_VERSION" ]; then
  curl -fsSLo "$tmp/kubectl" "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl"
  echo "${KUBECTL_SHA256}  $tmp/kubectl" | sha256sum -c --quiet -
  chmod +x "$tmp/kubectl" && mv -f "$tmp/kubectl" "$BIN/kubectl"
  log "installed $BIN/kubectl $KUBECTL_VERSION"
fi
if [ "$(version "$BIN/helm")" != "$HELM_VERSION" ]; then
  curl -fsSLo "$tmp/helm.tar.gz" "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz"
  echo "${HELM_SHA256}  $tmp/helm.tar.gz" | sha256sum -c --quiet -
  tar -xzf "$tmp/helm.tar.gz" -C "$tmp" --strip-components=1 linux-amd64/helm
  mv -f "$tmp/helm" "$BIN/helm"
  log "installed $BIN/helm $HELM_VERSION"
fi
