# Migrating from Kargo

This guide walks through migrating a Kargo-managed delivery pipeline to kardinal-promoter, step by step. It assumes familiarity with Kargo concepts.

---

## Concept mapping

[Coming from Kargo](../coming-from-kargo.md) maps each Kargo concept (Warehouse, Freight, Stage,
verification, promotion steps) to its kardinal counterpart, and lists the Kargo Enterprise
features that kardinal ships as open source.

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

Kargo pipelines that keep one rendered branch per environment (`env/test`, `env/prod`) map to
`layout: branch`: keep the overlays or charts on the base branch, and kardinal renders each
environment into its branch at promotion time ([Rendered Manifests](../rendered-manifests.md)).
A branch that already holds manifests kardinal did not write is drift for the first
promotion: set `render.onDrift: overwrite` for it, or start from an empty branch.

---

## Migration steps

### Step 1: Map your Stages to Pipeline environments

Kargo Stages are individual resources; kardinal collapses them into a single Pipeline.

For each Kargo Stage:
1. Add an entry to `spec.environments[]` in the Pipeline
2. Set `approval` from the Stage's approval configuration:
   - Auto-promotion → `approval: auto`
   - Manual approval → `approval: pr-review` (a reviewed pull request), or an approval gate
     with named approvers ([Require approvals](../approvals.md))
3. Translate `sources.stages: [upstream]` → `dependsOn: [upstream]`
4. Translate `requiredSoakTime` → a gate on `upstream.<env>.soakMinutes`
   ([Coming from Kargo](../coming-from-kargo.md#requiredsoaktime-to-soak-gates))

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
[`/webhook/subscriptions/...`](../subscription.md#webhooks). See
[Subscription](../subscription.md).

### Step 3: Remove Kargo `Promotion` objects (if any)

kardinal creates `PromotionStep` objects automatically via the Graph controller. You do not create them manually.

### Step 4: Keep your AnalysisTemplates

Name them in the environment's `verification`; kardinal runs each as an AnalysisRun after the
health check, with the version's `tag`, `image` and `environment` as args, and the environment is
Verified only when they all succeed ([Analysis](../analysis.md)):

```yaml
environments:
  - name: prod
    verification:
      analysisTemplates:
        - name: success-rate
```

Without Argo Rollouts, convert a Prometheus template to a MetricCheck and gate the next
environment on it:

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

Kargo AnalysisRun arguments map to CEL expressions in gates:

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

Install kro and kardinal-promoter first ([Install](../installation.md)), then:

```bash
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

## Common differences to be aware of

**Version supersession:** When a new version of the same type is created while an older one is still Promoting, kardinal supersedes the older version (marks it `Superseded`). Kargo allows several Freight in flight at once. kardinal has no setting to turn supersession off; `spec.maxConcurrentPromotions` only caps how many versions promote at the same time.

**Namespace model:** Kargo Projects map to Kubernetes Namespaces in both systems. In kardinal, the Pipeline, its versions and the Subscriptions that feed it live in the same namespace: a Subscription creates versions only in its own namespace, for a Pipeline there (a different `spec.namespace` sets the Subscription to phase `Error`).

**GitOps repo structure:** kardinal's `kustomize` update strategy edits `kustomization.yaml` the way Kargo's `kustomize-set-image` step does. Kargo's `kustomize-build` and `helm-template` steps map to `layout: branch`, which renders in a sandboxed Job per promotion (Kargo runs these steps in its controller) and commits plain YAML to the environment's branch ([Rendered Manifests](../rendered-manifests.md)).

**Policy gates:** Kargo's AnalysisTemplates run as verification after a promotion. kardinal's gates are PolicyGate objects that evaluate independently and are wired into the Graph. This means gates are cluster-reusable and visible to all pipelines that reference the same gate namespace.
