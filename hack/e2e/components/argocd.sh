#!/usr/bin/env bash
# hack/e2e/components/argocd.sh
#
# Installs Argo CD ARGOCD_RELEASE (release manifests) in namespace argocd and
# makes it poll git every 10s without jitter, so a merged commit syncs within
# a test timeout. Dex is scaled to zero: tests use Applications, not SSO.
# Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=argocd
"${KUBECTL[@]}" create namespace "$NS" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
# Server-side: the Application CRD is too large for client-side apply.
"${KUBECTL[@]}" -n "$NS" apply --server-side --force-conflicts \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_RELEASE/manifests/install.yaml" >/dev/null
"${KUBECTL[@]}" -n "$NS" patch configmap argocd-cm --type merge \
  -p '{"data":{"timeout.reconciliation":"10s","timeout.reconciliation.jitter":"0s"}}' >/dev/null
"${KUBECTL[@]}" -n "$NS" scale deploy/argocd-dex-server --replicas=0 >/dev/null
# The controllers read the reconciliation settings at start.
"${KUBECTL[@]}" -n "$NS" rollout restart statefulset/argocd-application-controller deploy/argocd-repo-server >/dev/null
"${KUBECTL[@]}" -n "$NS" rollout status statefulset/argocd-application-controller --timeout=300s >/dev/null
for d in argocd-repo-server argocd-server argocd-redis; do
  "${KUBECTL[@]}" -n "$NS" rollout status "deploy/$d" --timeout=300s >/dev/null
done
log "Argo CD $ARGOCD_RELEASE ready"
