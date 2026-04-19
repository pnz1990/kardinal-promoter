# GitHub Demo — kardinal-promoter full GitHub feature exercise

This example demonstrates every GitHub-specific feature of kardinal-promoter: structured PR evidence, PR review gating, PolicyGates (schedule + soak + bundle metadata), emergency override, and rollback PRs.

## Features Covered

| Feature | How it's exercised |
|---|---|
| GitHub SCM provider | `spec.git.provider: github` |
| Structured PR evidence body | Prod PR body contains image digest, CI run URL, gate results, soak time |
| PR review gate | `approval: pr-review` on prod — requires GitHub review before merge |
| PolicyGate: schedule | `!schedule.isWeekend` — blocks Saturday/Sunday UTC |
| PolicyGate: upstream soak | `upstream.uat.soakMinutes >= 30` — 30-min contiguous healthy soak |
| PolicyGate: bundle metadata | `bundle.provenance.author != "dependabot[bot]"` |
| `kardinal explain` | Shows all three gates, CEL expressions, and current values |
| `kardinal policy simulate` | Simulate gate results for any time/context |
| Emergency override | `kardinal override kardinal-test-app --env prod --reason "..."` |
| Auto-rollback | `onHealthFailure: rollback` opens rollback PR if prod health fails |
| Rollback PR | `kardinal rollback kardinal-test-app --env prod` opens PR with `kardinal/rollback` label |

## Prerequisites

- GitHub token with `repo` scope (read + write + PR creation)
- ArgoCD installed (for the health checks)
- `kubectl` connected to your cluster

## Setup

```bash
# 1. Create namespaces and secrets
kubectl create namespace kardinal-test-app-test
kubectl create namespace kardinal-test-app-uat  
kubectl create namespace prod
kubectl create secret generic github-token \
  --from-literal=token=$GITHUB_TOKEN

# 2. Apply ArgoCD Applications
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
kardinal create bundle kardinal-test-app \
  --image $TEST_IMAGE \
  --ci-run-url "https://github.com/pnz1990/kardinal-test-app/actions/runs/123456"

# 2. Watch test auto-promote
kardinal get pipelines
# NAME                 TEST      UAT       PROD
# kardinal-test-app    Verified  Baking    Gated

# 3. Check what's gating prod
kardinal explain kardinal-test-app --env prod
# PIPELINE: kardinal-test-app
# ENV: prod
# GATES:
#   no-weekend-deploys  !schedule.isWeekend          ALLOWED  (today is Wednesday)
#   uat-soak-gate       upstream.uat.soakMinutes>=30  WAITING  (UAT at 12 min)
#   no-bot-deploys      bundle.provenance.author!=... ALLOWED  (author: your-alias)

# 4. After UAT bake completes (30+ min), prod PR opens automatically
# The PR body includes:
#   Image: ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}
#   Digest: sha256:...
#   CI Run: https://github.com/pnz1990/kardinal-test-app/actions/runs/123456
#   UAT soak: 31 minutes
#   Gates: no-weekend-deploys=ALLOWED, uat-soak-gate=ALLOWED, no-bot-deploys=ALLOWED

# 5. Review and merge the PR
gh pr list --repo pnz1990/kardinal-demo
gh pr merge <PR_NUMBER> --repo pnz1990/kardinal-demo --squash
```

## Policy Simulation

```bash
# Test the weekend gate
kardinal policy simulate --pipeline kardinal-test-app --env prod --time "Saturday 2pm UTC"
# RESULT: BLOCKED
# GATE: no-weekend-deploys — schedule.isWeekend=true

kardinal policy simulate --pipeline kardinal-test-app --env prod --time "Tuesday 10am UTC"
# RESULT: ALLOWED
# All 3 gates ALLOWED

# Test with dependabot author
kardinal policy simulate --pipeline kardinal-test-app --env prod \
  --author "dependabot[bot]"
# RESULT: BLOCKED
# GATE: no-bot-deploys — bundle.provenance.author="dependabot[bot]"
```

## Emergency Override

When a hotfix must be deployed despite a failing gate:

```bash
# Override blocks the failing gate and creates an audit record
kardinal override kardinal-test-app --env prod \
  --reason "Critical security fix CVE-2026-1234 — approved by on-call lead"

# The override is recorded in PromotionStep.status.overrides
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
kardinal rollback kardinal-test-app --env prod
# Opens a PR with:
#   - label: kardinal/rollback
#   - title: "revert(prod): roll back kardinal-test-app to sha-<previous>"
#   - body: original evidence + rollback reason

# After merging the rollback PR, promote back to good state:
kardinal create bundle kardinal-test-app --image $TEST_IMAGE
```

## PR Evidence Body Structure

Every production PR opened by kardinal includes:

```markdown
## kardinal Promotion Evidence

**Bundle**: kardinal-test-app@sha-abc1234
**Image**: ghcr.io/pnz1990/kardinal-test-app:sha-abc1234
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
