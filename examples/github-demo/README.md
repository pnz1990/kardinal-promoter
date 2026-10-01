# GitHub Demo — kardinal-promoter full GitHub feature exercise

This example demonstrates every GitHub-specific feature of kardinal-promoter: structured PR evidence, PR review gating, PolicyGates (schedule + soak + bundle metadata), emergency override, and rollback PRs.

## Features Covered

| Feature | How it's exercised |
|---|---|
| GitHub SCM provider | the controller's default `--scm-provider` (github) |
| Structured PR evidence body | Prod PR body contains image digest, CI run URL, gate results, soak time |
| PR review gate | `approval: pr-review` on prod — requires GitHub review before merge |
| PolicyGate: schedule | `!schedule.isWeekend` — blocks Saturday/Sunday UTC |
| PolicyGate: upstream soak | `upstream.uat.soakMinutes >= 30` — 30-min contiguous healthy soak |
| PolicyGate: bundle metadata | `bundle.provenance.author != "dependabot[bot]"` |
| `kardinal explain` | Shows all three gates, CEL expressions, and current values |
| `kardinal policy simulate` | Simulate gate results for any time/context |
| Emergency override | `kardinal override github-demo --stage prod --gate no-weekend-deploys --reason "..."` |
| Auto-rollback | `onHealthFailure: rollback` opens rollback PR if prod health fails |
| Rollback PR | `kardinal rollback github-demo --env prod` opens PR with `kardinal/rollback` label |

## Prerequisites

- GitHub token with `repo` scope (read + write + PR creation)
- ArgoCD installed (for the health checks)
- `kubectl` connected to your cluster

## Setup

```bash
# 1. Create namespaces and secrets
kubectl create namespace kardinal-test-app-test
kubectl create namespace kardinal-test-app-uat
kubectl create namespace kardinal-test-app-prod
kubectl create secret generic github-token \
  --from-literal=token=$GITHUB_TOKEN

# 2. Apply the quickstart's Argo CD Applications (kardinal-test-app-{test,uat,prod}).
#    They sync the kardinal-demo paths this Pipeline promotes into, and
#    health.argocd.name in pipeline.yaml points at them.
kubectl apply -f examples/quickstart/argocd-applications.yaml

# 3. Apply Pipeline and PolicyGates
kubectl apply -f examples/github-demo/pipeline.yaml

# Verify PolicyGates are registered
kubectl get policygates -n default
# NAME                EXPRESSION                              READY
# no-weekend-deploys  !schedule.isWeekend                    true
# uat-soak-gate       upstream.uat.soakMinutes >= 30         true
# no-bot-deploys      bundle.provenance.author != "depen..." true
```

## Walkthrough: Full Promotion with Evidence

```bash
LATEST_SHA=$(gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]')
TEST_IMAGE="ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}"

# 1. Create bundle (simulates CI trigger)
# The CLI records no CI provenance; a Bundle created by CI (the create-bundle
# action or POST /api/v1/bundles) carries the commit SHA and CI run URL.
kardinal create bundle github-demo --image "$TEST_IMAGE"

# 2. Watch test auto-promote
kardinal get pipelines
# NAME          TEST      UAT       PROD
# github-demo   Verified  Baking    Gated

# 3. Check what's gating prod
kardinal explain github-demo --env prod
# ENVIRONMENT   BUNDLE              TYPE         NAME                 STATE   EXPRESSION                                      REASON
# prod          github-demo-9tptr   PolicyGate   no-bot-deploys       Pass    bundle.provenance.author != "dependabot[bot]"   bundle.version=sha-abc1234: bundle.provenance.author != "dependabot[bot]" = true
# prod          github-demo-9tptr   PolicyGate   no-weekend-deploys   Pass    !schedule.isWeekend                             bundle.version=sha-abc1234: !schedule.isWeekend = true
# prod          github-demo-9tptr   PolicyGate   uat-soak-gate        Block   upstream.uat.soakMinutes >= 30                  UAT must have been healthy for at least 30 contiguous minutes (bundle.version=sha-abc1234: upstream.uat.soakMinutes >= 30 = false)
#
# prod   deployed: none

# 4. After UAT bake completes (30+ min), prod PR opens automatically
# The PR body includes:
#   Image: ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}
#   Digest: sha256:...
#   CI Run: <the ciRunURL on the Bundle, when CI created it>
#   UAT soak: 31 minutes
#   Gates: no-weekend-deploys=ALLOWED, uat-soak-gate=ALLOWED, no-bot-deploys=ALLOWED

# 5. Review and merge the PR
gh pr list --repo pnz1990/kardinal-demo
gh pr merge <PR_NUMBER> --repo pnz1990/kardinal-demo --squash
```

## Policy Simulation

```bash
# Test the weekend gate
kardinal policy simulate --pipeline github-demo --env prod --time "Saturday 2pm" --soak-minutes 45
# RESULT: BLOCKED
# Blocked by: no-weekend-deploys
# Message: "Block deployments on Saturday and Sunday UTC"
# Next window: Monday 00:00 UTC
#
# no-bot-deploys:       PASS    (bundle.provenance.author != "dependabot[bot]" = true)
# no-weekend-deploys:   BLOCK   (!schedule.isWeekend = false)
# uat-soak-gate:        PASS    (upstream.uat.soakMinutes >= 30 = true)

kardinal policy simulate --pipeline github-demo --env prod --time "Tuesday 10am" --soak-minutes 45
# RESULT: PASS
```

`--time` is UTC. `--soak-minutes` sets the soak time of every upstream
environment (default 0, which blocks `uat-soak-gate`). The simulated Bundle has
no provenance author, so `no-bot-deploys` always passes in a simulation.

## Emergency Override

When a hotfix must be deployed despite a failing gate:

```bash
# Force-pass the failing gate for the Bundle waiting on it; creates an audit record.
# --gate is the name of the PolicyGate you applied (as `kardinal explain` shows it).
kardinal override github-demo --stage prod --gate no-weekend-deploys \
  --reason "Critical security fix CVE-2026-1234 — approved by on-call lead"

# The override is recorded in spec.overrides of the Bundle's instance of the gate
# The prod PR body will show:
#   ⚠️ OVERRIDE APPLIED
#   Reason: Critical security fix CVE-2026-1234 — approved by on-call lead
#   Overridden by: your-alias
#   At: 2026-04-18T14:23:45Z
```

## Rollback

```bash
# Option 1: Automatic rollback (triggered when health check fails after merge)
# Set onHealthFailure: rollback on the environment (already in pipeline.yaml)
# kardinal opens a PR reverting the image to the previous version

# Option 2: Manual rollback
kardinal rollback github-demo --env prod
# Opens a PR with:
#   - label: kardinal/rollback
#   - title: "revert(prod): roll back github-demo to sha-<previous>"
#   - body: original evidence + rollback reason

# After merging the rollback PR, promote back to good state:
kardinal create bundle github-demo --image $TEST_IMAGE
```

## PR Evidence Body Structure

Every production PR opened by kardinal includes:

```markdown
## kardinal Promotion Evidence

**Bundle**: github-demo@sha-9349a3f
**Image**: ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f
**Image digest**: sha256:deadbeef...
**CI Run**: https://github.com/pnz1990/kardinal-test-app/actions/runs/123456
**Author**: your-alias

## Gate Results

| Gate | Expression | Result |
|---|---|---|
| no-weekend-deploys | !schedule.isWeekend | ✅ ALLOWED (Wednesday) |
| uat-soak-gate | upstream.uat.soakMinutes >= 30 | ✅ ALLOWED (42 min) |
| no-bot-deploys | bundle.provenance.author != "dependabot[bot]" | ✅ ALLOWED |

## Upstream Environments

| Env | Status | Soak (min) |
|---|---|---|
| test | Verified | 45 |
| uat | Verified | 42 |
```

## Validation

```bash
# Unit tests for GitHub SCM features are in pkg/scm/
go test ./pkg/scm/... -v

# Full demo validation including all adapters
bash scripts/demo-validate.sh
```
