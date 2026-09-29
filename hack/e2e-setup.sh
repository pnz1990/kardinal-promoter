#!/usr/bin/env bash
# hack/e2e-setup.sh
#
# Creates a kind cluster, installs kro (Graph controller) and kardinal-promoter
# built from this checkout, and applies the quickstart examples. Run once
# before executing the cluster e2e tests (go test -tags e2e ./test/e2e/...).
#
# Every kubectl/helm call targets kind-$KIND_CLUSTER explicitly; the current
# kube context is never used.
#
# Usage:
#   ./hack/e2e-setup.sh               # uses default kind cluster name
#   KIND_CLUSTER=my-cluster ./hack/e2e-setup.sh
#
# After setup, run:
#   make test-e2e-kind         # cluster tests against kind-$KIND_CLUSTER

set -euo pipefail

KIND_CLUSTER="${KIND_CLUSTER:-kardinal-e2e}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
IMG_REPO="${KARDINAL_IMAGE_REPO:-ghcr.io/pnz1990/kardinal-promoter}"
IMG_TAG="${KARDINAL_IMAGE_TAG:-dev}"
# shellcheck source=hack/kind-context.sh
source "$SCRIPT_DIR/kind-context.sh"

echo "[e2e-setup] Setting up kind cluster: $KIND_CLUSTER"

# ── 1. Create kind cluster ─────────────────────────────────────────────────────
ensure_kind_cluster "$KIND_CLUSTER" "$REPO_ROOT/test/e2e/kind-config.yaml"
CTX="kind-$KIND_CLUSTER"
use_kind_context "$CTX"

# ── 2. Install the kro Graph controller ──────────────────────────────────────
echo "[e2e-setup] Installing kro..."
KUBE_CONTEXT="$CTX" bash "$SCRIPT_DIR/install-kro.sh"

# ── 3. Build and load kardinal-promoter image ────────────────────────────────
echo "[e2e-setup] Building kardinal-promoter image..."
docker build -t "${IMG_REPO}:${IMG_TAG}" "$REPO_ROOT"
kind load docker-image "${IMG_REPO}:${IMG_TAG}" --name "$KIND_CLUSTER"

# ── 4. Install CRDs and controller ───────────────────────────────────────────
echo "[e2e-setup] Installing kardinal-promoter CRDs and controller..."
"${KUBECTL[@]}" apply -f "$REPO_ROOT/config/crd/bases/"
"${HELM[@]}" upgrade --install kardinal-promoter "$REPO_ROOT/chart/kardinal-promoter" \
  --namespace kardinal-system --create-namespace \
  --set image.repository="$IMG_REPO" \
  --set image.tag="$IMG_TAG" \
  --set image.pullPolicy=Never \
  --set validatingAdmissionPolicy.enabled=false

# ── 5. Wait for controller to be ready ───────────────────────────────────────
echo "[e2e-setup] Waiting for the controller to be ready..."
"${KUBECTL[@]}" rollout status deployment/kardinal-promoter -n kardinal-system --timeout=120s

# ── 6. Apply quickstart examples ─────────────────────────────────────────────
echo "[e2e-setup] Applying quickstart examples..."
"${KUBECTL[@]}" apply -f "$REPO_ROOT/examples/quickstart/pipeline.yaml"
"${KUBECTL[@]}" apply -f "$REPO_ROOT/examples/quickstart/policy-gates.yaml"

echo "[e2e-setup] Setup complete!"
echo "[e2e-setup] Run: make test-e2e-kind KIND_CLUSTER=$KIND_CLUSTER"
