# Advanced Patterns

This guide covers production patterns for kardinal-promoter that go beyond the basic
quickstart. Each pattern addresses a real-world scenario encountered at scale.

## Repository Strategy: Monorepo vs Multi-Repo

The repository strategy you choose affects Pipeline configuration and team autonomy.
kardinal-promoter supports both.

### Monorepo

All GitOps manifests for all applications live in one repository. Each application is
a directory; each environment is a subdirectory.

```
gitops-repo/
  apps/
    my-app/
      environments/
        dev/     kustomization.yaml
        staging/ kustomization.yaml
        prod/    kustomization.yaml
    payment-service/
      environments/
        dev/
        prod/
```

**Pipeline for a monorepo app:**

```yaml
spec:
  git:
    url: https://github.com/myorg/gitops-repo
    layout: directory
  environments:
    - name: dev
      path: apps/my-app/environments/dev
    - name: prod
      path: apps/my-app/environments/prod
      approval: pr-review
```

**Advantages:**
- Atomic cross-application changes (update a shared ConfigMap and all consumers in one commit)
- Centralized audit trail
- Single set of GitHub token credentials

**Challenges:**
- RBAC is coarse: one token has write access to all applications
- Noisy PR history (all apps in one repo)
- Git clone size grows with repo size (mitigated by shallow clone)

**kardinal-promoter behavior in monorepo:** The `git-clone` step uses sparse checkout
to fetch only the `path` directory. Git history is scoped to the target path in commit
messages. PRs target the specific path, so CODEOWNERS can still enforce per-app review.

### Multi-Repo

Each application has its own GitOps repository. The Pipeline's `git.url` points to
the application-specific repo.

```yaml
spec:
  git:
    url: https://github.com/myorg/my-app-gitops   # per-app repo
    layout: directory
```

**Advantages:**
- Strong isolation: one team cannot accidentally break another team's config
- Fine-grained RBAC (per-repo GitHub token)
- Independent Git history and branch protection per application

**Challenges:**
- Credential management: one GitHub token Secret per Pipeline namespace
- More complex ApplicationSet configuration (one generator per repo or a list of repos)

**Recommended:** Use multi-repo for teams with 5+ applications or strict security
requirements. Use monorepo for small teams or when cross-application atomicity matters.

## Multi-Tenant Self-Service via ApplicationSet

At scale, a platform team cannot manually create a Pipeline CRD for every new service.
The self-service pattern uses Argo CD ApplicationSets to provision Pipelines automatically
when a developer creates a new service folder.

### Repository structure

```
platform-repo/
  teams/
    payment-service/
      pipeline-values.yaml     # team-specific config (image, envs, approval mode)
    checkout-service/
      pipeline-values.yaml
```

### Root ApplicationSet (platform team applies once)

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: team-pipelines
  namespace: argocd
spec:
  generators:
    - git:
        repoURL: https://github.com/myorg/platform-repo
        revision: main
        directories:
          - path: teams/*         # one entry per team folder
  template:
    metadata:
      name: "{{path.basename}}-pipeline"
    spec:
      project: default
      source:
        repoURL: https://github.com/myorg/platform-repo
        targetRevision: main
        path: "teams/{{path.basename}}"
        helm:
          valueFiles:
            - pipeline-values.yaml
      destination:
        server: https://kubernetes.default.svc
        namespace: "{{path.basename}}"
      syncPolicy:
        automated: {}
        syncOptions:
          - CreateNamespace=true
```

### Pipeline Helm template (platform team owns)

The ApplicationSet renders a Pipeline CRD for each team:

```yaml
# chart/templates/pipeline.yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: {{ .Values.appName | default .Release.Namespace }}
  namespace: {{ .Release.Namespace }}
spec:
  git:
    url: {{ .Values.gitRepo }}
    provider: github
    secretRef:
      name: github-token
  environments:
  {{- range .Values.environments }}
    - name: {{ .name }}
      path: environments/{{ .name }}
      approval: {{ .approval | default "auto" }}
  {{- end }}
```

```yaml
# teams/payment-service/pipeline-values.yaml
appName: payment-service
gitRepo: https://github.com/myorg/payment-service-gitops
environments:
  - name: dev
  - name: staging
  - name: prod
    approval: pr-review
```

### How org PolicyGates apply to new teams

Because org-level PolicyGates in `platform-policies` are automatically injected into
every Pipeline targeting matching environments, the new team's Pipeline inherits
production controls with zero configuration:

```
New service Pipeline created → controller injects no-weekend-deploys gate → prod is gated
```

Teams cannot remove org gates. They can add their own team-level gates in their namespace.

### RBAC isolation

Each team's namespace should have RBAC that prevents cross-namespace access:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: team-pipeline-access
  namespace: payment-service
rules:
  - apiGroups: ["kardinal.io"]
    resources: ["bundles", "pipelines"]
    verbs: ["get", "list", "watch", "create"]
  - apiGroups: ["kardinal.io"]
    resources: ["policygates"]
    verbs: ["get", "list", "watch", "create"]   # can create team gates, not org gates
```

Teams have no access to `platform-policies` namespace by default.

## Feature Branch / Ephemeral Environments

A common requirement is promoting a feature branch to a temporary environment for
integration testing before merging to main.

### Pattern: Short-lived Bundle with intent.target

The simplest approach is to create a Bundle with `intent.target: staging` from a
feature branch CI workflow. The Bundle promotes only up to staging, not to prod.

```yaml
# feature branch GitHub Actions
- name: Create feature Bundle
  uses: kardinal-dev/create-bundle-action@v1
  with:
    pipeline: my-app
    image: ghcr.io/myorg/my-app:feature-auth-${{ github.sha }}
    token: ${{ secrets.KARDINAL_TOKEN }}
    target: staging          # stop at staging, do not promote to prod
```

The Bundle is marked `Verified` when staging is healthy. It does not supersede the
main-branch Bundle in prod (different intent target).

### Pattern: Skip environment for hotfixes

For hotfixes that must skip staging and go directly to prod:

```yaml
# CI creates a Bundle with skip intent
{
  "pipeline": "my-app",
  "artifacts": { "images": [...] },
  "provenance": { "commitSHA": "...", "author": "engineer" },
  "intent": { "skip": ["staging"] },
  "labels": { "hotfix": "true" }
}
```

This requires a SkipPermission PolicyGate allowing hotfix Bundles to skip staging:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: allow-staging-skip-for-hotfix
  namespace: platform-policies
  labels:
    kardinal.io/scope: org
    kardinal.io/type: skip-permission
    kardinal.io/applies-to: staging
spec:
  expression: "bundle.labels.hotfix == true"
  message: "Hotfix bundles may skip staging"
```

Without this gate, the skip is denied and the Bundle remains in `SkipDenied` state.

### Pattern: Ephemeral Pipeline for feature environments

For teams that need a fully isolated environment per feature branch, create a separate
Pipeline per branch using the ApplicationSet pattern:

```yaml
# ApplicationSet that generates a Pipeline per open PR
generators:
  - pullRequest:
      github:
        owner: myorg
        repo: my-app
        tokenRef: { secretName: github-token, key: token }
      requeueAfterSeconds: 60
template:
  metadata:
    name: "my-app-pr-{{number}}"
  spec:
    source:
      path: ephemeral/pipeline-template
      helm:
        values: |
          envSuffix: pr-{{number}}
          targetBranch: {{head_sha}}
```

Each PR gets its own Pipeline (and namespace). When the PR closes, the ApplicationSet
removes the Pipeline and its Bundles are garbage-collected.

## Bundle Supersession

When a new Bundle is created while an existing Bundle is still promoting through the
same Pipeline, the older Bundle is superseded:

1. The old Bundle's in-progress PromotionSteps are cancelled.
2. The old Bundle's status is set to `Superseded`.
3. The old Bundle's Graph is deleted (and its PromotionStep CRs are cascade-deleted).
4. The new Bundle starts promoting from the beginning.

Open PRs from the superseded Bundle are **not automatically closed**. The controller
adds a comment to the old PR noting it has been superseded. The reviewer should close
the old PR manually.

```bash
# Check which Bundles were superseded
kardinal get bundles my-app
# BUNDLE          PHASE       ENV     AGE
# v1.29.0-feat    Superseded  uat     5m    (superseded by v1.30.0)
# v1.30.0         Promoting   prod    2m
```

**When supersession does not occur:** Config Bundles (`type: config`) and Image Bundles
(`type: image`) have separate supersession tracking. A new image Bundle does not
supersede an in-flight config Bundle, and vice versa.

## Webhook Responsiveness

kardinal-promoter detects PR merges via GitHub webhooks for fast response. Without
webhooks, the controller polls for PR status on reconcile.

### Setting up webhooks

In the GitHub repository settings:

```
Payload URL: https://kardinal.example.com/webhooks
Content type: application/json
Secret: <same as WEBHOOK_SECRET env var on controller>
Events: Pull requests (pull_request), Pushes (push)
```

With webhooks configured, the controller advances the promotion within seconds of
PR merge. Without webhooks, advancement happens within 30 seconds (next reconcile).

### Local development with webhooks

For local clusters or clusters behind firewalls, use a webhook forwarding service:

```bash
# Using smee.io
npm install --global smee-client
smee --url https://smee.io/your-channel-id \
     --target http://localhost:8081/webhooks
```

Or configure the Pipeline to use polling:

```yaml
spec:
  git:
    webhookMode: polling    # disable webhook, use periodic polling
    pollInterval: 30s       # default: 30s (GitHub rate limit: 5000 req/h)
```

The polling mode is less responsive but works in environments where inbound webhooks
are not possible.

## Namespace Sprawl Management

Each Pipeline creates PromotionStep and PolicyGate CRs in its own namespace. For
organizations with many Pipelines, this can create dozens of additional CRDs per
namespace.

### Recommendations

1. **Use team namespaces**: group related Pipelines in one namespace rather than one
   namespace per Pipeline. `kardinal-promoter` scopes Bundles by `kardinal.io/pipeline`
   label, not by namespace.

2. **Set historyLimit**: the default `historyLimit: 20` retains the last 20 Bundles.
   For high-frequency teams, reduce to `5` to limit CRD count.

3. **Monitor CRD count**: the controller exposes `kardinal_bundles_total{phase}`
   and `kardinal_steps_total{type}` Prometheus metrics. Alert if these exceed
   expected levels.

4. **Resource quotas**: set `ResourceQuota` on team namespaces to prevent unbounded
   Bundle creation.

## GitOps Tool Agnosticism

kardinal-promoter is not tied to Argo CD. If your cluster does not have a GitOps
tool installed, the `resource` health adapter checks Deployment readiness directly.

### Without any GitOps tool

```yaml
health:
  type: resource
  resource:
    kind: Deployment
    name: my-app
    namespace: prod
```

The controller verifies that the Deployment's `Available` condition is `True` after
pushing to Git. You are responsible for ensuring Git changes reach the cluster (e.g.,
via CI, Flux Receiver webhooks, or ArgoCD App-of-Apps).

### With Flux

```yaml
health:
  type: flux
  flux:
    name: my-app-prod
    namespace: flux-system
```

Auto-detected if Flux CRDs are installed. Waits for Kustomization `Ready=True`.

### Mixing GitOps tools across environments

```yaml
environments:
  - name: dev
    health:
      type: flux      # dev cluster uses Flux
  - name: prod
    health:
      type: argocd    # prod cluster uses Argo CD
      argocd: { name: my-app-prod }
```

Different environments can use different health adapters in the same Pipeline.

## Anti-Patterns to Avoid

### Pseudo-GitOps: mutating Argo CD targetRevision directly

Some tools shortcut promotion by patching `spec.source.targetRevision` on an Argo CD
Application CRD without writing to Git. This breaks GitOps: Git is no longer the
source of truth. Cluster state cannot be reconstructed from Git after a disaster.

kardinal-promoter never mutates GitOps tool CRDs directly. All promotions write to
Git first.

### Committing templated sources to rendered branches

Do not commit Kustomize `kustomization.yaml` files or Helm `values.yaml` files to a
rendered branch. Rendered branches must contain only plain Kubernetes YAML. Argo CD's
`Directory` source type does not process Kustomize or Helm — if template files are
present, Argo CD may fail to apply them or silently ignore them.

### Using `approval: auto` for production

`approval: auto` pushes directly to the target branch without a PR. This is appropriate
for dev and staging where speed matters, but not for prod. A human reviewer should
always merge the production PR to confirm:
- The rendered diff looks correct
- Policy gates have all passed
- The upstream environments are verified

### Not setting `historyLimit`

The default `historyLimit: 20` retains 20 Bundles per Pipeline. In active pipelines
with frequent deployments, this creates many PromotionStep CRDs. If you deploy
multiple times per day, set `historyLimit: 5`. The Git audit trail is permanent
regardless of this setting.
