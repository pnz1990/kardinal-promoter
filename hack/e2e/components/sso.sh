#!/usr/bin/env bash
# hack/e2e/components/sso.sh
#
# The sso suite's identity provider: Dex in namespace dex, serving
# $SSO_ISSUER over TLS with the certificate components/sso-pki.sh issued, at
# the static ClusterIP $SSO_DEX_IP, so the API server (on the node's network,
# through kube-proxy) and pods reach it at the same URL. Dex has:
#
#   the OAuth client $SSO_CLIENT_ID, whose redirect URI is the oauth2-proxy
#     callback on http://$SSO_HOST (docs/guides/security.md, Option 3, step 2)
#   two password users: alice in group release-managers and bob in group
#     developers (username = preferred_username), with one generated
#     password, SECRETS_DIR/sso-password
#   no approval screen, so a login goes straight back to the client
#
# It also pulls the oauth2-proxy image the TestSSO_ test deploys with the
# page's flags, and checks that the node reaches the issuer (the API server
# fetches its discovery document from there).
#
# Env set for the tests:
#   KARDINAL_E2E_SSO_DIR         components/sso-pki.sh's directory (ca.crt)
#   KARDINAL_E2E_SSO_SECRETS     SECRETS_DIR: sso-client-secret, sso-cookie-secret,
#                                sso-password
#   KARDINAL_E2E_SSO_ISSUER, KARDINAL_E2E_SSO_CLIENT_ID, KARDINAL_E2E_SSO_HOST,
#   KARDINAL_E2E_OAUTH2_PROXY_IMAGE
#
# Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

dir="$E2E_OUT/sso"
[ -s "$dir/dex.crt" ] || die "no $dir/dex.crt; components/sso-pki.sh must run before the cluster is created"
pull_images "$DEX_IMAGE" "$OAUTH2_PROXY_IMAGE"
secret_gen sso-client-secret >/dev/null
# oauth2-proxy takes a 16, 24 or 32 byte cookie secret: 32 hex characters.
secret_gen sso-cookie-secret 16 >/dev/null

secret_gen sso-password >/dev/null
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT
(cd "$REPO_ROOT" && go build -o "$work/htpasswd" ./hack/e2e/htpasswd)
hash=$("$work/htpasswd" sso <"$SECRETS_DIR/sso-password" | cut -d: -f2-)
"${KUBECTL[@]}" create namespace dex --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
"${KUBECTL[@]}" -n dex create secret tls dex-tls --cert="$dir/dex.crt" --key="$dir/dex.key" \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
kube_secret dex dex-client client-secret sso-client-secret
"${KUBECTL[@]}" -n dex create configmap dex --dry-run=client -o yaml --from-file=config.yaml=/dev/stdin <<EOF | "${KUBECTL[@]}" apply -f - >/dev/null
issuer: $SSO_ISSUER
storage:
  type: memory
web:
  https: 0.0.0.0:5556
  tlsCert: /etc/dex/tls/tls.crt
  tlsKey: /etc/dex/tls/tls.key
telemetry:
  http: 0.0.0.0:5558
oauth2:
  skipApprovalScreen: true
enablePasswordDB: true
staticClients:
  - id: $SSO_CLIENT_ID
    name: kardinal
    secretEnv: CLIENT_SECRET
    redirectURIs: [http://$SSO_HOST/ui/oauth2/callback]
staticPasswords:
  - email: alice@sso.test
    hash: "$hash"
    username: alice
    preferredUsername: alice
    userID: 6a4c5f0e-0001-4d6b-9a57-a11ce0000001
    groups: [release-managers]
  - email: bob@sso.test
    hash: "$hash"
    username: bob
    preferredUsername: bob
    userID: 6a4c5f0e-0002-4d6b-9a57-b0b000000002
    groups: [developers]
EOF
"${KUBECTL[@]}" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Service
metadata:
  name: dex
  namespace: dex
spec:
  clusterIP: $SSO_DEX_IP
  selector: {app: dex}
  ports:
    - {name: https, port: 5556, targetPort: https}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dex
  namespace: dex
spec:
  replicas: 1
  selector:
    matchLabels: {app: dex}
  template:
    metadata:
      labels: {app: dex}
      annotations:
        kardinal.io/config-hash: "$(sha256sum "$dir/dex.crt" | cut -c1-16)"
    spec:
      containers:
        - name: dex
          image: $DEX_IMAGE
          imagePullPolicy: IfNotPresent
          args: [dex, serve, /etc/dex/cfg/config.yaml]
          env:
            - name: CLIENT_SECRET
              valueFrom:
                secretKeyRef: {name: dex-client, key: client-secret}
          ports:
            - {name: https, containerPort: 5556}
            - {name: telemetry, containerPort: 5558}
          readinessProbe:
            httpGet: {path: /healthz/ready, port: telemetry}
          volumeMounts:
            - {name: cfg, mountPath: /etc/dex/cfg}
            - {name: tls, mountPath: /etc/dex/tls}
      volumes:
        - name: cfg
          configMap: {name: dex}
        - name: tls
          secret: {secretName: dex-tls}
EOF
# A changed ConfigMap is not picked up by a running Dex.
"${KUBECTL[@]}" -n dex rollout restart deploy/dex >/dev/null
"${KUBECTL[@]}" -n dex rollout status deploy/dex --timeout=180s >/dev/null

# The API server's view: the node reaches the issuer through kube-proxy and
# trusts its certificate with the CA of its AuthenticationConfiguration.
node="$KIND_CLUSTER-control-plane"
# (docker cp cannot write to the node's /tmp, a tmpfs mount)
docker exec -i "$node" sh -c 'cat >/tmp/sso-ca.crt' <"$dir/ca.crt"
waited=0
until docker exec "$node" curl -fs -o /dev/null -m 5 --cacert /tmp/sso-ca.crt \
  "$SSO_ISSUER/.well-known/openid-configuration"; do
  waited=$((waited + 2))
  [ "$waited" -lt 60 ] || die "the node cannot reach $SSO_ISSUER"
  sleep 2
done

env_set KARDINAL_E2E_SSO_DIR "$dir"
env_set KARDINAL_E2E_SSO_SECRETS "$SECRETS_DIR"
env_set KARDINAL_E2E_SSO_ISSUER "$SSO_ISSUER"
env_set KARDINAL_E2E_SSO_CLIENT_ID "$SSO_CLIENT_ID"
env_set KARDINAL_E2E_SSO_HOST "$SSO_HOST"
env_set KARDINAL_E2E_OAUTH2_PROXY_IMAGE "$OAUTH2_PROXY_IMAGE"
log "sso: Dex serving $SSO_ISSUER (users alice, bob)"
