#!/usr/bin/env bash
# hack/e2e/run.sh SUITE
#
# Runs SUITE's live tests against the cluster hack/e2e/up.sh set up, with
# the env file it wrote. The output goes through test/e2e/report, which
# fails the run when a test fails or skips, or when no test ran. The raw
# `go test -json` stream is kept in test/e2e/results/<cluster>/test.json.
#
# Env:
#   COUNT         go test -count (default 1; the weekly flake job uses more)
#   RUN           go test -run pattern (default: the suite's, from up.sh)
#   KIND_CLUSTER  cluster name (default kardinal-e2e-SUITE)
#
# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
set -euo pipefail

SUITE=${1:?usage: $0 SUITE}
export KIND_CLUSTER=${KIND_CLUSTER:-kardinal-e2e-$SUITE}
# shellcheck source=hack/e2e/lib.sh
source "$(dirname "$0")/lib.sh"
[ -f "$E2E_OUT/env" ] || die "no $E2E_OUT/env; run make e2e-up SUITE=$SUITE first"
set -a
# shellcheck disable=SC1091
source "$E2E_OUT/env"
set +a
target_cluster
[ "$KARDINAL_E2E_CONTEXT" = "$CTX" ] || die "env file is for $KARDINAL_E2E_CONTEXT, not $CTX"

cd "$REPO_ROOT"
go build -o "$E2E_OUT/bin/report" ./test/e2e/report
go test -tags e2e ./test/e2e/live -run "${RUN:-$KARDINAL_E2E_RUN}" -count="${COUNT:-1}" -timeout 90m -json 2>&1 |
  tee "$E2E_OUT/test.json" | "$E2E_OUT/bin/report" -suite "$SUITE"
