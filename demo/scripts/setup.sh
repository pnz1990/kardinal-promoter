#!/usr/bin/env bash
# demo/scripts/setup.sh
#
# kardinal-promoter Demo Environment Setup
# =========================================
#
# Creates a working demo environment on one kind cluster, kardinal-demo:
#
#   • the kardinal-promoter controller, built from this checkout and loaded
#     into the cluster, so the demo always runs the code you have checked out
#   • kro, the Graph controller kardinal drives
#   • Argo CD, syncing the three environments of kardinal-test-app from
#     github.com/pnz1990/kardinal-demo into namespaces
#     kardinal-test-app-{test,uat,prod}
#   • the Pipeline kardinal-test-app (auto test → auto uat → PR-review prod)
#     and the org PolicyGates in platform-policies
#
# Flux, Argo Rollouts and Flagger have their own tested examples:
# examples/flux-demo, examples/argo-rollouts-demo and examples/flagger-demo.
#
# Usage:
#   ./demo/scripts/setup.sh                    # one kind cluster
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

DEMO_CLUSTER="${DEMO_CLUSTER:-kardinal-demo}"
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

# GitHub token — required for the GitOps push that drives promotions. Use a
# fork of pnz1990/kardinal-demo to push to (demo/manifests/*: git.url and the
# Argo CD repoURL).
GITHUB_TOKEN="${GITHUB_TOKEN:-}"
# The test application repo
TEST_APP_REPO="${TEST_APP_REPO:-pnz1990/kardinal-test-app}"

# Argo CD: the version the live e2e suites install and test, from
# hack/e2e/versions.env, the one place it is set.
# shellcheck source=hack/e2e/versions.env
source "${REPO_ROOT}/hack/e2e/versions.env"
ARGOCD_VERSION="${ARGOCD_VERSION:-${ARGOCD_RELEASE}}"
# The controller image built from this checkout and loaded into the cluster.
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
info "  Cluster         : $DEMO_CLUSTER (kind)"
info "  Controller image: ${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG} (built from this checkout)"
echo ""

# ── Optional clean ────────────────────────────────────────────────────────────

if [[ "$CLEAN" == "true" ]]; then
  info "Tearing down the existing demo cluster..."
  "${DEMO_DIR}/scripts/teardown.sh" 2>/dev/null || true
fi

# ── Step 0: Resolve test app image ────────────────────────────────────────────

info "[0/5] Resolving latest test app image..."
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

info "[1/5] Creating the kind cluster..."

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

# The node image the live e2e suites use (test/e2e/kind-config.yaml).
create_kind_cluster "$DEMO_CLUSTER" "${REPO_ROOT}/test/e2e/kind-config.yaml"

success "[1/5] Cluster ready"

# ── Step 2: Install Argo CD ─────────────────────────────

info "[2/5] Installing Argo CD..."
kubectl config use-context "kind-${DEMO_CLUSTER}"
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
# Server-side: the Argo CD 3 Application CRD is too large for client-side apply.
kubectl apply -n argocd --server-side --force-conflicts \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml"
kubectl rollout status deployment/argocd-server -n argocd --timeout=240s
success "[2/5] ArgoCD installed"

# ── Step 3: Install kro and kardinal-promoter ─────────────────

info "[3/5] Installing kro and kardinal-promoter..."
kubectl config use-context "kind-${DEMO_CLUSTER}"
kubectl create namespace kardinal-system --dry-run=client -o yaml | kubectl apply -f -

# Build the controller from this checkout and load it into the cluster.
PULL_POLICY=IfNotPresent
if [[ "$SKIP_BUILD" != "true" ]]; then
  info "  Building controller image ${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG}..."
  docker build -t "${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG}" "${REPO_ROOT}"
  kind load docker-image "${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG}" --name "$DEMO_CLUSTER"
  PULL_POLICY=Never
fi

# CRDs must be applied before Helm — the chart includes a ScheduleClock resource
# that requires the CRDs to exist before the chart renders (#593)
info "  Applying CRDs from this checkout..."
kubectl apply -f "${REPO_ROOT}/config/crd/bases/"

# GitHub token Secret. The chart reads it in kardinal-system; the demo
# Pipeline (namespace default) resolves spec.git.secretRef in its own
# namespace, so create it in both. A Secret a custom resource names must be
# labelled kardinal.io/referenceable=true (docs/guides/security.md).
for ns in kardinal-system default; do
  kubectl create secret generic github-token \
    --namespace "$ns" \
    --from-literal=token="${GITHUB_TOKEN}" \
    --dry-run=client -o yaml | kubectl apply -f -
done
kubectl label secret github-token --namespace default kardinal.io/referenceable=true --overwrite

# Create platform-policies namespace for org-level PolicyGates
kubectl create namespace platform-policies --dry-run=client -o yaml | kubectl apply -f -

# kro (the Graph controller) is a prerequisite of kardinal-promoter.
KUBE_CONTEXT="kind-${DEMO_CLUSTER}" bash "${REPO_ROOT}/hack/install-kro.sh"

# Install the local chart with the image loaded above. Helm does not wait;
# the rollout status below waits for the controller itself.
helm --kube-context "kind-${DEMO_CLUSTER}" upgrade --install kardinal-promoter \
  "${REPO_ROOT}/chart/kardinal-promoter" \
  --namespace kardinal-system \
  --set image.repository="${KARDINAL_IMAGE_REPO}" \
  --set image.tag="${KARDINAL_IMAGE_TAG}" \
  --set image.pullPolicy="${PULL_POLICY}" \
  --set github.secretRef.name=github-token

kubectl rollout status deployment/kardinal-promoter -n kardinal-system --timeout=180s

success "[3/5] kardinal-promoter installed"

# ── Step 4: Apply the Pipeline and PolicyGates ────────────────────────────────

info "[4/5] Applying the Pipeline and PolicyGates..."
kubectl config use-context "kind-${DEMO_CLUSTER}"

# Wait for the CRDs to be established before applying resources of them.
kubectl wait --for=condition=established \
  crd/pipelines.kardinal.io \
  crd/policygates.kardinal.io \
  --timeout=60s

# Org-level PolicyGates (platform team owns these)
kubectl apply -f "${DEMO_DIR}/manifests/policy-gates/"

# The Pipeline: test → uat → prod
kubectl apply -f "${DEMO_DIR}/manifests/pipeline-simple/"

success "[4/5] Pipeline and PolicyGates applied"

# ── Step 5: Configure Argo CD Applications ───────────────────────────────────

info "[5/5] Configuring Argo CD applications..."
kubectl apply -f "${DEMO_DIR}/manifests/argocd/"

# argocd-application-controller is a StatefulSet.
kubectl rollout status statefulset/argocd-application-controller -n argocd --timeout=120s

success "[5/5] Argo CD applications configured"

# ── Summary ───────────────────────────────────────────────────────────────────

echo ""
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  kardinal-promoter Demo Environment Ready!${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════════════${NC}"
echo ""
echo "  Cluster: kind-${DEMO_CLUSTER} (kardinal, kro, Argo CD)"
echo ""
echo "  Pipelines:"
$KARDINAL get pipelines 2>/dev/null || kubectl get pipelines -A 2>/dev/null | head -10
echo ""
echo "  Access the UI:"
echo "    kubectl config use-context kind-${DEMO_CLUSTER}"
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
