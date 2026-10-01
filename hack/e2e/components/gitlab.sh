#!/usr/bin/env bash
# hack/e2e/components/gitlab.sh
#
# Installs a single-pod GitLab CE (omnibus) in namespace gitlab, trimmed for a
# kind cluster on a CI runner (puma single mode, sidekiq concurrency 5, no
# Prometheus, registry, KAS, Pages, packages, dependency proxy or usage ping),
# and seeds it for the live suites. Puma gets 24 threads: a commit through the
# API holds a thread while gitaly calls back into puma
# (/api/v4/internal/allowed), and with 4 threads the suite's parallel project
# setups took every thread and waited on each other for 55s. The seed:
#   - a root personal access token (scopes api, sudo): the test runner's
#     KARDINAL_E2E_GIT_TOKEN, used to create per-test projects, merge, close
#     and approve MRs. The first token can only be made with gitlab-rails
#     runner; it goes into the pod over stdin and is deleted after.
#   - the instance setting allow_local_requests_from_web_hooks_and_services,
#     without which GitLab refuses webhooks to the controller's ClusterIP
#   - bot user kardinal-bot with a token that has only the scope
#     docs/scm-providers.md lists (api); the controller and Pipelines use it
#   - public group kardinal with the bot as Maintainer: GitLab protects each
#     project's main for Maintainers, and auto environments push to it
#   - Secrets kardinal-system/git-token (key token, the bot token) and
#     kardinal-system/scm-webhook (key secret)
# Each test creates its own project and registers its own webhook. Repo URLs
# end in .git: without it GitLab answers info/refs with a 301 that Argo CD
# does not follow.
# Idempotent. Token values are never printed.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

NS=gitlab
GROUP=kardinal
# The host starts with "gitlab.", so kardinal's git client authenticates as
# user oauth2 (pkg/scm/git_client.go).
INCLUSTER="http://gitlab.$NS.svc.cluster.local"

# The image is about 1.6 GB compressed; crictl pulls it straight into the node.
pull_images "$GITLAB_IMAGE"
"${KUBECTL[@]}" create namespace "$NS" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
secret_gen gitlab-root-password 16 >/dev/null
kube_secret "$NS" gitlab-root password gitlab-root-password
cat <<EOF | "${KUBECTL[@]}" apply -f - >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gitlab
  namespace: $NS
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app: gitlab}}
  template:
    metadata:
      labels: {app: gitlab}
    spec:
      containers:
        - name: gitlab
          image: $GITLAB_IMAGE
          imagePullPolicy: IfNotPresent
          ports: [{name: http, containerPort: 80}]
          env:
            - name: GITLAB_ROOT_PASSWORD
              valueFrom: {secretKeyRef: {name: gitlab-root, key: password}}
            - name: GITLAB_OMNIBUS_CONFIG
              value: |
                external_url '$INCLUSTER'
                nginx['listen_port'] = 80
                nginx['listen_https'] = false
                letsencrypt['enable'] = false
                puma['worker_processes'] = 0
                puma['min_threads'] = 4
                puma['max_threads'] = 24
                sidekiq['concurrency'] = 5
                prometheus_monitoring['enable'] = false
                registry['enable'] = false
                gitlab_kas['enable'] = false
                gitlab_pages['enable'] = false
                gitlab_rails['usage_ping_enabled'] = false
                gitlab_rails['packages_enabled'] = false
                gitlab_rails['dependency_proxy_enabled'] = false
                gitlab_rails['terraform_state_enabled'] = false
                gitlab_rails['gitlab_default_projects_features_container_registry'] = false
                gitlab_rails['env'] = { 'MALLOC_CONF' => 'dirty_decay_ms:1000,muzzy_decay_ms:1000' }
                gitaly['env'] = { 'MALLOC_CONF' => 'dirty_decay_ms:1000,muzzy_decay_ms:1000' }
                postgresql['shared_buffers'] = '128MB'
          readinessProbe:
            httpGet: {path: /users/sign_in, port: http}
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 3
          resources:
            requests: {cpu: 500m, memory: 3Gi}
            limits: {memory: 8Gi}
          volumeMounts:
            - {name: data, mountPath: /etc/gitlab, subPath: config}
            - {name: data, mountPath: /var/opt/gitlab, subPath: data}
            - {name: logs, mountPath: /var/log/gitlab}
            - {name: shm, mountPath: /dev/shm}
      volumes:
        # The server lives as long as the kind cluster; nothing to persist.
        - {name: data, emptyDir: {}}
        - {name: logs, emptyDir: {}}
        - {name: shm, emptyDir: {medium: Memory, sizeLimit: 256Mi}}
---
apiVersion: v1
kind: Service
metadata:
  name: gitlab
  namespace: $NS
spec:
  type: NodePort
  selector: {app: gitlab}
  ports: [{name: http, port: 80, targetPort: http}]
EOF
# A cold boot (reconfigure, migrations) takes about 3 minutes on a CI runner.
"${KUBECTL[@]}" -n "$NS" rollout status deploy/gitlab --timeout=1200s >/dev/null
BASE="http://$(node_ip):$(nodeport "$NS" gitlab http)"
wait_http "$BASE/users/sign_in" 300 || die "gitlab not answering at $BASE"
log "gitlab ($GITLAB_IMAGE) at $BASE"

# api METHOD PATH [JSON] calls the API as root and prints the body.
api() {
  curl -fsS -X "$1" -H "PRIVATE-TOKEN: $(secret_get gitlab-root-token)" \
    -H 'Content-Type: application/json' ${3:+-d "$3"} "$BASE/api/v4$2"
}
status() {
  curl -sS -o /dev/null -w '%{http_code}' -H "PRIVATE-TOKEN: $(secret_get gitlab-root-token)" "$BASE/api/v4$1"
}
jget() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
# token_ok SECRET_NAME: the stored token answers GET /user.
token_ok() {
  local t
  t=$(secret_get "$1")
  [ -n "$t" ] && [ "$(curl -s -o /dev/null -w '%{http_code}' -H "PRIVATE-TOKEN: $t" "$BASE/api/v4/user")" = 200 ]
}

if ! token_ok gitlab-root-token; then
  secret_put gitlab-root-token "glpat-$(openssl rand -hex 16)"
  # gitlab-rails runner runs as user git, so the token file is chowned to it.
  "${KUBECTL[@]}" -n "$NS" exec -i deploy/gitlab -- sh -c \
    'umask 077; cat > /tmp/kardinal-seed-token; chown git:git /tmp/kardinal-seed-token' <"$SECRETS_DIR/gitlab-root-token"
  "${KUBECTL[@]}" -n "$NS" exec -i deploy/gitlab -- sh -c 'cat > /tmp/kardinal-seed.rb' <<'RUBY'
tok = File.read('/tmp/kardinal-seed-token').strip
u = User.find_by_username!('root')
u.personal_access_tokens.where(name: 'kardinal-e2e').each { |t| t.revoke! unless t.revoked? }
t = u.personal_access_tokens.build(name: 'kardinal-e2e', scopes: %w[api sudo], expires_at: 300.days.from_now.to_date)
t.organization_id ||= Organizations::Organization.first.id if t.respond_to?(:organization_id) && defined?(Organizations::Organization)
t.set_token(tok)
t.save!
RUBY
  "${KUBECTL[@]}" -n "$NS" exec deploy/gitlab -- sh -c \
    'gitlab-rails runner /tmp/kardinal-seed.rb; rc=$?; rm -f /tmp/kardinal-seed-token /tmp/kardinal-seed.rb; exit $rc' >&2
  token_ok gitlab-root-token || die "root token not accepted by GET /api/v4/user"
  log "root token created"
fi

api PUT /application/settings '{"allow_local_requests_from_web_hooks_and_services":true}' >/dev/null

BOT_ID=$(api GET "/users?username=kardinal-bot" | jget 'd[0]["id"] if d else ""')
if [ -z "$BOT_ID" ]; then
  BOT_ID=$(api POST /users "{\"username\":\"kardinal-bot\",\"name\":\"kardinal-bot\",\"email\":\"kardinal-bot@example.com\",
    \"password\":\"$(secret_gen gitlab-bot-password 16)\",\"skip_confirmation\":true}" | jget 'd["id"]')
  log "user kardinal-bot created"
fi
if ! token_ok gitlab-bot-token; then
  api POST "/users/$BOT_ID/personal_access_tokens" \
    "{\"name\":\"kardinal-bot-$(date +%s)\",\"scopes\":[\"api\"],\"expires_at\":\"$(date -u -d '+300 days' +%F)\"}" |
    jget 'd["token"]' | {
    read -r t
    secret_put gitlab-bot-token "$t"
  }
  token_ok gitlab-bot-token || die "bot token not accepted by GET /api/v4/user"
  log "token for kardinal-bot created, scopes [api]"
fi

if [ "$(status "/groups/$GROUP")" = 200 ]; then
  GID=$(api GET "/groups/$GROUP" | jget 'd["id"]')
else
  GID=$(api POST /groups "{\"name\":\"$GROUP\",\"path\":\"$GROUP\",\"visibility\":\"public\"}" | jget 'd["id"]')
fi
if [ "$(status "/groups/$GID/members/$BOT_ID")" = 200 ]; then
  api PUT "/groups/$GID/members/$BOT_ID" '{"access_level":40}' >/dev/null
else
  api POST "/groups/$GID/members" "{\"user_id\":$BOT_ID,\"access_level\":40}" >/dev/null
fi
log "group $GROUP: kardinal-bot is Maintainer on every project"

secret_gen webhook-secret 24 >/dev/null
kube_secret "$KARDINAL_NS" git-token token gitlab-bot-token
kube_secret "$KARDINAL_NS" scm-webhook secret webhook-secret

env_set KARDINAL_E2E_SCM_PROVIDER gitlab
env_set KARDINAL_E2E_SCM_API "$INCLUSTER"
env_set KARDINAL_E2E_GIT_KIND gitlab
env_set KARDINAL_E2E_GIT_API "$BASE"
env_set KARDINAL_E2E_GIT_CLONE_BASE "$INCLUSTER"
env_set KARDINAL_E2E_GIT_OWNER "$GROUP"
env_set KARDINAL_E2E_GIT_TOKEN "$(secret_get gitlab-root-token)"
env_set KARDINAL_E2E_WEBHOOK_URL "$KARDINAL_WEBHOOK_URL"
env_set KARDINAL_E2E_WEBHOOK_SECRET "$(secret_get webhook-secret)"
