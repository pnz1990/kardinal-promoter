#!/usr/bin/env bash
# hack/e2e/components/ticketapi.sh
#
# Installs the fake ticketing API (hack/e2e/ticketapi) in namespace
# ticket-api: the ServiceNow Table API subset ChangeRequests call, over HTTPS
# with a certificate from a CA of the suite's own, and a plain-HTTP control
# port tests reach through the API server's service proxy. Writes the CA to
# ConfigMap kardinal-system/change-request-ca (key ca.crt), which
# components/kardinal.sh passes as changeRequest.caBundle.configMap. Tests
# put the credential the fake accepts into a Secret of their own namespace.
# Run it before kardinal.sh. Built from this checkout on the host
# (standard library only) into a FROM scratch image. Idempotent: the CA and
# the password are kept between runs.
#
# Env file:
#   KARDINAL_E2E_TICKETAPI_URL       the HTTPS base URL, in-cluster
#   KARDINAL_E2E_SNOW_CREDENTIALS    user:password the fake accepts
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=ticket-api
HOST="ticketapi.$NS.svc.cluster.local"
IMAGE="kardinal-e2e/ticket-api:$KIND_CLUSTER"

dir="$E2E_OUT/ticketapi"
mkdir -p "$dir" && chmod 700 "$dir"
if [ ! -s "$dir/ca.crt" ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -keyout "$dir/ca.key" -out "$dir/ca.crt" -days 365 \
    -subj "/CN=kardinal-e2e-ticketapi-ca" \
    -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
  rm -f "$dir/tls.crt"
fi
if [ ! -s "$dir/tls.crt" ]; then
  openssl req -newkey rsa:2048 -nodes -keyout "$dir/tls.key" -out "$dir/tls.csr" -subj "/CN=$HOST" 2>/dev/null
  printf 'subjectAltName=DNS:%s,DNS:ticketapi.%s.svc\nextendedKeyUsage=serverAuth\n' "$HOST" "$NS" >"$dir/ext.cnf"
  openssl x509 -req -in "$dir/tls.csr" -CA "$dir/ca.crt" -CAkey "$dir/ca.key" -CAcreateserial \
    -out "$dir/tls.crt" -days 365 -extfile "$dir/ext.cnf" 2>/dev/null
fi
password=$(secret_gen ticketapi-snow-password 16)

ctx=$(mktemp -d)
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -ldflags="-s -w" \
  -o "$ctx/ticketapi" ./hack/e2e/ticketapi)
chmod 0755 "$ctx" "$ctx/ticketapi"
printf 'FROM scratch\nCOPY ticketapi /ticketapi\nUSER 65532:65532\nENTRYPOINT ["/ticketapi"]\n' >"$ctx/Dockerfile"
docker build -q -t "$IMAGE" "$ctx" >/dev/null
load_image "$IMAGE"

for ns in "$NS" "$KARDINAL_NS"; do
  "${KUBECTL[@]}" create namespace "$ns" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
done
"${KUBECTL[@]}" -n "$NS" create secret tls ticketapi-tls --cert="$dir/tls.crt" --key="$dir/tls.key" \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
"${KUBECTL[@]}" -n "$KARDINAL_NS" create configmap change-request-ca --from-file=ca.crt="$dir/ca.crt" \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null

cat <<EOT | "${KUBECTL[@]}" apply -f - >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ticketapi
  namespace: $NS
spec:
  replicas: 1
  selector: {matchLabels: {app: ticketapi}}
  template:
    metadata:
      labels: {app: ticketapi}
    spec:
      containers:
        - name: ticketapi
          image: $IMAGE
          imagePullPolicy: Never
          args: ["-user=kardinal-e2e", "-password=$password"]
          ports: [{name: http, containerPort: 8080}, {name: https, containerPort: 8443}]
          readinessProbe:
            httpGet: {path: /_healthz, port: http}
            periodSeconds: 2
          volumeMounts: [{name: tls, mountPath: /tls, readOnly: true}]
          resources:
            requests: {cpu: 10m, memory: 16Mi}
            limits: {memory: 128Mi}
      volumes:
        - name: tls
          secret: {secretName: ticketapi-tls}
---
apiVersion: v1
kind: Service
metadata:
  name: ticketapi
  namespace: $NS
spec:
  selector: {app: ticketapi}
  ports: [{name: http, port: 8080, targetPort: http}, {name: https, port: 8443, targetPort: https}]
EOT
# The image tag does not change between builds; restart to pick up a rebuild.
"${KUBECTL[@]}" -n "$NS" rollout restart deploy/ticketapi >/dev/null
"${KUBECTL[@]}" -n "$NS" rollout status deploy/ticketapi --timeout=120s >/dev/null

env_set KARDINAL_E2E_TICKETAPI_URL "https://$HOST:8443"
env_set KARDINAL_E2E_SNOW_CREDENTIALS "kardinal-e2e:$password"
log "fake ticketing API at https://$HOST:8443"
