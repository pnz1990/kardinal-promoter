# Definition of Done

> This is the north star. The project is complete when every journey below passes end-to-end.
> Read this before working on a journey.
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
# 1. Install kro (Graph controller), then kardinal-promoter with the GitHub token
#    (the controller opens the prod PR with it)
bash hack/install-kro.sh
kubectl create namespace kardinal-system
kubectl create secret generic github-token \
  --namespace kardinal-system \
  --from-literal=token=$GITHUB_PAT
helm install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --namespace kardinal-system \
  --set github.secretRef.name=github-token

# 2. Verify
kardinal version
# must print CLI: vX.Y.Z and Controller: vX.Y.Z (the installed release)

# 3. Create git credentials in the Pipeline's namespace (default)
kubectl create secret generic github-token \
  --from-literal=token=$GITHUB_PAT
kubectl label secret github-token kardinal.io/referenceable=true

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
# Must open a PR titled: "[kardinal] Promote <bundle-name> to prod"
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
Live tests run test and pre-prod on the hub and both prod regions on one spoke. The 4-cluster
layout is the demo (#1293).

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
# Must show a table with the columns
#   NAME  NAMESPACE  SCOPE  APPLIES-TO  RECHECK  CEL  LAST-EVALUATED
# and one row each for no-weekend-deploys and staging-soak-30m (scope org, applies-to prod)

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
# Message: "Must soak in staging for 30 minutes before promoting to prod"
#
# no-weekend-deploys:   PASS    (<reason>)
# staging-soak-30m:     BLOCK   (<reason>)

# 5. Simulate both gates passing
kardinal policy simulate --pipeline kardinal-test-app --env prod \
  --time "Tuesday 10am" --soak-minutes 45
# RESULT: PASS
# no-weekend-deploys:   PASS   (<reason>)
# staging-soak-30m:     PASS   (<reason>)

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
- [ ] Gates appear as nodes in the promotion Graph. Gates show in `kardinal explain <pipeline> --env prod` and in the UI graph.
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
# Rolling back kardinal-test-app in prod from <bundle> to <bundle> (...)
# Bundle <x> created (rollbackOf=<y>)
# Track with: kardinal explain kardinal-test-app --env prod

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
kardinal get steps <pipeline>             # PromotionSteps with states (gates show in explain and the UI graph)
kardinal get bundles <pipeline>           # Bundle history with provenance
kardinal create bundle <pipeline> --image # creates Bundle CRD, prints confirmation
kardinal promote <pipeline> --env <env>   # creates a Bundle; prints "Promoting <p> to <env>: bundle <x> created from <y> ..." and "Track with: kardinal get bundles <p>"
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

Not implemented yet (#1271).

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

# 2. Apply the Pipeline with branch layout
kubectl apply -f examples/rendered-manifests/pipeline.yaml

# There is no steps field. An environment with layout: branch runs the default sequence:
# git-clone, kustomize-set-image, kustomize-build, git-commit, git-push, then open-pr and
# wait-for-merge for pr-review, then health-check (pkg/steps/defaults.go).
# Today git-clone fails it, because layout: branch is not implemented (#1271).

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

- [ ] `layout: branch` renders manifests with `kustomize-build` and commits them to the env branch
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
appName: payment-service
gitRepo: https://github.com/myorg/gitops-repo
gitBranch: main
environments:
  - name: test
  - name: uat
  - name: prod
    approval: pr-review
EOF
git add . && git commit -m "feat: add payment-service" && git push

# 3. ApplicationSet detects the new folder and provisions the Pipeline
kubectl get pipeline -n payment-service
# NAME              PHASE     PAUSED   AGE
# payment-service   Unknown   false    10s

# 4. Team creates their first Bundle from CI
kardinal create bundle payment-service \
  --namespace payment-service \
  --image ghcr.io/myorg/payment-service:v1.0.0

# 5. Verify isolation: pipeline only affects payment-service namespace
kardinal get pipelines --all-namespaces
# NAMESPACE          PIPELINE           BUNDLE   TEST       UAT              PROD       SUB   AGE
# payment-service    payment-service    v1.0.0   Verified   HealthChecking   -          0     5m
# checkout-service   checkout-service   v3.1.2   Verified   Verified         Verified   0     2d
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

**Rule:** A journey is only marked ✅ when an e2e-live run
(`.github/workflows/e2e-live.yml`) on the commit passed the live tests that cover the journey's
steps, and every code example in the relevant doc page runs without error. The `e2e live` job
summary lists each test/e2e/coverage.tsv row's result. Put the run link in the Notes column
(AGENTS.md §Journey validation).

A passing `TestJourneyN` test is not evidence: those tests run the reconcilers against a fake
Kubernetes client, without the translator, the kro Graph or a cluster. A comment that only says
the tests pass does not count either.

| Journey | Status | Last checked | Notes |
|---|---|---|---|
| 1: Quickstart | live, partial | 2026-10-02 | `TestCore_QuickstartExample` (EX-QUICKSTART-01) passed in the v0.9.0 release e2e-live [run 37037330174](https://github.com/pnz1990/kardinal-promoter/actions/runs/37037330174) (956bb4ed, tagged v0.9.0) on core 1.35–1.37, gitea and gitlab. It uses podinfo on a local git server. `TestGitHub_ExampleGitHubDemo` (EX-GITHUB-DEMO-01) runs `examples/github-demo` on github.com: the three team gates, the prod PR's title, labels and evidence body, the bakes, and the auto-rollback PR after a bad release. It passed in e2e-live [run 37056344511](https://github.com/pnz1990/kardinal-promoter/actions/runs/37056344511) (79e1f4c1). |
| 2: Multi-cluster fleet | live, partial | 2026-10-02 | `TestMultiCluster_FleetExample`, `TestMultiCluster_ArgoHub`, `TestMultiCluster_FluxHub` and `TestMultiCluster_RolloutsInSpoke` (EX-FLEET-01, MC-ARGO-01, MC-FLUX-01) passed in [run 37037330174](https://github.com/pnz1990/kardinal-promoter/actions/runs/37037330174) on a kind hub and one spoke (#1388). The 4-cluster demo is tracked in #1293. |
| 3: Policy governance | live, partial | 2026-10-02 | GATE-ORG-01, GATE-TEAM-01, GATE-SOAK-01, GATE-RECHECK-01, CLI-POLICY-LIST-01 and CLI-POLICY-SIMULATE-01 passed in [run 37037330174](https://github.com/pnz1990/kardinal-promoter/actions/runs/37037330174). No live test covers pass criterion 7 (RBAC on `platform-policies`). |
| 4: Rollback | live, partial | 2026-10-02 | RB-PREV-01, RB-TO-01, RB-PR-01, RB-HISTORY-01, CLI-ROLLBACK-01 and CLI-HISTORY-01 passed in [run 37037330174](https://github.com/pnz1990/kardinal-promoter/actions/runs/37037330174). |
| 5: CLI workflow | live, partial | 2026-10-02 | All 35 live CLI-* rows passed in [run 37037330174](https://github.com/pnz1990/kardinal-promoter/actions/runs/37037330174). The two deprecated rows also passed: `approve` fails and points to `kardinal override`, and `rollback --emergency` has no effect. |
| 6: Rendered manifests | not implemented in v0.9.0 | 2026-10-02 | `layout: branch` is not implemented (#1271, open), planned for v0.10.0. PIPE-NOTIMPL-01 only checks that the Pipeline reports NotImplemented. This journey cannot pass yet. |
| 7: Multi-tenant self-service | live, partial | 2026-10-02 | `TestHealth_MultiTenantExample` (EX-TENANT-01) passed in [run 37037330174](https://github.com/pnz1990/kardinal-promoter/actions/runs/37037330174). It covers one Pipeline per team from the ApplicationSet, and each team promoting. It does not cover RBAC isolation, the org weekend gate on a new team, or cascade delete. |

The v0.9.0 evidence is e2e-live [run 37037330174](https://github.com/pnz1990/kardinal-promoter/actions/runs/37037330174) on 956bb4ed, the commit tagged v0.9.0: 16 of 17 jobs
passed (core 1.35–1.37, gitea, gitlab, flux, delivery, chart, ui, multi-cluster, upgrade on 1.30
and 1.37), 777 tests, none failed. The github suite did not start then: GitHub rejected the
DEMO_GITHUB_TOKEN secret (401). With a new token (#1431), e2e-live
[run 37054920646](https://github.com/pnz1990/kardinal-promoter/actions/runs/37054920646) on main
(7b6c4500) passed all 17 jobs, the github suite included (25 passed, none failed or skipped), and
[run 37056344511](https://github.com/pnz1990/kardinal-promoter/actions/runs/37056344511) added
EX-GITHUB-DEMO-01 (github: 26 passed). Every live row of `test/e2e/coverage.tsv` now has a passing
live test.

---

## The Acceptance Test Suite

A journey is ✅ only with live-cluster evidence (see Journey Status). It comes from the live e2e
suites (test/e2e/live; test/e2e/README.md), which run on kind clusters with a real git server, a
GitOps engine and podinfo as the test app:

```bash
make e2e-up SUITE=core          # hack/e2e/up.sh core
make test-e2e-live SUITE=core   # hack/e2e/run.sh core; fails when a test fails or skips, or none ran
make e2e-down SUITE=core
```

test/e2e/coverage.tsv lists every documented behavior and whether a live test covers it. A test
claims rows with `Covers ID, ID.` in its doc comment, and `go test ./test/hack -run TestE2ECoverage`
fails when the file and the tests disagree. `make e2e-all` (hack/e2e/all.sh) runs every suite
locally before a merge, and `.github/workflows/e2e-live.yml` weekly and on dispatch; both end
with `go run ./test/e2e/proof`, which fails when a claimed row's test failed or skipped.

The journey tests below run without a cluster: they prove reconciler logic, not that the product
works on a cluster.

```bash
go test ./test/e2e/ -run TestJourney   # TestJourney1..7; TestJourney5CLI needs make build first
```

Unit tests are necessary but not sufficient.
A feature is done when its journey has live-cluster evidence.
