#!/usr/bin/env bash
# hack/e2e-teardown.sh
#
# Deletes the kind cluster created by hack/e2e-setup.sh.
#
# Usage:
#   ./hack/e2e-teardown.sh               # uses default cluster name
#   KIND_CLUSTER=my-cluster ./hack/e2e-teardown.sh

set -euo pipefail

KIND_CLUSTER="${KIND_CLUSTER:-kardinal-e2e}"

echo "[e2e-teardown] Deleting kind cluster: $KIND_CLUSTER"
kind delete cluster --name "$KIND_CLUSTER"
echo "[e2e-teardown] Cluster deleted."
