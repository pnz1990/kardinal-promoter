#!/usr/bin/env bash
# hack/e2e/components/prometheus.sh
#
# Installs Prometheus Operator PROMETHEUS_OPERATOR_RELEASE (bundle.yaml,
# operator in namespace default) and, in namespace monitoring, a Prometheus
# PROMETHEUS_IMAGE (Service prometheus:9090) and a Pushgateway
# PUSHGATEWAY_IMAGE (Service pushgateway:9091).
#
# Prometheus selects every ServiceMonitor and PrometheusRule in every
# namespace, so the chart's serviceMonitor and prometheusRule are loaded.
# It scrapes and evaluates every 5s. Tests set metric values by pushing to the
# Pushgateway, which Prometheus scrapes with honorLabels. Run it before
# components/kardinal.sh: the chart's ServiceMonitor needs the CRDs.
# Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=monitoring
MANIFEST="$E2E_OUT/prometheus-operator-$PROMETHEUS_OPERATOR_RELEASE.yaml"
[ -s "$MANIFEST" ] || curl -fsSL --retry 5 -o "$MANIFEST" \
  "https://github.com/prometheus-operator/prometheus-operator/releases/download/$PROMETHEUS_OPERATOR_RELEASE/bundle.yaml"
# The operator, its config reloader (an argument of the operator), Prometheus
# and the Pushgateway.
mapfile -t images < <({
  awk '$1 == "image:" && $2 ~ /:/ {print $2}' "$MANIFEST"
  grep -o -- '--prometheus-config-reloader=[^" ]*' "$MANIFEST" | cut -d= -f2
  echo "$PROMETHEUS_IMAGE"
  echo "$PUSHGATEWAY_IMAGE"
} | sort -u)
pull_images "${images[@]}"
# Server-side: the operator CRDs are too large for client-side apply.
"${KUBECTL[@]}" apply --server-side --force-conflicts -f "$MANIFEST" >/dev/null
"${KUBECTL[@]}" wait --for=condition=Established --timeout=120s \
  crd/prometheuses.monitoring.coreos.com crd/servicemonitors.monitoring.coreos.com \
  crd/prometheusrules.monitoring.coreos.com >/dev/null
"${KUBECTL[@]}" -n default rollout status deploy/prometheus-operator --timeout=300s >/dev/null

"${KUBECTL[@]}" apply --server-side --force-conflicts -f - >/dev/null <<EOF
apiVersion: v1
kind: Namespace
metadata: {name: $NS}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: prometheus, namespace: $NS}
---
# Read-only service discovery and scraping.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: kardinal-e2e-prometheus}
rules:
  - apiGroups: [""]
    resources: [services, endpoints, pods]
    verbs: [get, list, watch]
  - apiGroups: [discovery.k8s.io]
    resources: [endpointslices]
    verbs: [get, list, watch]
  - nonResourceURLs: [/metrics]
    verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: kardinal-e2e-prometheus}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: kardinal-e2e-prometheus}
subjects:
  - {kind: ServiceAccount, name: prometheus, namespace: $NS}
---
apiVersion: monitoring.coreos.com/v1
kind: Prometheus
metadata: {name: e2e, namespace: $NS}
spec:
  image: $PROMETHEUS_IMAGE
  version: ${PROMETHEUS_IMAGE##*:}
  replicas: 1
  serviceAccountName: prometheus
  scrapeInterval: 5s
  evaluationInterval: 5s
  retention: 6h
  serviceMonitorSelector: {}
  serviceMonitorNamespaceSelector: {}
  podMonitorSelector: {}
  podMonitorNamespaceSelector: {}
  ruleSelector: {}
  ruleNamespaceSelector: {}
  resources:
    requests: {cpu: 50m, memory: 128Mi}
    limits: {memory: 1Gi}
---
apiVersion: v1
kind: Service
metadata: {name: prometheus, namespace: $NS}
spec:
  selector: {operator.prometheus.io/name: e2e}
  ports: [{name: web, port: 9090, targetPort: 9090}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: pushgateway, namespace: $NS}
spec:
  replicas: 1
  selector: {matchLabels: {app: pushgateway}}
  template:
    metadata: {labels: {app: pushgateway}}
    spec:
      containers:
        - name: pushgateway
          image: $PUSHGATEWAY_IMAGE
          ports: [{name: http, containerPort: 9091}]
          readinessProbe: {httpGet: {path: /-/ready, port: 9091}, periodSeconds: 3}
          resources: {requests: {cpu: 5m, memory: 16Mi}}
---
apiVersion: v1
kind: Service
metadata: {name: pushgateway, namespace: $NS, labels: {app: pushgateway}}
spec:
  selector: {app: pushgateway}
  ports: [{name: http, port: 9091, targetPort: 9091}]
---
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata: {name: pushgateway, namespace: $NS}
spec:
  selector: {matchLabels: {app: pushgateway}}
  endpoints:
    - {port: http, honorLabels: true}
EOF
"${KUBECTL[@]}" -n "$NS" rollout status deploy/pushgateway --timeout=300s >/dev/null
"${KUBECTL[@]}" -n "$NS" wait prometheus/e2e --for=condition=Available --timeout=300s >/dev/null
env_set KARDINAL_E2E_PROMETHEUS_URL "http://prometheus.$NS.svc:9090"
log "Prometheus Operator $PROMETHEUS_OPERATOR_RELEASE, Prometheus ${PROMETHEUS_IMAGE##*:} and Pushgateway ready"
