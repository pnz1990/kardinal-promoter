#!/usr/bin/env bash
# hack/e2e/components/sso-pki.sh
#
# Runs before the sso suite's kind cluster exists (up.sh BEFORE) and writes,
# in $E2E_OUT/sso (mode 0700):
#
#   ca.crt, ca.key     a CA for the suite
#   dex.crt, dex.key   Dex's serving certificate from that CA, for the IP
#                      $SSO_DEX_IP (the static ClusterIP of components/sso.sh)
#   apiserver/auth.yaml  the API server's AuthenticationConfiguration: one JWT
#                      authenticator for Dex's issuer, client ID kardinal,
#                      username preferred_username and groups groups, both
#                      prefixed oidc: (docs/guides/security.md, Option 3,
#                      step 1, "on a cluster you run")
#   kind-config.yaml   test/e2e/kind-config.yaml's node with apiserver/ mounted
#                      at /etc/kubernetes/sso and kubeadm config patches that
#                      start the API server with --authentication-config
#
# The API server runs on the node's network and reaches Dex through
# kube-proxy at the ClusterIP, as every pod does, so one issuer URL works for
# both. It starts before Dex does; the JWT authenticator keeps fetching the
# issuer's discovery document until Dex answers.
#
# Idempotent: files that exist are kept, so the API server of a cluster an
# earlier run created still trusts the CA.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"

dir="$E2E_OUT/sso"
mkdir -p "$dir/apiserver" && chmod 700 "$dir"
cd "$dir"
if [ ! -s ca.crt ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.crt -days 365 \
    -subj "/CN=kardinal-e2e-sso-ca" \
    -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
  rm -f dex.crt
fi
if [ ! -s dex.crt ]; then
  openssl req -newkey rsa:2048 -nodes -keyout dex.key -out dex.csr -subj "/CN=dex" 2>/dev/null
  printf 'subjectAltName=IP:%s,DNS:dex.dex.svc,DNS:dex.dex.svc.cluster.local\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n' \
    "$SSO_DEX_IP" >dex.ext
  openssl x509 -req -in dex.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out dex.crt -days 365 \
    -extfile dex.ext 2>/dev/null
  rm -f dex.csr dex.ext
fi

{
  cat <<EOF
apiVersion: apiserver.config.k8s.io/v1
kind: AuthenticationConfiguration
jwt:
  - issuer:
      url: $SSO_ISSUER
      audiences: [$SSO_CLIENT_ID]
      certificateAuthority: |
EOF
  sed 's/^/        /' ca.crt
  cat <<'EOF'
    claimMappings:
      username:
        claim: preferred_username
        prefix: "oidc:"
      groups:
        claim: groups
        prefix: "oidc:"
EOF
} >apiserver/auth.yaml

image=$(sed -n 's/^ *image: *//p' "$REPO_ROOT/test/e2e/kind-config.yaml")
[ -n "$image" ] || die "no node image in test/e2e/kind-config.yaml"
cat >kind-config.yaml <<EOF
# Written by hack/e2e/components/sso-pki.sh: test/e2e/kind-config.yaml with
# the API server trusting the sso suite's Dex.
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: kardinal-e2e
nodes:
  - role: control-plane
    image: $image
    extraMounts:
      - hostPath: $dir/apiserver
        containerPath: /etc/kubernetes/sso
        readOnly: true
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
      - |
        kind: ClusterConfiguration
        apiServer:
          extraArgs:
            authentication-config: /etc/kubernetes/sso/auth.yaml
          extraVolumes:
            - name: sso
              hostPath: /etc/kubernetes/sso
              mountPath: /etc/kubernetes/sso
              readOnly: true
              pathType: Directory
EOF
log "sso: CA, Dex certificate for $SSO_DEX_IP and the API server's AuthenticationConfiguration in $dir"
