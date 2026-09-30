#!/usr/bin/env bash
# hack/e2e/components/ui.sh
#
# What the ui suite needs besides the main release, which runs with no UI
# auth and which the tests reach through kubectl port-forward:
#
#   kardinal-system/kardinal-ui-nodeport  a NodePort Service on the main
#       release, so a test can call it from off the pod (not loopback)
#   four more releases of the chart and the same image, each namespace-scoped
#   (controller.watchNamespace) in its own namespace, with a NodePort Service
#   <release>-nodeport for the ui and webhook ports:
#     kui-token      kardinal-ui-token      static UI token, CORS origin
#                                           http://allowed.example, allowed
#                                           host kardinal-ui.test
#     kui-tr         kardinal-ui-tr         ui.auth.tokenReview=true
#     kui-tr-norbac  kardinal-ui-tr-norbac  --ui-tokenreview-auth through
#                                           extraArgs, without the RBAC the
#                                           chart adds for it: reviews fail
#     kui-tls        kardinal-ui-tls        TLS on the UI and webhook servers
#                                           (a self-signed cert for the node
#                                           IP) and the static UI token
#   Playwright's Chromium in web/ for the browser tests.
#
# Runs after components/kardinal.sh and uses the image it deployed.
# Idempotent. The token and the TLS key stay in SECRETS_DIR; the env file
# holds their paths.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

IMAGE=$("${KUBECTL[@]}" -n "$KARDINAL_NS" get "deploy/$KARDINAL_RELEASE" \
  -o jsonpath='{.spec.template.spec.containers[0].image}')
[ -n "$IMAGE" ] || die "no $KARDINAL_NS/$KARDINAL_RELEASE Deployment; components/kardinal.sh must run first"
IP=$(node_ip)

# nodeport_svc NS NAME RELEASE PORTS... exposes RELEASE's pods on a NodePort
# Service NAME; each port is ui or webhook.
nodeport_svc() {
  local ns=$1 name=$2 release=$3 ports=
  shift 3
  for p in "$@"; do
    case $p in
      ui) ports+="    - {name: ui, port: 8082, targetPort: ui}"$'\n' ;;
      webhook) ports+="    - {name: webhook, port: 8083, targetPort: webhook}"$'\n' ;;
    esac
  done
  "${KUBECTL[@]}" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Service
metadata:
  name: $name
  namespace: $ns
spec:
  type: NodePort
  selector:
    app.kubernetes.io/name: kardinal-promoter
    app.kubernetes.io/instance: $release
  ports:
$ports
EOF
}

# release NAME NS HELM_ARGS... installs a namespace-scoped release NAME in NS.
# The CRDs come with the main release, and the ScheduleClock it creates has a
# fixed name, so only the main release has one.
release() {
  local name=$1 ns=$2
  shift 2
  "${HELM[@]}" upgrade --install "$name" "$REPO_ROOT/chart/kardinal-promoter" \
    -n "$ns" --create-namespace --skip-crds \
    --set "image.repository=${IMAGE%:*}" --set "image.tag=${IMAGE##*:}" --set image.pullPolicy=Never \
    --set logLevel=debug --set "controller.watchNamespace=$ns" --set scheduleClock.enabled=false \
    "$@" --wait --timeout 5m >/dev/null
  # The image may have been rebuilt under the same tag.
  "${KUBECTL[@]}" -n "$ns" rollout restart "deploy/$name-kardinal-promoter" >/dev/null
  "${KUBECTL[@]}" -n "$ns" rollout status "deploy/$name-kardinal-promoter" --timeout=180s >/dev/null
  nodeport_svc "$ns" "$name-nodeport" "$name" ui webhook
}

nodeport_svc "$KARDINAL_NS" kardinal-ui-nodeport "$KARDINAL_RELEASE" ui
env_set KARDINAL_E2E_UI_NODEPORT_URL "http://$IP:$(nodeport "$KARDINAL_NS" kardinal-ui-nodeport ui)"

secret_gen ui-token >/dev/null
env_set KARDINAL_E2E_UI_TOKEN_FILE "$SECRETS_DIR/ui-token"

NS=kardinal-ui-token
kube_secret "$NS" ui-token token ui-token
release kui-token "$NS" --set ui.auth.tokenSecretRef.name=ui-token \
  --set 'ui.corsAllowedOrigins={http://allowed.example}' --set 'ui.allowedHosts={kardinal-ui.test}'
url="http://$IP:$(nodeport "$NS" kui-token-nodeport ui)"
wait_http "$url/ui/" 60 || die "kui-token UI at $url does not answer"
env_set KARDINAL_E2E_UI_TOKEN_URL "$url"

NS=kardinal-ui-tr
release kui-tr "$NS" --set ui.auth.tokenReview=true
url="http://$IP:$(nodeport "$NS" kui-tr-nodeport ui)"
wait_http "$url/ui/" 60 || die "kui-tr UI at $url does not answer"
env_set KARDINAL_E2E_UI_TR_URL "$url"
env_set KARDINAL_E2E_UI_TR_NAMESPACE "$NS"

NS=kardinal-ui-tr-norbac
release kui-tr-norbac "$NS" --set 'controller.extraArgs={--ui-tokenreview-auth=true}'
url="http://$IP:$(nodeport "$NS" kui-tr-norbac-nodeport ui)"
wait_http "$url/ui/" 60 || die "kui-tr-norbac UI at $url does not answer"
env_set KARDINAL_E2E_UI_TR_NORBAC_URL "$url"

# A self-signed cert for the node IP (the NodePorts) and the Service names,
# made again when the node IP changed.
NS=kardinal-ui-tls
CRT="$SECRETS_DIR/kui-tls.crt" KEY="$SECRETS_DIR/kui-tls.key"
san=$(openssl x509 -in "$CRT" -noout -ext subjectAltName 2>/dev/null || true)
if ! grep -qE "IP Address:${IP//./\\.}(,|$)" <<<"$san"; then
  mkdir -p "$SECRETS_DIR" && chmod 700 "$SECRETS_DIR"
  svc="kui-tls-kardinal-promoter"
  (umask 077 && openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj "/CN=kardinal-ui-tls" \
    -addext "subjectAltName=IP:$IP,DNS:$svc,DNS:$svc.$NS,DNS:$svc.$NS.svc,DNS:$svc.$NS.svc.cluster.local" \
    -keyout "$KEY" -out "$CRT" 2>/dev/null)
fi
kube_secret "$NS" ui-token token ui-token
"${KUBECTL[@]}" -n "$NS" create secret tls kui-tls-cert --cert="$CRT" --key="$KEY" \
  --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
release kui-tls "$NS" --set ui.auth.tokenSecretRef.name=ui-token \
  --set 'controller.extraVolumes[0].name=tls' --set 'controller.extraVolumes[0].secret.secretName=kui-tls-cert' \
  --set 'controller.extraVolumeMounts[0].name=tls' --set 'controller.extraVolumeMounts[0].mountPath=/etc/kardinal/tls' \
  --set 'controller.extraVolumeMounts[0].readOnly=true' \
  --set controller.tlsCertFile=/etc/kardinal/tls/tls.crt --set controller.tlsKeyFile=/etc/kardinal/tls/tls.key
url="https://$IP:$(nodeport "$NS" kui-tls-nodeport ui)"
wait_http "$url/ui/" 60 --cacert "$CRT" || die "kui-tls UI at $url does not answer over TLS"
env_set KARDINAL_E2E_UI_TLS_URL "$url"
env_set KARDINAL_E2E_UI_TLS_WEBHOOK_URL "https://$IP:$(nodeport "$NS" kui-tls-nodeport webhook)"
env_set KARDINAL_E2E_UI_TLS_CA "$CRT"

# The browser tests run `npx playwright test` in web/ (Playwright's version is
# pinned by web/package-lock.json).
NPX=$(command -v npx) || die "npx not found: the ui suite's browser tests need Node.js on PATH"
(cd "$REPO_ROOT/web" && { [ -x node_modules/.bin/playwright ] || npm ci --no-audit --no-fund >/dev/null; } &&
  "$NPX" playwright install chromium >/dev/null)
env_set KARDINAL_E2E_NPX "$NPX"
log "UI releases ready: kui-token, kui-tr, kui-tr-norbac, kui-tls; Playwright $(cd "$REPO_ROOT/web" && "$NPX" playwright --version)"
