#!/usr/bin/env bash
# hack/e2e/components/webhook-receiver.sh
#
# Installs the webhook receiver (hack/e2e/receiver) in namespace
# webhook-receiver: a recorder that NotificationHooks deliver to. Tests read
# what it got and make it fail through its host NodePort. It is built from
# this checkout on the host (standard library only) into a FROM scratch
# image, so nothing is pulled. Idempotent.
#
# Env file:
#   KARDINAL_E2E_RECEIVER_URL  base URL for hooks, in-cluster
#   KARDINAL_E2E_RECEIVER_API  the same server from the host (NodePort)
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=webhook-receiver
IMAGE="kardinal-e2e/webhook-receiver:$KIND_CLUSTER"

ctx=$(mktemp -d)
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -ldflags="-s -w" \
  -o "$ctx/receiver" ./hack/e2e/receiver)
chmod 0755 "$ctx" "$ctx/receiver"
printf 'FROM scratch\nCOPY receiver /receiver\nUSER 65532:65532\nENTRYPOINT ["/receiver"]\n' >"$ctx/Dockerfile"
docker build -q -t "$IMAGE" "$ctx" >/dev/null
load_image "$IMAGE"

cat <<EOF | "${KUBECTL[@]}" apply -f - >/dev/null
apiVersion: v1
kind: Namespace
metadata:
  name: $NS
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: receiver
  namespace: $NS
spec:
  replicas: 1
  selector: {matchLabels: {app: receiver}}
  template:
    metadata:
      labels: {app: receiver}
    spec:
      containers:
        - name: receiver
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
  name: receiver
  namespace: $NS
spec:
  type: NodePort
  selector: {app: receiver}
  ports: [{name: http, port: 8080, targetPort: http}]
EOF
# The image tag does not change between builds; restart to pick up a rebuild.
"${KUBECTL[@]}" -n "$NS" rollout restart deploy/receiver >/dev/null
"${KUBECTL[@]}" -n "$NS" rollout status deploy/receiver --timeout=120s >/dev/null
BASE="http://$(node_ip):$(nodeport "$NS" receiver http)"
wait_http "$BASE/_healthz" 60 || die "webhook receiver not reachable at $BASE"

env_set KARDINAL_E2E_RECEIVER_URL "http://receiver.$NS.svc.cluster.local:8080"
env_set KARDINAL_E2E_RECEIVER_API "$BASE"
log "webhook receiver at $BASE"
