#!/usr/bin/env bash
# hack/e2e/components/github.sh
#
# Wires the suite to real GitHub. Nothing is installed: tests work on
# branches of one shared public repo (KARDINAL_E2E_GITHUB_REPO, default
# pnz1990/kardinal-demo). Each test creates its own branch under e2e/ and
# deletes it, with every PR into it, when it ends. Argo CD in the kind
# cluster clones the public repo anonymously.
#
# One token is both the test runner's and the controller's (the PR author).
# It needs contents and pull request write on the repo. It comes from, in
# order:
#   KARDINAL_E2E_GITHUB_TOKEN_FILE  a file holding the token
#   DEMO_GITHUB_TOKEN               the token (the CI secret)
#   gh auth token                   the local gh login
# and is kept in SECRETS_DIR (0600) and the Secret kardinal-system/git-token.
# It is never printed.
#
# GitHub can't reach the kind cluster, so no webhook is registered; tests
# that need one post a signed delivery to the controller themselves
# (KARDINAL_E2E_WEBHOOK_SECRET, Secret kardinal-system/scm-webhook).
# Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

REPO=${KARDINAL_E2E_GITHUB_REPO:-pnz1990/kardinal-demo}

if [ -n "${KARDINAL_E2E_GITHUB_TOKEN_FILE:-}" ]; then
  [ -s "$KARDINAL_E2E_GITHUB_TOKEN_FILE" ] || die "KARDINAL_E2E_GITHUB_TOKEN_FILE=$KARDINAL_E2E_GITHUB_TOKEN_FILE is empty or missing"
  secret_put github-token "$(tr -d '\r\n' <"$KARDINAL_E2E_GITHUB_TOKEN_FILE")"
elif [ -n "${DEMO_GITHUB_TOKEN:-}" ]; then
  secret_put github-token "$DEMO_GITHUB_TOKEN"
elif command -v gh >/dev/null && tok=$(gh auth token 2>/dev/null) && [ -n "$tok" ]; then
  secret_put github-token "$tok"
  unset tok
else
  die "no GitHub token: set KARDINAL_E2E_GITHUB_TOKEN_FILE or DEMO_GITHUB_TOKEN, or log in with gh"
fi

# The token must be able to push to the repo. The status says why it can't:
# 401 is a token GitHub does not accept (expired, revoked or mistyped), 403
# or 404 one without access to the repo.
resp=$(curl -sS -w '\n%{http_code}' -H "Authorization: Bearer $(secret_get github-token)" -H 'X-GitHub-Api-Version: 2022-11-28' \
  "https://api.github.com/repos/$REPO") || die "can't reach api.github.com"
code=${resp##*$'\n'}
case $code in
  200) ;;
  401) die "GitHub does not accept the token (401): it is expired, revoked or wrong" ;;
  *) die "the GitHub token can't read $REPO ($code)" ;;
esac
perm=$(python3 -c 'import json,sys; d=json.load(sys.stdin); print("%s %s" % (d.get("permissions",{}).get("push",False), d.get("private")))' <<<"${resp%$'\n'*}") ||
  die "the GitHub answer for $REPO is not JSON"
[ "${perm%% *}" = True ] || die "the GitHub token can't push to $REPO"
[ "${perm##* }" = False ] || die "$REPO is private; Argo CD clones it anonymously"
log "github: $REPO, token can push"

secret_gen webhook-secret 24 >/dev/null
kube_secret "$KARDINAL_NS" git-token token github-token
kube_secret "$KARDINAL_NS" scm-webhook secret webhook-secret

env_set KARDINAL_E2E_SCM_PROVIDER github
env_set KARDINAL_E2E_GIT_KIND github
env_set KARDINAL_E2E_GIT_REPO "$REPO"
env_set KARDINAL_E2E_GIT_TOKEN "$(secret_get github-token)"
env_set KARDINAL_E2E_WEBHOOK_SECRET "$(secret_get webhook-secret)"
