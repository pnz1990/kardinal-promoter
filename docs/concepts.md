# Core Concepts

## Bundle

A Bundle is an immutable, versioned snapshot of what to deploy. It contains container image references (tag and digest), optionally a Helm chart version or Git commit SHA, and build provenance (who built it, what commit, which CI run).

Bundles are created by your CI pipeline after building and pushing an image. All creation paths produce the same CRD in etcd:

```bash
# From CI via webhook
curl -X POST https://kardinal.example.com/api/v1/bundles \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"pipeline":"my-app","artifacts":{"images":[{"reference":"ghcr.io/myorg/my-app:1.29.0","digest":"sha256:abc..."}]},"provenance":{"commitSHA":"abc123","ciRunURL":"https://...","author":"engineer"}}'

# From CLI
kardinal create bundle my-app --image ghcr.io/myorg/my-app:1.29.0

# From kubectl
kubectl apply -f bundle.yaml
```

### Bundle phases

| Phase | Meaning |
|---|---|
| Available | Discovered, not yet promoted to any environment |
| Promoting | Actively being promoted through the pipeline |
| Verified | Successfully promoted to all target environments |
| Failed | A promotion step or health check failed |
| Superseded | Replaced by a newer Bundle |

### Bundle intent

The `spec.intent` field declares how far the Bundle should be promoted:

```yaml
spec:
  intent:
    target: prod         # promote through all environments up to and including prod (default)
```

```yaml
spec:
  intent:
    target: staging      # stop after staging, do not proceed to prod
```

```yaml
spec:
  intent:
    skip: [staging]      # skip staging (requires SkipPermission PolicyGate, see PolicyGates)
```

## Pipeline

A Pipeline defines the promotion path for one application: which Git repo contains the manifests, which environments exist, and what order they promote in.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
    provider: github
    secretRef: { name: github-token }
  environments:
    - name: dev
      path: environments/dev
      approval: auto
    - name: staging
      path: environments/staging
      approval: auto
    - name: prod
      path: environments/prod
      approval: pr-review
```

Environments promote sequentially by default (dev, then staging, then prod). For parallel fan-out, use `dependsOn`:

```yaml
  environments:
    - name: staging
    - name: prod-us
      dependsOn: [staging]
      approval: pr-review
    - name: prod-eu
      dependsOn: [staging]
      approval: pr-review
```

Both prod regions promote in parallel after staging is verified.

### How it works under the hood

When a Bundle is created, the kardinal-controller generates a [kro Graph](https://github.com/ellistarn/kro/tree/krocodile/experimental) from the Pipeline CRD. The Graph controller executes the DAG, creating PromotionStep and PolicyGate CRs in dependency order. You do not need to know about Graphs to use kardinal-promoter. The Pipeline CRD is the interface.

### Approval modes

| Mode | Behavior |
|---|---|
| `auto` | Manifests are pushed directly to the target branch. No PR. Promotion proceeds automatically when the upstream environment is verified. |
| `pr-review` | A PR is opened in the GitOps repo with promotion evidence (artifact provenance, upstream metrics, policy gate compliance). A human reviews and merges. |

## PromotionStep

A PromotionStep represents one environment promotion for one Bundle. You do not create these directly. The Graph controller creates them as nodes in the promotion DAG.

Each PromotionStep tracks:
- Which environment it targets
- Which Bundle it promotes
- The current state (Pending, Promoting, WaitingForMerge, HealthChecking, Verified, Failed)
- The PR URL (for pr-review environments)
- Promotion evidence (metrics, gate results, approver, timing)

Use `kardinal get steps <pipeline>` to see all active PromotionSteps.

## PolicyGate

A PolicyGate is a policy check that blocks a promotion until its CEL expression evaluates to true. PolicyGates are nodes in the promotion DAG, visible in the UI and inspectable via CLI.

```yaml
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
```

### How gates are applied

- **Org-level gates** (namespace `platform-policies`) are injected into every Pipeline that targets the matching environment. Teams cannot remove them.
- **Team-level gates** (team namespace) are added alongside org gates. Teams can add their own restrictions.
- The `kardinal.io/applies-to` label determines which environments the gate blocks. Comma-separated for multiple: `prod-eu,prod-us`.

### CEL context

PolicyGate expressions are evaluated against a context that includes:

| Attribute | Type | Example |
|---|---|---|
| `bundle.version` | string | "1.29.0" |
| `bundle.labels.*` | map | bundle.labels.hotfix == true |
| `bundle.provenance.author` | string | "dependabot[bot]" |
| `bundle.provenance.commitSHA` | string | "abc123" |
| `bundle.intent.target` | string | "prod" |
| `schedule.isWeekend` | bool | false |
| `schedule.hour` | int | 14 |
| `schedule.dayOfWeek` | string | "Tuesday" |
| `environment.name` | string | "prod" |
| `environment.approval` | string | "pr-review" |

Additional attributes are added in later phases (metrics, upstream soak time, delegation status).

### Inspecting gates

```bash
# See which gates are blocking a promotion
kardinal explain my-app --env prod

# Output:
# PROMOTION: my-app / prod
#   Bundle: v1.29.0
#
# POLICY GATES:
#   no-weekend-deploys  [org]   PASS   schedule.isWeekend = false
#   staging-soak        [org]   FAIL   bundle.upstreamSoakMinutes = 12 (threshold: >= 30)
#                                      ETA: ~18 minutes (based on staging verifiedAt)
#
# RESULT: BLOCKED by staging-soak
```

### Skip permissions

If a Bundle's `intent.skip` lists an environment that has org-level gates, the skip is denied unless a SkipPermission PolicyGate exists and permits it:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: allow-staging-skip-for-hotfix
  namespace: platform-policies
  labels:
    kardinal.io/type: skip-permission
    kardinal.io/applies-to: staging
spec:
  expression: "bundle.labels.hotfix == true"
  message: "Hotfix bundles may skip staging"
```

## Health Verification

After a promotion is applied (manifests written to Git), kardinal-promoter verifies that the target environment is healthy. Health adapters are pluggable and auto-detected.

| Adapter | What it checks | When to use |
|---|---|---|
| `resource` (default) | Deployment Available condition | Clusters without a GitOps tool |
| `argocd` | Argo CD Application health + sync status | Argo CD users (auto-detected) |
| `flux` | Flux Kustomization Ready condition | Flux users (auto-detected) |
| `argoRollouts` | Argo Rollouts Rollout phase | Canary/blue-green deployments (Phase 2) |
| `flagger` | Flagger Canary phase | Canary deployments (Phase 2) |

Most teams do not need to configure health adapters. If Argo CD is installed, the controller detects it and uses the `argocd` adapter automatically. Same for Flux.

For multi-cluster deployments where the workload is in a different cluster, add a `cluster` field referencing a kubeconfig Secret:

```yaml
health:
  type: argocd
  argocd:
    name: my-app-prod-us
  cluster: prod-us-cluster    # kubeconfig Secret name
```

## Subscription (Phase 3)

A Subscription watches an OCI registry for new image tags and auto-creates Bundles. This is an alternative to the CI webhook for teams that want fully passive promotion triggers.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: my-app-watch
spec:
  pipeline: my-app
  source:
    type: image
    image:
      repository: ghcr.io/myorg/my-app
      constraint: ">=1.0.0"
      interval: 5m
```

When a new tag matching the constraint is discovered, a Bundle is created automatically.
