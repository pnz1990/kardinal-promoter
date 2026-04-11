#!/usr/bin/env bash
# hack/e2e-setup.sh
#
# Creates a kind cluster, installs krocodile and kardinal-promoter, and
# applies the quickstart examples. Run once before executing e2e tests.
#
# Usage:
#   ./hack/e2e-setup.sh               # uses default kind cluster name
#   KIND_CLUSTER=my-cluster ./hack/e2e-setup.sh
#
# After setup, run:
#   make test-e2e-journey-1    # run J1 quickstart
#   make test-e2e-journey-3    # run J3 policy governance
#   make test-e2e             # run all journeys

set -euo pipefail

KIND_CLUSTER="${KIND_CLUSTER:-kardinal-e2e}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "[e2e-setup] Setting up kind cluster: $KIND_CLUSTER"

# ── 1. Create kind cluster ─────────────────────────────────────────────────────
echo "[e2e-setup] Creating kind cluster..."
kind create cluster \
  --name "$KIND_CLUSTER" \
  --config "$REPO_ROOT/test/e2e/kind-config.yaml" \
  --wait 60s
kubectl config use-context "kind-$KIND_CLUSTER"
echo "[e2e-setup] Kind cluster created."

# ── 2. Install krocodile Graph controller ────────────────────────────────────
echo "[e2e-setup] Installing krocodile..."
KIND_CLUSTER="$KIND_CLUSTER" bash "$SCRIPT_DIR/install-krocodile.sh"

# ── 3. Build and load kardinal-promoter image ────────────────────────────────
echo "[e2e-setup] Building kardinal-promoter image..."
(cd "$REPO_ROOT" && make docker-build IMG=ghcr.io/pnz1990/kardinal-promoter:dev)
kind load docker-image ghcr.io/pnz1990/kardinal-promoter:dev --name "$KIND_CLUSTER"

# ── 4. Install CRDs and controller ───────────────────────────────────────────
echo "[e2e-setup] Installing kardinal-promoter CRDs and controller..."
(cd "$REPO_ROOT" && make install)
kubectl apply -f "$REPO_ROOT/config/manager/"

# ── 5. Wait for controller to be ready ───────────────────────────────────────
echo "[e2e-setup] Waiting for kardinal-controller to be ready..."
kubectl rollout status deployment/kardinal-controller -n kardinal-system --timeout=60s

# ── 6. Apply quickstart examples ─────────────────────────────────────────────
echo "[e2e-setup] Applying quickstart examples..."
kubectl apply -f "$REPO_ROOT/examples/quickstart/pipeline.yaml"
kubectl apply -f "$REPO_ROOT/examples/quickstart/policy-gates.yaml"

echo "[e2e-setup] Setup complete!"
echo "[e2e-setup] Run: make test-e2e-journey-1"
