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
#           says which token it uses) + Argo CD, webhook receiver
#   delivery Forgejo + Argo CD + Argo Rollouts + Flagger
#   ui      Forgejo + Argo CD + the UI auth, CORS and TLS releases (ui.sh)
#   flux    Forgejo + Flux + Prometheus Operator, Prometheus, Pushgateway,
#           Grafana
#   chart   Forgejo + Argo CD + cert-manager + podinfo on the node, and no
#           controller release: each TestChart_ test installs the chart from
#           this checkout itself
#   upgrade Forgejo + Argo CD + kardinal-promoter v0.8.1 (kardinal-v081.sh) and
#           no kro: the TestUpgrade_ test upgrades v0.8.1 to this checkout,
#           so the cluster serves one run; delete it before the next
#   multi-cluster  the hub (Forgejo, Argo CD, Flux, Argo Rollouts, kardinal)
#           and a second kind cluster, <cluster>-spoke, with Argo Rollouts,
#           which the hub's Argo CD and Flux manage (spoke.sh); both have
#           podinfo on the node
#   scale   Forgejo behind Toxiproxy (git latency and outages), Prometheus,
#           and two controller replicas built with -race (KARDINAL_E2E_RACE,
#           default 1 here): the TestScale_ topology, load, race and chaos
#           tests and their invariants (test/e2e/framework/scale); sizes
#           come from KARDINAL_E2E_SCALE_PROFILE at test time (ci, full, soak)
#
# Env:
#   KIND_CLUSTER     cluster name (default kardinal-e2e-SUITE)
#   KIND_K8S         Kubernetes minor, e.g. 1.37: boots KIND_NODE_1_37 from
#                    hack/tool-versions.env, or KIND_NODE_<SUITE>_1_37 there
#                    for a minor only SUITE runs on (default: the node image
#                    in test/e2e/kind-config.yaml)
#   KUBECONFIG       honoured; recorded in the env file. When unset, it is
#                    test/e2e/results/<cluster>/kubeconfig, so kind does not
#                    write the default ~/.kube/config
#   KARDINAL_E2E_NODE_MEMORY  docker memory limit of the kind node, e.g. 24g
#                    (default none; the scale suite's full profile wants one
#                    on a shared host)
#   KARDINAL_E2E_TOOLS  pinned (default): install the kind, kubectl and helm
#                    of hack/tool-versions.env into bin/e2e (tools.sh) and use
#                    them; path: use the ones on PATH
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
  # The webhook receiver is the other API host TestGitHub_SCMAPIURL points
  # --scm-api-url at.
  github) COMPONENTS=(github.sh argocd.sh webhook-receiver.sh) RUN='^Test(Core|SCM|GitHub)_' ;;
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
  chart) COMPONENTS=("giteafamily.sh forgejo" argocd.sh cert-manager.sh podinfo.sh) RUN='^Test(Chart|Deprecated)_'
    export KARDINAL_E2E_INSTALL=0 ;;
  # v0.8.1 ran its own Graph controller, so kro is not installed: the test
  # installs it as the upgrade guide's step 6. kardinal.sh only builds and
  # loads the image and the CLI; the test applies the CRDs and upgrades.
  upgrade) COMPONENTS=("giteafamily.sh forgejo" argocd.sh kardinal-v081.sh) RUN='^TestUpgrade_'
    export KARDINAL_E2E_INSTALL=build KARDINAL_E2E_KRO=0 ;;
  # Argo Rollouts runs in the hub too, so a Rollout the hub's argoRollouts
  # check cannot find is missing from the hub, not from its API.
  multi-cluster) COMPONENTS=("giteafamily.sh forgejo" argocd.sh flux.sh rollouts.sh podinfo.sh spoke.sh)
    RUN='^TestMultiCluster_' ;;
  # Production-scale topologies, load, races and chaos. The controller
  # reaches Forgejo through Toxiproxy (components/toxiproxy.sh), runs two
  # replicas so a killed leader fails over, and is built with -race; its
  # ServiceMonitor feeds the invariants' Prometheus queries. Info logs: the
  # invariants read every controller log line.
  scale) COMPONENTS=("toxiproxy.sh forgejo.forgejo.svc.cluster.local:3000" "giteafamily.sh forgejo" prometheus.sh)
    RUN='^TestScale_'
    export KARDINAL_E2E_RACE=${KARDINAL_E2E_RACE:-1}
    export KARDINAL_E2E_GIT_ROOT_URL=http://toxiproxy.toxiproxy.svc.cluster.local:3000
    HELM_ARGS='--set serviceMonitor.enabled=true --set replicaCount=2 --set logLevel=info
      --set resources.limits.cpu=4 --set resources.limits.memory=4Gi --set resources.requests.cpu=500m --set resources.requests.memory=512Mi' ;;
  *)
    echo "unknown suite $SUITE" >&2
    exit 1
    ;;
esac
export KIND_CLUSTER=${KIND_CLUSTER:-kardinal-e2e-$SUITE}
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
export E2E_OUT
bash "$E2E_DIR/tools.sh"

NODE_IMAGE=
if [ -n "${KIND_K8S:-}" ]; then
  suitevar=${SUITE^^}
  var="KIND_NODE_${KIND_K8S//./_}" suitevar="KIND_NODE_${suitevar//-/_}_${KIND_K8S//./_}"
  # shellcheck disable=SC1091
  NODE_IMAGE=$(source "$REPO_ROOT/hack/tool-versions.env" && echo "${!var:-${!suitevar:-}}")
  [ -n "$NODE_IMAGE" ] || die "KIND_K8S=$KIND_K8S: no $var or $suitevar in hack/tool-versions.env"
fi

start=$(date +%s)
kind_cluster "$KIND_CLUSTER"
target_cluster
if [ -n "${KARDINAL_E2E_NODE_MEMORY:-}" ]; then
  docker update --memory "$KARDINAL_E2E_NODE_MEMORY" --memory-swap "$KARDINAL_E2E_NODE_MEMORY" \
    "$KIND_CLUSTER-control-plane" >/dev/null
  log "kind node memory limit $KARDINAL_E2E_NODE_MEMORY"
fi
on_exit() {
  local rc=$?
  [ "$rc" -eq 0 ] || dump_setup_diagnostics
  exit "$rc"
}
trap on_exit EXIT
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
env_set KUBECONFIG "$KUBECONFIG"

if [ "${KARDINAL_E2E_KRO:-1}" = 1 ]; then
  KUBE_CONTEXT="$CTX" bash "$REPO_ROOT/hack/install-kro.sh" >/dev/null
  log "kro ready"
fi
for c in "${COMPONENTS[@]}"; do
  # shellcheck disable=SC2086
  bash "$E2E_DIR/components/"$c
done
KARDINAL_E2E_HELM_ARGS="$HELM_ARGS ${KARDINAL_E2E_HELM_ARGS:-}" bash "$E2E_DIR/components/kardinal.sh"
if [ "${KARDINAL_E2E_INSTALL:-1}" != 1 ]; then
  # No release of this checkout in kardinal-system receives webhooks; tests
  # that need one register their own release's URL.
  env_set KARDINAL_E2E_WEBHOOK_URL ""
fi
for c in "${AFTER[@]}"; do
  # shellcheck disable=SC2086
  bash "$E2E_DIR/components/"$c
done
log "suite $SUITE up on $CTX in $(($(date +%s) - start))s; env: $E2E_OUT/env; KUBECONFIG=$KUBECONFIG"
