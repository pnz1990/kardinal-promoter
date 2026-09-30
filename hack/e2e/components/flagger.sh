#!/usr/bin/env bash
# hack/e2e/components/flagger.sh
#
# Installs Flagger FLAGGER_RELEASE (Helm chart, CRDs included) in namespace
# flagger-system with the kubernetes provider: no service mesh, Flagger
# shifts traffic by scaling the primary and canary Deployments. No Prometheus
# is installed: the suite's Canaries have no metric checks, so each analysis
# step only needs the canary Deployment to be ready. Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=flagger-system
CHART=(flagger --repo https://flagger.app --version "${FLAGGER_RELEASE#v}" -n "$NS"
  --set meshProvider=kubernetes --set prometheus.install=false)
mapfile -t images < <("${HELM[@]}" template flagger "${CHART[@]}" |
  awk '$1 == "image:" {gsub(/"/, "", $2); print $2}' | sort -u)
pull_images "${images[@]}"
"${HELM[@]}" upgrade --install flagger "${CHART[@]}" --create-namespace --wait --timeout 5m >/dev/null
"${KUBECTL[@]}" -n "$NS" rollout status deploy/flagger --timeout=300s >/dev/null
log "Flagger $FLAGGER_RELEASE ready (meshProvider=kubernetes)"
