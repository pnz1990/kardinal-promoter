#!/usr/bin/env bash
# hack/kind-context.sh
#
# Sourced by the hack/ scripts that install into a local kind cluster.
# They must never act on whatever kube context happens to be current, so every
# kubectl/helm call goes through the KUBECTL/HELM arrays set here, which carry
# an explicit --context.
#
#   source hack/kind-context.sh
#   ensure_kind_cluster "$KIND_CLUSTER" test/e2e/kind-config.yaml
#   use_kind_context "kind-$KIND_CLUSTER"
#   "${KUBECTL[@]}" get nodes
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

# ensure_kind_cluster NAME CONFIG creates the kind cluster NAME from CONFIG
# unless it exists. CONFIG sets the node image (test/e2e/kind-config.yaml).
ensure_kind_cluster() {
  local name="$1" config="$2"
  if kind get clusters 2>/dev/null | grep -qx "$name"; then
    echo "kind cluster '$name' already exists — reusing it"
    return 0
  fi
  kind create cluster --name "$name" --config "$config" --wait 60s
}

# require_kind_context CTX exits unless CTX is a kind context whose API server
# is on the local host. It reads only the local kubeconfig.
require_kind_context() {
  local ctx="$1" cluster server
  case "$ctx" in
    kind-*) ;;
    *)
      echo "ERROR: refusing to use kube context '$ctx': not a kind-* context" >&2
      exit 1
      ;;
  esac
  cluster=$(kubectl config view -o jsonpath="{.contexts[?(@.name==\"$ctx\")].context.cluster}")
  if [ -z "$cluster" ]; then
    echo "ERROR: kube context '$ctx' not found in kubeconfig" >&2
    exit 1
  fi
  server=$(kubectl config view -o jsonpath="{.clusters[?(@.name==\"$cluster\")].cluster.server}")
  case "$server" in
    https://127.0.0.1:* | https://localhost:* | https://\[::1\]:*) ;;
    *)
      if [ "${ALLOW_NON_LOCAL_KIND:-0}" != "1" ]; then
        echo "ERROR: refusing to use kube context '$ctx': API server '$server' is not local." >&2
        echo "       Set ALLOW_NON_LOCAL_KIND=1 if this really is a kind cluster." >&2
        exit 1
      fi
      ;;
  esac
}

# use_kind_context CTX checks CTX and sets KUBECTL and HELM to target it.
use_kind_context() {
  require_kind_context "$1"
  KUBECTL=(kubectl --context "$1")
  HELM=(helm --kube-context "$1")
  echo "Target kube context: $1"
}
