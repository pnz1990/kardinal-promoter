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

### Pattern: Short-lived Bundle with intent.targetEnvironment

The simplest approach is to create a Bundle with `intent.targetEnvironment: staging` from a
feature branch CI workflow. The Bundle promotes only up to staging, not to prod.

The [create-bundle action](ci-integration.md#github-action) has no intent input, so
post the Bundle to the Bundle API directly:

```yaml
# feature branch GitHub Actions
- name: Create feature Bundle
  env:
    KARDINAL_TOKEN: ${{ secrets.KARDINAL_TOKEN }}
  run: |
    curl -fsS -X POST https://kardinal.example.com/api/v1/bundles \
      -H "Authorization: Bearer $KARDINAL_TOKEN" \
      -H "Content-Type: application/json" \
      -d '{
        "pipeline": "my-app",
        "type": "image",
        "images": [{"repository": "ghcr.io/myorg/my-app", "tag": "feature-auth-${{ github.sha }}"}],
        "intent": {"targetEnvironment": "staging"}
      }'
```

The Bundle is marked `Verified` when staging is healthy.

Supersession ignores the intent: a newer Bundle of the same type in the same Pipeline
supersedes every older Bundle that is still promoting. A feature Bundle created while a
main-branch Bundle is on its way to prod therefore stops that promotion, and the next
main-branch Bundle stops the feature Bundle. To keep them apart, give feature branches
their own Pipeline (for example `my-app-feature`, with only the test and staging
environments) and create the feature Bundles in that Pipeline.

### Pattern: Skip environment for hotfixes

For hotfixes that must skip staging and go directly to prod, create the Bundle with `spec.intent.skipEnvironments`:

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  generateName: my-app-hotfix-
  namespace: my-team
spec:
  type: image
  pipeline: my-app
  images:
    - repository: ghcr.io/myorg/my-app
      tag: hotfix-1.29.1
  provenance:
    commitSHA: "..."
    author: engineer
  intent:
    skipEnvironments: [staging]
```

If an org gate applies to staging, the platform team must allow the skip. It does so with a skip-permission gate in an org policy namespace:

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
  skipPermission: true
  expression: 'bundle.version.startsWith("hotfix-")'
  message: "Only hotfix bundles may skip staging"
```

With no such gate, the skip is denied: the Bundle goes to phase `Failed` with a `skip denied` reason. With the gate, prod waits until the controller has found the expression true for this Bundle; `bundle.version` is the tag of the first image. See [Skip Permissions](policy-gates.md#skip-permissions).

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

1. The old Bundle's status is set to `Superseded`, which is final.
2. Its unfinished PromotionSteps are failed. A PR one of them opened that is still open
   is closed with a comment noting it was superseded, and its head branch
   (`kardinal/<bundle>/<env>`) is deleted, so the closed PR cannot be merged later.
3. Its Graph, PromotionSteps and PolicyGates are kept as history. The Graph creates no
   new PromotionStep (for a Graph built before this behaviour, only a step created at the
   moment of supersession), and its PolicyGates are no longer evaluated: they keep the
   status they had when the Bundle was superseded.
4. A step created just before supersession that never started is failed with
   "superseded before this step started" and writes no AuditEvent.
5. The new Bundle starts promoting from the beginning.

Deleting the superseded Bundle deletes its Graph and everything the Graph created.

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

kardinal-promoter detects PR merges through SCM webhooks for fast response. Without
webhooks, the controller polls each open PR every 30 seconds.

### Setting up webhooks

The webhook endpoint is `/webhook/scm` on port 8083 of the `kardinal-promoter` Service. It
checks each event against the controller's webhook secret (an HMAC signature for GitHub and
Forgejo, a shared token for GitLab). The secret is set with `--webhook-secret` or
`KARDINAL_WEBHOOK_SECRET` (with the chart, `webhook.secretRef.name`). With no secret set,
the endpoint rejects every event and merges are detected by polling only.

In the GitHub repository settings:

```
Payload URL: https://<host>:8083/webhook/scm
Content type: application/json
Secret: <the controller's webhook secret>
Events: Pull requests (pull_request)
```

The `https://` URL needs controller TLS (`controller.tlsCertFile` and `controller.tlsKeyFile`)
or an Ingress that terminates TLS in front of port 8083; without either, the endpoint is plain
`http://`.

Only merged `pull_request` events advance a promotion. See SCM Providers for
[GitLab](scm-providers.md#webhook-configuration_1) and
[Forgejo](scm-providers.md#webhook-configuration_2).

With webhooks configured, the controller advances the promotion within seconds of
PR merge. Without webhooks, advancement happens at the next poll (within about 30 seconds).

### Local development with webhooks

For local clusters or clusters behind firewalls, use a webhook forwarding service:

```bash
# Using smee.io, with port 8083 forwarded to localhost:
#   kubectl port-forward -n kardinal-system svc/kardinal-promoter 8083:8083
npm install --global smee-client
smee --url https://smee.io/your-channel-id \
     --target http://localhost:8083/webhook/scm
```

Or skip webhooks: leave the webhook secret unset and the controller polls open PRs. There is
no Pipeline field for this; polling is always on.

## Namespace Sprawl Management

Each Pipeline creates PromotionStep and PolicyGate CRs in its own namespace. For
organizations with many Pipelines, this can create dozens of additional CRDs per
namespace.

### Recommendations

1. **Use team namespaces**: group related Pipelines in one namespace rather than one
   namespace per Pipeline. `kardinal-promoter` scopes Bundles by `kardinal.io/pipeline`
   label, not by namespace.

2. **Set historyLimit**: the default `historyLimit: 50` retains the last 50 finished Bundles.
   For high-frequency teams, reduce to `5` to limit CRD count.

3. **Monitor CRD count**: count the objects themselves, for example
   `kubectl get bundles,promotionsteps -A --no-headers | wc -l`, or a kube-state-metrics
   custom-resource metric. The controller's `kardinal_bundles_total{phase}` and
   `kardinal_steps_total` are counters of phase transitions and finished steps, not object
   counts: they only grow, and deleting Bundles does not lower them. Use their `rate()` to
   alert on unusual Bundle creation.

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

The controller verifies that the Deployment runs the Bundle images, has finished rolling
out and is `Available` after pushing to Git. You are responsible for ensuring Git changes reach the cluster (e.g.,
via CI, Flux Receiver webhooks, or ArgoCD App-of-Apps).

### With Flux

```yaml
health:
  type: flux
  flux:
    name: my-app-prod
    namespace: flux-system
```

Set `type: flux` explicitly (there is no auto-detection). Waits for Kustomization `Ready=True`
with `lastAppliedRevision` at the promoted commit.

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

The default `historyLimit: 50` retains 50 finished Bundles per Pipeline. In active pipelines
with frequent deployments, this creates many PromotionStep CRDs. If you deploy
multiple times per day, set `historyLimit: 5`. The Git audit trail is permanent
regardless of this setting.
