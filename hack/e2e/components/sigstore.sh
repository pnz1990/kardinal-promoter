#!/usr/bin/env bash
# hack/e2e/components/sigstore.sh
#
# A private sigstore for keyless receipts (the receipts-keyless suite,
# spec 21 section 13): sigstore scaffolding SIGSTORE_SCAFFOLDING_RELEASE's
# Fulcio, Rekor v2, CT log, timestamp authority and TUF repository, on
# Knative Serving KNATIVE_RELEASE with Kourier. Fulcio trusts the cluster's
# own service account issuer (https://kubernetes.default.svc.cluster.local,
# kind's default), whose discovery document and keys the API server serves
# to unauthenticated callers here (system:service-account-issuer-discovery).
# The services are reachable in the cluster only, at
# http://<service>.<service>-system.svc.cluster.local, and the controller
# reaches Fulcio, Rekor and the TSA through hack/e2e/faultproxy (namespace
# sigstore-faults), which the outage tests switch. Writes the Sigstore
# trusted root (trusted_root.json, from the TUF repository) to
# $E2E_OUT/sigstore/trusted_root.json and the URLs to the env file.
# Idempotent: keys are generated once per cluster and kept in $SECRETS_DIR.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster
# shellcheck disable=SC1091
source "$E2E_DIR/versions.env"

DIR="$E2E_OUT/sigstore"
mkdir -p "$DIR"

# fetch URL SHA256 FILE: download once and check the pinned digest.
fetch() {
  local url=$1 sum=$2 out="$DIR/$3"
  if ! echo "$sum  $out" | sha256sum -c --status 2>/dev/null; then
    curl -fsSL --retry 5 -o "$out.tmp" "$url"
    echo "$sum  $out.tmp" | sha256sum -c --status || die "$url: sha256 mismatch"
    mv "$out.tmp" "$out"
  fi
  echo "$out"
}
# images FILE: the images a manifest runs, pre-pulled into the node.
images() {
  awk '($1 == "image:" || ($1 == "-" && $2 == "image:")) && $NF != "image:" {print $NF}' "$1" | tr -d '"' | sort -u
}

kn="https://github.com/knative"
crds=$(fetch "$kn/serving/releases/download/$KNATIVE_RELEASE/serving-crds.yaml" "$KNATIVE_SERVING_CRDS_SHA256" serving-crds.yaml)
core=$(fetch "$kn/serving/releases/download/$KNATIVE_RELEASE/serving-core.yaml" "$KNATIVE_SERVING_CORE_SHA256" serving-core.yaml)
kourier=$(fetch "$kn/net-kourier/releases/download/$KNATIVE_RELEASE/kourier.yaml" "$KNATIVE_KOURIER_SHA256" kourier.yaml)
sc="https://github.com/sigstore/scaffolding/releases/download/$SIGSTORE_SCAFFOLDING_RELEASE"
fulcio=$(fetch "$sc/release-fulcio.yaml" "$SIGSTORE_FULCIO_SHA256" release-fulcio.yaml)
rekor=$(fetch "$sc/release-rekor-tiles.yaml" "$SIGSTORE_REKOR_TILES_SHA256" release-rekor-tiles.yaml)
ctlog=$(fetch "$sc/release-ctlog.yaml" "$SIGSTORE_CTLOG_SHA256" release-ctlog.yaml)
tsa=$(fetch "$sc/release-tsa.yaml" "$SIGSTORE_TSA_SHA256" release-tsa.yaml)
tuf=$(fetch "$sc/release-tuf.yaml" "$SIGSTORE_TUF_SHA256" release-tuf.yaml)
mapfile -t imgs < <(images "$core"; images "$kourier"; for f in "$fulcio" "$rekor" "$ctlog" "$tsa" "$tuf"; do images "$f"; done)
pull_images "${imgs[@]}"

# Knative Serving and Kourier. Services get cluster-local URLs
# (<name>.<namespace>.svc.cluster.local), one replica each, kept up.
"${KUBECTL[@]}" apply -f "$crds" >/dev/null
"${KUBECTL[@]}" wait --for=condition=Established crd/services.serving.knative.dev --timeout=60s >/dev/null
"${KUBECTL[@]}" apply -f "$core" >/dev/null
"${KUBECTL[@]}" apply -f "$kourier" >/dev/null
"${KUBECTL[@]}" -n knative-serving rollout status deploy --timeout=300s >/dev/null
patch_cm() {
  until "${KUBECTL[@]}" -n knative-serving patch configmap "$1" --type merge -p "$2" >/dev/null 2>&1; do sleep 2; done
}
patch_cm config-network '{"data":{"ingress-class":"kourier.ingress.networking.knative.dev"}}'
patch_cm config-domain '{"data":{"svc.cluster.local":""}}'
patch_cm config-features '{"data":{"kubernetes.podspec-fieldref":"enabled","kubernetes.podspec-volumes-emptydir":"enabled","multicontainer":"enabled","kubernetes.podspec-persistent-volume-claim":"enabled","kubernetes.podspec-persistent-volume-write":"enabled"}}'
patch_cm config-autoscaler '{"data":{"min-scale":"1","max-scale":"1"}}'
log "knative serving $KNATIVE_RELEASE ready"

# Fulcio fetches the cluster issuer's discovery document and keys.
"${KUBECTL[@]}" create clusterrolebinding oidc-reviewer --clusterrole=system:service-account-issuer-discovery \
  --group=system:unauthenticated --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f - >/dev/null

# Keys, generated once per cluster.
keys=$SECRETS_DIR/sigstore
mkdir -p "$keys"
(
  umask 077
  [ -s "$keys/rekor.pass" ] || uuidgen >"$keys/rekor.pass"
  [ -s "$keys/rekor.key" ] || openssl genpkey -algorithm ed25519 -out "$keys/rekor.key" -pass "pass:$(cat "$keys/rekor.pass")"
  openssl pkey -in "$keys/rekor.key" -pubout -out "$keys/rekor.pub"
  [ -s "$keys/fulcio.pass" ] || uuidgen >"$keys/fulcio.pass"
  if [ ! -s "$keys/fulcio.key" ]; then
    openssl ecparam -name prime256v1 -genkey | openssl pkcs8 -topk8 -passout "pass:$(cat "$keys/fulcio.pass")" -out "$keys/fulcio.key"
    openssl req -x509 -new -key "$keys/fulcio.key" -passin "pass:$(cat "$keys/fulcio.pass")" -out "$keys/fulcio.crt" \
      -sha256 -days 10 -subj "/O=kardinal-e2e/CN=fulcio.kardinal-e2e.test"
  fi
  [ -s "$keys/ctlog.key" ] || openssl ecparam -name prime256v1 -genkey -noout -out "$keys/ctlog.key"
  openssl ec -in "$keys/ctlog.key" -pubout -out "$keys/ctlog.pub" 2>/dev/null
)
b64() { base64 -w0 <"$1"; }
b64s() { printf '%s' "$1" | base64 -w0; }

sed -e "s|<private-placeholder>|$(b64 "$keys/rekor.key")|" -e "s|<public-placeholder>|$(b64 "$keys/rekor.pub")|" \
  -e "s|<password-placeholder>|$(b64s "$(cat "$keys/rekor.pass")")|" "$rekor" | "${KUBECTL[@]}" apply -f - >/dev/null
sed -e "s|<private-placeholder>|$(b64 "$keys/fulcio.key")|" -e "s|<cert-placeholder>|$(b64 "$keys/fulcio.crt")|" \
  -e "s|<password-placeholder>|$(b64s "$(cat "$keys/fulcio.pass")")|" "$fulcio" | "${KUBECTL[@]}" apply -f - >/dev/null
sed -e "s|<private-placeholder>|$(b64 "$keys/ctlog.key")|" -e "s|<public-placeholder>|$(b64 "$keys/ctlog.pub")|" \
  -e "s|<cert-placeholder>|$(b64 "$keys/fulcio.crt")|" "$ctlog" | "${KUBECTL[@]}" apply -f - >/dev/null
"${KUBECTL[@]}" apply -f "$tsa" >/dev/null
ksvc_ready() {
  local ns=$1 name=$2
  if "${KUBECTL[@]}" -n "$ns" get job -o name 2>/dev/null | grep -q .; then
    "${KUBECTL[@]}" -n "$ns" wait --for=condition=Complete jobs --all --timeout=300s >/dev/null
  fi
  "${KUBECTL[@]}" -n "$ns" wait --for=condition=Ready "ksvc/$name" --timeout=300s >/dev/null
}
ksvc_ready rekor-system rekor
ksvc_ready fulcio-system fulcio
ksvc_ready ctlog-system ctlog
ksvc_ready tsa-system tsa
log "fulcio, rekor (v2), ctlog and tsa ready"

# The TUF repository is built from the services' public keys.
"${KUBECTL[@]}" apply -f "$tuf" >/dev/null
copy_secret() {
  "${KUBECTL[@]}" -n "$1" get secret "$2" -o json |
    python3 -c 'import json,sys; s=json.load(sys.stdin); m=s["metadata"]; s["metadata"]={"name":m["name"],"namespace":"tuf-system"}; print(json.dumps(s))' |
    "${KUBECTL[@]}" apply -f - >/dev/null
}
copy_secret ctlog-system ctlog-public-key
copy_secret fulcio-system fulcio-pub-key
copy_secret rekor-system rekor-pub-key
copy_secret tsa-system tsa-cert-chain
ksvc_ready tuf-system tuf

url() { "${KUBECTL[@]}" -n "$1" get ksvc "$2" -o jsonpath='{.status.url}'; }
FULCIO_URL=$(url fulcio-system fulcio)
REKOR_URL=$(url rekor-system rekor)
TSA_URL=$(url tsa-system tsa)/api/v1/timestamp
TUF_URL=$(url tuf-system tuf)
# The trusted root, as a verifier outside the cluster gets it: from the TUF
# repository's targets, through a Pod in the cluster.
"${KUBECTL[@]}" -n tuf-system get secret tuf-root -o jsonpath='{.data.root}' | base64 -d >"$DIR/root.json"
# The repository uses consistent snapshots: the file is
# targets/<sha512>.trusted_root.json, the hash from targets.json.
pull_images "$CURL_IMAGE"
# in_cluster URL OUT: GET URL from a Pod in the cluster, to OUT.
in_cluster() {
  local pod=sigstore-get
  "${KUBECTL[@]}" -n tuf-system delete pod "$pod" --ignore-not-found --wait >/dev/null
  "${KUBECTL[@]}" -n tuf-system run "$pod" --restart=Never --image="$CURL_IMAGE" --command -- curl -fsS "$1" >/dev/null
  "${KUBECTL[@]}" -n tuf-system wait --for=jsonpath='{.status.phase}'=Succeeded "pod/$pod" --timeout=120s >/dev/null ||
    die "GET $1 from the cluster failed: $("${KUBECTL[@]}" -n tuf-system logs "$pod" 2>&1 | tail -3)"
  "${KUBECTL[@]}" -n tuf-system logs "$pod" >"$2"
  "${KUBECTL[@]}" -n tuf-system delete pod "$pod" --wait=false >/dev/null
}
in_cluster "$TUF_URL/targets.json" "$DIR/targets.json"
hash=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["signed"]["targets"]["trusted_root.json"]["hashes"]["sha512"])' \
  "$DIR/targets.json")
in_cluster "$TUF_URL/targets/$hash.trusted_root.json" "$DIR/trusted_root.json"
echo "$hash  $DIR/trusted_root.json" | sha512sum -c --status || die "trusted_root.json does not match the TUF targets' hash"
python3 -c 'import json,sys; t=json.load(open(sys.argv[1])); assert t.get("tlogs") and t.get("certificateAuthorities"), "no trusted root"' \
  "$DIR/trusted_root.json" || die "no trusted_root.json from the TUF repository"

# The controller reaches Fulcio, Rekor and the TSA through hack/e2e/faultproxy,
# which the outage tests switch to down, slow or an error status, and which
# counts each route's answers.
FAULTS=sigstore-faults
FAULT_IMAGE="kardinal-e2e/faultproxy:$KIND_CLUSTER"
ctx=$(mktemp -d)
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -ldflags="-s -w" \
  -o "$ctx/faultproxy" ./hack/e2e/faultproxy)
chmod 0755 "$ctx" "$ctx/faultproxy"
printf 'FROM scratch\nCOPY faultproxy /faultproxy\nUSER 65532:65532\nENTRYPOINT ["/faultproxy"]\n' >"$ctx/Dockerfile"
docker build -q -t "$FAULT_IMAGE" "$ctx" >/dev/null
load_image "$FAULT_IMAGE"
"${KUBECTL[@]}" apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Namespace
metadata: {name: $FAULTS}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: faultproxy, namespace: $FAULTS}
spec:
  replicas: 1
  selector: {matchLabels: {app: faultproxy}}
  template:
    metadata: {labels: {app: faultproxy}}
    spec:
      containers:
        - name: faultproxy
          image: $FAULT_IMAGE
          imagePullPolicy: Never
          args: [-admin=:9000, -route=fulcio=:8081=$FULCIO_URL, -route=rekor=:8082=$REKOR_URL,
            "-route=tsa=:8083=$(url tsa-system tsa)"]
          securityContext: {allowPrivilegeEscalation: false, runAsNonRoot: true, capabilities: {drop: [ALL]}}
          readinessProbe: {httpGet: {path: /routes, port: 9000}, periodSeconds: 2}
---
apiVersion: v1
kind: Service
metadata: {name: faultproxy, namespace: $FAULTS}
spec:
  selector: {app: faultproxy}
  ports:
    - {name: admin, port: 9000}
    - {name: fulcio, port: 8081}
    - {name: rekor, port: 8082}
    - {name: tsa, port: 8083}
YAML
"${KUBECTL[@]}" -n "$FAULTS" rollout restart deploy/faultproxy >/dev/null
"${KUBECTL[@]}" -n "$FAULTS" rollout status deploy/faultproxy --timeout=120s >/dev/null
proxy="http://faultproxy.$FAULTS.svc.cluster.local"

env_set KARDINAL_E2E_FULCIO_URL "$proxy:8081"
env_set KARDINAL_E2E_REKOR_URL "$proxy:8082"
env_set KARDINAL_E2E_TSA_URL "$proxy:8083/api/v1/timestamp"
env_set KARDINAL_E2E_SIGSTORE_FAULTS "$FAULTS"
env_set KARDINAL_E2E_TUF_URL "$TUF_URL"
env_set KARDINAL_E2E_TRUSTED_ROOT "$DIR/trusted_root.json"
log "sigstore ready: fulcio $FULCIO_URL, rekor $REKOR_URL, tsa $TSA_URL, through $proxy"
