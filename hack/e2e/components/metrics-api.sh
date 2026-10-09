#!/usr/bin/env bash
# hack/e2e/components/metrics-api.sh
#
# Installs the fake metrics APIs (hack/e2e/metricsapi) in namespace
# metrics-api: stand-ins for Datadog, New Relic NerdGraph, CloudWatch
# GetMetricData and generic JSON web endpoints that MetricChecks query. They
# check credentials as the real services do (API keys, a SigV4 signature)
# and answer with values tests set through the API server's service proxy.
# Built from this checkout on the host (standard library only) into a FROM
# scratch image, so nothing is pulled. Idempotent.
#
# Env file:
#   KARDINAL_E2E_METRICSAPI_URL  base URL for MetricChecks, in-cluster
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=metrics-api
IMAGE="kardinal-e2e/metrics-api:$KIND_CLUSTER"

ctx=$(mktemp -d)
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -ldflags="-s -w" \
  -o "$ctx/metricsapi" ./hack/e2e/metricsapi)
chmod 0755 "$ctx" "$ctx/metricsapi"
printf 'FROM scratch\nCOPY metricsapi /metricsapi\nUSER 65532:65532\nENTRYPOINT ["/metricsapi"]\n' >"$ctx/Dockerfile"
docker build -q -t "$IMAGE" "$ctx" >/dev/null
load_image "$IMAGE"

cat <<EOT | "${KUBECTL[@]}" apply -f - >/dev/null
apiVersion: v1
kind: Namespace
metadata:
  name: $NS
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: metrics-api
  namespace: $NS
spec:
  replicas: 1
  selector: {matchLabels: {app: metrics-api}}
  template:
    metadata:
      labels: {app: metrics-api}
    spec:
      containers:
        - name: metrics-api
          image: $IMAGE
          imagePullPolicy: Never
          ports: [{name: http, containerPort: 8080}]
          readinessProbe:
            httpGet: {path: /_healthz, port: http}
            periodSeconds: 2
          resources:
            requests: {cpu: 10m, memory: 16Mi}
            limits: {memory: 256Mi}
---
apiVersion: v1
kind: Service
metadata:
  name: metrics-api
  namespace: $NS
spec:
  selector: {app: metrics-api}
  ports: [{name: http, port: 8080, targetPort: http}]
EOT
# The image tag does not change between builds; restart to pick up a rebuild.
"${KUBECTL[@]}" -n "$NS" rollout restart deploy/metrics-api >/dev/null
"${KUBECTL[@]}" -n "$NS" rollout status deploy/metrics-api --timeout=120s >/dev/null

env_set KARDINAL_E2E_METRICSAPI_URL "http://metrics-api.$NS.svc.cluster.local:8080"
log "fake metrics APIs at http://metrics-api.$NS.svc.cluster.local:8080"
