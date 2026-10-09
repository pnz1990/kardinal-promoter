#!/usr/bin/env bash
# hack/e2e/components/giteafamily.sh forgejo|gitea
#
# Installs a single-pod Forgejo or Gitea (rootless image, SQLite, no SSH) in
# namespace <flavor> and seeds it for the live suites:
#   - admin user; its token (scope all) is the test runner's
#     KARDINAL_E2E_GIT_TOKEN, used to create per-test repos, merge and close PRs
#   - bot user whose token has only the scopes docs/scm-providers.md lists
#     (write:repository, write:issue); the controller and Pipelines use it
#   - public org kardinal with a team that gives the bot write on every repo,
#     so the bot can push to and open PRs on repos the tests create
#   - Secrets kardinal-system/git-token (key token, the bot token) and
#     kardinal-system/scm-webhook (key secret)
# Webhooks go to the controller's ClusterIP, which the webhook allow list
# must let through; each test registers its own.
# KARDINAL_E2E_GIT_ROOT_URL, when set, is the URL in-cluster clients use
# instead of the server's Service (the scale suite's Toxiproxy in front of
# it): the server's ROOT_URL, so its clone URLs, and the controller's SCM API.
# Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

FLAVOR=${1:?usage: $0 forgejo|gitea}
case "$FLAVOR" in
  # Forgejo reads the webhook allow list from [webhook], where "*" allows
  # every host. Gitea 28 reads it from [security] (the [webhook] key only logs
  # a deprecation error), rejects "*", and needs private hosts listed.
  forgejo) IMAGE=$FORGEJO_IMAGE PFX=FORGEJO ALLOW_SECTION=webhook ALLOW_HOSTS='*' ;;
  gitea) IMAGE=$GITEA_IMAGE PFX=GITEA ALLOW_SECTION=security ALLOW_HOSTS='*.svc.cluster.local' ;;
  *) die "flavor must be forgejo or gitea" ;;
esac
NS=$FLAVOR
INCLUSTER=${KARDINAL_E2E_GIT_ROOT_URL:-"http://$FLAVOR.$NS.svc.cluster.local:3000"}
ORG=kardinal

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
  name: $FLAVOR
  namespace: $NS
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: $FLAVOR}}
  template:
    metadata:
      labels: {app: $FLAVOR}
    spec:
      securityContext: {fsGroup: 1000}
      containers:
        - name: $FLAVOR
          image: $IMAGE
          imagePullPolicy: IfNotPresent
          ports: [{name: http, containerPort: 3000}]
          env:
            - {name: ${PFX}__security__INSTALL_LOCK, value: "true"}
            - {name: ${PFX}__database__DB_TYPE, value: sqlite3}
            - {name: ${PFX}__server__ROOT_URL, value: "$INCLUSTER/"}
            - {name: ${PFX}__server__HTTP_PORT, value: "3000"}
            - {name: ${PFX}__server__DISABLE_SSH, value: "true"}
            - {name: ${PFX}__server__START_SSH_SERVER, value: "false"}
            - {name: ${PFX}__server__OFFLINE_MODE, value: "true"}
            # The default ("external") blocks webhooks to the controller's ClusterIP.
            - {name: ${PFX}__${ALLOW_SECTION}__ALLOWED_HOST_LIST, value: "$ALLOW_HOSTS"}
            - {name: ${PFX}__webhook__DELIVER_TIMEOUT, value: "10"}
            - {name: ${PFX}__service__DISABLE_REGISTRATION, value: "true"}
            - {name: ${PFX}__repository__DEFAULT_BRANCH, value: main}
            - {name: ${PFX}__indexer__REPO_INDEXER_ENABLED, value: "false"}
            - {name: ${PFX}__actions__ENABLED, value: "false"}
            - {name: ${PFX}__mailer__ENABLED, value: "false"}
            - {name: ${PFX}__cron_0X2E_update_checker__ENABLED, value: "false"}
          readinessProbe:
            httpGet: {path: /api/healthz, port: http}
            periodSeconds: 3
          resources:
            requests: {cpu: 50m, memory: 128Mi}
            limits: {memory: 1Gi}
          volumeMounts:
            - {name: data, mountPath: /var/lib/gitea}
            - {name: config, mountPath: /etc/gitea}
      volumes:
        # The server lives as long as the kind cluster; nothing to persist.
        - {name: data, emptyDir: {}}
        - {name: config, emptyDir: {}}
---
apiVersion: v1
kind: Service
metadata:
  name: $FLAVOR
  namespace: $NS
spec:
  type: NodePort
  selector: {app: $FLAVOR}
  ports: [{name: http, port: 3000, targetPort: http}]
EOF
"${KUBECTL[@]}" -n "$NS" rollout status "deploy/$FLAVOR" --timeout=300s >/dev/null
BASE="http://$(node_ip):$(nodeport "$NS" "$FLAVOR" http)"
wait_http "$BASE/api/healthz" 120 || die "$FLAVOR not healthy at $BASE"
log "$FLAVOR $(curl -fsS "$BASE/api/v1/version") at $BASE"

# api METHOD PATH [JSON] calls the API as the admin and prints the body.
api() {
  curl -fsS -X "$1" -H "Authorization: token $(secret_get "$FLAVOR-admin-token")" \
    -H 'Content-Type: application/json' ${3:+-d "$3"} "$BASE/api/v1$2"
}
status() {
  curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: token $(secret_get "$FLAVOR-admin-token")" "$BASE/api/v1$1"
}

# ensure_user NAME ADMIN(true|false)
ensure_user() {
  [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/users/$1")" = 200 ] && return 0
  local flag=()
  [ "$2" = true ] && flag=(--admin)
  "${KUBECTL[@]}" -n "$NS" exec "deploy/$FLAVOR" -- gitea admin user create "${flag[@]}" \
    --username "$1" --password "$(secret_gen "$FLAVOR-$1-password" 16)" --email "$1@example.com" \
    --must-change-password=false >/dev/null
  log "user $1 created (admin=$2)"
}

# ensure_token USER SECRET_NAME SCOPES_JSON
ensure_token() {
  local tok
  tok=$(secret_get "$2")
  if [ -n "$tok" ] && [ "$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: token $tok" "$BASE/api/v1/repos/search?limit=1")" = 200 ]; then
    return 0
  fi
  tok=$(curl -fsS -u "$1:$(secret_get "$FLAVOR-$1-password")" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$2-$(date +%s)\",\"scopes\":$3}" "$BASE/api/v1/users/$1/tokens" |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["sha1"])')
  secret_put "$2" "$tok"
  log "token for $1 created, scopes $3"
}

ensure_user kardinal-admin true
ensure_token kardinal-admin "$FLAVOR-admin-token" '["all"]'
ensure_user kardinal-bot false
ensure_token kardinal-bot "$FLAVOR-bot-token" '["write:repository","write:issue"]'

[ "$(status "/orgs/$ORG")" = 200 ] ||
  api POST /orgs "{\"username\":\"$ORG\",\"visibility\":\"public\"}" >/dev/null
TEAM_ID=$(api GET "/orgs/$ORG/teams" | python3 -c 'import json,sys; print(next((str(t["id"]) for t in json.load(sys.stdin) if t["name"]=="bots"),""))')
if [ -z "$TEAM_ID" ]; then
  # units_map only: Gitea 28 rejects a team with both units and units_map.
  TEAM_ID=$(api POST "/orgs/$ORG/teams" '{"name":"bots","includes_all_repositories":true,
    "units_map":{"repo.code":"write","repo.pulls":"write","repo.issues":"write"}}' |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
fi
api PUT "/teams/$TEAM_ID/members/kardinal-bot" >/dev/null
log "org $ORG: kardinal-bot has write on every repo (team $TEAM_ID)"

secret_gen webhook-secret 24 >/dev/null
kube_secret "$KARDINAL_NS" git-token token "$FLAVOR-bot-token"
kube_secret "$KARDINAL_NS" scm-webhook secret webhook-secret

env_set KARDINAL_E2E_SCM_PROVIDER "$FLAVOR"
env_set KARDINAL_E2E_SCM_API "$INCLUSTER"
env_set KARDINAL_E2E_GIT_KIND "$FLAVOR"
env_set KARDINAL_E2E_GIT_API "$BASE"
env_set KARDINAL_E2E_GIT_CLONE_BASE "$INCLUSTER"
env_set KARDINAL_E2E_GIT_OWNER "$ORG"
env_set KARDINAL_E2E_GIT_TOKEN "$(secret_get "$FLAVOR-admin-token")"
env_set KARDINAL_E2E_WEBHOOK_URL "$KARDINAL_WEBHOOK_URL"
env_set KARDINAL_E2E_WEBHOOK_SECRET "$(secret_get webhook-secret)"
