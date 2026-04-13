#!/usr/bin/env bash
# hack/setup-multi-cluster-env.sh
#
# Sets up a multi-cluster E2E environment for kardinal-promoter:
#
#   Cluster 1 (kind): pre-prod environments — test and uat
#   Cluster 2 (EKS):  prod-like environment — prod (the 'krombat' cluster)
#
# This enables testing the full promotion path across cluster boundaries,
# validating that kardinal's distributed mode and cross-cluster health checks
# work correctly.
#
# The EKS cluster must already exist. Create it with:
#   cd terraform/krombat && terraform init && terraform apply
#
# Usage:
#   ./hack/setup-multi-cluster-env.sh

set -euo pipefail

KIND_CLUSTER="${KIND_CLUSTER:-kardinal-e2e}"
# EKS cluster name — read from Terraform output if available, otherwise use env var or default
EKS_CLUSTER_NAME="${EKS_CLUSTER_NAME:-$(cd terraform/krombat && terraform output -raw cluster_name 2>/dev/null || echo "kardinal-e2e-prod")}"
EKS_REGION="${EKS_REGION:-$(cd terraform/krombat && terraform output -raw cluster_region 2>/dev/null || echo "us-west-2")}"
KROCODILE_COMMIT="${KROCODILE_COMMIT:-501ea75f}"
ARGOCD_VERSION="${ARGOCD_VERSION:-v2.10.3}"
TEST_APP_IMAGE="${TEST_APP_IMAGE:-ghcr.io/pnz1990/kardinal-test-app:latest}"

echo "=== kardinal-promoter Multi-Cluster E2E Setup ==="
echo "kind cluster:  $KIND_CLUSTER (test + uat namespaces)"
echo "EKS cluster:   $EKS_CLUSTER_NAME in $EKS_REGION (prod namespace)"
echo "  (create EKS cluster first: cd terraform/krombat && terraform init && terraform apply)"
echo ""

# Verify EKS cluster is reachable before proceeding
if ! aws eks describe-cluster --name "$EKS_CLUSTER_NAME" --region "$EKS_REGION" \
    --query 'cluster.status' --output text 2>/dev/null | grep -q "ACTIVE"; then
  echo "ERROR: EKS cluster '$EKS_CLUSTER_NAME' not found or not ACTIVE in $EKS_REGION."
  echo "Create it first:"
  echo "  cd terraform/krombat && terraform init && terraform apply"
  exit 1
fi

# ── Step 1: Kind cluster for pre-prod ────────────────────────────────────────
echo ""
echo "[1/6] Setting up kind cluster '$KIND_CLUSTER' (pre-prod: test + uat)..."
if ! kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER}$"; then
  kind create cluster --name "$KIND_CLUSTER" --config test/e2e/kind-config.yaml
fi
kubectl config use-context "kind-${KIND_CLUSTER}"

# ── Step 2: Install krocodile on kind ────────────────────────────────────────
echo ""
echo "[2/6] Installing krocodile on kind cluster..."
KROCODILE_COMMIT=$KROCODILE_COMMIT KIND_CLUSTER=$KIND_CLUSTER bash hack/install-krocodile.sh

# ── Step 3: Install ArgoCD on kind ───────────────────────────────────────────
echo ""
echo "[3/6] Installing ArgoCD on kind cluster..."
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argocd \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml"
kubectl rollout status deployment/argocd-server -n argocd --timeout=180s

# ── Step 4: Deploy test app to pre-prod namespaces on kind ───────────────────
echo ""
echo "[4/6] Deploying kardinal-test-app to test + uat on kind..."
for ENV in test uat; do
  NS="kardinal-test-app-${ENV}"
  kubectl create namespace $NS --dry-run=client -o yaml | kubectl apply -f -
  kubectl create deployment kardinal-test-app \
    --image="$TEST_APP_IMAGE" --namespace="$NS" --dry-run=client -o yaml | kubectl apply -f -
  echo "  $NS ready"
done

# ── Step 5: Configure EKS cluster for prod ───────────────────────────────────
echo ""
echo "[5/6] Configuring EKS cluster '$EKS_CLUSTER_NAME' for prod..."
aws eks update-kubeconfig \
  --name "$EKS_CLUSTER_NAME" \
  --region "$EKS_REGION" \
  --alias "eks-${EKS_CLUSTER_NAME}" 2>&1

kubectl config use-context "eks-${EKS_CLUSTER_NAME}"

# Install ArgoCD on EKS too (if not present)
if ! kubectl get namespace argocd --context "eks-${EKS_CLUSTER_NAME}" &>/dev/null; then
  echo "  Installing ArgoCD on EKS..."
  kubectl create namespace argocd
  kubectl apply -n argocd \
    -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml"
  kubectl rollout status deployment/argocd-server -n argocd --timeout=300s
fi

# Deploy test app to prod namespace on EKS
kubectl create namespace kardinal-test-app-prod --dry-run=client -o yaml | kubectl apply -f -
kubectl create deployment kardinal-test-app \
  --image="$TEST_APP_IMAGE" --namespace="kardinal-test-app-prod" --dry-run=client -o yaml | kubectl apply -f -
echo "  kardinal-test-app-prod ready on EKS"

# ── Step 6: Apply multi-cluster Pipeline ─────────────────────────────────────
echo ""
echo "[6/6] Multi-cluster environment ready."
echo ""
echo "Contexts:"
echo "  Pre-prod (kind): kubectl config use-context kind-${KIND_CLUSTER}"
echo "  Prod (EKS):      kubectl config use-context eks-${EKS_CLUSTER_NAME}"
echo ""
echo "Namespaces:"
echo "  kind: kardinal-test-app-test, kardinal-test-app-uat"
echo "  EKS:  kardinal-test-app-prod"
echo ""
echo "Next steps:"
echo "  kubectl config use-context kind-${KIND_CLUSTER}"
echo "  kubectl apply -f examples/multi-cluster/pipeline.yaml"
echo "  kardinal create bundle test-app --image ghcr.io/pnz1990/kardinal-test-app:sha-<SHA>"
