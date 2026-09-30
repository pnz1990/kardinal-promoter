#!/usr/bin/env bash
# hack/e2e/components/registry.sh
#
# Installs two OCI registries (distribution, REGISTRY_IMAGE) in namespace
# registry for the Subscription tests:
#   - registry: public, plain HTTP, no auth. Seeded with podinfo 6.13.0,
#     6.14.0 and 6.15.0 in repository e2e/podinfo (their linux/amd64
#     manifests, pushed byte for byte from the host's docker with
#     hack/e2e/ocipush); tests copy tags from there into their own
#     repositories.
#   - registry-private: htpasswd auth, empty; every anonymous request gets
#     401 with a Basic challenge. Nobody logs in to it.
# Both keep their data in emptyDir: it lives as long as the pod, and this
# script re-seeds on every run. Idempotent.
#
# Env file:
#   KARDINAL_E2E_REGISTRY          public registry base URL, in-cluster
#   KARDINAL_E2E_REGISTRY_API      the same registry from the host (NodePort)
#   KARDINAL_E2E_REGISTRY_SEED     the seeded repository (e2e/podinfo)
#   KARDINAL_E2E_PRIVATE_REGISTRY  private registry base URL, in-cluster
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=registry
SEED_REPO=e2e/podinfo
SEED_IMAGE=ghcr.io/stefanprodan/podinfo
SEED_TAGS=(6.13.0 6.14.0 6.15.0)

pull_images "$REGISTRY_IMAGE"
"${KUBECTL[@]}" create namespace "$NS" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
# One user whose "hash" is not a bcrypt hash, so no password matches: the
# private registry refuses every login as well as every anonymous request.
"${KUBECTL[@]}" -n "$NS" create secret generic registry-htpasswd --from-literal=htpasswd='e2e:!no-login' \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null

# registry NAME EXTRA_ENV_YAML
registry() {
  cat <<EOF | "${KUBECTL[@]}" apply -f - >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $1
  namespace: $NS
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: $1}}
  template:
    metadata:
      labels: {app: $1}
    spec:
      containers:
        - name: registry
          image: $REGISTRY_IMAGE
          imagePullPolicy: IfNotPresent
          ports: [{name: http, containerPort: 5000}]
          env:
            - {name: OTEL_TRACES_EXPORTER, value: none}
$2
          readinessProbe:
            tcpSocket: {port: http}
            periodSeconds: 2
          resources:
            requests: {cpu: 20m, memory: 32Mi}
            limits: {memory: 512Mi}
          volumeMounts:
            - {name: data, mountPath: /var/lib/registry}
            - {name: auth, mountPath: /auth, readOnly: true}
      volumes:
        - {name: data, emptyDir: {}}
        - {name: auth, secret: {secretName: registry-htpasswd}}
---
apiVersion: v1
kind: Service
metadata:
  name: $1
  namespace: $NS
spec:
  type: NodePort
  selector: {app: $1}
  ports: [{name: http, port: 5000, targetPort: http}]
EOF
  "${KUBECTL[@]}" -n "$NS" rollout status "deploy/$1" --timeout=120s >/dev/null
}
registry registry ""
registry registry-private "            - {name: REGISTRY_AUTH, value: htpasswd}
            - {name: REGISTRY_AUTH_HTPASSWD_REALM, value: kardinal-e2e}
            - {name: REGISTRY_AUTH_HTPASSWD_PATH, value: /auth/htpasswd}"

IP=$(node_ip)
BASE="http://$IP:$(nodeport "$NS" registry http)"
PRIVATE="http://$IP:$(nodeport "$NS" registry-private http)"
wait_http "$BASE/v2/" 60 || die "registry not reachable at $BASE"
code=$(curl -sS -o /dev/null -w '%{http_code}' "$PRIVATE/v2/")
[ "$code" = 401 ] || die "registry-private at $PRIVATE answered $code to an anonymous request, want 401"

work=$(mktemp -d)
(cd "$REPO_ROOT" && go build -o "$work/ocipush" ./hack/e2e/ocipush)
for tag in "${SEED_TAGS[@]}"; do
  docker image inspect "$SEED_IMAGE:$tag" >/dev/null 2>&1 || pull_retry docker pull -q "$SEED_IMAGE:$tag" >/dev/null ||
    die "can't pull $SEED_IMAGE:$tag"
  docker save "$SEED_IMAGE:$tag" -o "$work/image.tar"
  "$work/ocipush" -archive "$work/image.tar" -registry "$BASE" -repo "$SEED_REPO" -tag "$tag" >/dev/null
done
rm -f "$work/image.tar"

env_set KARDINAL_E2E_REGISTRY "http://registry.$NS.svc.cluster.local:5000"
env_set KARDINAL_E2E_REGISTRY_API "$BASE"
env_set KARDINAL_E2E_REGISTRY_SEED "$SEED_REPO"
env_set KARDINAL_E2E_PRIVATE_REGISTRY "http://registry-private.$NS.svc.cluster.local:5000"
log "registry at $BASE ($SEED_REPO: ${SEED_TAGS[*]}), private registry at $PRIVATE"
