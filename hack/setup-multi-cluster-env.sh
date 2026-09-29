#!/usr/bin/env bash
# hack/setup-multi-cluster-env.sh
#
# Sets up a multi-cluster E2E environment for kardinal-promoter:
#
#   Cluster 1 (kind): pre-prod environments — test and uat
#   Cluster 2 (EKS):  prod-like environment — prod (the 'kardinal-e2e-prod' cluster)
#
# This enables testing the full promotion path across cluster boundaries,
# validating that kardinal's distributed mode and cross-cluster health checks
# work correctly.
#
# The EKS cluster must already exist. Create it with:
#   cd terraform/eks-e2e && terraform init && terraform apply
#
# Every kubectl/helm call carries an explicit --context. The kind part refuses
# any context that is not a local kind cluster; the original current-context is
# restored on exit (aws eks update-kubeconfig switches it).
#
# Usage:
#   ./hack/setup-multi-cluster-env.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"
# shellcheck source=hack/kind-context.sh
source "$SCRIPT_DIR/kind-context.sh"

ORIG_CTX="$(kubectl config current-context 2>/dev/null || true)"
restore_context() {
  if [ -n "$ORIG_CTX" ]; then
    kubectl config use-context "$ORIG_CTX" >/dev/null 2>&1 || true
  fi
}
trap restore_context EXIT

KIND_CLUSTER="${KIND_CLUSTER:-kardinal-e2e}"
# EKS cluster name — read from Terraform output if available, otherwise use env var or default
EKS_CLUSTER_NAME="${EKS_CLUSTER_NAME:-$(cd terraform/eks-e2e && terraform output -raw cluster_name 2>/dev/null || echo "kardinal-e2e-prod")}"
EKS_REGION="${EKS_REGION:-$(cd terraform/eks-e2e && terraform output -raw cluster_region 2>/dev/null || echo "us-east-2")}"
ARGOCD_VERSION="${ARGOCD_VERSION:-v2.10.3}"
TEST_APP_IMAGE="${TEST_APP_IMAGE:-ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f}"
KARDINAL_IMAGE_REPO="${KARDINAL_IMAGE_REPO:-ghcr.io/pnz1990/kardinal-promoter}"
KARDINAL_IMAGE_TAG="${KARDINAL_IMAGE_TAG:-dev}"

echo "=== kardinal-promoter Multi-Cluster E2E Setup ==="
echo "kind cluster:  $KIND_CLUSTER (test + uat namespaces)"
echo "EKS cluster:   $EKS_CLUSTER_NAME in $EKS_REGION (prod namespace)"
echo "  (create EKS cluster first: cd terraform/eks-e2e && terraform init && terraform apply)"
echo ""

# Verify EKS cluster is reachable before proceeding
if ! aws eks describe-cluster --name "$EKS_CLUSTER_NAME" --region "$EKS_REGION" \
    --query 'cluster.status' --output text 2>/dev/null | grep -q "ACTIVE"; then
  echo "ERROR: EKS cluster '$EKS_CLUSTER_NAME' not found or not ACTIVE in $EKS_REGION."
  echo "Create it first:"
  echo "  cd terraform/eks-e2e && terraform init && terraform apply"
  exit 1
fi

# ── Step 1: Kind cluster for pre-prod ────────────────────────────────────────
echo ""
echo "[1/6] Setting up kind cluster '$KIND_CLUSTER' (pre-prod: test + uat)..."
ensure_kind_cluster "$KIND_CLUSTER" test/e2e/kind-config.yaml
KIND_CTX="kind-${KIND_CLUSTER}"
use_kind_context "$KIND_CTX"

# ── Step 2: Install kro and kardinal-promoter on kind ────────────────────────
echo ""
echo "[2/6] Installing kro and kardinal-promoter on kind cluster..."
KUBE_CONTEXT="$KIND_CTX" bash hack/install-kro.sh
docker build -t "${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG}" "$REPO_ROOT"
kind load docker-image "${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG}" --name "$KIND_CLUSTER"
"${KUBECTL[@]}" apply -f config/crd/bases/
"${HELM[@]}" upgrade --install kardinal-promoter chart/kardinal-promoter \
  --namespace kardinal-system --create-namespace \
  --set image.repository="$KARDINAL_IMAGE_REPO" \
  --set image.tag="$KARDINAL_IMAGE_TAG" \
  --set image.pullPolicy=Never \
  --wait --timeout 180s

# ── Step 3: Install ArgoCD on kind ───────────────────────────────────────────
echo ""
echo "[3/6] Installing ArgoCD on kind cluster..."
"${KUBECTL[@]}" create namespace argocd --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" apply -n argocd \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml"
"${KUBECTL[@]}" rollout status deployment/argocd-server -n argocd --timeout=180s

# ── Step 4: Deploy test app to pre-prod namespaces on kind ───────────────────
echo ""
echo "[4/6] Deploying kardinal-test-app to test + uat on kind..."
for ENV in test uat; do
  NS="kardinal-test-app-${ENV}"
  "${KUBECTL[@]}" create namespace "$NS" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
  "${KUBECTL[@]}" create deployment kardinal-test-app \
    --image="$TEST_APP_IMAGE" --namespace="$NS" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
  echo "  $NS ready"
done

# ── Step 5: Configure EKS cluster for prod ───────────────────────────────────
echo ""
echo "[5/6] Configuring EKS cluster '$EKS_CLUSTER_NAME' for prod..."
aws eks update-kubeconfig \
  --name "$EKS_CLUSTER_NAME" \
  --region "$EKS_REGION" \
  --alias "eks-${EKS_CLUSTER_NAME}" 2>&1

EKS_KUBECTL=(kubectl --context "eks-${EKS_CLUSTER_NAME}")

# Install ArgoCD on EKS too (if not present)
if ! "${EKS_KUBECTL[@]}" get namespace argocd &>/dev/null; then
  echo "  Installing ArgoCD on EKS..."
  "${EKS_KUBECTL[@]}" create namespace argocd
  "${EKS_KUBECTL[@]}" apply -n argocd \
    -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml"
  "${EKS_KUBECTL[@]}" rollout status deployment/argocd-server -n argocd --timeout=300s
fi

# Deploy test app to prod namespace on EKS
"${EKS_KUBECTL[@]}" create namespace kardinal-test-app-prod --dry-run=client -o yaml | "${EKS_KUBECTL[@]}" apply -f -
"${EKS_KUBECTL[@]}" create deployment kardinal-test-app \
  --image="$TEST_APP_IMAGE" --namespace="kardinal-test-app-prod" --dry-run=client -o yaml | "${EKS_KUBECTL[@]}" apply -f -
echo "  kardinal-test-app-prod ready on EKS"

# ── Step 6: Apply multi-cluster Pipeline ─────────────────────────────────────
echo ""
echo "[6/6] Multi-cluster environment ready."
echo ""
echo "Contexts (your current context is left unchanged):"
echo "  Pre-prod (kind): kind-${KIND_CLUSTER}"
echo "  Prod (EKS):      eks-${EKS_CLUSTER_NAME}"
echo ""
echo "Namespaces:"
echo "  kind: kardinal-test-app-test, kardinal-test-app-uat"
echo "  EKS:  kardinal-test-app-prod"
echo ""
echo "Next steps:"
echo "  kubectl --context kind-${KIND_CLUSTER} apply -f examples/multi-cluster-fleet/pipeline.yaml"
echo "  kardinal --context kind-${KIND_CLUSTER} create bundle rollouts-demo --image ghcr.io/pnz1990/kardinal-test-app:sha-<SHA>"
