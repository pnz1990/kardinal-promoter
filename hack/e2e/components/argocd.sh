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
# The controllers read the reconciliation settings at start, so they go in
# before the install. The install's argocd-cm does not set these two keys, so
# its server-side apply leaves them to our field manager.
cm=(--from-literal=timeout.reconciliation=10s --from-literal=timeout.reconciliation.jitter=0s)
apply_cm() {
  "${KUBECTL[@]}" -n "$NS" create configmap argocd-cm "$@" \
    --dry-run=client -o yaml | "${KUBECTL[@]}" apply --server-side --force-conflicts \
    --field-manager=kardinal-e2e -f - >/dev/null
}
apply_cm "${cm[@]}"
MANIFEST="$E2E_OUT/argocd-$ARGOCD_RELEASE.yaml"
[ -s "$MANIFEST" ] || curl -fsSL --retry 5 -o "$MANIFEST" \
  "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGOCD_RELEASE/manifests/install.yaml"
# Redis comes from public.ecr.aws, which rate-limits CI runners (Dex is scaled
# to zero below).
mapfile -t images < <(awk '$1 == "image:" && $2 !~ /dex/ {print $2}' "$MANIFEST" | sort -u)
pull_images "${images[@]}"
# Server-side: the Application CRD is too large for client-side apply.
"${KUBECTL[@]}" -n "$NS" apply --server-side --force-conflicts -f "$MANIFEST" >/dev/null
"${KUBECTL[@]}" -n "$NS" scale deploy/argocd-dex-server --replicas=0 >/dev/null
# KARDINAL_E2E_ARGOCD_EXCLUDE_KARDINAL=1 (the scale suite) keeps kardinal's
# and kro's kinds out of Argo CD's resource tracking: the application
# controller would otherwise watch and cache every PromotionStep, PolicyGate,
# AuditEvent and Graph the suite makes, thousands of them, though no
# Application deploys them. The release's argocd-cm sets resource.exclusions
# itself, and the install above (--force-conflicts) takes that key back, so
# this runs after it, appends our groups to the shipped list, and restarts the
# application controller to load it.
if [ "${KARDINAL_E2E_ARGOCD_EXCLUDE_KARDINAL:-0}" = 1 ]; then
  groups='"kardinal.io", "internal.kro.run", "kro.run"'
  shipped=$("${KUBECTL[@]}" -n "$NS" get cm argocd-cm -o jsonpath='{.data.resource\.exclusions}')
  apply_cm "${cm[@]}" --from-literal=resource.exclusions="${shipped%$'\n'}
- apiGroups: [$groups]
  kinds: [\"*\"]
  clusters: [\"*\"]"
  got=$("${KUBECTL[@]}" -n "$NS" get cm argocd-cm -o jsonpath='{.data.resource\.exclusions}')
  case "$got" in
    *"- apiGroups: [$groups]"*) ;;
    *) die "argocd-cm resource.exclusions does not exclude $groups after the merge: $got" ;;
  esac
  "${KUBECTL[@]}" -n "$NS" rollout restart statefulset/argocd-application-controller >/dev/null
fi
"${KUBECTL[@]}" -n "$NS" rollout status statefulset/argocd-application-controller --timeout=300s >/dev/null
for d in argocd-repo-server argocd-server argocd-redis; do
  "${KUBECTL[@]}" -n "$NS" rollout status "deploy/$d" --timeout=300s >/dev/null
done
log "Argo CD $ARGOCD_RELEASE ready"
