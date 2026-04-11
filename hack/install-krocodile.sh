#!/usr/bin/env bash
# hack/install-krocodile.sh
#
# Builds and installs the krocodile Graph controller into the current cluster.
# Pinned to a specific commit for reproducibility.
#
# Usage:
#   ./hack/install-krocodile.sh                 # use default pinned commit
#   KROCODILE_COMMIT=abc1234 ./hack/install-krocodile.sh  # override commit
#
# Prerequisites: git, go, docker, kubectl, kind (for kind clusters)
# The current kubectl context must point to the target cluster.

set -euo pipefail

# ── Pinned version ─────────────────────────────────────────────────────────────
# Update this when intentionally upgrading krocodile.
# Minimum required: 1b0ce353 (fixes double-dispatch race in DAG coordinator)
# Last verified:    e205934b (2026-04-11 — redesign graph reconciliation)
KROCODILE_REPO="https://github.com/ellistarn/kro.git"
KROCODILE_COMMIT="${KROCODILE_COMMIT:-e205934b}"
KROCODILE_IMAGE="krocodile-graph-controller:${KROCODILE_COMMIT}"
KIND_CLUSTER="${KIND_CLUSTER:-kardinal-e2e}"

echo "=== Installing krocodile Graph controller (commit: ${KROCODILE_COMMIT}) ==="

# ── 1. Clone krocodile at pinned commit ────────────────────────────────────────
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "[krocodile] Cloning krocodile..."
git clone --quiet --no-tags --depth=10 "$KROCODILE_REPO" "$TMPDIR/kro" -b krocodile 2>/dev/null
git -C "$TMPDIR/kro" checkout --quiet "$KROCODILE_COMMIT"
echo "[krocodile] Checked out commit ${KROCODILE_COMMIT}."

# ── 2. Build the controller binary ────────────────────────────────────────────
echo "[krocodile] Building controller binary..."
# Patch go.mod to build with local toolchain if needed
LOCAL_GO_VERSION=$(GOTOOLCHAIN=local go version 2>/dev/null | awk '{print $3}' | sed 's/go//')
REQUIRED_VERSION=$(grep '^go ' "$TMPDIR/kro/go.mod" | awk '{print $2}')
if [ -n "$LOCAL_GO_VERSION" ] && [ -n "$REQUIRED_VERSION" ]; then
  # Compare: if local < required, patch go.mod
  if printf '%s\n%s' "$LOCAL_GO_VERSION" "$REQUIRED_VERSION" | sort -V | head -1 | grep -q "^${LOCAL_GO_VERSION}$"; then
    sed -i.bak "s/^go ${REQUIRED_VERSION}/go ${LOCAL_GO_VERSION}/" "$TMPDIR/kro/go.mod"
    echo "[krocodile] Patched go.mod: go ${REQUIRED_VERSION} → ${LOCAL_GO_VERSION}"
  fi
fi
(cd "$TMPDIR/kro" && \
  GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-w -s" -o bin/graph-controller ./experimental/cmd/)
echo "[krocodile] Binary built."

# ── 3. Build a minimal container image ────────────────────────────────────────
echo "[krocodile] Building container image ${KROCODILE_IMAGE}..."
cat > "$TMPDIR/Dockerfile" << 'EOF'
FROM gcr.io/distroless/static:nonroot
COPY kro/bin/graph-controller /graph-controller
USER nonroot:nonroot
ENTRYPOINT ["/graph-controller"]
EOF
docker build --quiet -t "$KROCODILE_IMAGE" -f "$TMPDIR/Dockerfile" "$TMPDIR"
echo "[krocodile] Image built."

# ── 4. Load image into kind (skip if not a kind cluster) ──────────────────────
if kind get clusters 2>/dev/null | grep -q "^${KIND_CLUSTER}$"; then
  echo "[krocodile] Loading image into kind cluster ${KIND_CLUSTER}..."
  kind load docker-image "$KROCODILE_IMAGE" --name "$KIND_CLUSTER"
  echo "[krocodile] Image loaded."
fi

# ── 5. Apply CRDs, RBAC, and Deployment ───────────────────────────────────────
echo "[krocodile] Applying CRDs and RBAC..."
kubectl create namespace kro-system --dry-run=client -o yaml | kubectl apply -f -

# Apply CRDs from the pinned commit's deploy directory
kubectl apply -f "$TMPDIR/kro/experimental/deploy/experimental.kro.run_graphs.yaml"
kubectl apply -f "$TMPDIR/kro/experimental/deploy/experimental.kro.run_graphrevisions.yaml"
kubectl apply -f "$TMPDIR/kro/experimental/deploy/rbac.yaml"

# Apply the Deployment with our locally-built image
echo "[krocodile] Deploying Graph controller..."
sed "s|ko://github.com/kubernetes-sigs/kro/experimental/cmd|${KROCODILE_IMAGE}|g" \
  "$TMPDIR/kro/experimental/deploy/deployment.yaml" | \
  kubectl apply -f -

# ── 6. Wait for the controller to be ready ────────────────────────────────────
echo "[krocodile] Waiting for Graph controller to be ready..."
kubectl rollout status deployment/graph-controller -n kro-system --timeout=120s
echo "[krocodile] Graph controller is ready."

# ── 7. Verify CRDs are installed ──────────────────────────────────────────────
kubectl get crd graphs.experimental.kro.run -o name
kubectl get crd graphrevisions.experimental.kro.run -o name
echo "=== krocodile Graph controller installed successfully (commit: ${KROCODILE_COMMIT}) ==="
