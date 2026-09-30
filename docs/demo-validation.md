# Demo Validation — kardinal-promoter Health Adapters

This document describes what each health adapter checks, how to validate it, and the expected output. All adapter code is in `pkg/health/adapter.go`. All unit tests are in `pkg/health/health_test.go`.

---

## Quick Start

```bash
# Run all adapter unit tests (no cluster required)
bash scripts/demo-validate.sh

# Run with verbose output
bash scripts/demo-validate.sh -v

# Run a specific adapter
bash scripts/demo-validate.sh -v -run Flagger
```

**Expected output:**

```
================================================================
kardinal-promoter demo-validate: health adapter coverage check
================================================================

[1/3] Build check...
      ✅ Build passed

[2/3] Running pkg/health tests (race, count=1)...

--- PASS: TestDeploymentAdapter_Healthy (0.00s)
--- PASS: TestDeploymentAdapter_Degraded (0.00s)
--- PASS: TestDeploymentAdapter_NotFound (0.00s)
... (all tests)

      ✅ All health adapter tests passed

[3/3] Adapter coverage summary:

  Adapter        | Test count | GVR
  --------------|------------|--------------------------------------------
  resource       | 3          | apps/v1 Deployment
  argocd         | 5          | argoproj.io/v1alpha1 Application
  flux           | 3          | kustomize.toolkit.fluxcd.io/v1 Kustomization
  argoRollouts   | 4          | argoproj.io/v1alpha1 Rollout
  flagger        | 4          | flagger.app/v1beta1 Canary

  Total: 19 adapter tests
  ✅ All adapters have ≥3 tests

================================================================
```

---

## Adapter 1: `resource` — Kubernetes Deployment

**What it checks:** `Deployment.status.conditions[type=Available].status == True`

**Kubernetes resource:** `apps/v1 Deployment`

**Healthy when:** the Deployment runs the Bundle images, `observedGeneration` is current, every replica is updated and available, and the `Available` condition (or configured condition) is `True`. `ProgressDeadlineExceeded` fails the step at once. See [Health Adapters](health-adapters.md#adapter-resource-default) for the exact rules.

**Configuration:**

```yaml
health:
  type: resource
  resource:
    name: my-app          # default: pipeline name
    namespace: prod       # default: environment name
    condition: Available  # default: "Available"
```

**Example Pipeline:**

```yaml
environments:
  - name: prod
    health:
      type: resource
      resource:
        name: kardinal-test-app
        namespace: prod
```

**State table:**

| Deployment state | Adapter result | Reason |
|---|---|---|
| Rolled out on the Bundle images, `Available=True` | Healthy | `Available=True: MinimumReplicasAvailable, 1/1 replicas updated and available` |
| Pod template still on the previous image | Wait | `Deployment prod/my-app not updated yet: runs ...:v1, Bundle has ...:v2` |
| New generation not observed, or replicas still rolling | Wait | `waiting for the Deployment controller to observe generation 2` / `rolling out: 1 of 3 replicas updated` |
| `Progressing` reason `ProgressDeadlineExceeded` | **Failed** (applies `onHealthFailure`) | `rollout failed: ProgressDeadlineExceeded` |
| Rollout finished, `Available=False` or condition absent | Unhealthy (counts as a failure) | `Available=False: ...` |
| Deployment not found | Unhealthy (counts as a failure) | `Deployment prod/my-app not found` |

**Known limitations:**

- Only `kind: Deployment` is supported; any other `resource.kind` fails the step.
- Label-selector mode: when `health.labelSelector` is set, every matching Deployment in the namespace must pass the checks above; no match is unhealthy.

**Unit tests:** `TestDeploymentAdapter_Healthy`, `TestDeploymentAdapter_Degraded`, `TestDeploymentAdapter_NotFound`

**Demo example:** `examples/quickstart/pipeline.yaml`, `examples/wave-topology/pipeline.yaml`

---

## Adapter 2: `argocd` — Argo CD Application

**What it checks:** `Application.status.health.status == "Healthy" AND Application.status.sync.status == "Synced" AND (operationState.phase == "Succeeded" OR "")` AND the Application synced the promoted commit (`status.sync.revision(s)`, `operationState.syncResult.revision(s)` or `status.history`)

**Kubernetes resource:** `argoproj.io/v1alpha1 Application`

**Healthy when:** All four conditions are true simultaneously.

**Configuration:**

```yaml
health:
  type: argocd
  argocd:
    name: my-app-prod   # Application name (default: <pipeline>-<environment>)
    namespace: argocd   # default: "argocd"
  timeout: 15m
```

**Example Pipeline:**

```yaml
environments:
  - name: prod
    health:
      type: argocd
      argocd:
        name: kardinal-test-app-prod
        namespace: argocd
      timeout: 15m
```

**State table:**

| health | sync | opPhase | Adapter result |
|---|---|---|---|
| `Healthy` | `Synced` to the promoted commit | `Succeeded` or `""` | **Healthy** |
| `Healthy` | `Synced` to an older commit | any | Wait (`revision=<old>, waiting for <new>`) |
| `Healthy` | `Synced` | `Running` | Wait (sync in progress) |
| `Progressing`, `Missing` or `Suspended` | any | any | Wait → `onHealthFailure` at `health.timeout` |
| any | `OutOfSync` | any | Wait (mid-sync) |
| `Degraded` | any | any | Unhealthy (counts as a failure) → `onHealthFailure` at `health.timeout` |
| any | any | `Failed` or `Error` | Unhealthy (counts as a failure) → `onHealthFailure` at `health.timeout` |
| not found | — | — | Unhealthy (counts as a failure) |

**Known limitations:**

- Uses the dynamic client to avoid a compile-time dependency on the Argo CD API types. This means the adapter works with any Argo CD version that keeps the `health`/`sync`/`operationState` path in `Application.status`.
- In multi-cluster hub-spoke mode, all Applications live in the hub cluster. The adapter reads from the hub.

**Unit tests:** `TestArgoCDAdapter_Healthy`, `TestArgoCDAdapter_Degraded`, `TestArgoCDAdapter_NotFound`, `TestArgoCDAdapter_OutOfSync`, `TestArgoCDAdapter_OperationInProgress`

**Demo examples:** `examples/quickstart/pipeline.yaml`, `examples/github-demo/pipeline.yaml`, `examples/argo-rollouts-demo/pipeline.yaml`

---

## Adapter 3: `flux` — Flux Kustomization

**What it checks:** `Kustomization.status.conditions[type=Ready].status == "True"` AND `observedGeneration == metadata.generation` AND `status.lastAppliedRevision` is the promoted commit

**Kubernetes resource:** `kustomize.toolkit.fluxcd.io/v1 Kustomization`

**Healthy when:** Ready condition is True AND generation matches (Flux has reconciled the current spec).

**Configuration:**

```yaml
health:
  type: flux
  flux:
    name: my-app-prod      # Kustomization name (default: <pipeline>-<environment>)
    namespace: flux-system  # default: "flux-system"
  timeout: 20m
```

**Example Pipeline:**

```yaml
environments:
  - name: prod
    health:
      type: flux
      flux:
        name: kardinal-test-app-prod
        namespace: flux-system
      timeout: 20m
```

**State table:**

| Ready | observedGen == gen | lastAppliedRevision | Adapter result |
|---|---|---|---|
| `True` | Yes | promoted commit | **Healthy** |
| `True` | Yes | older commit | Wait (Flux has not applied the new commit) |
| `True` | No (stale) | any | Wait (Flux reconciling new spec) |
| `Unknown` or Ready absent | any | any | Wait |
| `False` | any | any | Unhealthy (counts as a failure) |
| Not found | — | — | Unhealthy (counts as a failure) |

**The revision check is critical:** `Ready=True` with a matching generation only says Flux applied *some* revision. Without comparing `lastAppliedRevision` with the promoted commit, kardinal would advance the promotion before Flux has applied the new commit.

**Known limitations:**

- Requires Flux v2 (`kustomize.toolkit.fluxcd.io/v1`). Flux v1 (deprecated) uses a different GVR.
- Multi-cluster: Flux runs in each cluster. `health.cluster` is not supported (a non-empty value fails the step), so a Kustomization in another cluster cannot be checked; see [Remote Clusters](health-adapters.md#remote-clusters).
- The interval between Flux reconciliations (default `1m`) adds latency. For faster iteration, set `interval: 30s` on the Kustomization.

**Unit tests:** `TestFluxAdapter_Healthy`, `TestFluxAdapter_Progressing`, `TestFluxAdapter_NotFound`

**Demo example:** `examples/flux-demo/pipeline.yaml`

---

## Adapter 4: `argoRollouts` — Argo Rollouts Rollout

**What it checks:** `Rollout.status.phase == "Healthy"`

**Kubernetes resource:** `argoproj.io/v1alpha1 Rollout`

**Healthy when:** Phase is `Healthy` (all canary steps completed and promoted).

**Configuration:**

```yaml
health:
  type: argoRollouts
  argoRollouts:
    name: my-app    # Rollout name (default: pipeline name)
    namespace: prod  # namespace (default: environment name)
  timeout: 30m      # must exceed total canary step duration
```

**Example Pipeline:**

```yaml
environments:
  - name: prod
    health:
      type: argoRollouts
      argoRollouts:
        name: kardinal-test-app
        namespace: prod
      timeout: 30m
```

**State table:**

| Rollout phase | Adapter result | Meaning |
|---|---|---|
| `Progressing` | Wait | Canary steps running |
| `Paused` | Wait | Waiting at a manual pause step |
| `Healthy` | **Healthy** | All replicas on new image, analysis passed |
| `Degraded` | Unhealthy → `onHealthFailure` at `health.timeout` | Rollout failed / analysis failed |
| Not found | Unhealthy → `onHealthFailure` at `health.timeout` | Rollout CR not yet created |

**Degraded handling:** `Degraded` counts as a health failure (`status.consecutiveHealthFailures`, which a `RollbackPolicy` can act on). It is not terminal, so `onHealthFailure` applies when `health.timeout` expires (`none` fails the step, `abort` sets AbortedByAlarm, `rollback` creates a rollback Bundle), or at once during a bake window with `bake.policy: fail-on-alarm`.

The adapter checks the Rollout phase, not its revision: a Rollout still `Healthy` on the previous version can report Verified before the change reaches it. Use a `bake` window when that matters.

**Known limitations:**

- Does not distinguish between `Paused` (normal canary step) and `Paused` (manual operator pause). Both return Wait.
- Argo Rollouts must be installed before the Rollout CR is applied.

**Unit tests:** `TestArgoRolloutsAdapter_Healthy`, `TestArgoRolloutsAdapter_Progressing`, `TestArgoRolloutsAdapter_Degraded`, `TestArgoRolloutsAdapter_NotFound`, `TestAutoDetector_ArgoRolloutsType`

**Demo examples:** `examples/argo-rollouts-demo/pipeline.yaml`, `examples/multi-cluster-fleet/pipeline.yaml`

---

## Adapter 5: `flagger` — Flagger Canary

**What it checks:** `Canary.status.phase == "Succeeded"`

**Kubernetes resource:** `flagger.app/v1beta1 Canary`

**Healthy when:** Phase is `Succeeded` (Flagger has promoted the canary to primary).

**Configuration:**

```yaml
health:
  type: flagger
  flagger:
    name: my-app    # Canary CR name (default: pipeline name)
    namespace: prod  # namespace (default: environment name)
  timeout: 30m      # must exceed Flagger's canary analysis duration
```

**Example Pipeline:**

```yaml
environments:
  - name: prod
    health:
      type: flagger
      flagger:
        name: kardinal-test-app
        namespace: prod
      timeout: 30m
```

**State table:**

| Canary phase | Adapter result | Meaning |
|---|---|---|
| `Initializing` | Wait | Flagger setting up traffic split |
| `Initialized` | Wait | Canary ready, no new image yet |
| `Waiting` | Wait | Waiting for new image |
| `Progressing` | Wait | Canary analysis running |
| `Promoting` | Wait | Copying canary spec to primary |
| `Finalising` | Wait | Scaling down canary |
| `Succeeded` | **Healthy** | Canary promoted — promotion Verified |
| `Failed` | **Failed at once** | Canary analysis failed, Flagger rolled back |
| Not found | Unhealthy → `onHealthFailure` at `health.timeout` | Canary CR not found |

**Failed handling:** a `Failed` phase is terminal, so `onHealthFailure` applies at once: `none` fails the step, `abort` sets AbortedByAlarm, and `rollback` creates a rollback Bundle for the previous version.

**Known limitations:**

- Requires Flagger v1 (the `flagger.app/v1beta1` API).
- Flagger must be installed with a metrics provider (Prometheus) for metric-based analysis. Without metrics, Flagger promotes on iteration count alone.
- The `timeout` on the environment health config must be longer than Flagger's total canary duration: `interval * threshold * steps`.

**Unit tests:** `TestFlaggerAdapter_Succeeded`, `TestFlaggerAdapter_Failed`, `TestFlaggerAdapter_Progressing`, `TestFlaggerAdapter_NotFound`, `TestAutoDetector_FlaggerType`

**Demo example:** `examples/flagger-demo/pipeline.yaml`

---

## Running the Full Validation

```bash
# Unit tests only (no cluster required)
bash scripts/demo-validate.sh -v

# With live cluster (kind + all adapters installed)
make setup-e2e-env

# Apply all demo examples
kubectl apply -f examples/flux-demo/flux-kustomizations.yaml
kubectl apply -f examples/flux-demo/pipeline.yaml

kubectl apply -f examples/flagger-demo/canary.yaml
kubectl apply -f examples/flagger-demo/pipeline.yaml

kubectl apply -f examples/argo-rollouts-demo/rollout.yaml
kubectl apply -f examples/argo-rollouts-demo/pipeline.yaml

kubectl apply -f examples/github-demo/pipeline.yaml

# Trigger promotions
LATEST_SHA=$(gh api repos/pnz1990/kardinal-test-app/commits/main --jq '.sha[:7]')
kardinal create bundle kardinal-test-app \
  --image "ghcr.io/pnz1990/kardinal-test-app:sha-${LATEST_SHA}"
kardinal get pipelines
```

---

## Adapter Selection Reference

| When to use | `health.type` | Required CRDs |
|---|---|---|
| Raw Kubernetes (no GitOps) | `resource` | None — always available |
| Argo CD manages deployments | `argocd` | `applications.argoproj.io` |
| Flux manages deployments | `flux` | `kustomizations.kustomize.toolkit.fluxcd.io` |
| Argo Rollouts canary | `argoRollouts` | `rollouts.argoproj.io` |
| Flagger progressive delivery | `flagger` | `canaries.flagger.app` |

**health.type defaults to `resource`** when omitted (`delivery.delegate`, when set, takes precedence). There is no auto-detection by probing for CRDs. See `pkg/health/options.go:EffectiveType()`.

---

## Live Cluster Validation (demo/scripts/validate.sh)

Scenarios 11–13 validate the new adapters against a live cluster. They are included in `demo/scripts/validate.sh` and run as part of the nightly `demo-validate` GitHub Actions workflow.

### Running locally

```bash
# Full demo environment (kind + ArgoCD + Flux + Argo Rollouts + Flagger)
bash demo/scripts/setup.sh

# Run all scenarios including 11-13
bash demo/scripts/validate.sh
```

### Scenario results format

Each adapter scenario checks:
1. The adapter's Pipeline is registered (`kardinal get pipelines`)
2. The adapter-specific CRD exists in the cluster
3. The live resource is in a known phase (Healthy/Progressing/Succeeded)
4. The Pipeline spec has the correct `health.type`

Example output:

```
── Scenario 11: Flux health adapter — Kustomization Ready check
  ✓ kardinal-test-app-flux pipeline registered
  ✓ Flux Kustomizations found: 2
  ✓ Kustomization 'kardinal-test-app-flux-test' Ready=True — adapter would report Healthy
  ✓ Pipeline spec confirms health.type=flux

── Scenario 12: Argo Rollouts health adapter — Rollout phase check
  ✓ kardinal-test-app-rollouts pipeline registered
  ✓ Rollout phase=Healthy — argoRollouts adapter reports Healthy
  ✓ Pipeline spec confirms health.type=argoRollouts

── Scenario 13: Flagger health adapter — Canary phase check
  ✓ kardinal-test-app-flagger pipeline registered
  ✓ Canary phase=Succeeded — flagger adapter reports Healthy
  ✓ Pipeline spec confirms health.type=flagger
```

### Skipped vs failed

Scenarios gracefully skip when an adapter is not installed (`INSTALL_FLUX=false` etc.), rather than failing. This allows running the demo on minimal clusters without all tools installed. A skip is not a failure — it means the adapter was not exercised, not that it is broken.

### CI integration

The `demo-validate.yml` GitHub Actions workflow runs all scenarios nightly (including 11-13) on a full kind cluster with all adapters installed. Failed scenarios post `❌ FAIL` to the daily report issue. Passing scenarios post `✅ PASS`.

```bash
# Trigger manually on GitHub
gh workflow run demo-validate.yml --repo pnz1990/kardinal-promoter

# Or trigger specific scenario
gh workflow run demo-validate.yml --repo pnz1990/kardinal-promoter \
  -f scenario=11
```
