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
#
# Prerequisites: helm >= 3.8 (OCI), kubectl. The current kubectl context (or
# KUBECONFIG / KUBE_CONTEXT) must point to the target cluster.
#
# RBAC: kro runs in "aggregation" mode by default — it gets only its own
# permissions plus any ClusterRole labelled
# rbac.kro.run/aggregate-to-controller=true. The kardinal chart ships that
# ClusterRole (templates/graph-rbac.yaml). Graph children are applied by
# impersonating the Graph's spec.serviceAccountName, which the kardinal
# controller provisions.

set -euo pipefail

KRO_VERSION="${KRO_VERSION:-0.10.0-rc.0}"
KRO_NAMESPACE="${KRO_NAMESPACE:-kro-system}"
KRO_RBAC_MODE="${KRO_RBAC_MODE:-aggregation}"
KRO_CHART="${KRO_CHART:-oci://registry.k8s.io/kro/charts/kro}"
KRO_CRD_BASE="https://raw.githubusercontent.com/kubernetes-sigs/kro/v${KRO_VERSION}/helm/crds"

KUBECTL=(kubectl)
HELM=(helm)
if [ -n "${KUBE_CONTEXT:-}" ]; then
  KUBECTL+=(--context "$KUBE_CONTEXT")
  HELM+=(--kube-context "$KUBE_CONTEXT")
fi

echo "=== Installing kro v${KRO_VERSION} (Graph controller, rbac.mode=${KRO_RBAC_MODE}) ==="
echo "Target kube context: ${KUBE_CONTEXT:-$(kubectl config current-context 2>/dev/null || echo '<none>') (current)}"

# ── 1. Helm install with the GraphKind feature gate ───────────────────────────
"${HELM[@]}" upgrade --install kro "$KRO_CHART" \
  --version "$KRO_VERSION" \
  --namespace "$KRO_NAMESPACE" --create-namespace \
  --set "config.featureGates.GraphKind=true" \
  --set "rbac.mode=${KRO_RBAC_MODE}" \
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
