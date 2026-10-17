# Concepts

kardinal moves a **version** of your application through a **pipeline** of **environments**,
and **gates** decide when the version may enter the next environment. Those four words are
all you need to start; the [glossary](reference/glossary.md) maps every other term to them.

```station-map
title: A version moving through a pipeline of three environments
caption: The red thread is the version. Test and uat verified it; its gates into prod passed; prod waits for its pull request to merge.
lead: {into: test, label: Your CI, note: creates a version}
stations:
  test: {state: verified, notes: [commit · health check], status: Verified}
  uat: {state: verified, notes: [commit · health check], status: Verified}
  prod: {state: waiting, notes: [pull request · merge, health check], status: Waiting for PR merge}
edges:
  - test -> uat
  - {from: uat, to: prod, gates: [no-weekend-deploys, uat-soak]}
thread: [test, uat, prod]
```

Your CI creates a version when it has built one. Each station on the line is an **environment**
of the **pipeline**, and the bars on the line into prod are its **gates**. A version enters an
environment once the environment before it has verified it and the environment's gates pass. kardinal then writes the version to the environment's directory
in your GitOps repository (directly, or through a pull request you review), waits for Argo CD,
Flux or Kubernetes to roll it out, and checks its health. A healthy environment is **Verified**,
and the version moves on. [Architecture](architecture.md#components) shows the components that
do this and what they talk to.

## Version

A **version** is one immutable snapshot of what to deploy: container images (tag and digest),
a Helm chart version or a commit of a configuration repository, together with where it came
from (author, commit, CI run). It is a Kubernetes object of kind `Bundle`.

Your CI creates a version after it builds and pushes an image. A **Subscription** can do it
instead: it watches a registry, a Git repository or a chart repository and creates a version for
each new artifact ([Subscriptions](subscription.md)). Every way of creating a version produces the
same object:

```bash
# From CI, through the API
curl -X POST https://kardinal.example.com/api/v1/bundles \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"pipeline":"my-app","type":"image","images":[{"repository":"ghcr.io/myorg/my-app","tag":"1.29.0","digest":"sha256:abc..."}],"provenance":{"commitSHA":"abc123","ciRunURL":"https://...","author":"engineer"}}'

# From the CLI
kardinal create bundle my-app --image ghcr.io/myorg/my-app:1.29.0

# With kubectl
kubectl apply -f bundle.yaml
```

A version cannot change: the API server refuses an update to its type, images, chart, config
commit, provenance, pipeline or intent (`spec.<field> is immutable: create a new Bundle`),
because gates and health checks were decided for that artifact. Labels and annotations stay
editable. To ship something else, create a new version. A newer version of the same type
replaces, or **supersedes**, an older one that has not finished; see
[supersession](architecture.md#bundle-supersession).

### Version types

- **`image`** (the default): container images. Promoting it updates the image references in each
  environment's manifests (kustomize `images:` or a Helm values path, depending on the
  environment's `update.strategy`, which says how kardinal edits its manifests). `POST /api/v1/bundles`
  and `kardinal create bundle` use it when `type` is omitted; a version applied with kubectl
  must set `type`.
- **`config`**: a commit of a configuration repository. Promoting it copies the environment's
  directory from that commit, so configuration changes (resource limits, environment variables,
  feature flags) move through the same pipeline independently from images.
- **`mixed`**: images and a config commit in one version, promoted together.
- **`chart`**: a Helm chart version (`spec.chart`), promoted with `update.strategy: helm`.

Image version:
```yaml
spec:
  type: image
  pipeline: my-app
  images:
    - repository: ghcr.io/myorg/my-app
      tag: "1.29.0"
      digest: sha256:a1b2c3d4...
```

Config version:
```yaml
spec:
  type: config
  pipeline: my-app
  configRef:
    gitRepo: https://github.com/myorg/app-config
    commitSHA: "abc123def456"
```

Every type goes through the same pipeline, the same gates and the same pull request flow.
For a config or mixed version, the `config-merge` step copies the environment's directory
(`environments/<name>` or `environments[].path`) from the `configRef` commit over the
same directory in the GitOps repository. Nothing outside that directory is copied, and files
deleted in the config commit are not deleted.

[`examples/config-promotion/`](https://github.com/pnz1990/kardinal-promoter/tree/main/examples/config-promotion)
has a Pipeline and a config version.

### How far a version goes

A version's **intent** (`spec.intent`) says how far it is promoted:

```yaml
spec:
  intent:
    targetEnvironment: prod   # promote through all environments up to and including prod (default)
```

```yaml
spec:
  intent:
    targetEnvironment: uat  # stop after uat, do not proceed to prod
```

```yaml
spec:
  intent:
    skipEnvironments: [uat]  # skip uat (if an org gate applies to uat, a skip-permission gate must allow it; see Skip permissions below)
```

The intent is set when the version is created and cannot be changed afterwards. To change the
target, create a new version (or use `kardinal promote`, which creates one).

### Version phases

| Phase | Meaning |
|---|---|
| Available | Created, not yet in any environment |
| Promoting | Moving through the pipeline |
| Verified | Verified in every environment it targets |
| Failed | A promotion or health check failed, or the Pipeline, the version or a gate is invalid: the version's `InvalidSpec` condition then gives the reason. It promotes again when the failed promotion is retried or the invalid object is fixed ([details](architecture.md#when-a-failed-bundle-promotes-again)) |
| Superseded | A newer version of the same type replaced it before it finished |
| Rejected | Rejected with `kardinal reject`: never promoted again, and rollback never picks it. See [Reject a version](reject.md) |

## Pipeline and environments

A **pipeline** is the promotion path of one application: the Git repository that holds its
manifests, its **environments**, and the order they promote in. Its Kubernetes kind is
`Pipeline`.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: my-app
spec:
  git:
    url: https://github.com/myorg/gitops-repo
    secretRef: { name: github-token }
  environments:
    - name: test
      path: environments/test
      approval: auto
    - name: uat
      path: environments/uat
      approval: auto
    - name: prod
      path: environments/prod
      approval: pr-review
```

Environments promote in order by default (test, then uat, then prod). For parallel fan-out, use
`dependsOn`:

```yaml
  environments:
    - name: uat
    - name: prod-us
      dependsOn: [uat]
      approval: pr-review
    - name: prod-eu
      dependsOn: [uat]
      approval: pr-review
```

Both prod regions promote in parallel after uat is verified.

Wave numbers (`wave`) and [fleets](pipeline-reference.md#fleets) promote many environments in
parallel; see [Design a pipeline](pipeline-reference.md).

### Promotion steps

The promotion of one version into one environment is a **promotion step** (kind
`PromotionStep`). kardinal creates them; you do not. On the UI's map, each
environment's step is a station. A step clones the repository, updates the manifests,
commits, pushes or opens a pull request, waits for the merge, and checks health. Its state is
one of Pending, Promoting, WaitingForMerge, HealthChecking, Verifying (the environment's
[post-deploy hooks](hooks.md) run), Verified, Failed, AbortedByAlarm, RollingBack or Superseded
(a newer version had already reached the environment; not a failure).
`kardinal get steps <pipeline>` lists them.

The sequence is fixed per environment: it follows from `update.strategy` and `approval`.
kardinal has no custom step engine; see
[Promotion steps](pipeline-reference.md#promotion-steps).

### Approval modes

An environment's `approval` decides how a version's change lands in Git:

| Mode | Behavior |
|---|---|
| `auto` | The change is pushed directly to the target branch. No pull request. |
| `pr-review` | A pull request is opened in the GitOps repository with promotion evidence (provenance, gate results, upstream verification). A person reviews and merges it; the merge is recorded as a `PRMerged` AuditEvent naming who merged it ([Audit logging](guides/security.md#audit-logging)). |

`approval: auto` does not mean "nobody approves". To require named people to approve a version
before an environment, with a quorum and no self-approval, add an
[approval gate](approvals.md): it works with either mode.

### Health checks

A step is Verified when its environment is healthy and runs the promoted version. A **health
adapter** decides that:

| Adapter | What it checks | When to use |
|---|---|---|
| `resource` | Deployment runs the version's images, is fully rolled out and `Available` | Clusters without a GitOps tool |
| `argocd` | Argo CD Application healthy and synced to the promoted commit | Argo CD users |
| `flux` | Flux Kustomization `Ready` with `lastAppliedRevision` at the promoted commit | Flux users |
| `argoRollouts` | Argo Rollouts Rollout phase | Canary/blue-green deployments |
| `flagger` | Flagger Canary phase (`Failed` fails the step at once) | Canary deployments |

kardinal picks the adapter in this order: `delivery.delegate` (set it to `argoRollouts` or
`flagger` when a progressive-delivery controller rolls the workload out), then `health.type`,
then `resource`. See [Health checks](health-adapters.md).

With `argocd` and `flux`, a step whose commit is in git asks the Application, or the Kustomization
and its GitRepository, to sync it now, once, instead of waiting for the tool's poll (every 3
minutes by default for Argo CD). See [Sync on promotion](health-adapters.md#sync-on-promotion).

### Several clusters

kardinal runs in one cluster. If an Argo CD or Flux installation (a **hub**) manages your
workload clusters, run kardinal next to it. Declare one environment per cluster or region (for
example `prod-eu` and `prod-us`) and promote them in parallel with `wave` or `dependsOn`. Each
environment reads its health from its cluster through a kubeconfig Secret
(`health.kubeconfigSecretRef`), or from the hub: `health.type: argocd` on its Application, or
`health.type: flux` on a hub Kustomization that targets the remote cluster. See
[Remote clusters](health-adapters.md#remote-clusters).

## Gates

A **gate** holds a version out of an environment until its expression is true. The expression
is written in [CEL](https://cel.dev) (Common Expression Language), and reads the version, the
time, metrics and the history of the pipeline. A gate is a Kubernetes object of kind
`PolicyGate`. Gates show on the UI's map and in `kardinal why -o wide`.

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

- **Org-level gates** (the controller's `--policy-namespaces`, default `platform-policies`) are injected into every Pipeline that targets the matching environment. Teams cannot remove them.
- **Team-level gates** (team namespace) are added alongside org gates. Teams can add their own restrictions.
- The `kardinal.io/applies-to` label names the environment the gate blocks. A label value cannot contain a comma, so a gate blocks one environment; create one gate per environment to block several.

### CEL context

Gate expressions read the version (`bundle.version`, `bundle.labels`, `bundle.provenance.*`,
`bundle.upstreamSoakMinutes`), the time (`schedule.isWeekend`, `schedule.hour`), the target
environment (`environment.name`), upstream soak and history (`upstream.<env>.*`), MetricCheck
results (`metrics.*`), change windows (`changewindow.*`) and approvals (`approvals.*`).
Referencing an attribute that does not exist blocks the gate. See the
[CEL context reference](reference/cel-context.md) for the full list.

### Inspecting gates

```bash
# See which gates are blocking a promotion
kardinal why my-app prod -o wide

# Output:
# ENVIRONMENT   VERSION                 TYPE         NAME                 STATE      EXPRESSION                         REASON
# prod          1.29.0 (my-app-9tptr)   PolicyGate   staging-soak         Blocking   bundle.upstreamSoakMinutes >= 30   bundle.upstreamSoakMinutes >= 30 = false (version 1.29.0)
# prod          1.29.0 (my-app-9tptr)   PolicyGate   no-weekend-deploys   Passing    !schedule.isWeekend                !schedule.isWeekend = true (version 1.29.0)
#
# prod   deployed: 1.28.0 (my-app-7qvsr)
```

Gates that are not ready come first. STATE is Passing, Blocking (holding the version),
Replaced by newer version, Version rejected, Not evaluated yet or Not ready; REASON is the
controller's latest evaluation. See [Inspecting gates](policy-gates.md#inspecting-gates).

### Skip permissions

If a version's `intent.skipEnvironments` lists an environment that an org gate applies to, the skip is denied unless a skip-permission PolicyGate allows it. Only a gate in an org policy namespace (the controller's `--policy-namespaces`, default `platform-policies`) can grant a skip; gates in the Pipeline's namespace or in `spec.policyNamespaces` never can.

```yaml
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: allow-uat-skip-for-hotfix
  namespace: platform-policies
  labels:
    kardinal.io/type: skip-permission
    kardinal.io/applies-to: uat
spec:
  skipPermission: true
  expression: 'bundle.version.startsWith("hotfix-")'
  message: "Hotfix versions may skip uat"
```

When the skip is allowed, the permission's expression is evaluated in front of the next environment: that environment waits until the expression is true. When no such gate exists, the version goes to phase `Failed` and its status conditions say `skip denied for environment "uat": ...`. See [Skip Permissions](policy-gates.md#skip-permissions).

## Under the hood

For each version, kardinal translates the Pipeline into a [kro Graph](https://kro.run/next/docs/concepts/graph/overview/):
one node per gate and per promotion step, in dependency order. kro's Graph controller creates
them, and kardinal's controllers run the steps and evaluate the gates. You never write a Graph;
the Pipeline is the interface. kardinal uses kro because its Graph controller already creates
objects in dependency order and waits for them to be ready, so kardinal does not reimplement DAG
orchestration.

There is no database and no extra service: all state is in Kubernetes objects, the UI is served
by the controller, and the controller can restart at any time without losing a promotion.
[Architecture](architecture.md) has the details, including supersession and when a finished
version's Graph is retired.

## Where next

- Create versions without CI: [Subscriptions](subscription.md) watch registries and Git.
- Commit rendered YAML instead of templates: [Rendered manifests](rendered-manifests.md).
- Multi-tenant setups, ephemeral environments and anti-patterns: [Patterns](advanced-patterns.md).
- Every transition is recorded as an `AuditEvent`: [Audit logging](guides/security.md#audit-logging).
