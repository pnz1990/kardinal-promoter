#!/usr/bin/env bash
# hack/e2e/components/rollouts.sh
#
# Installs Argo Rollouts ROLLOUTS_RELEASE (release manifest) in namespace
# argo-rollouts. The delivery suite's Rollouts come from git through Argo CD,
# so no kubectl plugin or dashboard is installed. Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=argo-rollouts
MANIFEST="$E2E_OUT/argo-rollouts-$ROLLOUTS_RELEASE.yaml"
# The controller's imagePullPolicy is Always; IfNotPresent lets it start from
# the copy pull_images put on the node (registries rate-limit CI runners).
if [ ! -s "$MANIFEST" ]; then
  curl -fsSL --retry 5 -o "$MANIFEST.download" \
    "https://github.com/argoproj/argo-rollouts/releases/download/$ROLLOUTS_RELEASE/install.yaml"
  sed 's/imagePullPolicy: Always/imagePullPolicy: IfNotPresent/' "$MANIFEST.download" >"$MANIFEST.tmp"
  mv "$MANIFEST.tmp" "$MANIFEST"
fi
# Container images only: the CRD schemas have bare "image:" property keys.
mapfile -t images < <(awk '$1 == "-" { $1 = ""; $0 = $0 } $1 == "image:" && NF == 2 {print $2}' "$MANIFEST" | sort -u)
pull_images "${images[@]}"
"${KUBECTL[@]}" create namespace "$NS" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
"${KUBECTL[@]}" -n "$NS" apply --server-side --force-conflicts -f "$MANIFEST" >/dev/null
"${KUBECTL[@]}" -n "$NS" rollout status deploy/argo-rollouts --timeout=300s >/dev/null
log "Argo Rollouts $ROLLOUTS_RELEASE ready"
