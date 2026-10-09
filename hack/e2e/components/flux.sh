#!/usr/bin/env bash
# hack/e2e/components/flux.sh
#
# Installs Flux FLUX_RELEASE (release manifests) in namespace flux-system.
# Only source-controller and kustomize-controller run: tests use
# GitRepositories and Kustomizations, so the other controllers are scaled to
# zero, and the two that run do not send events to notification-controller.
# Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=flux-system
MANIFEST="$E2E_OUT/flux-$FLUX_RELEASE.yaml"
[ -s "$MANIFEST" ] || curl -fsSL --retry 5 -o "$MANIFEST" \
  "https://github.com/fluxcd/flux2/releases/download/$FLUX_RELEASE/install.yaml"
mapfile -t images < <(awk '$1 == "image:" && $2 ~ /(source|kustomize)-controller/ {print $2}' "$MANIFEST" | sort -u)
pull_images "${images[@]}"
# Server-side: the Flux CRDs are too large for client-side apply. Without
# --events-addr the controllers do not post events to the scaled-down
# notification-controller: each post blocks a reconcile for about 40s.
# --concurrent replaces it: the default of 4 workers starves a suite of
# parallel tests. Every Kustomization with healthChecks holds a worker for at
# least one 5s health poll, and one waiting on a broken rollout holds it until
# its timeout, so a reconcile a test requested waited behind them for tens of
# seconds (TestFlux_BakeSurvivesFluxReconcile saw one reconcile in a minute).
sed 's/^\( *\)- --events-addr=.*/\1- --concurrent=20/' "$MANIFEST" | "${KUBECTL[@]}" apply --server-side --force-conflicts -f - >/dev/null
for d in helm-controller notification-controller image-automation-controller \
  image-reflector-controller source-watcher; do
  if "${KUBECTL[@]}" -n "$NS" get "deploy/$d" >/dev/null 2>&1; then
    "${KUBECTL[@]}" -n "$NS" scale "deploy/$d" --replicas=0 >/dev/null
  fi
done
for d in source-controller kustomize-controller; do
  "${KUBECTL[@]}" -n "$NS" rollout status "deploy/$d" --timeout=300s >/dev/null
done
log "Flux $FLUX_RELEASE ready"
