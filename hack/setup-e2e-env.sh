#!/usr/bin/env bash
# hack/setup-e2e-env.sh
#
# Sets up a complete kardinal-promoter E2E environment on a local kind cluster.
# This goes beyond unit tests — it creates a real environment where:
#   - the kro Graph controller runs and processes real Graph CRs
#   - ArgoCD runs and syncs real Application resources
#   - kardinal-test-app is deployed across test/uat/prod namespaces
#   - kardinal-promoter (built from this checkout) runs the full promotion loop
#
# The script owns its cluster: it creates (or reuses) the kind cluster
# $KIND_CLUSTER and runs every kubectl/helm command with an explicit
# --context kind-$KIND_CLUSTER. It never uses the current kube context, and it
# refuses to run against a context that is not a local kind cluster.
#
# Usage:
#   ./hack/setup-e2e-env.sh                 # full setup
#   SKIP_ARGOCD=1 ./hack/setup-e2e-env.sh  # skip ArgoCD (faster, for unit testing)
#   SKIP_BUILD=1 ./hack/setup-e2e-env.sh   # reuse an image already loaded into kind
#
# Prerequisites: docker, kind, kubectl, helm
#
# kro (the Graph controller) is a prerequisite installed by hack/install-kro.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"
# shellcheck source=hack/kind-context.sh
source "$SCRIPT_DIR/kind-context.sh"

KIND_CLUSTER="${KIND_CLUSTER:-kardinal-e2e}"
CHART_VERSION="${CHART_VERSION:-}" # empty = use local chart and a locally built image
ARGOCD_VERSION="${ARGOCD_VERSION:-v2.10.3}"
# The image kardinal-demo's overlays pin; override with a newer sha-<7> tag.
TEST_APP_IMAGE="${TEST_APP_IMAGE:-ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f}"
KARDINAL_IMAGE_REPO="${KARDINAL_IMAGE_REPO:-ghcr.io/pnz1990/kardinal-promoter}"
KARDINAL_IMAGE_TAG="${KARDINAL_IMAGE_TAG:-dev}"
SKIP_BUILD="${SKIP_BUILD:-0}"
SKIP_ARGOCD="${SKIP_ARGOCD:-0}"
GITHUB_TOKEN="${GITHUB_TOKEN:-}"
KARDINAL_DEMO_TOKEN="${KARDINAL_DEMO_TOKEN:-${GITHUB_TOKEN}}"

echo "=== kardinal-promoter E2E Environment Setup ==="
echo "Cluster: $KIND_CLUSTER | Chart: ${CHART_VERSION:-local} | ArgoCD: $ARGOCD_VERSION"

# ── Step 0: Create or reuse the kind cluster and pin the context ─────────────
ensure_kind_cluster "$KIND_CLUSTER" "$REPO_ROOT/test/e2e/kind-config.yaml"
CTX="kind-${KIND_CLUSTER}"
use_kind_context "$CTX"

# ── Step 1: Install kro, then kardinal-promoter via Helm ─────────────────────
echo ""
echo "[1/5] Installing kro and kardinal-promoter..."
KUBE_CONTEXT="$CTX" bash "$SCRIPT_DIR/install-kro.sh"

if [ -n "$KARDINAL_DEMO_TOKEN" ]; then
  # The controller reads the chart's github.secretRef in kardinal-system; the
  # quickstart Pipeline (namespace default) resolves spec.git.secretRef in its
  # own namespace. Create the Secret in both.
  for ns in kardinal-system default; do
    "${KUBECTL[@]}" create namespace "$ns" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
    "${KUBECTL[@]}" create secret generic github-token \
      --namespace "$ns" \
      --from-literal=token="$KARDINAL_DEMO_TOKEN" \
      --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
  done
fi

if [ -n "$CHART_VERSION" ]; then
  # Install from OCI registry (production-like)
  "${HELM[@]}" upgrade --install kardinal-promoter \
    oci://ghcr.io/pnz1990/charts/kardinal-promoter \
    --version "$CHART_VERSION" \
    --namespace kardinal-system --create-namespace \
    --set github.secretRef.name=github-token \
    --wait --timeout 120s
else
  # Install from the local chart with a controller image built from this checkout.
  if [ "$SKIP_BUILD" != "1" ]; then
    docker build -t "${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG}" "$REPO_ROOT"
    kind load docker-image "${KARDINAL_IMAGE_REPO}:${KARDINAL_IMAGE_TAG}" --name "$KIND_CLUSTER"
  fi
  # CRDs must be applied first — the Helm chart includes CRD-dependent resources
  # (ScheduleClock) that require CRDs to exist before the chart can be rendered. (#593)
  # ValidatingAdmissionPolicy is disabled here because it requires k8s 1.30+ (GA) or
  # the beta feature gate enabled on 1.28/1.29 kind clusters.
  "${KUBECTL[@]}" apply -f config/crd/bases/

  "${HELM[@]}" upgrade --install kardinal-promoter \
    chart/kardinal-promoter \
    --namespace kardinal-system --create-namespace \
    --set image.repository="$KARDINAL_IMAGE_REPO" \
    --set image.tag="$KARDINAL_IMAGE_TAG" \
    --set image.pullPolicy=Never \
    --set github.secretRef.name=github-token \
    --set validatingAdmissionPolicy.enabled=false \
    --wait --timeout 180s
fi

echo "[1/5] kro and kardinal-promoter installed."

# ── Step 2: Install ArgoCD ────────────────────────────────────────────────────
if [ "$SKIP_ARGOCD" != "1" ]; then
  echo ""
  echo "[2/5] Installing ArgoCD $ARGOCD_VERSION..."
  "${KUBECTL[@]}" create namespace argocd --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
  "${KUBECTL[@]}" apply -n argocd \
    -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml"

  echo "Waiting for ArgoCD to be ready..."
  "${KUBECTL[@]}" rollout status deployment/argocd-server -n argocd --timeout=180s
  # argocd-application-controller is a StatefulSet in ArgoCD v2.x, not a Deployment.
  "${KUBECTL[@]}" rollout status statefulset/argocd-application-controller -n argocd --timeout=180s
  echo "ArgoCD ready."
else
  echo "[2/5] Skipping ArgoCD (SKIP_ARGOCD=1)"
fi

# ── Step 3: Create application namespaces ─────────────────────────────────────
echo ""
echo "[3/5] Creating application namespaces (test / uat / prod)..."
for NS in kardinal-test-app-test kardinal-test-app-uat kardinal-test-app-prod; do
  "${KUBECTL[@]}" create namespace "$NS" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
  echo "  namespace/$NS ready"
done

# ── Step 4: Deploy the test application ───────────────────────────────────────
# With ArgoCD, the Applications in step 5 deploy kardinal-test-app from the
# kardinal-demo overlays — the same paths the quickstart Pipeline writes to.
# Without ArgoCD, deploy it directly so health.type=resource checks have a target.
if [ "$SKIP_ARGOCD" = "1" ]; then
  echo ""
  echo "[4/5] Deploying kardinal-test-app to all environments (no ArgoCD)..."
  for ENV in test uat prod; do
    NS="kardinal-test-app-${ENV}"
    cat <<EOF | "${KUBECTL[@]}" apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kardinal-test-app
  namespace: $NS
  labels:
    app: kardinal-test-app
    environment: $ENV
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kardinal-test-app
  template:
    metadata:
      labels:
        app: kardinal-test-app
        environment: $ENV
    spec:
      containers:
      - name: app
        image: $TEST_APP_IMAGE
        ports:
        - containerPort: 8080
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: kardinal-test-app
  namespace: $NS
spec:
  selector:
    app: kardinal-test-app
  ports:
  - port: 80
    targetPort: 8080
EOF
    echo "  kardinal-test-app deployed to $NS"
  done
else
  echo "[4/5] kardinal-test-app is deployed by the ArgoCD Applications (step 5)"
fi

# ── Step 5: Create ArgoCD Applications (if ArgoCD is installed) ───────────────
# One Application per environment, named <pipeline>-<env> as health.type=argocd
# expects, tracking the kardinal-demo overlay the quickstart Pipeline promotes
# into. The overlays hardcode namespace "default"; kustomize.namespace moves each
# environment into its own namespace so the three Applications do not collide.
if [ "$SKIP_ARGOCD" != "1" ]; then
  echo ""
  echo "[5/5] Creating ArgoCD Applications for each environment..."
  for ENV in test uat prod; do
    NS="kardinal-test-app-${ENV}"
    cat <<EOF | "${KUBECTL[@]}" apply -f -
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: kardinal-test-app-${ENV}
  namespace: argocd
  labels:
    app: kardinal-test-app
    environment: $ENV
    managed-by: kardinal-promoter
spec:
  project: default
  source:
    repoURL: https://github.com/pnz1990/kardinal-demo
    targetRevision: main
    path: environments/${ENV}
    kustomize:
      namespace: $NS
  destination:
    server: https://kubernetes.default.svc
    namespace: $NS
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
    - CreateNamespace=true
EOF
    echo "  ArgoCD Application kardinal-test-app-${ENV} created"
  done
else
  echo "[5/5] Skipping ArgoCD Applications (SKIP_ARGOCD=1)"
fi

echo ""
echo "=== E2E Environment Ready (context $CTX) ==="
echo ""
echo "Test app image: $TEST_APP_IMAGE"
echo "Namespaces:     kardinal-test-app-{test,uat,prod}"
if [ "$SKIP_ARGOCD" != "1" ]; then
  echo "ArgoCD:         kubectl --context $CTX port-forward svc/argocd-server -n argocd 8080:443"
fi
echo ""
echo "Next: apply examples/quickstart/pipeline.yaml and create a Bundle:"
echo "  kubectl --context $CTX apply -f examples/quickstart/pipeline.yaml"
echo "  kardinal --context $CTX create bundle kardinal-test-app --image ghcr.io/pnz1990/kardinal-test-app:sha-<SHA>"
