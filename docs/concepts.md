# Core Concepts

## Bundle

A Bundle is an immutable, versioned snapshot of what to deploy. It contains container image references (tag and digest), optionally a Helm chart version or Git commit SHA, and build provenance (who built it, what commit, which CI run).

Bundles are created by your CI pipeline after building and pushing an image. All creation paths produce the same CRD in etcd:

```bash
# From CI via webhook
curl -X POST https://kardinal.example.com/api/v1/bundles \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"pipeline":"my-app","type":"image","images":[{"repository":"ghcr.io/myorg/my-app","tag":"1.29.0","digest":"sha256:abc..."}],"provenance":{"commitSHA":"abc123","ciRunURL":"https://...","author":"engineer"}}'

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

### Bundle supersession

When a new Bundle is created while a previous Bundle is still promoting through the
same Pipeline, the older Bundle is **superseded**:

- The older Bundle's status transitions to `Superseded`
- Its in-progress PromotionSteps are cancelled
- Its kro Graph is deleted (PromotionStep CRs cascade-deleted)
- Open PRs are commented on but not automatically closed

Supersession is tracked independently by Bundle type. A new `image` Bundle does not
supersede an in-flight `config` Bundle, and vice versa.

```bash
kardinal get bundles my-app
# BUNDLE     PHASE       ENV      AGE
# v1.29.0    Superseded  uat      10m    (superseded by v1.30.0)
# v1.30.0    Promoting   prod     3m
```

### Bundle types

- **`image`** (default): References container image tags. The promotion updates image references in manifests using `kustomize-set-image` or `helm-set-image`.
- **`config`**: References a Git commit SHA from a configuration repository. The promotion merges that commit's changes into each environment directory. This supports promoting configuration changes (resource limits, env vars, feature flags) independently from image changes.

Image Bundle:
```yaml
spec:
  type: image
  pipeline: my-app
  images:
    - repository: ghcr.io/myorg/my-app
      tag: "1.29.0"
      digest: sha256:a1b2c3d4...
```

Config Bundle:
```yaml
spec:
  type: config
  pipeline: my-app
  configRef:
    gitRepo: https://github.com/myorg/app-config
    commitSHA: "abc123def456"
```

Both types go through the same Pipeline, same PolicyGates, and same PR flow.

### Bundle intent

The `spec.intent` field declares how far the Bundle should be promoted:

```yaml
spec:
  intent:
    targetEnvironment: prod   # promote through all environments up to and including prod (default)
```

```yaml
spec:
  intent:
    targetEnvironment: staging  # stop after staging, do not proceed to prod
```

```yaml
spec:
  intent:
    skipEnvironments: [staging]  # skip staging (requires SkipPermission PolicyGate, see PolicyGates)
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

### Promotion steps

By default, each environment uses a standard promotion sequence (clone, update image, commit, push/PR, health check). For custom workflows, you can define explicit steps:

```yaml
  environments:
    - name: prod
      approval: pr-review
      steps:
        - uses: git-clone
        - uses: kustomize-set-image
        - uses: run-tests                  # custom step (HTTP webhook)
          config:
            url: https://test-runner.internal/validate
            timeout: 5m
        - uses: git-commit
        - uses: git-push
        - uses: open-pr
        - uses: wait-for-merge
        - uses: health-check
```

Built-in steps: `git-clone`, `kustomize-set-image`, `helm-set-image`, `kustomize-build`, `config-merge`, `git-commit`, `git-push`, `open-pr`, `wait-for-merge`, `health-check`. Custom steps call an HTTP endpoint that returns pass/fail.

When `steps` is omitted, the default sequence is inferred from `update.strategy` and `approval`.

### Distributed mode and sharding

For multi-cluster deployments where some clusters are behind firewalls, environments can be assigned to a `shard`. A kardinal-agent running in the target cluster reconciles PromotionSteps for that shard.

```yaml
  environments:
    - name: prod-eu
      shard: eu-cluster              # handled by the agent in the EU cluster
      dependsOn: [staging]
      approval: pr-review
```

In standalone mode (single binary), the shard field is ignored and all PromotionSteps are reconciled locally.

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

## PromotionTemplate

A `PromotionTemplate` is a reusable named step sequence that multiple Pipeline environments can reference. It solves the repetition problem: without templates, every environment in every Pipeline must repeat the full step list (git-clone, kustomize-set-image, git-commit, open-pr, wait-for-merge, health-check). For organizations with many pipelines, a step change (for example, adding a webhook notification after `git-commit`) requires editing every Pipeline YAML.

**Create a template:**

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PromotionTemplate
metadata:
  name: standard-with-notify
  namespace: kardinal-system
spec:
  description: "Standard promotion with post-commit Slack notification"
  steps:
    - uses: git-clone
    - uses: kustomize-set-image
    - uses: git-commit
    - uses: notify-slack
      webhook:
        url: https://hooks.slack.com/services/T00/B00/XXX
    - uses: open-pr
    - uses: wait-for-merge
    - uses: health-check
```

**Reference from a Pipeline environment:**

```yaml
spec:
  environments:
    - name: test
      promotionTemplate:
        name: standard-with-notify
        namespace: kardinal-system   # optional — defaults to Pipeline namespace
    - name: uat
      promotionTemplate:
        name: standard-with-notify
    - name: prod
      approval: pr-review
      promotionTemplate:
        name: standard-with-notify
```

**Resolution rules:**

1. When `promotionTemplate` is set and `steps` is **empty**: the template's steps are inlined into the environment at translation time.
2. When `promotionTemplate` is set and `steps` is **also set**: the local `steps` take precedence (local override wins). The template is validated (must exist) but its steps are not used.
3. When `promotionTemplate` is **not set**: existing behavior — `steps` or the default sequence.

**Important:** Template inlining happens at Graph build time (inside the translator), before the promotion DAG is created. The `PromotionTemplate` CR is read once per Bundle creation; there is no runtime dependency. Modifying a `PromotionTemplate` after a Graph is created does not affect in-flight promotions.

**When to use templates:**
- Shared notification or audit steps across many Pipelines
- Organization-mandated steps (security scans, compliance webhooks) applied uniformly
- Platform teams offering pre-tested step sequences that application teams reference

**When NOT to use templates:**
- When a single Pipeline has a custom step list that no other Pipeline will use — prefer `steps` directly.
- When the step list is trivial (2–3 steps) — templates add indirection without benefit.

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

Additional attributes are available including metrics results (`metrics.*`), upstream soak time (`bundle.upstreamSoakMinutes`), and previously deployed version (`previousBundle.version`). See the [CEL context reference](policy-gates.md#cel-context) for the full list.

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

After a promotion is applied (manifests written to Git), kardinal-promoter verifies that the target environment is healthy. The `health.type` field is required in every Pipeline environment. Health adapters are pluggable.

| Adapter | What it checks | When to use |
|---|---|---|
| `resource` | Deployment Available condition | Clusters without a GitOps tool |
| `argocd` | Argo CD Application health + sync status | Argo CD users |
| `flux` | Flux Kustomization Ready condition | Flux users |
| `argoRollouts` | Argo Rollouts Rollout phase | Canary/blue-green deployments |
| `flagger` | Flagger Canary phase | Canary deployments |

`health.type` must be set explicitly in each Pipeline environment — there is no auto-detection. This prevents misconfigurations from being silently masked.

For multi-cluster deployments where the workload is in a different cluster, add a `cluster` field referencing a kubeconfig Secret:

```yaml
health:
  type: argocd
  argocd:
    name: my-app-prod-us
  cluster: prod-us-cluster    # kubeconfig Secret name
```

## Subscription

A Subscription watches external sources and auto-creates Bundles. This is an alternative to the CI webhook for teams that want fully passive promotion triggers.

**Image Subscription** (watches OCI registries for new image tags):

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: my-app-image-watch
spec:
  type: image
  pipeline: my-app
  image:
    registry: ghcr.io/myorg/my-app
    tagFilter: "^sha-"
    interval: 5m
```

**Git Subscription** (watches a Git repository for config changes, creates config Bundles):

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Subscription
metadata:
  name: my-app-config-watch
spec:
  type: git
  pipeline: my-app
  git:
    repoURL: https://github.com/myorg/app-config
    branch: main
    pathGlob: "configs/my-app/**"
    interval: 5m
```

When a new image tag or Git commit is discovered, a Bundle of the appropriate type (`image` or `config`) is created automatically.

## Rendered Manifests

In the **rendered manifests** pattern, Kustomize (or Helm) templates are executed at
promotion time and the rendered plain YAML is committed to Git. Argo CD and Flux sync
from the rendered output, not from the source templates.

This is the standard pattern for large Argo CD deployments because:
- PR reviewers see exact YAML diffs, not template changes
- Argo CD never runs `kustomize build` on every reconciliation cycle (significant performance gain at scale)
- CODEOWNERS rules can be placed on individual rendered YAML files in the environment branch

Enable this pattern by setting `renderManifests: true` on an environment and using
`layout: branch` in `spec.git`:

```yaml
spec:
  git:
    layout: branch
    sourceBranch: main     # DRY templates live here
    branchPrefix: env/     # rendered manifests go to env/dev, env/staging, env/prod
  environments:
    - name: prod
      approval: pr-review
      renderManifests: true
```

See [Rendered Manifests](rendered-manifests.md) for a complete guide including
Argo CD ApplicationSet configuration and CODEOWNERS integration.

## Advanced Patterns

### Multi-tenant self-service

Use Argo CD ApplicationSets to auto-provision a Pipeline CRD for each new team
service. A developer commits a folder to a platform repository and receives a
complete promotion pipeline without platform team intervention. Org-level PolicyGates
are inherited automatically.

### Feature branch and ephemeral environments

Use `intent.targetEnvironment: staging` to create Bundles that stop at staging, not prod.
Use `intent.skipEnvironments` with SkipPermission PolicyGates for hotfixes. Use ApplicationSet
pull-request generators for fully isolated ephemeral Pipelines per PR.

### Repository strategies

`layout: directory` (one branch, environments as directories) works well for small
teams and monorepos. `layout: branch` (environments as separate branches) works well
for multi-repo and rendered-manifest workflows.

See [Advanced Patterns](advanced-patterns.md) for detailed guidance on all of these.

## Key Anti-Patterns

### Pseudo-GitOps

Some tools shortcut promotion by patching `spec.source.targetRevision` on an Argo CD
Application directly, without writing to Git. This breaks the GitOps contract: Git is
no longer the source of truth. kardinal-promoter never mutates GitOps tool CRDs.
All promotions write to Git first.

### `approval: auto` for production environments

`approval: auto` pushes directly to the target branch without a PR. Use this only for
dev and staging environments. Production should always use `approval: pr-review` so a
human reviewer confirms the diff and gate compliance before the change lands.

### Missing `historyLimit`

The default `historyLimit: 20` retains 20 Bundles per Pipeline. In high-frequency
pipelines (multiple deployments per day), reduce this to `5`. The Git audit trail in
GitHub is permanent regardless — only the CRD state in etcd is bounded.

## Audit Log

kardinal-promoter writes an immutable `AuditEvent` CRD for each key promotion lifecycle transition:

| Action | When |
|---|---|
| `PromotionStarted` | A Bundle moves from Pending to Promoting |
| `PromotionSucceeded` | Health check passes and the step reaches Verified |
| `PromotionFailed` | The step reaches Failed state |
| `PromotionSuperseded` | A newer Bundle supersedes an in-flight promotion |

```bash
# List all audit events across namespaces
kubectl get auditevent --all-namespaces

# Filter by pipeline
kubectl get auditevent -l kardinal.io/pipeline=nginx-demo

# Filter by outcome
kubectl get auditevent -l kardinal.io/action=PromotionFailed
```

AuditEvents are immutable — they are written once at the transition and never updated. Use `kubectl get auditevent -o yaml` to inspect the full record including timestamp, bundle image, and message.
