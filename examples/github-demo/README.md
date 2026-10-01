# GitHub Demo — kardinal-promoter full GitHub feature exercise

This example demonstrates every GitHub-specific feature of kardinal-promoter: structured PR evidence, PR review gating, PolicyGates (schedule + soak + bundle metadata), emergency override, and rollback PRs.

## Features Covered

| Feature | How it's exercised |
|---|---|
| GitHub SCM provider | the controller's default `--scm-provider` (github) |
| Structured PR evidence body | Prod PR body lists the image provenance, each gate's result and reason, and when test and uat were Verified |
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

# 4. uat is Verified when its 30-minute bake ends. 30 minutes later
#    uat-soak-gate passes and kardinal opens the prod PR:
#   title:  [kardinal] Promote github-demo-9tptr to prod
#   labels: kardinal, kardinal/promotion
# The body lists the image, every prod gate's result and reason, and when
# test and uat were Verified (see "PR Evidence Body" below).

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

# The override is recorded in spec.overrides of the Bundle's instance of the gate.
# It expires after 1h (--expires-in). Until then the gate passes, and its
# reason names the override.
```

The override is applied before the prod PR opens, because the PR waits for the
gate. In the PR body, the gate's row in "Policy Gate Compliance" shows the
override. `your-alias` is the local user who ran `kardinal override`:

```markdown
| no-weekend-deploys | default | Pass | OVERRIDDEN by your-alias: Critical security fix CVE-2026-1234 — approved by on-call lead (expires 2026-04-18T15:23Z) | 2026-04-18T14:23Z |
```

## Rollback

```bash
# Option 1: Automatic rollback (triggered when health check fails after merge)
# Set onHealthFailure: rollback on the environment (already in pipeline.yaml).
# kardinal creates the rollback Bundle <bundle>-rollback-alarm. It restores the
# artifacts of the most recent other Bundle that was Verified in prod.

# Option 2: Manual rollback
kardinal rollback github-demo --env prod
# Creates the rollback Bundle github-demo-rollback-<suffix>, with the same target.
```

A rollback Bundle is a forward promotion. It goes through test and uat first,
including the uat bake and `uat-soak-gate`, before it opens the prod PR (see
[Multi-Environment Rollback](../../docs/rollback.md#multi-environment-rollback)).
The prod PR has:

- title `[kardinal] Rollback prod to github-demo-rollback-x7k2p (restores sha-1a2b3c4)`
- labels `kardinal`, `kardinal/promotion` and `kardinal/rollback`
- a body that starts with a rollback note, followed by the sections of a
  promotion PR:

```markdown
<!-- kardinal-promoter auto-generated PR -->
## ROLLBACK: github-demo-rollback-x7k2p -> github-demo/prod

> **This is a rollback PR.** It reverts environment prod to the state of bundle github-demo-4fq8m.
> Rolling back FROM: github-demo-9tptr (sha-abc1234)
> Rolling back TO: github-demo-4fq8m (sha-1a2b3c4)
> Rolled back by: your-alias
```

`Rolled back by` is the local user who ran `kardinal rollback`. For an
automatic rollback it is `kardinal-controller (onHealthFailure=rollback)`.

After the rollback, ship the fix as a new Bundle with a new image:

```bash
kardinal create bundle github-demo --image ghcr.io/pnz1990/kardinal-test-app:sha-<fixed>
```

## PR Evidence Body

The prod PR body from the walkthrough. The Bundle came from `kardinal create
bundle` without `--commit`, `--author` or `--ci-run-url`, and the image has a
tag but no digest, so those cells are `—`. A Bundle created by CI fills them in.
`Elapsed` is the time between that environment's Verified and the PR opening.
See [PR Evidence](../../docs/pr-evidence.md).

```markdown
<!-- kardinal-promoter auto-generated PR -->
## Promotion: github-demo-9tptr -> github-demo/prod

### Artifact Provenance

| Image | Tag | Digest | CI Run | Commit SHA | Author |
|---|---|---|---|---|---|
| ghcr.io/pnz1990/kardinal-test-app | sha-abc1234 | — | — | — | — |

### Policy Gate Compliance

| Gate | Namespace | Result | Reason | Last Evaluated |
|---|---|---|---|---|
| no-bot-deploys | default | Pass | bundle.version=sha-abc1234: bundle.provenance.author != "dependabot[bot]" = true | 2026-04-15T14:02Z |
| no-weekend-deploys | default | Pass | bundle.version=sha-abc1234: !schedule.isWeekend = true | 2026-04-15T14:02Z |
| uat-soak-gate | default | Pass | bundle.version=sha-abc1234: upstream.uat.soakMinutes >= 30 = true | 2026-04-15T14:02Z |

### Upstream Verification

| Environment | Health Checked At | Elapsed |
|---|---|---|
| test | 2026-04-15T12:55Z | 1h7m |
| uat | 2026-04-15T13:31Z | 31m |

---
*Generated by [kardinal-promoter](https://github.com/pnz1990/kardinal-promoter)*
```

## Validation

```bash
# Unit tests for GitHub SCM features are in pkg/scm/
go test ./pkg/scm/... -v

# Full demo validation including all adapters
bash scripts/demo-validate.sh
```
