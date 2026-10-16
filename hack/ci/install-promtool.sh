#!/usr/bin/env bash
# hack/ci/install-promtool.sh
#
# Installs the promtool of hack/tool-versions.env (PROMTOOL_VERSION) into
# bin/e2e/promtool, from the Prometheus release tarball, checked against
# PROMTOOL_SHA256 before it is unpacked; a promtool already there at that
# version is kept. test/helm runs bin/e2e/promtool first, then promtool on
# PATH (TestChartPrometheusRuleAlertsOnlyOnKardinal). ci.yml runs this in
# the jobs that run test/helm, release.yml in its test job and
# hack/release-gate before its unit-race step; run it yourself for those
# tests locally.
# linux-amd64 only, as the sum is.
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=hack/tool-versions.env
source "$REPO_ROOT/hack/tool-versions.env"
BIN="$REPO_ROOT/bin/e2e"
v="${PROMTOOL_VERSION#v}"

if [ -x "$BIN/promtool" ] && "$BIN/promtool" --version 2>/dev/null | grep -q "^promtool, version ${v} "; then
  exit 0
fi
mkdir -p "$BIN"
tmp=$(mktemp -d "$BIN/.download.XXXXXX")
trap 'rm -r "$tmp"' EXIT
curl -fsSLo "$tmp/prometheus.tar.gz" \
  "https://github.com/prometheus/prometheus/releases/download/${PROMTOOL_VERSION}/prometheus-${v}.linux-amd64.tar.gz"
echo "${PROMTOOL_SHA256}  $tmp/prometheus.tar.gz" | sha256sum -c --quiet -
tar -xzf "$tmp/prometheus.tar.gz" -C "$tmp" --strip-components=1 "prometheus-${v}.linux-amd64/promtool"
mv -f "$tmp/promtool" "$BIN/promtool"
echo "installed $BIN/promtool $PROMTOOL_VERSION" >&2
