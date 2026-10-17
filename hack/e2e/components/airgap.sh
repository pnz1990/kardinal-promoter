#!/usr/bin/env bash
# hack/e2e/components/airgap.sh
#
# The evidence-airgap suite's network: NetworkPolicy airgap in the controller
# namespace lets its Pods reach only the cluster (the Pod and Service CIDRs:
# DNS, the git server, the in-cluster registry, Argo CD) and the API server
# on each node's :6443. Nothing outside the cluster, so the public Sigstore
# TUF repository, Fulcio and Rekor are out of reach. kindnet enforces it.
# Run after components/kardinal.sh. Idempotent.
#
# Env file:
#   KARDINAL_E2E_AIRGAP_PROBE_IMAGE  an image with a shell and wget, on the
#                                    node, for the tests' egress probes
#   KARDINAL_E2E_AIRGAP_NODE_IP      a node address outside the allowed
#                                    CIDRs that the probes try (kubelet :10250)
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

pod_cidr=$("${KUBECTL[@]}" get nodes -o jsonpath='{.items[0].spec.podCIDR}')
pod_cidr=${pod_cidr:-10.244.0.0/16}
# kind's default Service CIDR; the kubernetes Service's address proves it.
svc_ip=$("${KUBECTL[@]}" -n default get svc kubernetes -o jsonpath='{.spec.clusterIP}')
case $svc_ip in 10.96.*) svc_cidr=10.96.0.0/12 ;; *) die "kubernetes Service at $svc_ip: not kind's 10.96.0.0/12" ;; esac
pod_net=${pod_cidr%.*.*/*}.0.0/16

nodes=()
while read -r ip; do nodes+=("$ip"); done < <("${KUBECTL[@]}" get nodes \
  -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}')
[ "${#nodes[@]}" -gt 0 ] || die "no node InternalIP"

{
  cat <<YAML
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: airgap
  namespace: $KARDINAL_NS
spec:
  podSelector: {}
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock: {cidr: $pod_net}
        - ipBlock: {cidr: $svc_cidr}
    - ports: [{port: 6443, protocol: TCP}]
      to:
YAML
  for ip in "${nodes[@]}"; do
    echo "        - ipBlock: {cidr: $ip/32}"
  done
} | "${KUBECTL[@]}" apply -f - >/dev/null

pull_images "$REGISTRY_IMAGE"
env_set KARDINAL_E2E_AIRGAP_PROBE_IMAGE "$REGISTRY_IMAGE"
env_set KARDINAL_E2E_AIRGAP_NODE_IP "${nodes[0]}"
log "airgap: $KARDINAL_NS egress limited to $pod_net, $svc_cidr and ${nodes[*]} :6443"
