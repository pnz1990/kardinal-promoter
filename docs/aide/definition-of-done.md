# Definition of Done

> This is the north star. The project is complete when every journey below passes end-to-end.
> Every agent reads this document before starting work.
> Every feature is implemented to make these journeys pass — not to satisfy internal specs.
> If a journey fails, the project is not done, regardless of what the code says.

---

## Journey 1: Quickstart — First Promotion in 15 Minutes

**Source**: `docs/quickstart.md`, `examples/quickstart/`

**The user story**: A platform engineer installs kardinal-promoter on a kind cluster,
applies a 15-line Pipeline CRD, creates a Bundle, and watches it promote through
test → uat → prod automatically, with a PR opened for prod that they review and merge.

### Exact steps that must work

```bash
# 1. Install kro (Graph controller), then kardinal-promoter
bash hack/install-kro.sh
helm install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0-rc.1 \
  --namespace kardinal-system --create-namespace

# 2. Verify
kardinal version
# must print CLI: vX.Y.Z and Controller: vX.Y.Z (the installed release)

# 3. Create git credentials
kubectl create secret generic github-token \
  --from-literal=token=$GITHUB_PAT

# 4. Apply the Pipeline
kubectl apply -f examples/quickstart/pipeline.yaml

# 5. Apply org PolicyGates
kubectl apply -f examples/quickstart/policy-gates.yaml

# 6. Create a Bundle
kardinal create bundle kardinal-test-app \
  --image ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f

# 7. Watch promotion start
kardinal get pipelines
# PIPELINE              BUNDLE        TEST       UAT     PROD     SUB   AGE
# kardinal-test-app     sha-9349a3f   Verified   ...     ...      0     2m

# 8. Check policy gate explanation
kardinal explain kardinal-test-app --env prod
# Must show: PolicyGates evaluated, reason why prod is waiting or ready

# 9. Prod PR is opened automatically
# Must open a PR titled: "[kardinal] Promote kardinal-test-app sha-9349a3f to prod"
# PR body must contain: artifact info, upstream verification, policy compliance table

# 10. After PR merge
kardinal get pipelines
# PIPELINE              BUNDLE        TEST       UAT        PROD       SUB   AGE
# kardinal-test-app     sha-9349a3f   Verified   Verified   Verified   0     8m
```

### Pass criteria

- [ ] `kardinal version` returns valid version strings
- [ ] `kubectl apply -f examples/quickstart/pipeline.yaml` succeeds with no errors
- [ ] Bundle creation triggers automatic promotion to test and uat
- [ ] PolicyGates block prod on weekends (verify with `kardinal explain`)
- [ ] `kardinal explain kardinal-test-app --env prod` shows gate evaluation with values
- [ ] A PR is opened for prod with structured evidence body (not a raw diff)
- [ ] After PR merge, `kardinal get pipelines` shows PROD=Verified
- [ ] `kardinal get steps kardinal-test-app` shows all steps with correct states

---

## Journey 2: Multi-Cluster Fleet — Parallel Prod Through an Argo CD Hub

**Source**: `examples/multi-cluster-fleet/`, AWS workshops:
- https://catalog.workshops.aws/platform-engineering-on-eks/en-US/30-progressiveapplicationdelivery/40-production-deploy-kargo
- https://github.com/aws-samples/fleet-management-on-amazon-eks-workshop/tree/mainline/patterns/kro-eks-cluster-mgmt#promote-the-application-to-prod-clusters

**The user story**: A platform engineer promotes `rollouts-demo` through test → pre-prod → [prod-eu, prod-us] in parallel.
Argo CD in the hub manages Applications for 4 workload clusters; the prod clusters run an Argo
Rollouts canary. kardinal checks each environment's Argo CD Application in the hub
(`health.type: argocd`), not the Rollout. Two PRs are opened in parallel for prod-eu and prod-us.

### Exact steps that must work

```bash
# 1. Apply the multi-cluster pipeline
kubectl apply -f examples/multi-cluster-fleet/pipeline.yaml

# 2. Apply org PolicyGates
kubectl apply -f examples/multi-cluster-fleet/policy-gates.yaml

# 3. Create an Argo CD ApplicationSet for all 4 clusters
kubectl apply -f examples/multi-cluster-fleet/argocd-applications.yaml

# 4. Create a Bundle
kardinal create bundle rollouts-demo \
  --image ghcr.io/myorg/rollouts-demo:v2.0.0

# 5. Watch the DAG-structured pipeline
kardinal get pipelines
# PIPELINE        BUNDLE   TEST       PRE-PROD   PROD-EU   PROD-US   AGE
# rollouts-demo   v2.0.0   Verified   Verified   PR open   PR open   15m

# 6. Both prod PRs are opened simultaneously (parallel fan-out)
# Two PRs must exist concurrently, both labeled kardinal

# 7. After merging both PRs, Argo Rollouts canary runs in each prod cluster
kardinal get steps rollouts-demo
# prod-eu: HealthChecking (waits for the hub's rollouts-demo-prod-eu Application)
# prod-us: HealthChecking (waits for the hub's rollouts-demo-prod-us Application)

# 8. When rollouts complete
kardinal get pipelines
# PROD-EU: Verified, PROD-US: Verified
```

### Pass criteria

- [ ] `dependsOn` fan-out works: prod-eu and prod-us start simultaneously after pre-prod
- [ ] Two PRs opened in parallel, both with evidence and policy compliance
- [ ] `kardinal explain rollouts-demo --env prod-eu` shows gate states correctly
- [ ] Health adapter reads Argo CD Application status from hub cluster
- [ ] Each prod step stays HealthChecking until its hub Application is Healthy and Synced, which
      Argo CD reports only after the canary finishes
- [ ] prod-eu and prod-us reach Verified independently

---

## Journey 3: Policy Governance

**Source**: `docs/policy-gates.md`

**The user story**: A platform engineer adds a `no-weekend-deploys` PolicyGate and an
upstream soak-time gate, verifies they block prod promotion, and uses
`kardinal policy simulate` to preview the result. A second engineer adds a team-level
gate without touching org gates.

### Exact steps that must work

```bash
# 1. Apply org gates (time-based and soak-time)
kubectl apply -f - <<EOF
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: no-weekend-deploys
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/applies-to: prod
spec:
  expression: "!schedule.isWeekend"
  message: "Production deployments are blocked on weekends"
  recheckInterval: 5m
---
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: staging-soak-30m
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/applies-to: prod
spec:
  expression: "bundle.upstreamSoakMinutes >= 30"
  message: "Must soak in staging for 30 minutes before promoting to prod"
  recheckInterval: 2m
EOF

# 2. Verify they're listed
kardinal policy list
# Must show:
#   no-weekend-deploys  [org] applies-to: prod  recheckInterval: 5m
#   staging-soak-30m    [org] applies-to: prod  recheckInterval: 2m

# 3. Simulate a weekend promotion
kardinal policy simulate --pipeline kardinal-test-app --env prod --time "Saturday 3pm"
# RESULT: BLOCKED
# Blocked by: no-weekend-deploys
# Message: "Production deployments are blocked on weekends"
# Next window: Monday 00:00 UTC

# 4. Simulate with soak-time insufficient
kardinal policy simulate --pipeline kardinal-test-app --env prod \
  --time "Tuesday 10am" --soak-minutes 10
# RESULT: BLOCKED
# Blocked by: staging-soak-30m
# bundle.upstreamSoakMinutes = 10 (threshold: >= 30)
# ETA: ~20 minutes

# 5. Simulate both gates passing
kardinal policy simulate --pipeline kardinal-test-app --env prod \
  --time "Tuesday 10am" --soak-minutes 45
# RESULT: PASS
# no-weekend-deploys: PASS (Tuesday 10:00 UTC, isWeekend=false)
# staging-soak-30m:   PASS (soakMinutes=45 >= 30)

# 6. Apply a team-level gate in a different namespace
kubectl apply -f - <<EOF
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: no-bot-deploys
  namespace: my-team
  labels:
    kardinal.io/scope: team
    kardinal.io/applies-to: prod
spec:
  expression: 'bundle.provenance.author != "dependabot[bot]"'
  message: "Automated dependency updates must be manually promoted to prod"
  recheckInterval: 5m
EOF

# 7. Verify both org and team gates appear
kardinal policy list
# Must show no-weekend-deploys [org], staging-soak-30m [org], no-bot-deploys [team]

# 8. Verify gates in Graph
kardinal explain kardinal-test-app --env prod
# Must show all three gates as nodes with current evaluation state
```

### Pass criteria

- [ ] Both org-level PolicyGates (time-based and soak-time) apply correctly
- [ ] `kardinal policy list` shows gates with scope, applies-to, and recheckInterval
- [ ] `kardinal policy simulate --time "Saturday 3pm"` returns BLOCKED with reason
- [ ] `kardinal policy simulate --soak-minutes 10` returns BLOCKED on soak gate
- [ ] `kardinal policy simulate` with both gates passing returns PASS with table
- [ ] Team-level gate is additive alongside org gates
- [ ] Team cannot delete or modify org gates in `platform-policies` namespace (RBAC verified)
- [ ] `kardinal explain` shows all three gates as nodes with CEL expression and current value
- [ ] Gates appear as nodes in the promotion Graph (visible in `kardinal get steps`)
- [ ] Soak gate re-evaluates after `recheckInterval` without manual trigger

---

## Journey 4: Rollback

**Source**: `docs/rollback.md`

**The user story**: A bundle is promoted to prod. The engineer discovers a bug and rolls back.
One command. One PR. Same policy gates. Same audit trail.

### Exact steps that must work

```bash
# Assume kardinal-test-app v1.29.0 is verified in prod

# 1. Promote a bad version
kardinal create bundle kardinal-test-app --image ghcr.io/pnz1990/kardinal-test-app:sha-badbad0
# (goes through pipeline, reaches prod)

# 2. Roll back
kardinal rollback kardinal-test-app --env prod
# Rolling back kardinal-test-app in prod: v1.30.0-bad -> v1.29.0
# PR #N opened: https://github.com/.../pull/N

# 3. PR has kardinal/rollback label, same evidence structure as a forward promotion
# Must show previous version info, not just a diff

# 4. After merge
kardinal get pipelines
# PROD: v1.29.0 Verified
```

### Pass criteria

- [ ] `kardinal rollback` opens a PR with `kardinal/rollback` label
- [ ] Rollback PR has the same evidence structure as a promotion PR
- [ ] After merge, the environment reflects the rolled-back version
- [ ] `kardinal history kardinal-test-app` shows both the promotion and the rollback

---

## Journey 5: CLI — Core Operator Workflow

**Source**: `docs/cli-reference.md`

Every CLI command documented in `docs/cli-reference.md` must produce output
matching the documented format.

### Commands that must work

```bash
kardinal version                          # CLI + controller versions
kardinal get pipelines                    # table with PIPELINE/BUNDLE/ENV columns
kardinal get steps <pipeline>             # PromotionSteps + PolicyGates with states
kardinal get bundles <pipeline>           # Bundle history with provenance
kardinal create bundle <pipeline> --image # creates Bundle CRD, prints confirmation
kardinal promote <pipeline> --env <env>   # triggers promotion, prints PR URL
kardinal explain <pipeline> --env <env>   # policy gate trace with current values
kardinal rollback <pipeline> --env <env>  # opens rollback PR
kardinal pause <pipeline>                 # injects freeze gate
kardinal resume <pipeline>                # removes freeze gate
kardinal history <pipeline>               # promotion history with evidence
kardinal policy list                      # all PolicyGates with scope
kardinal policy simulate                  # gate simulation with result
```

### Pass criteria

- [ ] Every command above executes without error
- [ ] Output format matches examples in `docs/cli-reference.md`
- [ ] `kardinal explain` includes CEL expression, current value, and result
- [ ] `kardinal policy simulate` accepts `--time` flag and returns correct block/pass

---

## Journey 6: Rendered Manifests — Pre-Rendered GitOps

**Source**: `docs/rendered-manifests.md`, `examples/rendered-manifests/`

**The user story**: A platform engineer configures a pipeline that renders Kustomize
manifests at promotion time and commits the raw YAML output to environment-specific
branches. Argo CD syncs from the rendered branches. PR reviewers see exact YAML
diffs — no template expansion required. The GitOps agent never runs `kustomize build`.

This pattern is the enterprise standard for large Argo CD deployments (reduces agent
CPU load, enables CODEOWNERS on rendered output, surfaces hidden config changes in PRs).

### Exact steps that must work

```bash
# 1. GitOps repo has a "DRY" source branch and rendered environment branches
# Structure:
#   source/      (DRY: Kustomize base + overlays)
#   env/dev      (rendered: plain YAML for dev)
#   env/staging  (rendered: plain YAML for staging)
#   env/prod     (rendered: plain YAML for prod)

# 2. Apply the Pipeline with branch layout and kustomize-build step
kubectl apply -f examples/rendered-manifests/pipeline.yaml

# Pipeline uses layout: branch with kustomize-build in the step sequence:
# steps:
#   - uses: git-clone         # checks out source branch
#   - uses: kustomize-set-image
#   - uses: kustomize-build   # renders manifests to stdout
#   - uses: git-commit        # commits rendered YAML to env/prod branch
#   - uses: open-pr           # PR: env/prod-incoming -> env/prod
#   - uses: wait-for-merge
#   - uses: health-check

# 3. Create a Bundle
kardinal create bundle rendered-demo \
  --image ghcr.io/myorg/rendered-demo:v2.0.0

# 4. Inspect the PR
# PR must contain a rendered YAML diff (actual line-by-line YAML changes)
# not a diff of the kustomization.yaml values file
kardinal get steps rendered-demo
# prod: WaitingForMerge PR #N (rendered branch diff visible in GitHub)

# 5. After merge
kardinal get pipelines
# rendered-demo: PROD=Verified
```

### Pass criteria

- [ ] `layout: branch` with `kustomize-build` step renders manifests and commits to env branch
- [ ] PR diff shows rendered YAML, not template source
- [ ] Argo CD Application tracking `env/prod` branch reflects the merged content
- [ ] `kardinal explain` shows the branch each environment tracks
- [ ] Source branch (`source/`) is never modified by the promotion (only env branches change)
- [ ] Bundle supersession during an in-flight render: old render is discarded, new render begins

---

## Journey 7: Multi-Tenant Self-Service — Team Onboarding via ApplicationSet

**Source**: `docs/advanced-patterns.md`, `examples/multi-tenant/`

**The user story**: A platform team uses Argo CD ApplicationSets to provision
promotion pipelines automatically when a developer creates a new service folder
in a central repository. kardinal-promoter Pipelines are generated alongside
the Argo CD Applications. A new team member commits a folder to Git and receives
a complete 3-environment promotion pipeline without any manual platform team intervention.

This is the "nested ApplicationSet" pattern described in Kargo and Akuity workshops
as the target state for large-scale platform engineering.

### Exact steps that must work

```bash
# 1. Platform team installs the root ApplicationSet
kubectl apply -f examples/multi-tenant/root-appset.yaml

# root-appset.yaml watches the teams/ directory in the platform repo.
# When a new folder appears, it creates:
#   - A Namespace for the team
#   - An Argo CD Application for each environment
#   - A kardinal Pipeline for the team's service

# 2. Developer creates a new service
mkdir teams/payment-service
cat > teams/payment-service/pipeline-values.yaml <<EOF
image: ghcr.io/myorg/payment-service
environments: [dev, staging, prod]
prodApproval: pr-review
EOF
git add . && git commit -m "feat: add payment-service" && git push

# 3. ApplicationSet detects the new folder and provisions the Pipeline
kubectl get pipeline -n payment-service
# NAME              ENVS   STATUS
# payment-service   3      Ready

# 4. Team creates their first Bundle from CI
kardinal create bundle payment-service \
  --namespace payment-service \
  --image ghcr.io/myorg/payment-service:v1.0.0

# 5. Verify isolation: pipeline only affects payment-service namespace
kardinal get pipelines --all-namespaces
# NAMESPACE          PIPELINE          BUNDLE   STATUS
# payment-service    payment-service   v1.0.0   Promoting
# checkout-service   checkout-service  v3.1.2   Verified
```

### Pass criteria

- [ ] ApplicationSet creates a Pipeline CRD when a new team folder is committed to Git
- [ ] Pipeline is scoped to the team's namespace; org PolicyGates are inherited automatically
- [ ] Team cannot see or modify another team's Pipeline (RBAC isolation)
- [ ] `kardinal get pipelines --namespace payment-service` shows only that team's pipelines
- [ ] Org-level PolicyGate in `platform-policies` blocks prod promotion for the new team's pipeline on weekends
- [ ] Deleting the team folder from Git triggers ApplicationSet deletion of the Pipeline CRD (cascade)

---

## Journey Status

**Rule (Issue #418):** A journey is only marked ✅ when:
1. A PDCA workflow run (`.github/workflows/pdca.yml`; the result is in the run's job summary)
   reports the journey's scenario as passed on a live kind cluster with a real
   `kardinal-test-app` image, OR
2. A `[LIVE CLUSTER VALIDATED]` comment on the PR or release issue gives the commands, their
   output, the cluster version and the image SHA (AGENTS.md §Journey validation)

and every code example in the relevant doc page runs without error. Put the run or comment link
in the Notes column.

A passing `TestJourneyN` test is not evidence: those tests run the reconcilers against a fake
Kubernetes client, without the translator, the kro Graph or a cluster. A comment that only says
the tests pass does not count either.

| Journey | Status | Last checked | Notes |
|---|---|---|---|
| 1: Quickstart | unverified | 2026-09-29 | Fake-client test only (`TestJourney1Quickstart`). The PDCA workflow has not reported scenario 1 as passed. |
| 2: Multi-cluster fleet | unverified | 2026-09-29 | Fake-client test only (`TestJourney2MultiClusterFleet`). No multi-cluster run on record; live J2 evidence is tracked in #1293. |
| 3: Policy governance | unverified | 2026-09-29 | Fake-client test only (`TestJourney3PolicyGovernance`). |
| 4: Rollback | unverified | 2026-09-29 | Fake-client test only (`TestJourney4Rollback`). |
| 5: CLI workflow | unverified | 2026-09-29 | `TestJourney5CLI` runs `version`, `policy simulate` and `policy test` from the built CLI with no cluster, and skips if the binary is missing. |
| 6: Rendered manifests | unverified | 2026-09-29 | `TestJourney6RenderedManifests` only checks `DefaultSequenceForBundle`; it builds no Graph and no Bundle. |
| 7: Multi-tenant self-service | unverified | 2026-09-29 | Fake-client test only (`TestJourney7MultiTenantSelfService`); a fake client enforces no RBAC. |

---

## The Acceptance Test Suite

A journey is ✅ only with live-cluster evidence (see Journey Status). The Makefile targets
below run without a cluster: they prove reconciler logic, not that the product works on a cluster.

```bash
make test-e2e-journey-1    # TestJourney1Quickstart (fake client)
make test-e2e-journey-2    # TestJourney2MultiClusterFleet (fake client)
make test-e2e-journey-3    # TestJourney3PolicyGovernance (fake client)
make test-e2e-journey-4    # TestJourney4Rollback (fake client)
make test-e2e-journey-5    # TestJourney5CLI (built CLI, no cluster)
make test-e2e              # all five of the above
```

Journeys 6 and 7 have no Makefile target (`go test ./test/e2e/... -run 'TestJourney6|TestJourney7'`).
Live-cluster evidence comes from the PDCA workflow (`.github/workflows/pdca.yml`) or from
`make e2e-setup` followed by `make test-e2e-kind`.

Unit tests are necessary but not sufficient.
A feature is done when its journey has live-cluster evidence.
