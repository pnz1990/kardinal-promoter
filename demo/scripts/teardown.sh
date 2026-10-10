#!/usr/bin/env bash
# demo/scripts/teardown.sh
#
# Deletes the demo cluster created by setup.sh, and the kardinal-control,
# kardinal-dev and kardinal-prod clusters of the older three-cluster demo
# when they exist.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

set -euo pipefail

DEMO_CLUSTER="${DEMO_CLUSTER:-kardinal-demo}"

for arg in "$@"; do
  echo "[teardown] WARNING: unknown flag: $arg" >&2
done

echo "[teardown] Removing demo clusters..."

for cluster in "$DEMO_CLUSTER" kardinal-control kardinal-dev kardinal-prod; do
  if kind get clusters 2>/dev/null | grep "^${cluster}$" >/dev/null; then
    kind delete cluster --name "$cluster" && echo "  deleted $cluster"
    kubectl config delete-context "kind-${cluster}" 2>/dev/null || true
  fi
done

echo "[teardown] Complete."
