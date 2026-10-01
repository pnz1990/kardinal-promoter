#!/usr/bin/env bash
# hack/e2e/components/podinfo.sh
#
# Pulls PODINFO_IMAGE (versions.env) onto the node: the podinfo release the
# fixtures deploy first and the framework's probe Pods run. Their Pods use
# imagePullPolicy IfNotPresent, so they start from the node's copy instead
# of pulling from ghcr.io inside a test's timeout. Idempotent.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/../lib.sh"
target_cluster

pull_images "$PODINFO_IMAGE"
