#!/usr/bin/env bash
# demo/scripts/teardown.sh
#
# Tears down all demo clusters created by setup.sh.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

set -euo pipefail

CONTROL_CLUSTER="${CONTROL_CLUSTER:-kardinal-control}"
DEV_CLUSTER="${DEV_CLUSTER:-kardinal-dev}"
PROD_CLUSTER="${PROD_CLUSTER:-kardinal-prod}"

for arg in "$@"; do
  case $arg in
    --eks)
      echo "[teardown] WARNING: --eks was removed; this script deletes only the kind clusters." >&2
      echo "[teardown] WARNING: destroy an EKS cluster created with --eks or make eks-up from a checkout that still has terraform/eks-e2e/ (see demo/README.md)." >&2
      ;;
    *) echo "[teardown] WARNING: unknown flag: $arg" >&2 ;;
  esac
done

echo "[teardown] Removing demo clusters..."

for cluster in "$CONTROL_CLUSTER" "$DEV_CLUSTER" "$PROD_CLUSTER"; do
  if kind get clusters 2>/dev/null | grep "^${cluster}$" >/dev/null; then
    kind delete cluster --name "$cluster" && echo "  deleted $cluster"
  else
    echo "  $cluster not found — skipping"
  fi
done

echo "[teardown] Done. Pruning stale kubeconfig contexts..."
for ctx in "kind-${CONTROL_CLUSTER}" "kind-${DEV_CLUSTER}" "kind-${PROD_CLUSTER}"; do
  kubectl config delete-context "$ctx" 2>/dev/null || true
done

echo "[teardown] Complete."
