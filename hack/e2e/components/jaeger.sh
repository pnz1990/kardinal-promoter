#!/usr/bin/env bash
# hack/e2e/components/jaeger.sh
#
# Installs Jaeger (JAEGER_IMAGE, versions.env; the all-in-one with in-memory
# storage) in namespace tracing: the OTLP/HTTP receiver the chart suite's
# TestChart_Tracing points tracing.endpoint at, and the query API it reads
# the spans back from through a host NodePort. Idempotent.
#
# Env file:
#   KARDINAL_E2E_JAEGER_OTLP  OTLP/HTTP endpoint, in-cluster
#   KARDINAL_E2E_JAEGER_API   the query API from the host (NodePort)
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=tracing
pull_images "$JAEGER_IMAGE"

cat <<EOT | "${KUBECTL[@]}" apply -f - >/dev/null
apiVersion: v1
kind: Namespace
metadata:
  name: $NS
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: jaeger
  namespace: $NS
spec:
  replicas: 1
  selector: {matchLabels: {app: jaeger}}
  template:
    metadata:
      labels: {app: jaeger}
    spec:
      containers:
        - name: jaeger
          image: $JAEGER_IMAGE
          imagePullPolicy: IfNotPresent
          env:
            # Jaeger v2 listens on localhost unless told otherwise.
            - {name: JAEGER_LISTEN_HOST, value: 0.0.0.0}
          ports:
            - {name: otlp-http, containerPort: 4318}
            - {name: query, containerPort: 16686}
          readinessProbe:
            httpGet: {path: /, port: query}
            periodSeconds: 2
          resources:
            requests: {cpu: 50m, memory: 128Mi}
            limits: {memory: 1Gi}
---
apiVersion: v1
kind: Service
metadata:
  name: jaeger
  namespace: $NS
spec:
  type: NodePort
  selector: {app: jaeger}
  ports:
    - {name: otlp-http, port: 4318, targetPort: otlp-http}
    - {name: query, port: 16686, targetPort: query}
EOT
"${KUBECTL[@]}" -n "$NS" rollout status deploy/jaeger --timeout=180s >/dev/null
BASE="http://$(node_ip):$(nodeport "$NS" jaeger query)"
wait_http "$BASE/api/services" 60 || die "Jaeger query API not reachable at $BASE"

env_set KARDINAL_E2E_JAEGER_OTLP "http://jaeger.$NS.svc.cluster.local:4318"
env_set KARDINAL_E2E_JAEGER_API "$BASE"
log "Jaeger query API at $BASE"
