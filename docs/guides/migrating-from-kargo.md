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
| `VerifiedIn` / approval required | `approval: pr-review` on environment | PR approval required before HealthChecking |
| `AnalysisTemplate` | `MetricCheck` CRD | A Prometheus, Datadog, CloudWatch, New Relic or JSON web query with a pass/fail threshold; `perPromotion: true` with `{{ bundle.version }}` replaces AnalysisRun arguments ([Metric Checks](../metric-checks.md)) |
| Stage that updates an Argo CD Application in another cluster | Environment with `health.type: argocd` on that Application in the hub | Multi-cluster through an Argo CD or Flux hub, or `health.kubeconfigSecretRef` to read the other cluster directly (inline credentials only) |
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
    url: https://github.com/myorg/gitops-repo.git
    branch: main
    secretRef:
      name: github-token
  environments:
    - name: test
      path: environments/test    # a directory on spec.git.branch (see the note below)
      approval: auto
      update:
        strategy: kustomize
    - name: prod
      path: environments/prod
      approval: pr-review        # requires PR merge (Kargo: Stage with approval)
      update:
        strategy: kustomize
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
    tagFilter: '^1\.\d+\.\d+$'   # 1.x.y semantic versions; the highest is promoted
    interval: 2m
```

kardinal writes every environment to a directory on one branch. Kargo pipelines that
keep one branch per environment (`env/test`, `env/prod`) need their manifests moved to
`environments/<name>/` on the base branch first: `spec.git.layout: branch` is accepted by
the API but not implemented, and a promotion with it fails at `git-clone`.

---

## Migration steps

### Step 1: Map your Stages to Pipeline environments

Kargo Stages are individual resources; kardinal collapses them into a single Pipeline.

For each Kargo Stage:
1. Add an entry to `spec.environments[]` in the Pipeline
2. Set `approval` from the Stage's approval configuration:
   - Auto-promotion → `approval: auto`
   - Manual approval → `approval: pr-review`
3. Translate `sources.stages: [upstream]` → `dependsOn: [upstream]`

### Step 2: Convert Warehouses to Subscriptions

Kargo Warehouses poll container registries. kardinal's equivalent is the `Subscription` CRD:

```bash
# For each Warehouse, create a Subscription:
kubectl get warehouse my-app -n kargo-demo -o yaml
# Translate repoURL → spec.image.registry (image), spec.git.repoURL, or spec.helm.repoURL + chart
# Translate semverConstraint → spec.image.semverConstraint (same syntax)
# Translate allowTags / ignoreTags / discoveryLimit as they are; allowTagsRegexes → tagFilter
# Translate imageSelectionStrategy → spec.image.strategy (SemVer, Lexical, NewestBuild; Digest is a
#   tagFilter matching one tag)
# Translate includePaths → spec.git.pathGlob
# Registry and repository credentials → spec.<type>.secretRef (a Secret in the Subscription's namespace)
```

A Warehouse with several subscriptions becomes one Subscription per source. Kargo's
webhook receivers map to `spec.webhook` and
[`/webhook/subscriptions/...`](../subscription-webhooks.md). See
[Subscription](../subscription.md).

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
spec:
  provider: prometheus
  query: |
    sum(rate(http_requests_total{status=~"2.."}[5m])) /
    sum(rate(http_requests_total[5m]))
  prometheusURL: http://prometheus.monitoring.svc:9090
  threshold:
    operator: gte   # one of lt, gt, lte, gte, eq, ne
    value: 0.95
  interval: 1m
```

An AnalysisTemplate that takes the Freight's version as an argument becomes a per-promotion
MetricCheck: set `perPromotion: true` and write `{{ bundle.version }}` (or
`{{ environment.name }}`, see [Per-promotion analysis](../metric-checks.md#per-promotion-analysis))
in the query. Datadog, CloudWatch, New Relic and `web` providers take their credentials from
Secret refs.

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
  # metrics.<MetricCheck name>.result is "Pass" when the threshold holds;
  # .value is the raw value as a string (use double(...) to compare it).
  expression: 'metrics["success-rate"].result == "Pass"'
  message: "Success rate below 95%"
  recheckInterval: 1m
```

### Step 6: Install kardinal and verify

```bash
# Install kro with the Graph feature gate (from a kardinal-promoter checkout)
bash hack/install-kro.sh

# Create the SCM token secret and install kardinal
kubectl create namespace kardinal-system
kubectl create secret generic github-token \
  --namespace kardinal-system \
  --from-literal=token=$GITHUB_PAT
helm install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --namespace kardinal-system \
  --create-namespace \
  --set github.secretRef.name=github-token

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
| Auto-promotion | ProjectConfig `promotionPolicies[].autoPromotionEnabled` | `approval: auto` |
| Manual approval | Promote by hand (any Stage without auto-promotion) | `approval: pr-review` |
| Stage sequencing | `requestedFreight.sources.stages` | `dependsOn` |
| Parallel stages (fan-out) | Multiple Stages with same upstream | Multiple environments with same `dependsOn` |
| Argo Rollouts | Verification with Argo Rollouts AnalysisTemplates | `health.type: argoRollouts` |
| Metrics gates | `AnalysisTemplate` + `AnalysisRun` | `MetricCheck` CRD + `PolicyGate` CEL |
| Time-based gates | Promotion windows (Kargo Enterprise, v1.12+) | `PolicyGate` with `schedule.isWeekend` |
| Pause/freeze | Turn off auto-promotion; freeze windows in Kargo Enterprise (v1.12+) | `kardinal pause my-app` |
| Rollback | Manual re-promotion of older Freight (pins the Stage since v1.11); auto-rollback in Kargo Enterprise (beta) | `kardinal rollback my-app --env prod`, or `onHealthFailure: rollback` |
| Evidence / audit | Promotion objects and Kubernetes Events; `record-audit-event` in Kargo Enterprise (v1.12) | PR body with structured evidence, `AuditEvent` CRD, `kardinal history` |
| DAG visualization | Kargo UI | Built-in React UI (embedded in the controller) |
| Notifications | Slack, email and HTTP notifications in Kargo Enterprise | `NotificationHook` CRD: Slack, Microsoft Teams, JSON or a templated body ([Notifications](../notifications.md)) |
| REST API | Kargo API server with API tokens | UI and Bundle API described by OpenAPI (`GET /api/v1/openapi.json`); scripts use ServiceAccount tokens ([REST API](../reference/rest-api.md)) |
| Delivery metrics | Kargo Prometheus metrics (v1.12) | DORA metrics (deployment frequency, lead time, change failure rate, time to restore) in `Pipeline.status.deploymentMetrics`, `kardinal metrics` and the UI |
| Promotion steps | A Stage's `promotionTemplate` composes built-in steps and PromotionTasks | Fixed sequence per environment, chosen by the Bundle type, `update.strategy` and `approval`; no custom steps |
| Multi-cluster | Stages that update each cluster's Argo CD Application | Argo CD or Flux hub: `health.type: argocd` or `flux` reads each Application or Kustomization in the hub, or `health.kubeconfigSecretRef` reads a remote cluster through a kubeconfig Secret |

---

## Common differences to be aware of

**Bundle supersession:** When a new Bundle of the same type is created while an older one is still Promoting, kardinal supersedes the older Bundle (marks it `Superseded`). Kargo allows several Freight in flight at once. kardinal has no setting to turn supersession off; `spec.maxConcurrentPromotions` only caps how many Bundles promote at the same time.

**Namespace model:** Kargo Projects map to Kubernetes Namespaces in both systems. In kardinal, the Pipeline, its Bundles and the Subscriptions that feed it live in the same namespace: a Subscription creates Bundles only in its own namespace, for a Pipeline there (a different `spec.namespace` sets the Subscription to phase `Error`).

**GitOps repo structure:** kardinal's `kustomize` update strategy edits `kustomization.yaml` the way Kargo's `kustomize-set-image` step does. Promoting rendered manifests (`layout: branch`) is not implemented yet ([#1271](https://github.com/pnz1990/kardinal-promoter/issues/1271)); Kargo does it with its `kustomize-build` and `helm-template` steps.

**Policy gates:** Kargo's AnalysisTemplates run as verification after a promotion. kardinal's PolicyGates are separate CRDs that evaluate independently and are wired into the Graph. This means gates are cluster-reusable and visible to all pipelines that reference the same PolicyGate namespace.
