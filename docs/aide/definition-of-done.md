# Definition of Done

> This is the north star. The project is complete when every journey below passes end-to-end.
> Every agent reads this document before starting work.
> Every feature is implemented to make these journeys pass — not to satisfy internal specs.
> If a journey fails, the project is not done, regardless of what the code says.

---

## How to Use This Document

**Engineers**: Before writing a single line of code, read the journey your feature contributes to.
Understand what the user will type and what they expect to see. Build backwards from that.

**QA**: After every PR, ask: "Does this bring us closer to passing the journeys below?"
If a PR passes unit tests but moves us away from a journey, it fails QA.

**Coordinator**: After each batch completes, verify which journeys now pass end-to-end.
Update the journey status table at the bottom of this file.

---

## Journey 1: Quickstart — First Promotion in 15 Minutes

**Source**: `docs/quickstart.md`, `examples/quickstart/`

**The user story**: A platform engineer installs kardinal-promoter on a kind cluster,
applies a 15-line Pipeline CRD, creates a Bundle, and watches it promote through
test → uat → prod automatically, with a PR opened for prod that they review and merge.

### Exact steps that must work

```bash
# 1. Install
helm install kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
  --namespace kardinal-system --create-namespace

# 2. Verify
kardinal version
# must print: CLI: v0.1.x, Controller: v0.1.x

# 3. Create git credentials
kubectl create secret generic github-token \
  --from-literal=token=$GITHUB_PAT

# 4. Apply the Pipeline
kubectl apply -f examples/quickstart/pipeline.yaml

# 5. Apply org PolicyGates
kubectl apply -f examples/quickstart/policy-gates.yaml

# 6. Create a Bundle
kardinal create bundle nginx-demo \
  --image ghcr.io/nginx/nginx:1.29.0

# 7. Watch promotion start
kardinal get pipelines
# PIPELINE    BUNDLE    TEST       UAT     PROD     AGE
# nginx-demo  v1.29.0  Verified   ...     ...      2m

# 8. Check policy gate explanation
kardinal explain nginx-demo --env prod
# Must show: PolicyGates evaluated, reason why prod is waiting or ready

# 9. Prod PR is opened automatically
# Must open a PR titled: "[kardinal] Promote nginx-demo v1.29.0 to prod"
# PR body must contain: artifact info, upstream verification, policy compliance table

# 10. After PR merge
kardinal get pipelines
# PIPELINE    BUNDLE    TEST       UAT        PROD       AGE
# nginx-demo  v1.29.0  Verified   Verified   Verified   8m
```

### Pass criteria

- [ ] `kardinal version` returns valid version strings
- [ ] `kubectl apply -f examples/quickstart/pipeline.yaml` succeeds with no errors
- [ ] Bundle creation triggers automatic promotion to test and uat
- [ ] PolicyGates block prod on weekends (verify with `kardinal explain`)
- [ ] `kardinal explain nginx-demo --env prod` shows gate evaluation with values
- [ ] A PR is opened for prod with structured evidence body (not a raw diff)
- [ ] After PR merge, `kardinal get pipelines` shows PROD=Verified
- [ ] `kardinal get steps nginx-demo` shows all steps with correct states

---

## Journey 2: Multi-Cluster Fleet — Parallel Prod with Argo Rollouts

**Source**: `examples/multi-cluster-fleet/`, AWS workshops:
- https://catalog.workshops.aws/platform-engineering-on-eks/en-US/30-progressiveapplicationdelivery/40-production-deploy-kargo
- https://github.com/aws-samples/fleet-management-on-amazon-eks-workshop/tree/mainline/patterns/kro-eks-cluster-mgmt#promote-the-application-to-prod-clusters

**The user story**: A platform engineer promotes `rollouts-demo` through test → pre-prod → [prod-eu, prod-us] in parallel.
Prod environments use Argo Rollouts canary. Argo CD hub-spoke manages 4 clusters.
Two PRs are opened in parallel for prod-eu and prod-us.

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

# 7. After merging both PRs, Argo Rollouts canary runs in each cluster
kardinal get steps rollouts-demo
# prod-eu: HealthChecking (delegated to argoRollouts)
# prod-us: HealthChecking (delegated to argoRollouts)

# 8. When rollouts complete
kardinal get pipelines
# PROD-EU: Verified, PROD-US: Verified
```

### Pass criteria

- [ ] `dependsOn` fan-out works: prod-eu and prod-us start simultaneously after pre-prod
- [ ] Two PRs opened in parallel, both with evidence and policy compliance
- [ ] `kardinal explain rollouts-demo --env prod-eu` shows gate states correctly
- [ ] Health adapter reads Argo CD Application status from hub cluster
- [ ] Argo Rollouts delegation: health type `argoRollouts` waits for Rollout.status.phase=Healthy
- [ ] Both prod regions reach Verified independently

---

## Journey 3: Policy Governance

**Source**: `docs/policy-gates.md`

**The user story**: A platform engineer adds a `no-weekend-deploys` PolicyGate
and verifies it blocks a prod promotion, then simulates it with `kardinal policy simulate`.

### Exact steps that must work

```bash
# 1. Apply an org gate
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
EOF

# 2. Verify it's listed
kardinal policy list
# Must show: no-weekend-deploys [org] applies-to: prod

# 3. Simulate a weekend promotion
kardinal policy simulate --pipeline nginx-demo --env prod --time "Saturday 3pm"
# RESULT: BLOCKED
# Blocked by: no-weekend-deploys
# Message: "Production deployments are blocked on weekends"
# Next window: Monday 00:00 UTC

# 4. On a weekday, verify it passes
kardinal explain nginx-demo --env prod
# no-weekend-deploys [org] PASS  schedule.isWeekend = false

# 5. Verify gates are injected into the Graph
# kubectl get promotionstep -l kardinal.io/pipeline=nginx-demo
# Must include PolicyGate instances as Graph nodes
```

### Pass criteria

- [ ] PolicyGate CRD applies correctly
- [ ] `kardinal policy list` shows gate with correct scope and applies-to
- [ ] `kardinal policy simulate --time "Saturday 3pm"` returns BLOCKED with reason
- [ ] `kardinal explain` shows gate evaluation with current values
- [ ] Gate appears as a node in the promotion Graph (visible in `kardinal get steps`)

---

## Journey 4: Rollback

**Source**: `docs/rollback.md`

**The user story**: A bundle is promoted to prod. The engineer discovers a bug and rolls back.
One command. One PR. Same policy gates. Same audit trail.

### Exact steps that must work

```bash
# Assume nginx-demo v1.29.0 is verified in prod

# 1. Promote a bad version
kardinal create bundle nginx-demo --image ghcr.io/nginx/nginx:1.30.0-bad
# (goes through pipeline, reaches prod)

# 2. Roll back
kardinal rollback nginx-demo --env prod
# Rolling back nginx-demo in prod: v1.30.0-bad -> v1.29.0
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
- [ ] `kardinal history nginx-demo` shows both the promotion and the rollback

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

## Journey Status

Updated by the coordinator after each batch.

| Journey | Status | Last checked | Notes |
|---|---|---|---|
| 1: Quickstart | ❌ Not started | — | Requires Stages 0-8 |
| 2: Multi-cluster fleet | ❌ Not started | — | Requires Stages 0-8, 11, 14 |
| 3: Policy governance | ❌ Not started | — | Requires Stages 0-5 |
| 4: Rollback | ❌ Not started | — | Requires Stages 0-7, 10 |
| 5: CLI workflow | ❌ Not started | — | Requires Stages 0-9 |

---

## How Journeys Map to Roadmap Stages

| Stage | Journeys it enables |
|---|---|
| 0: Project Skeleton | All (prerequisites) |
| 1: CRD Types | All (type system) |
| 2: Bundle + Pipeline Reconcilers | All (basic flow) |
| 3: Graph Generation | All (core engine) |
| 4: PolicyGate CEL Evaluator | J1 partial, J3 full |
| 5: Git Operations + GitHub PR | J1 full, J4 full |
| 6: PromotionStep Reconciler | J1 full end-to-end |
| 7: Health Adapters | J1 with Argo CD, J2 partial |
| 8: CLI | J5 full |
| 9: kardinal-ui | UI visibility (bonus) |
| 10: PR Evidence + Webhook | J1 PR quality, J4 rollback |
| 11: GitHub Action + kardinal init | J1 CI integration |
| 13: Rollback | J4 full |
| 14: Distributed Mode | J2 full with agents |
| 15: MetricCheck | J3 metrics gates |

---

## The Acceptance Test Suite

When a journey is marked ✅, it means the following passes in CI on a kind cluster:

```bash
make test-e2e-journey-1    # quickstart end-to-end
make test-e2e-journey-2    # multi-cluster fleet (simulated)
make test-e2e-journey-3    # policy governance
make test-e2e-journey-4    # rollback
make test-e2e-journey-5    # CLI commands
```

These E2E tests are the final arbiter of project completeness.
Unit tests are necessary but not sufficient.
A feature is done when its journey test passes.
