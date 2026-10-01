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
#     • kro Graph controller
#     • ArgoCD managing all environments
#     • The "control plane" — watches for Bundles, drives promotions
#
#   Cluster 2 — kardinal-dev  (kind)
#     • test + uat namespaces
#     • kardinal-test-app deployed
#     • Represents pre-production environments
#
#   Cluster 3 — kardinal-prod  (kind)
#     • prod namespace
#     • kardinal-test-app deployed
#     • Represents production
#
#   The main pipeline, kardinal-test-app (auto test → auto uat → PR prod),
#   exercises auto-promote, pr-review, the org PolicyGates and rollback. Argo CD
#   on the control cluster syncs its three environments. Optional Flux, Argo
#   Rollouts and Flagger pipelines are applied too (see steps 8-10).
#
#   The controller is built from this checkout and loaded into the control
#   cluster, so the demo always runs the code you have checked out.
#
# Usage:
#   ./demo/scripts/setup.sh                    # 3 kind clusters
#   ./demo/scripts/setup.sh --skip-build       # reuse the controller image already loaded into kind
#   ./demo/scripts/setup.sh --clean            # tear down first, then set up
#   GITHUB_TOKEN=xxx ./demo/scripts/setup.sh   # set GitHub token inline
#
# Prerequisites:
#   - Docker Desktop running
#   - kind, kubectl, helm installed
#   - GitHub PAT with repo write access (for the GitOps push step)
#
# After setup, run:
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
  (cd "${REPO_ROOT}" && go build -o "${REPO_ROOT}/bin/kardinal" ./cmd/kardinal/)
  KARDINAL="${REPO_ROOT}/bin/kardinal"
fi

# GitHub token — required for the GitOps push that drives promotions
GITHUB_TOKEN="${GITHUB_TOKEN:-}"
# The GitOps repo kardinal writes environment updates to
GITOPS_REPO="${GITOPS_REPO:-https://github.com/pnz1990/kardinal-demo}"
# The test application repo
TEST_APP_REPO="${TEST_APP_REPO:-pnz1990/kardinal-test-app}"

ARGOCD_VERSION="${ARGOCD_VERSION:-v2.10.3}"
FLUX_VERSION="${FLUX_VERSION:-v2.3.0}"
ARGO_ROLLOUTS_VERSION="${ARGO_ROLLOUTS_VERSION:-v1.7.1}"
# The controller image built from this checkout and loaded into the control cluster.
KARDINAL_IMAGE_REPO="${KARDINAL_IMAGE_REPO:-ghcr.io/pnz1990/kardinal-promoter}"
KARDINAL_IMAGE_TAG="${KARDINAL_IMAGE_TAG:-dev}"

# Colours
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
info()    { echo -e "${BLUE}[demo]${NC} $*"; }
success() { echo -e "${GREEN}[demo] ✓${NC} $*"; }
warn()    { echo -e "${YELLOW}[demo] ⚠${NC} $*"; }
error()   { echo -e "${RED}[demo] ✗${NC} $*" >&2; exit 1; }

# ── Parse flags ───────────────────────────────────────────────────────────────

for arg in "$@"; do
  case $arg in
    --skip-build) SKIP_BUILD=true ;;
    --clean)      CLEAN=true ;;
    --help|-h)
      sed -n '/^# Usage/,/^# Copyright/p' "${BASH_SOURCE[0]}" | grep -v "^#$" | sed 's/^# //'
      exit 0
      ;;
    --eks)        warn "--eks was removed; the demo runs on kind only (see demo/README.md)" ;;
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

if [[ -z "$GITHUB_TOKEN" ]]; then
  warn "GITHUB_TOKEN is not set. The controller will install but promotions"
  warn "will fail at the GitOps push step."
  warn "To promote, set GITHUB_TOKEN before running."
fi

if ! docker info &>/dev/null; then
  error "Docker is not running. Start Docker Desktop and retry."
fi

info "Prerequisites OK."
echo ""
info "Demo configuration:"
info "  Control cluster : $CONTROL_CLUSTER (kind)"
info "  Dev cluster     : $DEV_CLUSTER (kind)"
info "  Prod cluster    : $PROD_CLUSTER (kind)"
info "  Controller image: ${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG} (built from this checkout)"
info "  GitOps repo     : $GITOPS_REPO"
echo ""

# ── Optional clean ────────────────────────────────────────────────────────────

if [[ "$CLEAN" == "true" ]]; then
  info "Tearing down existing clusters..."
  "${DEMO_DIR}/scripts/teardown.sh" 2>/dev/null || true
fi

# ── Step 0: Resolve test app image ────────────────────────────────────────────

info "[0/10] Resolving latest test app image..."
# The token is sent as a header read from stdin, so it is not on the command line.
GH_API_HEADER="Accept: application/vnd.github+json"
[[ -n "$GITHUB_TOKEN" ]] && GH_API_HEADER="Authorization: Bearer ${GITHUB_TOKEN}"
LATEST_SHA=$(printf '%s\n' "$GH_API_HEADER" |
  curl -sf --max-time 10 -H @- "https://api.github.com/repos/${TEST_APP_REPO}/commits/main" |
  python3 -c "import sys,json; print(json.load(sys.stdin)['sha'][:7])" 2>/dev/null || true)
# Fall back to the image kardinal-demo's overlays pin; there is no :latest tag.
TEST_APP_IMAGE="${TEST_APP_IMAGE:-ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA:-9349a3f}}"
info "  Test app image: ${TEST_APP_IMAGE}"

# ── Step 1: Create kind clusters ──────────────────────────────────────────────

info "[1/10] Creating kind clusters..."

create_kind_cluster() {
  local name="$1"
  local config="$2"
  if kind get clusters 2>/dev/null | grep "^${name}$" >/dev/null; then
    warn "  Cluster '${name}' already exists — skipping create"
  else
    info "  Creating cluster '${name}'..."
    kind create cluster --name "$name" --config "$config"
    success "  Cluster '${name}' created"
  fi
}

# Every kind cluster uses the same node image (see test/e2e/kind-config.yaml).
create_kind_cluster "$CONTROL_CLUSTER" "${REPO_ROOT}/test/e2e/kind-config.yaml"
create_kind_cluster "$DEV_CLUSTER" "${REPO_ROOT}/test/e2e/kind-config.yaml"
create_kind_cluster "$PROD_CLUSTER" "${REPO_ROOT}/test/e2e/kind-config.yaml"

success "[1/10] Clusters ready"

# ── Step 2: Install ArgoCD on the control cluster ─────────────────────────────

info "[2/10] Installing ArgoCD on control cluster..."
kubectl config use-context "kind-${CONTROL_CLUSTER}"
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argocd \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml" \
  --wait=false
kubectl rollout status deployment/argocd-server -n argocd --timeout=240s
success "[2/10] ArgoCD installed"

# ── Step 3: Install kardinal-promoter on the control cluster ─────────────────

info "[3/10] Installing kardinal-promoter on control cluster..."
kubectl config use-context "kind-${CONTROL_CLUSTER}"
kubectl create namespace kardinal-system --dry-run=client -o yaml | kubectl apply -f -

# Build the controller from this checkout and load it into the control cluster.
PULL_POLICY=IfNotPresent
if [[ "$SKIP_BUILD" != "true" ]]; then
  info "  Building controller image ${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG}..."
  docker build -t "${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG}" "${REPO_ROOT}"
  kind load docker-image "${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG}" --name "$CONTROL_CLUSTER"
  PULL_POLICY=Never
fi

# CRDs must be applied before Helm — the chart includes a ScheduleClock resource
# that requires the CRDs to exist before the chart renders (#593)
info "  Applying CRDs from this checkout..."
kubectl apply -f "${REPO_ROOT}/config/crd/bases/"

# GitHub token Secret. The chart reads it in kardinal-system; the demo
# Pipelines (namespace default) resolve spec.git.secretRef in their own
# namespace, so create it in both.
for ns in kardinal-system default; do
  kubectl create secret generic github-token \
    --namespace "$ns" \
    --from-literal=token="${GITHUB_TOKEN}" \
    --dry-run=client -o yaml | kubectl apply -f -
done

# Create platform-policies namespace for org-level PolicyGates
kubectl create namespace platform-policies --dry-run=client -o yaml | kubectl apply -f -

# kro (the Graph controller) is a prerequisite of kardinal-promoter.
KUBE_CONTEXT="kind-${CONTROL_CLUSTER}" bash "${REPO_ROOT}/hack/install-kro.sh"

# Install the local chart with the image loaded above. Helm does not wait;
# the rollout status below waits for the controller itself.
helm --kube-context "kind-${CONTROL_CLUSTER}" upgrade --install kardinal-promoter \
  "${REPO_ROOT}/chart/kardinal-promoter" \
  --namespace kardinal-system \
  --set image.repository="${KARDINAL_IMAGE_REPO}" \
  --set image.tag="${KARDINAL_IMAGE_TAG}" \
  --set image.pullPolicy="${PULL_POLICY}" \
  --set github.secretRef.name=github-token

kubectl rollout status deployment/kardinal-promoter -n kardinal-system --timeout=180s

success "[3/10] kardinal-promoter installed"

# ── Step 4: Deploy test app to dev cluster (test + uat) ──────────────────────

info "[4/10] Deploying test app to dev cluster (test + uat)..."
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

success "[4/10] Test app deployed to dev cluster"

# ── Step 5: Deploy test app to prod cluster ───────────────────────────────────

info "[5/10] Deploying test app to prod cluster..."
kubectl config use-context "kind-${PROD_CLUSTER}"

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

success "[5/10] Test app deployed to prod cluster"

# ── Step 6: Apply pipelines and policy gates ──────────────────────────────────

info "[6/10] Applying pipelines and PolicyGates on control cluster..."
kubectl config use-context "kind-${CONTROL_CLUSTER}"

# Wait for Pipeline CRD to be established before applying any Pipeline resources.
# The Helm install is async — the CRD must be fully registered before kubectl apply.
kubectl wait --for=condition=established \
  crd/pipelines.kardinal.io \
  crd/policygates.kardinal.io \
  --timeout=60s

# Org-level PolicyGates (platform team owns these)
kubectl apply -f "${DEMO_DIR}/manifests/policy-gates/"

# The main pipeline: test → uat → prod, synced by Argo CD on the control cluster
kubectl apply -f "${DEMO_DIR}/manifests/pipeline-simple/"

success "[6/10] Pipelines and PolicyGates applied"

# ── Step 7: Configure ArgoCD Applications ────────────────────────────────────

info "[7/10] Configuring ArgoCD applications..."
kubectl config use-context "kind-${CONTROL_CLUSTER}"
kubectl apply -f "${DEMO_DIR}/manifests/argocd/"

# argocd-application-controller is a StatefulSet in Argo CD v2.x.
kubectl rollout status statefulset/argocd-application-controller -n argocd --timeout=120s

success "[7/10] ArgoCD applications configured"

# ── Step 8: Install Flux (optional — needed for scenario 11) ─────────────────

INSTALL_FLUX="${INSTALL_FLUX:-true}"
INSTALL_ARGO_ROLLOUTS="${INSTALL_ARGO_ROLLOUTS:-true}"
INSTALL_FLAGGER="${INSTALL_FLAGGER:-true}"

if [[ "$INSTALL_FLUX" == "true" ]]; then
  info "[8/10] Installing Flux..."
  kubectl config use-context "kind-${DEV_CLUSTER}"
  # Install Flux controllers (no bootstrap — we apply Kustomizations manually)
  kubectl create namespace flux-system --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -f "https://github.com/fluxcd/flux2/releases/download/${FLUX_VERSION}/install.yaml"
  kubectl -n flux-system rollout status deploy/source-controller --timeout=120s
  kubectl -n flux-system rollout status deploy/kustomize-controller --timeout=120s

  # kardinal-demo is public, so the GitRepository needs no credentials.
  # Apply Flux Kustomizations (on dev cluster — Kustomization resources live here)
  kubectl apply -f "${DEMO_DIR}/manifests/flux/kustomizations.yaml"

  # Apply the Flux pipeline on the control cluster (Pipeline CRD is only registered there)
  kubectl config use-context "kind-${CONTROL_CLUSTER}"
  kubectl apply -f "${DEMO_DIR}/manifests/flux/pipeline.yaml"
  success "[8/10] Flux installed and Kustomizations applied"
else
  info "[8/10] Flux install skipped (INSTALL_FLUX=false)"
fi

# ── Step 9: Install Argo Rollouts ─────────────────────────────────────────────

if [[ "$INSTALL_ARGO_ROLLOUTS" == "true" ]]; then
  info "[9/10] Installing Argo Rollouts..."
  kubectl config use-context "kind-${DEV_CLUSTER}"
  kubectl create namespace argo-rollouts --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -n argo-rollouts \
    -f "https://github.com/argoproj/argo-rollouts/releases/download/${ARGO_ROLLOUTS_VERSION}/install.yaml"
  kubectl -n argo-rollouts rollout status deploy/argo-rollouts --timeout=120s

  # Apply Rollout fixture only (pipeline.yaml requires the control cluster — applied separately below)
  kubectl create namespace kardinal-test-app-test --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -f "${DEMO_DIR}/manifests/rollouts/rollout.yaml"
  # Apply the rollouts Pipeline on the control cluster (Pipeline CRD is only there)
  kubectl config use-context "kind-${CONTROL_CLUSTER}"
  kubectl apply -f "${DEMO_DIR}/manifests/rollouts/pipeline.yaml"
  success "[9/10] Argo Rollouts installed and Rollout applied"
else
  info "[9/10] Argo Rollouts install skipped (INSTALL_ARGO_ROLLOUTS=false)"
fi

# ── Step 10: Install Flagger ──────────────────────────────────────────────────

if [[ "$INSTALL_FLAGGER" == "true" ]]; then
  info "[10/10] Installing Flagger..."
  kubectl config use-context "kind-${DEV_CLUSTER}"
  # Install Flagger (no service mesh — uses Kubernetes provider)
  helm repo add flagger https://flagger.app 2>/dev/null || true
  helm repo update 2>/dev/null || true
  kubectl create namespace flagger-system --dry-run=client -o yaml | kubectl apply -f -
  helm upgrade -i flagger flagger/flagger \
    --namespace flagger-system \
    --set prometheus.install=false \
    --set meshProvider=kubernetes \
    --wait --timeout=120s 2>/dev/null || \
  kubectl apply -f "https://raw.githubusercontent.com/fluxcd/flagger/main/artifacts/flagger/crd.yaml" 2>/dev/null || true

  # Apply Flagger Canary and Deployment only (pipeline.yaml requires the control cluster)
  kubectl create namespace kardinal-test-app-test --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -f "${DEMO_DIR}/manifests/flagger/canary.yaml"
  # Apply the flagger Pipeline on the control cluster
  kubectl config use-context "kind-${CONTROL_CLUSTER}"
  kubectl apply -f "${DEMO_DIR}/manifests/flagger/pipeline.yaml"
  success "[10/10] Flagger installed and Canary applied"
else
  info "[10/10] Flagger install skipped (INSTALL_FLAGGER=false)"
fi

# ── Summary ───────────────────────────────────────────────────────────────────

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  kardinal-promoter Demo Environment Ready!${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo ""
echo "  Clusters:"
echo "    kind-${CONTROL_CLUSTER}  → kardinal controller + ArgoCD"
echo "    kind-${DEV_CLUSTER}      → test + uat environments"
echo "    kind-${PROD_CLUSTER}     → prod environment"
echo ""
echo "  Pipelines:"
kubectl config use-context "kind-${CONTROL_CLUSTER}" &>/dev/null
$KARDINAL get pipelines 2>/dev/null || kubectl get pipelines -A 2>/dev/null | head -10
echo ""
echo "  Access the UI:"
echo "    kubectl config use-context kind-${CONTROL_CLUSTER}"
echo "    kubectl port-forward -n kardinal-system deployment/kardinal-promoter 8082:8082 &"
echo "    kardinal dashboard"
echo ""
echo "  Trigger a promotion:"
echo "    kardinal create bundle kardinal-test-app \\"
echo "      --image ${TEST_APP_IMAGE}"
echo ""
echo "  Tear down:"
echo "    ./demo/scripts/teardown.sh"
echo ""
