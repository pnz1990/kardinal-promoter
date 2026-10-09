#!/usr/bin/env bash
# hack/install-kro.sh
#
# Installs upstream kro (kubernetes-sigs/kro) with the Graph controller
# (kro.run/v1alpha1 Graph, GraphKind feature gate) into the current cluster.
# kardinal builds one Graph per Bundle; kro reconciles it.
#
# Usage:
#   ./hack/install-kro.sh                        # pinned version
#   KRO_VERSION=0.10.0 ./hack/install-kro.sh     # override version
#   KRO_RBAC_MODE=unrestricted ./hack/install-kro.sh
#   KRO_GRAPH_CONCURRENT_RECONCILES=16 ./hack/install-kro.sh
#
# Prerequisites: Kubernetes 1.30 or later (the script checks first), helm >= 3.8
# (OCI), kubectl. The current kubectl context (or KUBECONFIG / KUBE_CONTEXT)
# must point to the target cluster.
#
# RBAC: kro runs in "aggregation" mode by default — it gets only its own
# permissions plus any ClusterRole labelled
# rbac.kro.run/aggregate-to-controller=true. The kardinal chart ships that
# ClusterRole (templates/graph-rbac.yaml). Graph children are applied by
# impersonating the Graph's spec.serviceAccountName, which the kardinal
# controller provisions.
#
# Tuning: kro reconciles Graphs one at a time by default
# (--graph-concurrent-reconciles=1), and every reconcile makes about three API
# calls per object the Graph applies. One large Graph then delays every other
# Graph in the cluster by seconds (4-29 s measured with two 150-environment
# Graphs; 0.1-0.3 s with 8 workers). The script raises the worker count and the
# client rate limit; see docs/design/16-graph-capability-ledger.md gap G9.

set -euo pipefail

KRO_VERSION="${KRO_VERSION:-0.10.0-rc.0}"
# cel-go version in kro's go.mod at KRO_VERSION. kardinal's Graphs rely on kro
# classifying cel-go's "index out of bounds" error as data-pending (ledger gap
# G1, resolvableWhen), and kardinal's unit tests check that text with
# kardinal's own cel-go. test/hack TestCelGoParity fails when go.mod's cel-go
# differs from this. On a kro upgrade, copy it from kro's go.mod at the new tag.
KRO_CEL_GO_VERSION="v0.31.0"
KRO_NAMESPACE="${KRO_NAMESPACE:-kro-system}"
KRO_RBAC_MODE="${KRO_RBAC_MODE:-aggregation}"
KRO_CHART="${KRO_CHART:-oci://registry.k8s.io/kro/charts/kro}"
KRO_GRAPH_CONCURRENT_RECONCILES="${KRO_GRAPH_CONCURRENT_RECONCILES:-8}"
KRO_CLIENT_QPS="${KRO_CLIENT_QPS:-300}"
KRO_CLIENT_BURST="${KRO_CLIENT_BURST:-500}"
KRO_CRD_BASE="https://raw.githubusercontent.com/kubernetes-sigs/kro/v${KRO_VERSION}/helm/crds"

KUBECTL=(kubectl)
HELM=(helm)
if [ -n "${KUBE_CONTEXT:-}" ]; then
  KUBECTL+=(--context "$KUBE_CONTEXT")
  HELM+=(--kube-context "$KUBE_CONTEXT")
fi

echo "=== Installing kro v${KRO_VERSION} (Graph controller, rbac.mode=${KRO_RBAC_MODE}, ${KRO_GRAPH_CONCURRENT_RECONCILES} Graph workers, client QPS ${KRO_CLIENT_QPS}/${KRO_CLIENT_BURST}) ==="
echo "Target kube context: ${KUBE_CONTEXT:-$(kubectl config current-context 2>/dev/null || echo '<none>') (current)}"

# ── 0. Kubernetes version ─────────────────────────────────────────────────────
# kro's graphrevisions.internal.kro.run CRD declares selectableFields, which
# Kubernetes accepts from 1.30. Check before installing anything. Managed
# clusters report minors like "30+" (EKS, GKE); only the digits count.
if ! version_json=$("${KUBECTL[@]}" version -o json); then
  echo "install-kro.sh: cannot read the Kubernetes server version (kubectl version failed)" >&2
  exit 1
fi
server_major="" server_minor=""
if [[ $version_json == *'"serverVersion"'* ]]; then
  server_json=${version_json#*\"serverVersion\"}
  major_re='"major":[[:space:]]*"([0-9]+)'
  minor_re='"minor":[[:space:]]*"([0-9]+)'
  git_re='"gitVersion":[[:space:]]*"v([0-9]+)\.([0-9]+)'
  if [[ $server_json =~ $major_re ]]; then
    server_major=${BASH_REMATCH[1]}
    if [[ $server_json =~ $minor_re ]]; then
      server_minor=${BASH_REMATCH[1]}
    fi
  fi
  if [ -z "$server_minor" ] && [[ $server_json =~ $git_re ]]; then
    server_major=${BASH_REMATCH[1]} server_minor=${BASH_REMATCH[2]}
  fi
fi
if [ -z "$server_minor" ]; then
  echo "install-kro.sh: cannot read the Kubernetes server version from kubectl version -o json" >&2
  exit 1
fi
if (( 10#$server_major < 1 || (10#$server_major == 1 && 10#$server_minor < 30) )); then
  echo "install-kro.sh: Kubernetes ${server_major}.${server_minor} is not supported: kro's CRDs need 1.30 or later (CRD selectableFields). Upgrade the cluster first." >&2
  exit 1
fi
echo "Kubernetes server: ${server_major}.${server_minor}"

# ── 1. Helm install with the GraphKind feature gate ───────────────────────────
"${HELM[@]}" upgrade --install kro "$KRO_CHART" \
  --version "$KRO_VERSION" \
  --namespace "$KRO_NAMESPACE" --create-namespace \
  --set "config.featureGates.GraphKind=true" \
  --set "rbac.mode=${KRO_RBAC_MODE}" \
  --set "config.graphConcurrentReconciles=${KRO_GRAPH_CONCURRENT_RECONCILES}" \
  --set "config.clientQps=${KRO_CLIENT_QPS}" \
  --set "config.clientBurst=${KRO_CLIENT_BURST}" \
  --wait --timeout 180s

# ── 2. CRDs ───────────────────────────────────────────────────────────────────
# Helm installs crds/ only on first install and never upgrades them. Apply them
# server-side so upgrades pick up schema changes (the Graph CRD is too large
# for client-side apply's last-applied annotation).
for crd in kro.run_graphs.yaml internal.kro.run_graphrevisions.yaml kro.run_resourcegraphdefinitions.yaml; do
  "${KUBECTL[@]}" apply --server-side --force-conflicts -f "${KRO_CRD_BASE}/${crd}"
done
"${KUBECTL[@]}" wait --for=condition=Established crd/graphs.kro.run --timeout=60s

# ── 3. Wait for the controller ────────────────────────────────────────────────
"${KUBECTL[@]}" -n "$KRO_NAMESPACE" rollout status deployment/kro --timeout=180s

echo "=== kro v${KRO_VERSION} installed (graphs.kro.run served) ==="
