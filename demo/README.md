# kardinal-promoter Demo Environment

This directory contains everything needed to create a **complete, working demo environment** for kardinal-promoter — three clusters, one main pipeline plus optional Flux, Argo Rollouts and Flagger pipelines. The controller is built from your checkout, so the demo always runs the code you have.

CI does not run the demo. The live e2e suites validate kardinal: see [Validation](#validation).

## What You Get

```
kind-kardinal-control   ← kardinal controller + kro + ArgoCD
kind-kardinal-dev       ← test + uat environments (kardinal-test-app)
kind-kardinal-prod      ← prod environment
```

> **Current limitation:** promotions do not yet reach the dev and prod clusters.
> Argo CD runs on the control cluster and syncs all three environments of
> `kardinal-test-app` into namespaces `kardinal-test-app-{test,uat,prod}` there.
> The copies setup.sh deploys directly to the dev and prod clusters are static,
> and the Flux, Argo Rollouts and Flagger fixtures on the dev cluster are not
> visible to the controller.

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

# GitHub PAT with repo write access (needed for GitOps push)
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

# Watch: test verifies in ~60s, uat in ~90s, then prod PR opens
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

## EKS Prod Cluster (Removed)

The `--eks` option and the Terraform behind it were removed; the demo runs on kind only. If you created the `kardinal-e2e-prod` EKS cluster with `setup.sh --eks` or `make eks-up`, destroy it from a checkout that still has `terraform/eks-e2e/` (`cd terraform/eks-e2e && terraform destroy`). A cluster from the older `demo/terraform/` directory is destroyed the same way from a checkout that has that directory.

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
│   ├── setup.sh                 # create all clusters + install everything
│   └── teardown.sh              # destroy all clusters
└── manifests/
    ├── policy-gates/
    │   └── org-gates.yaml       # 4 PolicyGates covering all gate types
    ├── pipeline-simple/
    │   └── pipeline.yaml        # kardinal-test-app (test→uat→prod)
    ├── argocd/
    │   └── applications.yaml    # ArgoCD Applications for all envs
    ├── flux/                    # Flux pipeline + Kustomizations
    ├── rollouts/                # Argo Rollouts pipeline + Rollout
    └── flagger/                 # Flagger pipeline + Canary
```
