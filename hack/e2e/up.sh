#!/usr/bin/env bash
# hack/e2e/up.sh SUITE
#
# Creates the kind cluster kardinal-e2e-SUITE and installs what SUITE's live
# tests need: kro, the suite's git server and GitOps engine, and the
# controller and CLI built from this checkout. Writes
# test/e2e/results/<cluster>/env, which `make test-e2e-live` sources.
# Re-running reuses the cluster and re-applies every component.
#
# Suites (test/e2e/README.md lists the tests each one runs):
#   core    Forgejo + Argo CD, webhook receiver, OCI registry, Bundle API
#   gitea   Gitea + Argo CD
#   gitlab  GitLab CE + Argo CD
#   github  real GitHub (branches of one shared repo, components/github.sh
#           says which token it uses) + Argo CD
#   delivery Forgejo + Argo CD + Argo Rollouts + Flagger
#   ui      Forgejo + Argo CD + the UI auth, CORS and TLS releases (ui.sh)
#   flux    Forgejo + Flux + Prometheus Operator, Prometheus, Pushgateway,
#           Grafana
#
# Env:
#   KIND_CLUSTER     cluster name (default kardinal-e2e-SUITE)
#   KIND_K8S         Kubernetes minor, e.g. 1.37: boots KIND_NODE_1_37 from
#                    hack/tool-versions.env (default: the node image in
#                    test/e2e/kind-config.yaml)
#   KUBECONFIG       honoured; recorded in the env file
#   plus the KARDINAL_E2E_* build settings of components/kardinal.sh
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

SUITE=${1:?usage: $0 SUITE}
# AFTER lists components that need the controller image, so they run after
# components/kardinal.sh. HELM_ARGS are the suite's chart values for the main
# release, before KARDINAL_E2E_HELM_ARGS.
AFTER=() HELM_ARGS=
case "$SUITE" in
  # RUN is the go test -run pattern of the suite's tests; the prefix names
  # the area (test/e2e/README.md). The core, gitea, gitlab and github suites
  # each run the TestCore_ promotion tests and the provider-agnostic TestSCM_
  # tests against their git server, plus the tests named after it.
  core) COMPONENTS=("giteafamily.sh forgejo" argocd.sh webhook-receiver.sh registry.sh ciapi.sh)
    RUN='^Test(Core|SCM|Forgejo|Gate|Bundle|Pipeline|Graph|Step|Rollback|Health|CLI|CIAPI|Notify|Sub|Audit)_' ;;
  gitea) COMPONENTS=("giteafamily.sh gitea" argocd.sh) RUN='^Test(Core|SCM|Gitea)_' ;;
  gitlab) COMPONENTS=(gitlab.sh argocd.sh) RUN='^Test(Core|SCM|GitLab)_' ;;
  github) COMPONENTS=(github.sh argocd.sh) RUN='^Test(Core|SCM|GitHub)_' ;;
  delivery) COMPONENTS=("giteafamily.sh forgejo" argocd.sh rollouts.sh flagger.sh)
    RUN='^Test(Rollouts|Flagger|Delivery)_' ;;
  # The UI API and the web app in a browser: the main release with no UI
  # auth (reached through kubectl port-forward; the browser also uses the
  # allowed host kardinal-ui.test), plus the auth, CORS and TLS releases of
  # components/ui.sh.
  ui) COMPONENTS=("giteafamily.sh forgejo" argocd.sh) AFTER=(ui.sh) RUN='^TestUI_'
    HELM_ARGS='--set ui.allowedHosts={kardinal-ui.test}' ;;
  # Flux health checks, MetricChecks against Prometheus, and the chart's
  # ServiceMonitor, PrometheusRule and Grafana dashboard.
  flux) COMPONENTS=("giteafamily.sh forgejo" flux.sh prometheus.sh grafana.sh) RUN='^Test(Flux|Metric|Obs)_'
    HELM_ARGS='--set serviceMonitor.enabled=true --set prometheusRule.enabled=true --set grafanaDashboard.enabled=true' ;;
  *)
    echo "unknown suite $SUITE" >&2
    exit 1
    ;;
esac
export KIND_CLUSTER=${KIND_CLUSTER:-kardinal-e2e-$SUITE}
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
export E2E_OUT

NODE_IMAGE=
if [ -n "${KIND_K8S:-}" ]; then
  var="KIND_NODE_${KIND_K8S//./_}"
  # shellcheck disable=SC1091
  NODE_IMAGE=$(source "$REPO_ROOT/hack/tool-versions.env" && echo "${!var:-}")
  [ -n "$NODE_IMAGE" ] || die "KIND_K8S=$KIND_K8S: no $var in hack/tool-versions.env"
fi

start=$(date +%s)
if ! kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER"; then
  kind create cluster --name "$KIND_CLUSTER" --config "$REPO_ROOT/test/e2e/kind-config.yaml" \
    ${NODE_IMAGE:+--image "$NODE_IMAGE"} --wait 120s
fi
target_cluster
trap 'rc=$?; [ "$rc" -eq 0 ] || dump_setup_diagnostics; exit "$rc"' EXIT
if [ -n "$NODE_IMAGE" ]; then
  have=$("${KUBECTL[@]}" version -o json | python3 -c 'import json,sys; v=json.load(sys.stdin)["serverVersion"]; print(v["major"]+"."+v["minor"].rstrip("+"))')
  [ "$have" = "$KIND_K8S" ] || die "$KIND_CLUSTER runs Kubernetes $have, not $KIND_K8S; make e2e-down SUITE=$SUITE first"
fi
mkdir -p "$E2E_OUT"
rm -f "$E2E_OUT/env"
env_set KARDINAL_E2E_SUITE "$SUITE"
env_set KARDINAL_E2E_CONTEXT "$CTX"
env_set KARDINAL_E2E_RUN "$RUN"
env_set KARDINAL_E2E_ARTIFACTS "$E2E_OUT/diagnostics"
[ -n "${KUBECONFIG:-}" ] && env_set KUBECONFIG "$KUBECONFIG"

KUBE_CONTEXT="$CTX" bash "$REPO_ROOT/hack/install-kro.sh" >/dev/null
log "kro ready"
for c in "${COMPONENTS[@]}"; do
  # shellcheck disable=SC2086
  bash "$E2E_DIR/components/"$c
done
KARDINAL_E2E_HELM_ARGS="$HELM_ARGS ${KARDINAL_E2E_HELM_ARGS:-}" bash "$E2E_DIR/components/kardinal.sh"
for c in "${AFTER[@]}"; do
  # shellcheck disable=SC2086
  bash "$E2E_DIR/components/"$c
done
log "suite $SUITE up on $CTX in $(($(date +%s) - start))s; env: $E2E_OUT/env"
