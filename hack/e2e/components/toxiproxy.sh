#!/usr/bin/env bash
# hack/e2e/components/toxiproxy.sh UPSTREAM_HOST:PORT
#
# Installs Toxiproxy TOXIPROXY_IMAGE in namespace toxiproxy with one proxy,
# "git", listening on :3000 and forwarding to UPSTREAM_HOST:PORT (the suite's
# git server). Service toxiproxy port git (3000) is what the controller
# reaches the git server through; port api (8474) is Toxiproxy's HTTP API,
# which tests call through the API server's service proxy to add latency or
# cut the connection (framework/scale.Toxiproxy). The test runner reaches the
# git server directly, so a test can still merge PRs during an outage.
#
# Run it before the git server component, with KARDINAL_E2E_GIT_ROOT_URL set
# to TOXIPROXY_GIT_URL (printed in the env file), so the git server's clone
# URLs and the controller's SCM API URL go through the proxy. Toxiproxy
# resolves the upstream on each connection, so the git server need not exist
# yet. Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

UPSTREAM=${1:?usage: $0 UPSTREAM_HOST:PORT}
NS=toxiproxy
load_image "$TOXIPROXY_IMAGE"
"${KUBECTL[@]}" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Namespace
metadata: {name: $NS}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: toxiproxy, namespace: $NS}
data:
  proxies.json: |
    [{"name": "git", "listen": "0.0.0.0:3000", "upstream": "$UPSTREAM", "enabled": true}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: toxiproxy, namespace: $NS}
spec:
  replicas: 1
  selector: {matchLabels: {app: toxiproxy}}
  template:
    metadata: {labels: {app: toxiproxy}}
    spec:
      containers:
        - name: toxiproxy
          image: $TOXIPROXY_IMAGE
          imagePullPolicy: IfNotPresent
          args: [-host=0.0.0.0, -config=/config/proxies.json]
          ports:
            - {name: git, containerPort: 3000}
            - {name: api, containerPort: 8474}
          readinessProbe:
            httpGet: {path: /version, port: api}
            periodSeconds: 3
          resources:
            requests: {cpu: 50m, memory: 32Mi}
            limits: {memory: 256Mi}
          volumeMounts: [{name: config, mountPath: /config}]
      volumes: [{name: config, configMap: {name: toxiproxy}}]
---
apiVersion: v1
kind: Service
metadata: {name: toxiproxy, namespace: $NS}
spec:
  selector: {app: toxiproxy}
  ports:
    - {name: git, port: 3000, targetPort: git}
    - {name: api, port: 8474, targetPort: api}
EOF
"${KUBECTL[@]}" -n "$NS" rollout status deploy/toxiproxy --timeout=180s >/dev/null
env_set TOXIPROXY_GIT_URL "http://toxiproxy.$NS.svc.cluster.local:3000"
env_set KARDINAL_E2E_TOXIPROXY "$NS/toxiproxy:api"
log "toxiproxy ${TOXIPROXY_IMAGE##*:} ready: git proxy -> $UPSTREAM"
