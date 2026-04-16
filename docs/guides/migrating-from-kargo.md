# Migrating from Kargo

This guide walks through migrating a Kargo-managed delivery pipeline to kardinal-promoter. It assumes familiarity with Kargo concepts.

---

## Concept mapping

| Kargo concept | kardinal equivalent | Notes |
|---|---|---|
| `Warehouse` | `Subscription` CRD | OCI watcher polls registries; creates `Bundle` on new digest |
| `Stage` | Environment in `Pipeline.spec.environments[]` | One Pipeline holds all environments |
| `Freight` | `Bundle` CRD | One Bundle per artifact version; carries provenance |
| `FreightRequest` | `Bundle.spec.intent` | Targets a specific environment; can skip others |
| `Promotion` | `PromotionStep` CRD | Created automatically by the Graph controller |
| `VerifiedIn` / approval required | `approvalMode: pr-review` on environment | PR approval required before HealthChecking |
| `AnalysisTemplate` | `MetricCheck` CRD | Prometheus / custom queries with pass/fail thresholds |
| `ClusterStage` | `Pipeline` with `namespace` per env | Multi-cluster via kubeconfig Secret |
| `Project` | Kubernetes Namespace | RBAC isolation is namespace-scoped |
| Argo Rollouts integration | `health.type: argoRollouts` on environment | Reads Rollout `.status.phase` |

---

## Side-by-side YAML

### Kargo: Warehouse + Stage

```yaml
# Kargo: Warehouse (artifact source)
apiVersion: kargo.akuity.io/v1alpha1
kind: Warehouse
metadata:
  name: my-app
  namespace: kargo-demo
spec:
  subscriptions:
    - image:
        repoURL: ghcr.io/myorg/my-app
        semverConstraint: ^1.0.0
        discoveryLimit: 5

---
# Kargo: Stages
apiVersion: kargo.akuity.io/v1alpha1
kind: Stage
metadata:
  name: test
  namespace: kargo-demo
spec:
  requestedFreight:
    - origin:
        kind: Warehouse
        name: my-app
      sources:
        direct: true
  promotionTemplate:
    spec:
      steps:
        - uses: git-clone
          config:
            repoURL: https://github.com/myorg/gitops-repo.git
            checkout:
              - branch: env/test
                path: ./out
        - uses: kustomize-set-image
          config:
            images:
              - image: ghcr.io/myorg/my-app
        - uses: git-commit
          config:
            path: ./out
        - uses: git-push
          config:
            path: ./out

---
apiVersion: kargo.akuity.io/v1alpha1
kind: Stage
metadata:
  name: prod
  namespace: kargo-demo
spec:
  requestedFreight:
    - origin:
        kind: Warehouse
        name: my-app
      sources:
        stages:
          - test
  promotionTemplate:
    spec:
      steps:
        # same steps as test...
```

### kardinal: Pipeline + Subscription

```yaml
# kardinal: Pipeline (replaces all Stages)
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    repoURL: https://github.com/myorg/gitops-repo.git
    credentialSecret: github-token
  environments:
    - name: test
      branch: env/test
      approvalMode: auto
      updateStrategy: kustomize
    - name: prod
      branch: env/prod
      approvalMode: pr-review    # requires PR merge (Kargo: Stage with approval)
      updateStrategy: kustomize
      dependsOn:
        - test                    # explicit sequencing (Kargo: sources.stages)
  policyNamespaces:
    - platform-policies

---
# kardinal: Subscription (replaces Warehouse)
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: my-app-sub
spec:
  pipeline: my-app
  type: image
  image:
    registry: ghcr.io/myorg/my-app
    tagFilter: "^1\\..*"         # semver-compatible regex
  interval: 2m
```

---

## Migration steps

### Step 1: Map your Stages to Pipeline environments

Kargo Stages are individual resources; kardinal collapses them into a single Pipeline.

For each Kargo Stage:
1. Add an entry to `spec.environments[]` in the Pipeline
2. Copy `approvalMode` from Stage's approval configuration:
   - Auto-promotion → `approvalMode: auto`
   - Manual approval → `approvalMode: pr-review`
3. Translate `sources.stages: [upstream]` → `dependsOn: [upstream]`

### Step 2: Convert Warehouses to Subscriptions

Kargo Warehouses poll container registries. kardinal's equivalent is the `Subscription` CRD:

```bash
# For each Warehouse, create a Subscription:
kubectl get warehouse my-app -n kargo-demo -o yaml
# Translate repoURL → spec.image.registry
# Translate semverConstraint → spec.image.tagFilter (Go regex)
```

### Step 3: Remove Kargo `Promotion` objects (if any)

kardinal creates `PromotionStep` objects automatically via the Graph controller. You do not create them manually.

### Step 4: Convert AnalysisTemplates to MetricChecks

```yaml
# Kargo: AnalysisTemplate
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: success-rate
spec:
  metrics:
    - name: success-rate
      interval: 60s
      count: 5
      successCondition: result[0] >= 0.95
      provider:
        prometheus:
          address: http://prometheus:9090
          query: |
            sum(rate(http_requests_total{status=~"2.."}[5m])) /
            sum(rate(http_requests_total[5m]))

---
# kardinal: MetricCheck
apiVersion: kardinal.io/v1alpha1
kind: MetricCheck
metadata:
  name: success-rate
  namespace: platform-policies
  labels:
    kardinal.io/applies-to: prod
spec:
  query: |
    sum(rate(http_requests_total{status=~"2.."}[5m])) /
    sum(rate(http_requests_total[5m]))
  prometheusURL: http://prometheus.monitoring.svc:9090
  threshold:
    operator: ">="
    value: 0.95
  recheckInterval: 1m
  windowDuration: 5m
```

### Step 5: Convert AnalysisRunArguments to PolicyGate CEL expressions

Kargo AnalysisRun arguments map to CEL expressions in PolicyGates:

```yaml
# kardinal: PolicyGate using MetricCheck result
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: success-rate-gate
  namespace: platform-policies
  labels:
    kardinal.io/applies-to: prod
spec:
  expression: "metrics.successRate >= 0.95"
  message: "Success rate below 95%"
  recheckInterval: 1m
```

### Step 6: Install kardinal and verify

```bash
# Install kardinal
helm install kardinal oci://ghcr.io/pnz1990/kardinal-promoter/chart \
  --namespace kardinal-system --create-namespace

# Apply your Pipeline
kubectl apply -f pipeline.yaml

# Create a Bundle manually to test (in Kargo you'd wait for Warehouse to detect a new image)
kardinal create bundle my-app \
  --image ghcr.io/myorg/my-app:1.2.3

# Watch promotion
kardinal get pipelines
```

### Step 7: Remove Kargo resources

Once you have validated end-to-end promotion with kardinal, remove the Kargo resources:

```bash
kubectl delete warehouse,stage -n kargo-demo --all
helm uninstall kargo -n kargo
```

---

## Feature parity reference

| Feature | Kargo | kardinal |
|---|---|---|
| Image watching | Warehouse `image` subscription | `Subscription` CRD with `type: image` |
| Git watching | Warehouse `git` subscription | `Subscription` CRD with `type: git` |
| Auto-promotion | `promotionTemplate` | `approvalMode: auto` |
| Manual approval | `Stage` with `promotionMechanisms.gitUpdateMechanisms` | `approvalMode: pr-review` |
| Stage sequencing | `requestedFreight.sources.stages` | `dependsOn` |
| Parallel stages (fan-out) | Multiple Stages with same upstream | Multiple environments with same `dependsOn` |
| Argo Rollouts | `argoRollouts` promotion mechanism | `health.type: argoRollouts` |
| Metrics gates | `AnalysisTemplate` + `AnalysisRun` | `MetricCheck` CRD + `PolicyGate` CEL |
| Time-based gates | Not built-in (requires AnalysisTemplate) | `PolicyGate` with `schedule.isWeekend` |
| Pause/freeze | Manual Stage freeze | `kardinal pause my-app` |
| Rollback | Manual re-promotion of older Freight | `kardinal rollback my-app --env prod` |
| Evidence / audit | Promotion annotations | PR body with structured evidence + `kardinal history` |
| DAG visualization | Kargo UI (separate install) | Built-in React UI (embedded in controller) |
| Multi-cluster | `ClusterStage` + RBAC | `Pipeline` environments with `kubeconfig` Secret |

---

## Common differences to be aware of

**Bundle supersession:** When a new Bundle is created while an older one is still Promoting, kardinal supersedes the older Bundle (marks it `Superseded`). Kargo allows multiple in-flight Freights simultaneously. To maintain Kargo-like behavior, set `spec.maxConcurrentBundles: 2` on the Pipeline (near-term feature).

**Namespace model:** Kargo Projects map to Kubernetes Namespaces in both systems. In kardinal, the Pipeline and Bundles live in the same namespace; Subscriptions can be in any namespace.

**GitOps repo structure:** kardinal's `kustomize` update strategy modifies `kustomization.yaml` files exactly like Kargo's git update mechanism. The `rendered-manifests` strategy (committing rendered YAML) has no direct Kargo equivalent.

**Policy gates:** Kargo's AnalysisTemplates run inline during promotion. kardinal's PolicyGates are separate CRDs that evaluate independently and are wired into the Graph. This means gates are cluster-reusable and visible to all pipelines that reference the same PolicyGate namespace.
