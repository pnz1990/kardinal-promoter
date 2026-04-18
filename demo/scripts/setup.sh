#!/usr/bin/env bash
# demo/scripts/setup.sh
#
# kardinal-promoter Demo Environment Setup
# =========================================
#
# Creates a complete, working demo environment:
#
#   Cluster 1 — kardinal-control  (kind)
#     • kardinal-promoter controller
#     • krocodile Graph controller
#     • ArgoCD managing all environments
#     • The "control plane" — watches for Bundles, drives promotions
#
#   Cluster 2 — kardinal-dev  (kind)
#     • test + uat namespaces
#     • kardinal-test-app deployed
#     • Represents pre-production environments
#
#   Cluster 3 — kardinal-prod  (kind, or EKS when --eks is passed)
#     • prod namespace
#     • kardinal-test-app deployed
#     • Represents production
#
#   Two pipelines are configured end-to-end:
#
#   Pipeline 1 — kardinal-test-app  (simple: auto test → auto uat → PR prod)
#     Exercises: auto-promote, pr-review, PolicyGates (weekend + soak), rollback
#
#   Pipeline 2 — kardinal-test-app-advanced  (multi-cluster, change window, metrics)
#     Exercises: multi-cluster shard, change window gate, upstream soak gate,
#                manual override, pause/resume
#
# Usage:
#   ./demo/scripts/setup.sh                    # 3 kind clusters
#   ./demo/scripts/setup.sh --eks              # kind control+dev, EKS prod
#   ./demo/scripts/setup.sh --skip-build       # skip local image build (use published)
#   ./demo/scripts/setup.sh --clean            # tear down first, then set up
#   GITHUB_TOKEN=xxx ./demo/scripts/setup.sh   # set GitHub token inline
#
# Prerequisites:
#   - Docker Desktop running
#   - kind, kubectl, helm, argocd CLI installed
#   - GitHub PAT with repo write access (for the GitOps push step)
#   - (optional) aws CLI + terraform for --eks mode
#
# After setup, run:
#   ./demo/scripts/validate.sh    to verify all features work end-to-end
#   kardinal dashboard            to open the UI
#   ./demo/scripts/teardown.sh    to clean up everything
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

set -euo pipefail

# ── Configuration ─────────────────────────────────────────────────────────────

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "${DEMO_DIR}/.." && pwd)"

CONTROL_CLUSTER="${CONTROL_CLUSTER:-kardinal-control}"
DEV_CLUSTER="${DEV_CLUSTER:-kardinal-dev}"
PROD_CLUSTER="${PROD_CLUSTER:-kardinal-prod}"
USE_EKS=false
SKIP_BUILD=false
CLEAN=false

# ── Find or build kardinal CLI ────────────────────────────────────────────────
if [[ -x "${REPO_ROOT}/bin/kardinal" ]]; then
  KARDINAL="${REPO_ROOT}/bin/kardinal"
elif command -v kardinal &>/dev/null; then
  KARDINAL="kardinal"
else
  mkdir -p "${REPO_ROOT}/bin"
  echo "[setup] Building kardinal CLI from source..."
  cd "${REPO_ROOT}"
  go build -mod=mod -o "${REPO_ROOT}/bin/kardinal" ./cmd/kardinal/ 2>/dev/null || \
    /usr/local/go126/bin/go build -mod=mod -o "${REPO_ROOT}/bin/kardinal" ./cmd/kardinal/
  KARDINAL="${REPO_ROOT}/bin/kardinal"
fi

# GitHub token — required for the GitOps push that drives promotions
GITHUB_TOKEN="${GITHUB_TOKEN:-}"
# The GitOps repo kardinal writes environment updates to
GITOPS_REPO="${GITOPS_REPO:-https://github.com/pnz1990/kardinal-demo}"
# The test application repo
TEST_APP_REPO="${TEST_APP_REPO:-pnz1990/kardinal-test-app}"

ARGOCD_VERSION="${ARGOCD_VERSION:-v2.10.3}"
CHART_IMAGE_TAG="${CHART_IMAGE_TAG:-v0.8.0}"   # last successfully published image

# Colours
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
info()    { echo -e "${BLUE}[demo]${NC} $*"; }
success() { echo -e "${GREEN}[demo] ✓${NC} $*"; }
warn()    { echo -e "${YELLOW}[demo] ⚠${NC} $*"; }
error()   { echo -e "${RED}[demo] ✗${NC} $*" >&2; exit 1; }

# ── Parse flags ───────────────────────────────────────────────────────────────

for arg in "$@"; do
  case $arg in
    --eks)        USE_EKS=true ;;
    --skip-build) SKIP_BUILD=true ;;
    --clean)      CLEAN=true ;;
    --help|-h)
      sed -n '/^# Usage/,/^# Copyright/p' "${BASH_SOURCE[0]}" | grep -v "^#$" | sed 's/^# //'
      exit 0
      ;;
    *) warn "Unknown flag: $arg" ;;
  esac
done

# ── Pre-flight checks ─────────────────────────────────────────────────────────

check_tool() {
  command -v "$1" &>/dev/null || error "Required tool not found: $1. Install it and retry."
}

info "Checking prerequisites..."
check_tool docker
check_tool kind
check_tool kubectl
check_tool helm
check_tool argocd 2>/dev/null || warn "argocd CLI not found — some validation steps will be skipped"

if [[ -z "$GITHUB_TOKEN" ]]; then
  warn "GITHUB_TOKEN is not set. The controller will install but promotions"
  warn "will fail at the GitOps push step (bundle creation in validate.sh)."
  warn "For full end-to-end validation, set GITHUB_TOKEN before running."
fi

if ! docker info &>/dev/null; then
  error "Docker is not running. Start Docker Desktop and retry."
fi

info "Prerequisites OK."
echo ""
info "Demo configuration:"
info "  Control cluster : $CONTROL_CLUSTER (kind)"
info "  Dev cluster     : $DEV_CLUSTER (kind)"
info "  Prod cluster    : $PROD_CLUSTER ($( [[ $USE_EKS == true ]] && echo "EKS" || echo "kind" ))"
info "  Controller image: ghcr.io/pnz1990/kardinal-promoter/controller:${CHART_IMAGE_TAG}"
info "  GitOps repo     : $GITOPS_REPO"
echo ""

# ── Optional clean ────────────────────────────────────────────────────────────

if [[ "$CLEAN" == "true" ]]; then
  info "Tearing down existing clusters..."
  "${DEMO_DIR}/scripts/teardown.sh" 2>/dev/null || true
fi

# ── Step 0: Resolve test app image ────────────────────────────────────────────

info "[0/7] Resolving latest test app image..."
LATEST_SHA=$(curl -sf "https://api.github.com/repos/${TEST_APP_REPO}/commits/main" \
  -H "Authorization: Bearer ${GITHUB_TOKEN}" | \
  python3 -c "import sys,json; print(json.load(sys.stdin)['sha'][:7])" 2>/dev/null || \
  echo "latest")
TEST_APP_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}"
info "  Test app image: ${TEST_APP_IMAGE}"

# ── Step 1: Create kind clusters ──────────────────────────────────────────────

info "[1/7] Creating kind clusters..."

create_kind_cluster() {
  local name="$1"
  local config="${2:-}"
  if kind get clusters 2>/dev/null | grep -q "^${name}$"; then
    warn "  Cluster '${name}' already exists — skipping create"
  else
    info "  Creating cluster '${name}'..."
    if [[ -n "$config" ]]; then
      kind create cluster --name "$name" --config "$config"
    else
      kind create cluster --name "$name" --image kindest/node:v1.29.0
    fi
    success "  Cluster '${name}' created"
  fi
}

create_kind_cluster "$CONTROL_CLUSTER" "${REPO_ROOT}/test/e2e/kind-config.yaml"
create_kind_cluster "$DEV_CLUSTER"

if [[ "$USE_EKS" == "true" ]]; then
  info "  EKS prod cluster — using --eks mode..."
  if ! aws eks describe-cluster --name kardinal-e2e-prod --region us-east-2 \
      --query 'cluster.status' --output text 2>/dev/null | grep -q "ACTIVE"; then
    info "  Creating EKS cluster via Terraform (this takes ~15 min)..."
    cd "${REPO_ROOT}/terraform/eks-e2e"
    terraform init -input=false
    terraform apply -input=false -auto-approve
    cd "${REPO_ROOT}"
  fi
  aws eks update-kubeconfig --name kardinal-e2e-prod --region us-east-2 \
    --alias "$PROD_CLUSTER"
  success "  EKS cluster configured"
else
  create_kind_cluster "$PROD_CLUSTER"
fi

success "[1/7] Clusters ready"

# ── Step 2: Install ArgoCD on the control cluster ─────────────────────────────

info "[2/7] Installing ArgoCD on control cluster..."
kubectl config use-context "kind-${CONTROL_CLUSTER}"
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argocd \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml" \
  --wait=false
kubectl rollout status deployment/argocd-server -n argocd --timeout=240s
success "[2/7] ArgoCD installed"

# ── Step 3: Install kardinal-promoter on the control cluster ─────────────────

info "[3/7] Installing kardinal-promoter on control cluster..."
kubectl config use-context "kind-${CONTROL_CLUSTER}"
kubectl create namespace kardinal-system --dry-run=client -o yaml | kubectl apply -f -

# CRDs must be applied before Helm — the chart includes a ScheduleClock resource
# that requires the CRDs to exist before the chart renders (#593)
if [[ -d "${REPO_ROOT}/config/crd/bases" ]]; then
  info "  Applying CRDs from local repo..."
  kubectl apply -f "${REPO_ROOT}/config/crd/bases/" 2>/dev/null || true
else
  # Running from a release tarball — pull CRDs from the published chart
  info "  Fetching CRDs from published chart..."
  helm show crds oci://ghcr.io/pnz1990/charts/kardinal-promoter \
    --version "${CHART_IMAGE_TAG#v}" 2>/dev/null | kubectl apply -f - || \
  helm pull oci://ghcr.io/pnz1990/charts/kardinal-promoter \
    --version "${CHART_IMAGE_TAG#v}" --untar --untardir /tmp/kardinal-chart 2>/dev/null && \
  kubectl apply -f /tmp/kardinal-chart/kardinal-promoter/crds/ 2>/dev/null || true
fi

# GitHub token secret
kubectl create secret generic github-token \
  --namespace kardinal-system \
  --from-literal=token="${GITHUB_TOKEN}" \
  --dry-run=client -o yaml | kubectl apply -f -

# Create platform-policies namespace for org-level PolicyGates
kubectl create namespace platform-policies --dry-run=client -o yaml | kubectl apply -f -

# Install from OCI registry — use last known-good published tag.
# Use --wait=false for the install, then wait for the controller pod
# separately — the Helm wait includes all chart resources (krocodile,
# CRDs, etc.) and can timeout on slow CI runners; the controller pod
# itself comes up quickly once the image is pulled.
helm upgrade --install kardinal-promoter \
  oci://ghcr.io/pnz1990/charts/kardinal-promoter \
  --namespace kardinal-system \
  --set "image.tag=${CHART_IMAGE_TAG}" \
  --set github.secretRef.name=github-token \
  --set validatingAdmissionPolicy.enabled=false \
  --wait=false --timeout 60s 2>/dev/null || \
helm upgrade --install kardinal-promoter \
  oci://ghcr.io/pnz1990/charts/kardinal-promoter \
  --namespace kardinal-system \
  --set "image.tag=${CHART_IMAGE_TAG}" \
  --set github.secretRef.name=github-token \
  --set validatingAdmissionPolicy.enabled=false

# Wait for the controller pod to be Ready (up to 3 min)
kubectl rollout status deployment -n kardinal-system --timeout=180s 2>/dev/null || true
# Also wait for kro-system if present
kubectl rollout status deployment -n kro-system --timeout=60s 2>/dev/null || true

success "[3/7] kardinal-promoter installed"

# ── Step 4: Deploy test app to dev cluster (test + uat) ──────────────────────

info "[4/7] Deploying test app to dev cluster (test + uat)..."
kubectl config use-context "kind-${DEV_CLUSTER}"

for ENV_NAME in test uat; do
  NS="kardinal-test-app-${ENV_NAME}"
  kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -n "$NS" -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kardinal-test-app
  namespace: ${NS}
  labels:
    app: kardinal-test-app
    env: ${ENV_NAME}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kardinal-test-app
  template:
    metadata:
      labels:
        app: kardinal-test-app
        env: ${ENV_NAME}
    spec:
      containers:
        - name: app
          image: ${TEST_APP_IMAGE}
          ports:
            - containerPort: 8080
          env:
            - name: ENV
              value: ${ENV_NAME}
---
apiVersion: v1
kind: Service
metadata:
  name: kardinal-test-app
  namespace: ${NS}
spec:
  selector:
    app: kardinal-test-app
  ports:
    - port: 80
      targetPort: 8080
EOF
  info "  Deployed kardinal-test-app to ${NS}"
done

success "[4/7] Test app deployed to dev cluster"

# ── Step 5: Deploy test app to prod cluster ───────────────────────────────────

info "[5/7] Deploying test app to prod cluster..."
if [[ "$USE_EKS" == "true" ]]; then
  kubectl config use-context "$PROD_CLUSTER"
else
  kubectl config use-context "kind-${PROD_CLUSTER}"
fi

kubectl create namespace kardinal-test-app-prod --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n kardinal-test-app-prod -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kardinal-test-app
  namespace: kardinal-test-app-prod
  labels:
    app: kardinal-test-app
    env: prod
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kardinal-test-app
  template:
    metadata:
      labels:
        app: kardinal-test-app
        env: prod
    spec:
      containers:
        - name: app
          image: ${TEST_APP_IMAGE}
          ports:
            - containerPort: 8080
          env:
            - name: ENV
              value: prod
---
apiVersion: v1
kind: Service
metadata:
  name: kardinal-test-app
  namespace: kardinal-test-app-prod
spec:
  selector:
    app: kardinal-test-app
  ports:
    - port: 80
      targetPort: 8080
EOF

success "[5/7] Test app deployed to prod cluster"

# ── Step 6: Apply pipelines and policy gates ──────────────────────────────────

info "[6/7] Applying pipelines and PolicyGates on control cluster..."
kubectl config use-context "kind-${CONTROL_CLUSTER}"

# Org-level PolicyGates (platform team owns these)
kubectl apply -f "${DEMO_DIR}/manifests/policy-gates/"

# Pipeline 1: simple (test → uat → prod, all in-cluster ArgoCD)
kubectl apply -f "${DEMO_DIR}/manifests/pipeline-simple/"

# Pipeline 2: advanced (multi-cluster, change window, metrics gate)
kubectl apply -f "${DEMO_DIR}/manifests/pipeline-advanced/"

success "[6/7] Pipelines and PolicyGates applied"

# ── Step 7: Configure ArgoCD Applications ────────────────────────────────────

info "[7/7] Configuring ArgoCD applications..."
kubectl config use-context "kind-${CONTROL_CLUSTER}"
kubectl apply -f "${DEMO_DIR}/manifests/argocd/"

# Wait for ArgoCD to sync
kubectl rollout status deployment/argocd-application-controller -n argocd --timeout=120s 2>/dev/null || true

success "[7/7] ArgoCD applications configured"

# ── Summary ───────────────────────────────────────────────────────────────────

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  kardinal-promoter Demo Environment Ready!${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo ""
echo "  Clusters:"
echo "    kind-${CONTROL_CLUSTER}  → kardinal controller + ArgoCD"
echo "    kind-${DEV_CLUSTER}      → test + uat environments"
echo "    $( [[ $USE_EKS == true ]] && echo "${PROD_CLUSTER} (EKS)" || echo "kind-${PROD_CLUSTER}" )       → prod environment"
echo ""
echo "  Pipelines:"
kubectl config use-context "kind-${CONTROL_CLUSTER}" &>/dev/null
$KARDINAL get pipelines 2>/dev/null || kubectl get pipelines -A 2>/dev/null | head -10
echo ""
echo "  Access the UI:"
echo "    kubectl config use-context kind-${CONTROL_CLUSTER}"
echo "    kubectl port-forward -n kardinal-system deployment/kardinal-kardinal-promoter 8082:8082 &"
echo "    kardinal dashboard"
echo ""
echo "  Trigger a promotion:"
echo "    kardinal create bundle kardinal-test-app \\"
echo "      --image ${TEST_APP_IMAGE}"
echo ""
echo "  Validate everything works:"
echo "    ./demo/scripts/validate.sh"
echo ""
echo "  Tear down:"
echo "    ./demo/scripts/teardown.sh"
echo ""
