#!/usr/bin/env bash
# hack/e2e/components/ciapi.sh
#
# Sets up the controller's Bundle API (POST /api/v1/bundles) the way
# docs/ci-integration.md tells users to: a generated token in Secret
# kardinal-system/bundle-api-token (key token), which components/kardinal.sh
# passes as bundleAPI.tokenSecretRef.name. Adds NodePort Service
# kardinal-e2e-webhook to the controller's webhook port (8083: the Bundle API
# and /webhook/scm/health), so tests call it from the host like a CI runner.
# Run it before kardinal.sh. Idempotent.
#
# Env file:
#   KARDINAL_E2E_BUNDLE_TOKEN    the Bundle API token
#   KARDINAL_E2E_CONTROLLER_URL  the controller's webhook port from the host
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

secret_gen bundle-api-token 32 >/dev/null
kube_secret "$KARDINAL_NS" bundle-api-token token bundle-api-token

cat <<EOF | "${KUBECTL[@]}" apply -f - >/dev/null
apiVersion: v1
kind: Service
metadata:
  name: kardinal-e2e-webhook
  namespace: $KARDINAL_NS
spec:
  type: NodePort
  # The chart's selector labels: the controller pods of release $KARDINAL_RELEASE.
  selector:
    app.kubernetes.io/name: kardinal-promoter
    app.kubernetes.io/instance: $KARDINAL_RELEASE
  ports: [{name: webhook, port: 8083, targetPort: webhook}]
EOF

env_set KARDINAL_E2E_BUNDLE_TOKEN "$(secret_get bundle-api-token)"
env_set KARDINAL_E2E_CONTROLLER_URL "http://$(node_ip):$(nodeport "$KARDINAL_NS" kardinal-e2e-webhook webhook)"
log "Bundle API token in $KARDINAL_NS/bundle-api-token; controller webhook port on the host via NodePort"
