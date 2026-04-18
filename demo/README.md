# kardinal-promoter Demo Environment

This directory contains everything needed to create a **complete, working demo environment** for kardinal-promoter — three clusters, two pipelines, all features exercised, and a validation script that serves as the source of truth that the product works.

## What You Get

```
kind-kardinal-control   ← kardinal controller + krocodile + ArgoCD
kind-kardinal-dev       ← test + uat environments (kardinal-test-app)
kind-kardinal-prod      ← prod environment  (kind locally, or EKS with --eks)
```

**Pipeline 1 — `kardinal-test-app` (simple)**
```
test (auto) → uat (auto) → prod (PR review)
                                ↑ gates: no-weekend-deploys, require-uat-soak, no-bot-deploys
```

**Pipeline 2 — `kardinal-test-app-advanced` (all features)**
```
test-fast (auto) → uat (auto) → prod-canary (PR) → prod-full (PR)
                                ↑ gates: business-hours-only, require-uat-soak, no-bot-deploys
```

**Features exercised end-to-end:**

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
| Multi-cluster | advanced pipeline across dev + prod clusters |

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
LATEST=$(curl -sf https://api.github.com/repos/pnz1990/kardinal-test-app/commits/main \
  -H "Authorization: Bearer $GITHUB_TOKEN" | python3 -c "import sys,json; print(json.load(sys.stdin)['sha'][:7])")
kardinal create bundle kardinal-test-app \
  --image ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST}

# 3. Watch it promote
kardinal get pipelines --watch

# 4. Open the UI
kubectl port-forward -n kardinal-system \
  deployment/kardinal-kardinal-promoter 8082:8082 &
kardinal dashboard     # opens http://localhost:8082/ui/

# 5. Validate everything works
./demo/scripts/validate.sh

# 6. Tear down
./demo/scripts/teardown.sh
```

---

## Scenario Walkthroughs

### Scenario A: Happy path

```bash
# Promote a new image
kardinal create bundle kardinal-test-app \
  --image ghcr.io/pnz1990/kardinal-test-app:sha-abc1234

# Watch: test verifies in ~60s, uat in ~90s, then prod PR opens
kardinal get pipelines --watch

# See the promotion evidence
kardinal explain kardinal-test-app --env prod --color
```

### Scenario B: Weekend gate

```bash
# Simulate what happens Saturday
kardinal policy simulate \
  --pipeline kardinal-test-app \
  --env prod \
  --time "Saturday 3pm"
# → RESULT: BLOCKED (no-weekend-deploys)

# Simulate weekday
kardinal policy simulate \
  --pipeline kardinal-test-app \
  --env prod \
  --time "Tuesday 10am"
# → RESULT: ALLOWED
```

### Scenario C: Pause mid-promotion

```bash
kardinal create bundle kardinal-test-app \
  --image ghcr.io/pnz1990/kardinal-test-app:sha-abc1234

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
# Override the weekend gate (requires reason)
kardinal override kardinal-test-app \
  --gate no-weekend-deploys \
  --reason "P0 hotfix: payment service down"
```

### Scenario F: --dry-run before creating bundle

```bash
kardinal create bundle kardinal-test-app \
  --image ghcr.io/pnz1990/kardinal-test-app:sha-abc1234 \
  --dry-run
# → Shows what Graph would be created, no resources written
```

---

## Validation (Source of Truth)

The `validate.sh` script is the canonical definition of "kardinal works":

```bash
./demo/scripts/validate.sh              # all 10 scenarios
./demo/scripts/validate.sh --scenario 5 # just policy gate scenario
./demo/scripts/validate.sh --fast       # skip soak waits (for CI)
```

**Scenarios validated:**

| # | Scenario | Feature |
|---|---|---|
| 1 | Controller health | `kardinal doctor` |
| 2 | Pipeline list | `kardinal get pipelines` |
| 3 | UI reachable | HTTP 200 from `/api/v1/ui/pipelines` |
| 4 | Happy path promotion | test → uat auto, prod PR |
| 5 | Weekend gate | `policy simulate` → BLOCKED / ALLOWED |
| 6 | Soak gate | gate visible in `kardinal explain` |
| 7 | Pause / resume | `kardinal pause` + `resume` |
| 8 | Rollback | `kardinal rollback` opens PR |
| 9 | CLI completeness | version, explain, history, completion, --dry-run |
| 10 | Multi-cluster pipeline | advanced pipeline registered |

This script also runs **nightly in CI** (`.github/workflows/demo-validate.yml`). If it's red, the product is broken.

---

## EKS Prod Cluster (Optional)

For a real production cluster:

```bash
# Create EKS cluster (us-east-2, ~15 min)
cd demo/terraform
terraform init
terraform apply

# Set up with EKS prod
GITHUB_TOKEN=ghp_xxx ./demo/scripts/setup.sh --eks

# Tear down EKS when done (costs money while running)
./demo/scripts/teardown.sh --eks
```

The Terraform creates a minimal EKS cluster: 2× t3.medium nodes in us-east-2. See `demo/terraform/` for configuration.

---

## Keeping the Demo Current

**When you add a new feature:**
1. Add a scenario to `demo/scripts/validate.sh`
2. Add a manifest to `demo/manifests/` if the feature requires a new CRD
3. Add a walkthrough to this README under "Scenario Walkthroughs"
4. The nightly CI will catch any regressions

**When you change a CRD field or CLI flag:**
1. Update `demo/manifests/` to use the new field
2. Update `demo/scripts/validate.sh` to check the new behaviour
3. Update the walkthrough section above

**The rule:** if a feature is not in `validate.sh`, it is not validated. If it is not validated, it will silently break.

---

## Directory Structure

```
demo/
├── README.md                    # this file
├── scripts/
│   ├── setup.sh                 # create all clusters + install everything
│   ├── teardown.sh              # destroy all clusters
│   └── validate.sh              # end-to-end validation (source of truth)
├── manifests/
│   ├── policy-gates/
│   │   └── org-gates.yaml       # 4 PolicyGates covering all gate types
│   ├── pipeline-simple/
│   │   └── pipeline.yaml        # kardinal-test-app (test→uat→prod)
│   ├── pipeline-advanced/
│   │   └── pipeline.yaml        # multi-env with all gate types
│   └── argocd/
│       └── applications.yaml    # ArgoCD Applications for all envs
└── terraform/
    ├── main.tf                  # EKS cluster definition
    ├── variables.tf
    ├── outputs.tf
    └── backend.tf
```
