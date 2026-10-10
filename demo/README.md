# kardinal-promoter Demo Environment

This directory creates a **working demo environment** for kardinal-promoter on one kind cluster, with one Pipeline and the org PolicyGates. The controller is built from your checkout, so the demo always runs the code you have.

Flux, Argo Rollouts and Flagger have their own examples, each covered by a live e2e test: [examples/flux-demo](../examples/flux-demo), [examples/argo-rollouts-demo](../examples/argo-rollouts-demo) and [examples/flagger-demo](../examples/flagger-demo).

CI does not run the demo. The live e2e suites validate kardinal: see [Validation](#validation).

## What You Get

```
kind-kardinal-demo   ← kardinal controller + kro + Argo CD
                       Argo CD syncs kardinal-test-app's test, uat and prod
                       environments into namespaces kardinal-test-app-{test,uat,prod}
```

For several clusters, see [Multi-Cluster](../docs/multi-cluster.md) and
[examples/multi-cluster-fleet](../examples/multi-cluster-fleet).

**Pipeline `kardinal-test-app`**
```
test (auto) → uat (auto) → prod (PR review)
                                ↑ gates: no-weekend-deploys, require-uat-soak,
                                         business-hours-only, no-bot-deploys
```

**Features the demo shows:**

| Feature | Where |
|---|---|
| Auto-promote | test, uat |
| PR-review gate | prod |
| PolicyGate: schedule | no-weekend-deploys, business-hours-only |
| PolicyGate: soak | require-uat-soak (30m in uat) |
| PolicyGate: provenance | no-bot-deploys |
| Pause / resume | `kardinal pause` / `kardinal resume` |
| Rollback | `kardinal rollback` |
| Explain gate state | `kardinal explain --color` |
| Policy simulate | `kardinal policy simulate --time "Saturday 3pm"` |
| CLI completeness | version, get, explain, logs, history, audit, completion, --dry-run |
| Web UI | `kardinal dashboard` → React DAG view |

---

## Prerequisites

```bash
# macOS
brew install kind kubectl helm

# kardinal CLI (build from source)
cd /path/to/kardinal-promoter
go build -o /usr/local/bin/kardinal ./cmd/kardinal/

# Docker Desktop — must be running
open -a Docker

# GitHub PAT with write access to your fork of pnz1990/kardinal-demo
# (kardinal pushes there). Point git.url in demo/manifests/pipeline-simple and
# repoURL in demo/manifests/argocd at your fork.
export GITHUB_TOKEN=ghp_your_token_here
```

---

## Quick Start

```bash
cd /path/to/kardinal-promoter

# 1. Set up everything (takes ~5 min)
GITHUB_TOKEN=ghp_xxx ./demo/scripts/setup.sh

# 2. Trigger a promotion
kardinal create bundle kardinal-test-app \
  --image ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0

# 3. Watch it promote
kardinal get pipelines --watch

# 4. Open the UI
kubectl port-forward -n kardinal-system \
  deployment/kardinal-promoter 8082:8082 &
kardinal dashboard     # opens http://localhost:8082/ui/

# 5. Tear down
./demo/scripts/teardown.sh
```

---

## Scenario Walkthroughs

The scenarios use a real kardinal-test-app image:

```bash
IMAGE=ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0
```

### Scenario A: Happy path

```bash
# Promote a new image
kardinal create bundle kardinal-test-app --image "$IMAGE"

# Watch: test and uat verify within a few minutes. The prod PR opens once
# uat has soaked 30 minutes (require-uat-soak), on a weekday in business hours.
kardinal get pipelines --watch

# See the promotion evidence
kardinal explain kardinal-test-app --env prod --color
```

### Scenario B: Weekend gate

```bash
# Simulate what happens Saturday, with uat soaked for 45 minutes
kardinal policy simulate \
  --pipeline kardinal-test-app \
  --env prod \
  --time "Saturday 3pm" \
  --soak-minutes 45
# → RESULT: BLOCKED
#   Blocked by: business-hours-only
#   Blocked by: no-weekend-deploys

# Simulate weekday
kardinal policy simulate \
  --pipeline kardinal-test-app \
  --env prod \
  --time "Tuesday 10am" \
  --soak-minutes 45
# → RESULT: PASS
#   Without --soak-minutes (default 0), require-uat-soak blocks.
```

### Scenario C: Pause mid-promotion

```bash
kardinal create bundle kardinal-test-app --image "$IMAGE"

# Immediately pause
kardinal pause kardinal-test-app
kardinal get pipelines
# → PAUSED badge visible, bundle frozen at test

# Resume when ready
kardinal resume kardinal-test-app
```

### Scenario D: Rollback

A rollback needs an earlier Verified Bundle in the environment: promote twice
(two prod PR merges) first.

```bash
# After a bad deploy to prod, rollback
kardinal rollback kardinal-test-app --env prod
# → Opens a PR with kardinal/rollback label and full evidence body
```

### Scenario E: Override a gate (break-glass)

```bash
# Override the weekend gate for the Bundle waiting on it in prod (requires reason).
# --gate is the name of the org gate in platform-policies; the command finds the
# Bundle's instance of it in the Pipeline's namespace.
kardinal override kardinal-test-app --stage prod \
  --gate no-weekend-deploys \
  --reason "P0 hotfix: payment service down"
```

### Scenario F: --dry-run before creating bundle

```bash
kardinal create bundle kardinal-test-app --image "$IMAGE" --dry-run
# → Shows what Graph would be created, no resources written
```

---

## Validation

The live e2e suites in `test/e2e/live` validate kardinal on kind clusters with
real git servers and GitOps engines, using podinfo as the test app:

```bash
make e2e-up SUITE=core        # hack/e2e/up.sh core
make test-e2e-live SUITE=core # hack/e2e/run.sh core
make e2e-down SUITE=core
```

[test/e2e/README.md](../test/e2e/README.md) lists the suites and their tests.
CI runs them in `.github/workflows/e2e-live.yml`.

---

## Keeping the Demo Current

**When you add a new feature:**
1. Add a manifest to `demo/manifests/` if the feature requires a new CRD
2. Add a walkthrough to this README under "Scenario Walkthroughs"

**When you change a CRD field or CLI flag:**
1. Update `demo/manifests/` to use the new field
2. Update the walkthrough section above

`go test ./test/examples/...` checks the manifests in `demo/manifests/` against
the CRD schemas and the `kardinal` commands in this README against the CLI.

---

## Directory Structure

```
demo/
├── README.md                    # this file
├── scripts/
│   ├── setup.sh                 # create the cluster + install everything
│   └── teardown.sh              # delete the cluster
└── manifests/
    ├── policy-gates/
    │   └── org-gates.yaml       # 4 PolicyGates covering all gate types
    ├── pipeline-simple/
    │   └── pipeline.yaml        # kardinal-test-app (test→uat→prod)
    └── argocd/
        └── applications.yaml    # Argo CD Applications for all envs
```
