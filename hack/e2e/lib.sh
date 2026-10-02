#!/usr/bin/env bash
# hack/e2e/lib.sh
#
# Shared helpers for hack/e2e/up.sh and the component scripts. Source it;
# do not run it.
#
# Every kubectl/helm call goes through KUBECTL/HELM, which carry
# --context kind-$KIND_CLUSTER, and require_kind_context (hack/kind-context.sh)
# refuses any context that is not a local kind cluster. The current kube
# context is never used.
#
# Env:
#   KIND_CLUSTER  kind cluster name (set by up.sh: kardinal-e2e-<suite>)
#   E2E_OUT       per-suite state dir (default test/e2e/results/<cluster>):
#                 the env file the tests source, generated secrets (0600)
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$E2E_DIR/../.." && pwd)"
# shellcheck source=hack/kind-context.sh
source "$REPO_ROOT/hack/kind-context.sh"
# shellcheck source=hack/e2e/versions.env
source "$E2E_DIR/versions.env"

: "${KIND_CLUSTER:?KIND_CLUSTER must be set}"
: "${E2E_OUT:=$REPO_ROOT/test/e2e/results/$KIND_CLUSTER}"
CTX="kind-$KIND_CLUSTER"
SECRETS_DIR="$E2E_OUT/secrets"
KARDINAL_NS=kardinal-system
KARDINAL_RELEASE=kardinal-promoter
# The controller's webhook as in-cluster git servers reach it (chart Service
# <release>, port webhook).
KARDINAL_WEBHOOK_URL="http://$KARDINAL_RELEASE.$KARDINAL_NS.svc.cluster.local:8083/webhook/scm"

log() { printf '[e2e %s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() {
  log "ERROR: $*"
  exit 1
}

# kind_cluster NAME creates the kind cluster NAME from test/e2e/kind-config.yaml,
# with the node image NODE_IMAGE if set, unless it exists. kind writes the
# cluster's context to the first file in KUBECONFIG, or to ~/.kube/config
# when KUBECONFIG is unset, so an unset KUBECONFIG defaults to
# $E2E_OUT/kubeconfig (printed) and kind always gets --kubeconfig: the
# user's kubeconfig is never touched. In that default file the context of
# an existing cluster is exported again, for a cluster an older run created.
kind_cluster() {
  local name=$1 file
  if [ -z "${KUBECONFIG:-}" ]; then
    mkdir -p "$E2E_OUT"
    export KUBECONFIG=$E2E_OUT/kubeconfig
    log "KUBECONFIG is not set: using $KUBECONFIG"
  fi
  file=${KUBECONFIG%%:*}
  if ! kind get clusters 2>/dev/null | grep -qx "$name"; then
    kind create cluster --name "$name" --config "$REPO_ROOT/test/e2e/kind-config.yaml" --kubeconfig "$file" \
      ${NODE_IMAGE:+--image "$NODE_IMAGE"} --wait 120s
  elif [ "$file" = "$E2E_OUT/kubeconfig" ]; then
    kind export kubeconfig --name "$name" --kubeconfig "$file" >&2
  fi
}

# target_cluster checks CTX and points KUBECTL and HELM at it.
target_cluster() {
  use_kind_context "$CTX" >&2
}

# node_ip is the kind node's address on the docker network; the host reaches
# NodePort Services on it.
node_ip() {
  docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$KIND_CLUSTER-control-plane"
}

# nodeport NS SVC PORT_NAME
nodeport() {
  "${KUBECTL[@]}" -n "$1" get svc "$2" -o jsonpath="{.spec.ports[?(@.name==\"$3\")].nodePort}"
}

# wait_http URL TIMEOUT_SECONDS [CURL_ARGS...] waits for a 2xx or 3xx.
wait_http() {
  local url=$1 timeout=$2 waited=0
  shift 2
  until curl -fs -o /dev/null -m 5 "$@" "$url"; do
    waited=$((waited + 2))
    [ "$waited" -ge "$timeout" ] && return 1
    sleep 2
  done
}

# secret_get NAME / secret_put NAME VALUE / secret_gen NAME [BYTES] keep
# generated credentials in SECRETS_DIR, mode 0600. Values are never printed.
secret_get() { cat "$SECRETS_DIR/$1" 2>/dev/null || true; }
secret_put() {
  mkdir -p "$SECRETS_DIR" && chmod 700 "$SECRETS_DIR"
  (umask 077 && printf '%s' "$2" >"$SECRETS_DIR/$1")
}
secret_gen() {
  [ -s "$SECRETS_DIR/$1" ] || secret_put "$1" "$(openssl rand -hex "${2:-20}")"
  secret_get "$1"
}

# kube_secret NS NAME KEY SECRET creates or updates a generic Secret whose
# KEY holds the SECRETS_DIR entry SECRET.
kube_secret() {
  "${KUBECTL[@]}" create namespace "$1" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
  "${KUBECTL[@]}" -n "$1" create secret generic "$2" --from-file="$3=$SECRETS_DIR/$4" \
    --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null
}

# mirror_of IMAGE prints mirror.gcr.io's copy of a Docker Hub image
# (docker.io/..., public.ecr.aws/docker/library/...), or nothing.
mirror_of() {
  case "$1" in
    public.ecr.aws/docker/library/*) echo "mirror.gcr.io/library/${1#public.ecr.aws/docker/library/}" ;;
    docker.io/*) echo "mirror.gcr.io/${1#docker.io/}" ;;
  esac
}

# pull_retry CMD... IMAGE runs CMD IMAGE, then CMD with IMAGE's mirror, for
# up to five rounds with growing pauses, and prints the reference that
# worked: registries rate-limit shared CI runners (public.ecr.aws and Docker
# Hub answer 429).
pull_retry() {
  local img=${*: -1} m src try
  local cmd=("${@:1:$#-1}") srcs=("${*: -1}")
  m=$(mirror_of "$img")
  [ -z "$m" ] || srcs+=("$m")
  for try in 1 2 3 4 5; do
    for src in "${srcs[@]}"; do
      if "${cmd[@]}" "$src" >/dev/null 2>&1; then
        echo "$src"
        return 0
      fi
    done
    log "pulling $img failed (round $try of 5)"
    [ "$try" -eq 5 ] || sleep $((try * 10))
  done
  return 1
}

# pull_images IMAGE... pulls images into the kind node before a component's
# pods need them (pods with imagePullPolicy IfNotPresent then start from the
# node's copy). A mirror's copy is tagged with the original name.
pull_images() {
  local node="$KIND_CLUSTER-control-plane" img src
  for img in "$@"; do
    docker exec "$node" crictl inspecti -q "$img" >/dev/null 2>&1 && continue
    src=$(pull_retry docker exec "$node" crictl pull "$img") || die "can't pull $img"
    [ "$src" = "$img" ] || docker exec "$node" ctr -n k8s.io images tag --force "$src" "$img" >/dev/null
  done
}

# load_image IMAGE puts a local or pullable image into the kind node.
# `kind load` fails on hosts whose containerd snapshotter it can't detect;
# piping docker save into the node's ctr works everywhere.
load_image() {
  local img=$1 node="$KIND_CLUSTER-control-plane" src
  if ! docker image inspect "$img" >/dev/null 2>&1; then
    src=$(pull_retry docker pull -q "$img") || die "can't pull $img"
    [ "$src" = "$img" ] || docker tag "$src" "$img"
  fi
  if ! kind load docker-image "$img" --name "$KIND_CLUSTER" >/dev/null 2>&1; then
    docker save "$img" | docker exec -i "$node" ctr -n k8s.io images import - >/dev/null
  fi
}

# dump_setup_diagnostics writes what a failed setup left behind to
# E2E_OUT/diagnostics/setup (CI uploads it) and prints the pods that are not
# ready. Logs are only of those pods; describe and events show no secret values.
dump_setup_diagnostics() {
  local dir="$E2E_OUT/diagnostics/setup" ns pod
  mkdir -p "$dir"
  "${KUBECTL[@]}" get pods -A -o wide >"$dir/pods.txt" 2>&1 || true
  "${KUBECTL[@]}" get events -A --sort-by=.lastTimestamp >"$dir/events.txt" 2>&1 || true
  log "setup failed; pods not ready (full state in $dir):"
  "${KUBECTL[@]}" get pods -A --no-headers 2>/dev/null |
    awk '{split($3, r, "/"); if (r[1] != r[2] && $4 != "Completed") print $1, $2}' |
    while read -r ns pod; do
      "${KUBECTL[@]}" -n "$ns" get pod "$pod" --no-headers >&2 || true
      "${KUBECTL[@]}" -n "$ns" describe pod "$pod" >"$dir/$ns.$pod.describe.txt" 2>&1 || true
      "${KUBECTL[@]}" -n "$ns" logs "$pod" --all-containers --tail=200 >"$dir/$ns.$pod.log" 2>&1 || true
    done
}

# env_set KEY VALUE records KEY=VALUE in the env file the tests source.
env_set() {
  local file="$E2E_OUT/env"
  mkdir -p "$E2E_OUT"
  (umask 077 && touch "$file")
  grep -v "^export $1=" "$file" >"$file.tmp" || true
  printf 'export %s=%q\n' "$1" "$2" >>"$file.tmp"
  mv "$file.tmp" "$file"
}
