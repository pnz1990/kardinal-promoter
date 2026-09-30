#!/usr/bin/env bash
# hack/e2e/up.sh SUITE
#
# Creates the kind cluster kardinal-e2e-SUITE and installs what SUITE's live
# tests need: kro, the suite's git server and GitOps engine, and the
# controller and CLI built from this checkout. Writes
# test/e2e/results/<cluster>/env, which `make test-e2e-live` sources.
# Re-running reuses the cluster and re-applies every component.
#
# Suites (test/e2e/README.md lists the tests each one runs):
#   core    Forgejo + Argo CD
#   gitea   Gitea + Argo CD
#
# Env:
#   KIND_CLUSTER     cluster name (default kardinal-e2e-SUITE)
#   KIND_K8S         Kubernetes minor, e.g. 1.37: boots KIND_NODE_1_37 from
#                    hack/tool-versions.env (default: the node image in
#                    test/e2e/kind-config.yaml)
#   KUBECONFIG       honoured; recorded in the env file
#   plus the KARDINAL_E2E_* build settings of components/kardinal.sh
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

SUITE=${1:?usage: $0 SUITE}
case "$SUITE" in
  # RUN is the go test -run pattern of the suite's tests; the prefix names
  # the area (test/e2e/README.md). Every git server suite runs the TestCore_
  # promotion tests against its server.
  core) COMPONENTS=("giteafamily.sh forgejo" argocd.sh)
    RUN='^Test(Core|Gate|Bundle|Pipeline|Graph|Step|Rollback|Health|CLI|CIAPI|Notify|Sub|Audit)_' ;;
  gitea) COMPONENTS=("giteafamily.sh gitea" argocd.sh) RUN='^Test(Core|SCM)_' ;;
  *)
    echo "unknown suite $SUITE" >&2
    exit 1
    ;;
esac
export KIND_CLUSTER=${KIND_CLUSTER:-kardinal-e2e-$SUITE}
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
export E2E_OUT

NODE_IMAGE=
if [ -n "${KIND_K8S:-}" ]; then
  var="KIND_NODE_${KIND_K8S//./_}"
  # shellcheck disable=SC1091
  NODE_IMAGE=$(source "$REPO_ROOT/hack/tool-versions.env" && echo "${!var:-}")
  [ -n "$NODE_IMAGE" ] || die "KIND_K8S=$KIND_K8S: no $var in hack/tool-versions.env"
fi

start=$(date +%s)
if ! kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER"; then
  kind create cluster --name "$KIND_CLUSTER" --config "$REPO_ROOT/test/e2e/kind-config.yaml" \
    ${NODE_IMAGE:+--image "$NODE_IMAGE"} --wait 120s
fi
target_cluster
if [ -n "$NODE_IMAGE" ]; then
  have=$("${KUBECTL[@]}" version -o json | python3 -c 'import json,sys; v=json.load(sys.stdin)["serverVersion"]; print(v["major"]+"."+v["minor"].rstrip("+"))')
  [ "$have" = "$KIND_K8S" ] || die "$KIND_CLUSTER runs Kubernetes $have, not $KIND_K8S; make e2e-down SUITE=$SUITE first"
fi
mkdir -p "$E2E_OUT"
rm -f "$E2E_OUT/env"
env_set KARDINAL_E2E_SUITE "$SUITE"
env_set KARDINAL_E2E_CONTEXT "$CTX"
env_set KARDINAL_E2E_RUN "$RUN"
env_set KARDINAL_E2E_ARTIFACTS "$E2E_OUT/diagnostics"
[ -n "${KUBECONFIG:-}" ] && env_set KUBECONFIG "$KUBECONFIG"

KUBE_CONTEXT="$CTX" bash "$REPO_ROOT/hack/install-kro.sh" >/dev/null
log "kro ready"
for c in "${COMPONENTS[@]}"; do
  # shellcheck disable=SC2086
  bash "$E2E_DIR/components/"$c
done
bash "$E2E_DIR/components/kardinal.sh"
log "suite $SUITE up on $CTX in $(($(date +%s) - start))s; env: $E2E_OUT/env"
