#!/usr/bin/env bash
# hack/e2e/components/spoke.sh
#
# The multi-cluster suite's workload cluster: creates the kind cluster
# $KIND_CLUSTER-spoke (test/e2e/kind-config.yaml's node image), installs Argo
# Rollouts and pulls podinfo (podinfo.sh) in it, and lets the hub ($KIND_CLUSTER) manage it the way
# docs/multi-cluster.md describes: Argo CD in the hub gets a cluster
# Secret for it, and Flux in the hub a kubeconfig Secret for Kustomizations'
# spec.kubeConfig.secretRef. kardinal gets nothing: it reads health from the
# hub's objects only. Run after argocd.sh and flux.sh. Idempotent.
#
# Both Secrets authenticate as the ServiceAccount kube-system/kardinal-e2e-hub
# in the spoke, bound to cluster-admin as `argocd cluster add` binds its
# argocd-manager. The hub reaches the spoke's API server by its node's name
# on the kind docker network (`kind get kubeconfig --internal`).
#
# Env set for the tests:
#   KARDINAL_E2E_SPOKE_CONTEXT  the spoke's kube context (kind-$KIND_CLUSTER-spoke)
#   KARDINAL_E2E_SPOKE_SERVER   the spoke's API server URL as the hub reaches it:
#                               the destination.server of Argo CD Applications
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"

HUB_CTX=$CTX
SPOKE=$KIND_CLUSTER-spoke
SPOKE_CTX=kind-$SPOKE
SPOKE_SERVER=https://$SPOKE-control-plane:6443
SA=kardinal-e2e-hub
# The names the tests use (test/e2e/framework/multicluster.go).
ARGO_CLUSTER=spoke
FLUX_SECRET=spoke-kubeconfig

# In the hub's kubeconfig (up.sh's KUBECONFIG), with the default node image.
NODE_IMAGE='' kind_cluster "$SPOKE"
KIND_CLUSTER=$SPOKE bash "$E2E_DIR/components/rollouts.sh"
# The fixtures' workloads run in the spoke.
KIND_CLUSTER=$SPOKE bash "$E2E_DIR/components/podinfo.sh"

use_kind_context "$SPOKE_CTX" >&2
SPOKE_KUBECTL=("${KUBECTL[@]}")
cat <<EOF | "${SPOKE_KUBECTL[@]}" apply -f - >/dev/null
apiVersion: v1
kind: ServiceAccount
metadata:
  name: $SA
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: $SA
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
  - kind: ServiceAccount
    name: $SA
    namespace: kube-system
---
apiVersion: v1
kind: Secret
metadata:
  name: $SA-token
  namespace: kube-system
  annotations:
    kubernetes.io/service-account.name: $SA
type: kubernetes.io/service-account-token
EOF
waited=0
until token=$("${SPOKE_KUBECTL[@]}" -n kube-system get secret "$SA-token" -o jsonpath='{.data.token}') && [ -n "$token" ]; do
  waited=$((waited + 2))
  [ "$waited" -lt 60 ] || die "the token controller did not fill kube-system/$SA-token in $SPOKE"
  sleep 2
done
token=$(printf '%s' "$token" | base64 -d)
ca=$("${SPOKE_KUBECTL[@]}" get --raw /api/v1/namespaces/kube-public/configmaps/kube-root-ca.crt |
  python3 -c 'import base64, json, sys; print(base64.b64encode(json.load(sys.stdin)["data"]["ca.crt"].encode()).decode())')

# The kubeconfig and the Argo CD cluster config hold the token: they go to
# SECRETS_DIR (0600) and reach kubectl on stdin or as files, never as
# arguments.
secret_put spoke-kubeconfig "apiVersion: v1
kind: Config
clusters:
  - name: $SPOKE
    cluster:
      server: $SPOKE_SERVER
      certificate-authority-data: $ca
users:
  - name: $SA
    user:
      token: $token
contexts:
  - name: $SPOKE
    context: {cluster: $SPOKE, user: $SA}
current-context: $SPOKE
"
secret_put spoke-argocd-config "{\"bearerToken\":\"$token\",\"tlsClientConfig\":{\"caData\":\"$ca\"}}"
unset token

target_cluster
kube_secret flux-system "$FLUX_SECRET" value spoke-kubeconfig
"${KUBECTL[@]}" -n argocd create secret generic "$ARGO_CLUSTER" \
  --from-literal=name="$ARGO_CLUSTER" --from-literal=server="$SPOKE_SERVER" \
  --from-file=config="$SECRETS_DIR/spoke-argocd-config" --dry-run=client -o yaml |
  "${KUBECTL[@]}" label --local -f - argocd.argoproj.io/secret-type=cluster -o yaml |
  "${KUBECTL[@]}" apply -f - >/dev/null

env_set KARDINAL_E2E_SPOKE_CONTEXT "$SPOKE_CTX"
env_set KARDINAL_E2E_SPOKE_SERVER "$SPOKE_SERVER"
log "spoke $SPOKE_CTX registered with Argo CD (cluster $ARGO_CLUSTER) and Flux (Secret flux-system/$FLUX_SECRET) in $HUB_CTX"
