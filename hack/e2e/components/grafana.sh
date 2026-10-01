#!/usr/bin/env bash
# hack/e2e/components/grafana.sh
#
# Installs the Grafana Helm chart GRAFANA_CHART_VERSION (grafana-community) in
# namespace monitoring: Service grafana:80, with Prometheus (prometheus.sh) as
# its default datasource. Its dashboard sidecar imports the ConfigMaps
# labelled grafana_dashboard=1 in kardinal-system, the label the chart's
# grafanaDashboard uses (values.yaml grafanaDashboard.sidecarLabel).
#
# Least privilege: the chart's RBAC is off (it would grant reading Secrets),
# and the sidecar may only read ConfigMaps in kardinal-system. Anonymous users
# are Viewers, so the tests read dashboards and query the datasource with no
# credentials, through the API server's service proxy; the Service is not
# exposed outside the cluster. The admin password is the chart's random one,
# in Secret grafana, and is not used. Run it after prometheus.sh. Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=monitoring
VALUES="$E2E_OUT/grafana-values.yaml"
mkdir -p "$E2E_OUT"
cat >"$VALUES" <<EOF
rbac: {create: false}
testFramework: {enabled: false}
persistence: {enabled: false}
grafana.ini:
  auth.anonymous: {enabled: true, org_role: Viewer}
  analytics: {check_for_updates: false, check_for_plugin_updates: false, reporting_enabled: false}
  news: {news_feed_enabled: false}
  plugins: {preinstall_disabled: true}
datasources:
  datasources.yaml:
    apiVersion: 1
    datasources:
      - {name: Prometheus, type: prometheus, uid: prometheus, access: proxy, isDefault: true,
         url: "http://prometheus.$NS.svc:9090"}
sidecar:
  dashboards:
    enabled: true
    label: grafana_dashboard
    labelValue: "1"
    searchNamespace: [$KARDINAL_NS]
    resource: configmap
EOF
CHART=(grafana --repo https://grafana-community.github.io/helm-charts --version "$GRAFANA_CHART_VERSION"
  -n "$NS" -f "$VALUES")
# The chart requires a kubeVersion, which helm template takes as an argument.
kube=$("${KUBECTL[@]}" version -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["serverVersion"]["gitVersion"])')
manifest=$("${HELM[@]}" template grafana "${CHART[@]}" --kube-version "$kube")
mapfile -t images < <(awk '$1 == "image:" {gsub(/"/, "", $2); print $2}' <<<"$manifest" | sort -u)
[ "${#images[@]}" -gt 0 ] || die "no images in the Grafana chart"
pull_images "${images[@]}"

# The sidecar reads the dashboard ConfigMaps in kardinal-system, which
# components/kardinal.sh creates later.
"${KUBECTL[@]}" apply --server-side --force-conflicts -f - >/dev/null <<EOF
apiVersion: v1
kind: Namespace
metadata: {name: $KARDINAL_NS}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: kardinal-e2e-grafana-dashboards, namespace: $KARDINAL_NS}
rules:
  - apiGroups: [""]
    resources: [configmaps]
    verbs: [get, list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: kardinal-e2e-grafana-dashboards, namespace: $KARDINAL_NS}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: kardinal-e2e-grafana-dashboards}
subjects:
  - {kind: ServiceAccount, name: grafana, namespace: $NS}
EOF
"${HELM[@]}" upgrade --install grafana "${CHART[@]}" --create-namespace --wait --timeout 5m >/dev/null
"${KUBECTL[@]}" -n "$NS" rollout status deploy/grafana --timeout=300s >/dev/null
log "Grafana chart $GRAFANA_CHART_VERSION ready (dashboards from $KARDINAL_NS, datasource Prometheus)"
