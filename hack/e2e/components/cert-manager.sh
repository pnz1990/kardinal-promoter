#!/usr/bin/env bash
# hack/e2e/components/cert-manager.sh
#
# Installs cert-manager CERT_MANAGER_RELEASE (release manifest) and a
# self-signed ClusterIssuer, so tests can issue the controller's TLS
# certificate the way docs/guides/security.md shows. Idempotent.
#
# Env set for the tests:
#   KARDINAL_E2E_CERT_ISSUER  the ClusterIssuer's name
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

ISSUER=e2e-selfsigned
MANIFEST="$E2E_OUT/cert-manager-$CERT_MANAGER_RELEASE.yaml"
[ -s "$MANIFEST" ] || curl -fsSL --retry 5 -o "$MANIFEST" \
  "https://github.com/cert-manager/cert-manager/releases/download/$CERT_MANAGER_RELEASE/cert-manager.yaml"
# Pre-pull with retries: shared CI runners get rate-limited by registries.
mapfile -t images < <(awk '$1 == "image:" {gsub(/"/, "", $2); print $2}' "$MANIFEST" | sort -u)
pull_images "${images[@]}"
"${KUBECTL[@]}" apply --server-side --force-conflicts -f "$MANIFEST" >/dev/null
for d in cert-manager cert-manager-cainjector cert-manager-webhook; do
  "${KUBECTL[@]}" -n cert-manager rollout status "deploy/$d" --timeout=300s >/dev/null
done
# The webhook serves only after cainjector has written its CA bundle, so the
# first creates can fail for a few seconds.
waited=0
until cat <<EOF | "${KUBECTL[@]}" apply -f - >/dev/null 2>&1; do
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: $ISSUER
spec:
  selfSigned: {}
EOF
  waited=$((waited + 3))
  [ "$waited" -ge 120 ] && die "cert-manager webhook not ready after 120s"
  sleep 3
done
"${KUBECTL[@]}" wait --for=condition=Ready "clusterissuer/$ISSUER" --timeout=60s >/dev/null
env_set KARDINAL_E2E_CERT_ISSUER "$ISSUER"
log "cert-manager $CERT_MANAGER_RELEASE ready (ClusterIssuer $ISSUER)"
