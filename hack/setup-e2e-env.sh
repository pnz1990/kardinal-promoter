#!/usr/bin/env bash
# hack/setup-e2e-env.sh
#
# Sets up a complete kardinal-promoter E2E environment on a kind cluster.
# This goes beyond unit tests — it creates a real environment where:
#   - krocodile Graph controller runs and processes real Graph CRs
#   - ArgoCD runs and syncs real Application resources
#   - kardinal-test-app is deployed across test/uat/prod namespaces
#   - kardinal-promoter can run the full promotion loop end-to-end
#
# Usage:
#   ./hack/setup-e2e-env.sh                 # full setup
#   SKIP_ARGOCD=1 ./hack/setup-e2e-env.sh  # skip ArgoCD (faster, for unit testing)
#
# Prerequisites: git, go, docker, kubectl, kind, helm

set -euo pipefail

KIND_CLUSTER="${KIND_CLUSTER:-kardinal-e2e}"
KROCODILE_COMMIT="${KROCODILE_COMMIT:-501ea75f}"
ARGOCD_VERSION="${ARGOCD_VERSION:-v2.10.3}"
TEST_APP_IMAGE="${TEST_APP_IMAGE:-ghcr.io/pnz1990/kardinal-test-app:latest}"
SKIP_ARGOCD="${SKIP_ARGOCD:-0}"

echo "=== kardinal-promoter E2E Environment Setup ==="
echo "Cluster: $KIND_CLUSTER | krocodile: $KROCODILE_COMMIT | ArgoCD: $ARGOCD_VERSION"

# ── Step 1: Install krocodile Graph controller ────────────────────────────────
echo ""
echo "[1/5] Installing krocodile Graph controller..."
KROCODILE_COMMIT=$KROCODILE_COMMIT KIND_CLUSTER=$KIND_CLUSTER bash hack/install-krocodile.sh

# ── Step 2: Install ArgoCD ────────────────────────────────────────────────────
if [ "$SKIP_ARGOCD" != "1" ]; then
  echo ""
  echo "[2/5] Installing ArgoCD $ARGOCD_VERSION..."
  kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -n argocd \
    -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_VERSION/manifests/install.yaml"

  echo "Waiting for ArgoCD to be ready..."
  kubectl rollout status deployment/argocd-server -n argocd --timeout=180s
  kubectl rollout status deployment/argocd-application-controller -n argocd --timeout=180s
  echo "ArgoCD ready."
else
  echo "[2/5] Skipping ArgoCD (SKIP_ARGOCD=1)"
fi

# ── Step 3: Create application namespaces ─────────────────────────────────────
echo ""
echo "[3/5] Creating application namespaces (test / uat / prod)..."
for NS in kardinal-test-app-test kardinal-test-app-uat kardinal-test-app-prod; do
  kubectl create namespace $NS --dry-run=client -o yaml | kubectl apply -f -
  echo "  namespace/$NS ready"
done

# ── Step 4: Deploy initial test application in each namespace ─────────────────
echo ""
echo "[4/5] Deploying initial kardinal-test-app to all environments..."
for ENV in test uat prod; do
  NS="kardinal-test-app-${ENV}"
  cat <<EOF | kubectl apply -f -
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
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 10
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

# ── Step 5: Create ArgoCD Applications (if ArgoCD is installed) ───────────────
if [ "$SKIP_ARGOCD" != "1" ]; then
  echo ""
  echo "[5/5] Creating ArgoCD Applications for each environment..."
  for ENV in test uat prod; do
    NS="kardinal-test-app-${ENV}"
    cat <<EOF | kubectl apply -f -
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
    repoURL: https://github.com/pnz1990/kardinal-test-app
    targetRevision: HEAD
    path: .
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
echo "=== E2E Environment Ready ==="
echo ""
echo "Test app image: $TEST_APP_IMAGE"
echo "Namespaces:     kardinal-test-app-{test,uat,prod}"
if [ "$SKIP_ARGOCD" != "1" ]; then
echo "ArgoCD:         kubectl port-forward svc/argocd-server -n argocd 8080:443"
fi
echo ""
echo "Next: apply examples/quickstart/pipeline.yaml and create a Bundle:"
echo "  kubectl apply -f examples/quickstart/pipeline.yaml"
echo "  kardinal create bundle test-app --image ghcr.io/pnz1990/kardinal-test-app:sha-<SHA>"
