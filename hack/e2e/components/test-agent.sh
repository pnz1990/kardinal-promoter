#!/usr/bin/env bash
# hack/e2e/components/test-agent.sh
#
# Builds the reference verifier (hack/e2e/testagent) from this checkout on
# the host (standard library only) into a FROM scratch image and loads it into
# the cluster, for the verify gate tests (TestGate_Verify*), which register it
# as a StepTemplate. Nothing is pulled and nothing is deployed. Idempotent.
#
# Env file:
#   KARDINAL_E2E_TEST_AGENT_IMAGE  the image name, loaded on the nodes
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

IMAGE="kardinal-e2e/test-agent:$KIND_CLUSTER"

ctx=$(mktemp -d)
(cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -ldflags="-s -w" \
  -o "$ctx/test-agent" ./hack/e2e/testagent)
chmod 0755 "$ctx" "$ctx/test-agent"
printf 'FROM scratch\nCOPY test-agent /test-agent\nUSER 65532:65532\nENTRYPOINT ["/test-agent"]\n' >"$ctx/Dockerfile"
docker build -q -t "$IMAGE" "$ctx" >/dev/null
load_image "$IMAGE"

env_set KARDINAL_E2E_TEST_AGENT_IMAGE "$IMAGE"
log "test agent image $IMAGE loaded"
