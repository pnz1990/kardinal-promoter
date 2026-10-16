# Security Guide

This guide covers RBAC configuration, GitHub token scopes, and security best practices for kardinal-promoter.

---

## Controller RBAC

The Helm chart creates the controller's RBAC from `chart/kardinal-promoter/templates/_rbac.tpl`.
Object names come from the chart's full name, which is `kardinal-promoter` for a release named
`kardinal-promoter`:

| Object | Where | Holds |
|---|---|---|
| ServiceAccount `kardinal-promoter` | Release namespace | The controller identity |
| ClusterRole `kardinal-promoter-manager-role`, ClusterRoleBinding `kardinal-promoter-manager-rolebinding` | Cluster (default) | The namespaced rules in every namespace, plus the cluster-scoped rules |
| Role and RoleBinding `kardinal-promoter-manager-role` / `-manager-rolebinding`, ClusterRole and binding `kardinal-promoter-cluster-scoped` | Namespace mode (`controller.watchNamespace`) | The namespaced rules in the watched namespace; the cluster-scoped rules |
| Role and RoleBinding `kardinal-promoter-leader-election` | Release namespace | The leader-election Lease, the `kardinal-version` ConfigMap, and `get` on the SCM token Secret by name |
| ClusterRoles `kardinal-promoter-graph-applier` and `kardinal-promoter-graph-reader` | Cluster | Bound (with RoleBindings only) to the ServiceAccount kro impersonates for each Graph (`graph.serviceAccountName`, default `kardinal-graph`) |
| ClusterRole `kardinal-promoter-kro-watch` | Cluster | Lets kro watch the kinds a Graph renders; aggregated into kro's role when kro uses `rbac.mode=aggregation`. Rendered only when `graph.aggregateToKro=true` (the default) |

What the namespaced rules grant:

| Resources | Verbs | Why |
|---|---|---|
| `secrets` | get | Pipeline `spec.git.secretRef`, NotificationHook `spec.webhook.secretRef` (only Secrets labeled `kardinal.io/referenceable: "true"` are used, see [Secrets referenced by custom resources](#secrets-referenced-by-custom-resources)) and the SCM token Secret. The controller reads Secrets straight from the API server, one by name, and never lists or watches them. In cluster mode `get` still reaches **every Secret in the cluster** by name, because the rule is in a ClusterRole |
| `events.k8s.io` `events` | create, patch | Events from every reconciler (the events.k8s.io/v1 API) |
| `events` (core) | get, list, watch, create, patch | The UI step event list reads Events through core/v1; leader election writes core Events |
| All kardinal.io kinds and their `/status` | full CRUD; get, update, patch on status | Reconcilers |
| `auditevents` | get, list, watch, create; delete with `audit.retention.enabled: true` (the default) | Records are never changed; retention (on by default) deletes old ones |
| `graphs.kro.run` | full CRUD; get on `graphs/status` | One Graph per Bundle |
| `serviceaccounts`; `rolebindings`; `clusterroles` (bind, limited to the two Graph ClusterRoles) | get, create; get, list, create, update, delete; bind | The Graph identity. `list` is for the sweep that deletes reader bindings no Graph reads through; it runs in cluster mode only and touches only RoleBindings labeled `app.kubernetes.io/managed-by=kardinal-promoter` |
| `deployments`, `argoproj.io` `applications` and `rollouts`, Flux `kustomizations`, Flagger `canaries` | get, list, watch | Health adapters. `health.refresh.enabled: true` (the default) adds `patch` on Applications and Kustomizations, and on `source.toolkit.fluxcd.io` `gitrepositories`, to set the refresh annotation when a promotion's commit lands; RBAC cannot limit `patch` to an annotation, so the `<fullname>-gitops-writes` admission policy denies the controller any other change to those objects (and HelmReleases) unless it may also `update` them ([Sync on promotion](../health-adapters.md#sync-on-promotion)). `rbac.argocdApplicationsWrite=true` adds `patch` and `update` on Applications for `update.strategy: argocd` |
| `replicasets` | get | The `resource` and `flux` health adapters read, by name, the ReplicaSet a Deployment's `ProgressDeadlineExceeded` names, to tell whether the rollout of the current pod template stalled |
| `pods` | list | While a Deployment's replicas are unavailable, the `resource` health adapter lists, uncached, the pods of its new ReplicaSet (by label selector) to name why one is not ready (`ErrImagePull`, `CrashLoopBackOff`). In cluster mode `list` reaches **every pod in the cluster**, which includes pod specs and their literal `env` values (not Secret contents). Use `controller.watchNamespace` to limit it, or remove the rule in RBAC of your own: the health messages then leave the pod out and nothing else changes |

The cluster-scoped rules cover `changewindows` and `clusterscmproviders` (read, and status writes; `patch` on `changewindows` for the freeze audit finalizer, and `create`, `update`, `delete` with `ui.freezeControls`), Argo Rollouts `clusteranalysistemplates` (get), the `gate-overrides` admission policy and its binding (get), `namespaces` (get,
limited to `controller.watchNamespace` in namespace mode: the controller checks whether a
namespace is being deleted before it translates a Bundle, removes kro's finalizer from a Graph, or
cleans up a deleted step's PR) and, with
`ui.auth.tokenReview=true` or `bundleAPI.tokenReview=true`, `tokenreviews` and `subjectaccessreviews` (create).

To see the exact rules for your values:

```bash
helm template kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.11.0 \
  --namespace kardinal-system -f my-values.yaml \
  --show-only templates/clusterrole.yaml
```

To keep Secret access out of other namespaces, run in namespace mode
(`controller.watchNamespace`), which turns the namespaced rules into a Role in that namespace.
The release-namespace Role also grants `get` on the SCM token Secret, limited to its name
(`scm.secretRef.name`, or the Secret the chart creates from `scm.token`). The rule is
left out when neither is set.

### Every permission the chart grants

The Helm chart creates the necessary `ClusterRole` (a `Role` in namespace mode). It grants:

- `get/list/watch/create/update/patch/delete` on the `kardinal.io` kinds, except
  `auditevents` (`get/list/watch/create`, plus `delete` for retention,
  `audit.retention.enabled: true` by default: the controller never changes a record),
  `approvals` (`get/list/watch/create/delete`: an Approval is never edited), `scmproviders`
  (`get/list/watch`: the controller never creates them) and the cluster-scoped
  `changewindows` and `clusterscmproviders` (`get/list/watch`, in a `ClusterRole` in both
  modes; `patch` on `changewindows` for the `kardinal.io/freeze-audit` finalizer, which keeps a
  deleted freeze until its `FreezeLifted` AuditEvent is written, and `create/update/delete` with
  `ui.freezeControls`)
- `get/update/patch` on the `status` subresources of those kinds (`auditevents` has none)
- `get/list/watch/create/update/patch/delete` on `graphs.kro.run`
- `get/list/watch` on the health targets: `deployments`, Argo CD `applications` and `rollouts`,
  Flux `kustomizations`, Flagger `canaries` (`patch` on `applications` and `kustomizations`, and on
  Flux `gitrepositories`, with `health.refresh.enabled`, the default; `update` on `applications` with `rbac.argocdApplicationsWrite`), `get` on `replicasets` (the ReplicaSet a Deployment's
  `ProgressDeadlineExceeded` names), and `list` on `pods` (the new ReplicaSet's pods, to name
  why one is not ready; cluster-wide in cluster mode), and `get` on Argo Rollouts
  `analysistemplates` and `clusteranalysistemplates` that `spec.environments[].verification` names
- `get/list` on `multicluster.x-k8s.io` `clusterprofiles`, to resolve a fleet's targets
- `get` on `secrets` (Pipeline `spec.git.secretRef` and the SCM token); no `list` or `watch`
- `get/create` on `serviceaccounts` and `get/list/create/update/delete` on `rolebindings`, plus
  `bind` on the two Graph ClusterRoles only, for the [Graph identity](#rbac-requirements)
- `get` on `namespaces` (only the watched one in namespace mode), to tell whether a Graph's,
  Bundle's or step's namespace is being deleted
- `create/patch` on `events.k8s.io` `events` and `get/list/watch/create/patch` on core `events`
- in the release namespace: the leader election `leases`, and `create` plus
  `get/update/patch` on the `kardinal-version` ConfigMap
- `get/list/watch/create/delete` on `batch` Jobs: each HookRun's Job ([Pipeline hooks](../hooks.md))
  and each RenderRun's render Job ([Rendered manifests](../rendered-manifests.md))
- `get` on the chart's `gate-overrides` ValidatingAdmissionPolicy and its binding, to know
  whether an override's `createdBy` was checked
- `create` on `tokenreviews` and `subjectaccessreviews`, only with `ui.auth.tokenReview` or
  `bundleAPI.tokenReview`; the shard Leases, only with `controller.namespaceShard`

It grants nothing on `services`, other ConfigMaps or other cluster objects, and reads `pods`
only to explain a rollout that is not ready.

---

## RBAC requirements

The chart creates the controller's ServiceAccount (`kardinal-promoter`) and its RBAC.
The installer itself (Helm, or a GitOps controller) also creates cluster-scoped objects: CRDs, ClusterRoles and bindings, admission policies, and the leader election FlowSchema, which needs `create`, `get`, `update` and `delete` on `flowschemas.flowcontrol.apiserver.k8s.io` (`leaderElectionFlowSchema.enabled=false` leaves it out; see [Leader election under API pressure](../operate.md#leader-election-under-api-pressure)).

The controller's RBAC, in summary:

| Resources | Verbs |
|---|---|
| All `kardinal.io` kinds and their `/status` | Full CRUD, except `auditevents` (get, list, watch, create, and delete with `audit.retention.enabled: true`, the default) `approvals` (get, list, watch, create, delete) and `changewindows`, `scmproviders` and `clusterscmproviders` (get, list, watch; get, update, patch on `/status`; `patch` on `changewindows` for the freeze audit finalizer, and create, update, delete with `ui.freezeControls`) |
| `graphs.kro.run` | Full CRUD; get on `graphs/status` |
| `serviceaccounts`, `rolebindings` | get, create; get, list, create, update, delete (Graph identity; `delete` removes reader bindings no Graph needs, `list` finds them for the sweep) |
| `namespaces` | get, limited to `controller.watchNamespace` in namespace mode (lets go of a Graph whose namespace is being deleted) |
| `clusterroles` | `bind`, limited to `<fullname>-graph-applier` and `<fullname>-graph-reader` |
| `deployments`, Argo CD `applications` and `rollouts`, Flux `kustomizations`, Flagger `canaries` | get, list, watch (health adapters) |
| `replicasets` | get only: the `resource` and `flux` adapters read the ReplicaSet a Deployment's `ProgressDeadlineExceeded` names |
| `pods` | list only: while a Deployment's replicas are unavailable, the `resource` adapter lists the pods of its new ReplicaSet to name why one is not ready. In the default cluster mode `list` covers **every pod in the cluster** (pod specs, including literal `env` values, not Secrets); `controller.watchNamespace` limits it to one namespace. Without it, health messages leave the pod out |
| `secrets` | get only: the controller reads each Secret by name and never lists or watches them. In the default cluster mode `get` covers **every Secret in the cluster**. The release-namespace Role adds `get` on the SCM token Secret by name |
| `configmaps` | None in the watched namespaces. The leader-election Role reads and writes the `kardinal-version` ConfigMap by name |
| `leases` | Leader election, through a Role in the release namespace |
| `events` | get, list, watch, create, patch |

Optional rules: `health.refresh.enabled` (on by default: patch Applications, Kustomizations
and GitRepositories to ask for a sync, confined to the refresh annotation by the
`<fullname>-gitops-writes` admission policy, and to the Pipeline's own objects and those
`health.refresh.namespaces` admits by the controller; see [Sync on promotion](../health-adapters.md#sync-on-promotion)), `rbac.argocdApplicationsWrite` (patch and update Applications) and `ui.auth.tokenReview` or `bundleAPI.tokenReview`
(create TokenReviews and SubjectAccessReviews). The full list of objects and rules is in
[Security: Controller RBAC](security.md#controller-rbac). Set `controller.watchNamespace`
to turn the namespaced rules into a Role in one namespace.

kro does not apply a Graph's children with its own identity. It impersonates the Graph's
`spec.serviceAccountName` (default `kardinal-graph`) in the Graph's namespace. The kardinal-promoter
controller creates that ServiceAccount and binds it with RoleBindings to `<fullname>-graph-applier`
(in the Graph namespace) and `<fullname>-graph-reader` (in each namespace a health `ref` node reads,
limited to the Graph's own namespace and `graph.readerNamespaces`). Each reader binding is named
`<fullname>-graph-reader-<graph namespace>`: with release `kp`, `kp-kardinal-promoter-graph-reader-<graph namespace>`.
`<fullname>` is the chart's full name: `fullnameOverride` if set, else the release name, plus
`-kardinal-promoter` unless the release name contains it. Reader bindings that no Graph
in the namespace needs any more are deleted: when a Bundle is translated, when a Graph is deleted,
and, in cluster mode, by a sweep at controller startup and every 10 minutes that also catches the
bindings of namespaces that are gone. The sweep lists only RoleBindings labeled
`app.kubernetes.io/managed-by=kardinal-promoter`.
See G5 in the [Graph capability ledger](../design/16-graph-capability-ledger.md).

The Helm chart creates all of these ClusterRoles, Roles and bindings.

---

## GitHub Token Scopes

Two tokens are used. The Pipeline's `git.secretRef` token clones and pushes (Contents: write).
The controller token opens, labels, comments on and closes PRs (Pull requests: write) and deletes
`kardinal/` branches (Contents: write); it never pushes.

The controller uses a GitHub Personal Access Token (PAT) to:

1. Open pull requests (one per environment promotion)
2. Read PR status (merged, closed, open)
3. Post comments on PRs (soak time, gate results, rollback evidence)
4. Delete the head branch of a PR it closed without a merge (`kardinal/<namespace hash>/<bundle>/<env>`)

### Minimum required scopes (classic PAT)

| Scope | Why |
|---|---|
| `repo` | Read/write access to repositories (open and close PRs, push and delete branches) |

No admin scopes are required. The token does **not** need:
- `admin:org`
- `admin:repo_hook`
- `delete_repo`
- `workflow`

### Fine-grained PAT (recommended)

GitHub fine-grained PATs give per-repository permissions:

| Permission | Level |
|---|---|
| `Contents` | Read and write (`git.secretRef` token: push branches; controller token: delete `kardinal/` branches) |
| `Pull requests` | Read and write (open PRs, post comments) |
| `Metadata` | Read (required by GitHub for all fine-grained PATs). The controller token also needs `Commit statuses: Read and write` for the [`kardinal/gates` status](../pr-evidence.md#gate-status-check-kardinalgates) (`scm.gatesCommitStatus.enabled`, the default) |

### Token rotation

Store the token in a Kubernetes Secret and update it without restarting the controller:

```bash
kubectl create secret generic github-token \
  --namespace kardinal-system \
  --from-literal=token=ghp_new_token \
  --dry-run=client -o yaml | kubectl apply -f -
```

The controller polls this Secret every 30 seconds and reloads the token when it changes. No
restart is needed. It logs `SCM credentials rotated` when it loads the new token. Wait for that
line before you revoke the old token.

### Using a GitHub App instead of a PAT

The controller can authenticate as a GitHub App installation (`github.app.enabled`): it mints
an installation token from the App's private key, caches it, and replaces it before it
expires, so no long-lived PAT is stored. See [GitHub App](../scm-providers.md#github-app).
The App needs `Contents: Read and write`, `Pull requests: Read and write`, `Commit statuses: Read and write` (for the `kardinal/gates` status) and `Metadata:
Read-only`. There is no OIDC token exchange; another short-lived token works if something
outside kardinal (an External Secrets generator, a CronJob) refreshes the Secret in
`scm.secretRef` before it expires: the controller reloads it within 30 seconds.

---

## Namespace Isolation

### Controller namespace

The controller runs in `kardinal-system` by default. It watches CRDs across all namespaces and
writes to the namespaces where Pipelines are deployed. It also writes its leader-election Lease
and the `kardinal-version` ConfigMap in its own namespace, and reader RoleBindings in the
`graph.readerNamespaces` namespaces. With `update.strategy: argocd` it patches Argo CD
Applications.

### Policy gate scoping

PolicyGates are namespace-scoped:

- **Org-level gates** (`namespace: platform-policies`): mandatory for all pipelines targeting the matching environment. Teams cannot override them. An org gate reads `metrics.*` from the MetricChecks of its own org policy namespace, so a team MetricCheck of the same name cannot decide it.
- **Team-level gates** (team namespace): additive — injected alongside org gates. Teams add restrictions, not bypasses.

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: platform-policies
```

The controller reads org gates from the namespaces in `--policy-namespaces` (default
`platform-policies`; Helm value `controller.policyNamespaces`). No namespace label is needed.

### Multi-tenant isolation

The namespace is kardinal's tenancy unit. There is no Project CRD.

- Put each team's Pipelines, Bundles and PolicyGates in the team's namespace, and give the team
  a `Role` and `RoleBinding` there on the `kardinal.io` resources. Kubernetes RBAC then keeps
  Team A out of Team B's objects, through kubectl and the CLI alike.
- `git.secretRef` must name a Secret in the Pipeline's own namespace. The Pipeline reconciler
  (`Ready=False`) and the PromotionStep reconciler refuse any other namespace, and the git steps
  read the token only from there, so a team's Pipeline clones and pushes with that team's token.
- With `ui.auth.tokenReview` on, the UI follows the same RBAC: every object the UI API reads or
  writes for a user is checked with a `SubjectAccessReview` (see
  [UI API Access Control](#ui-api-access-control)).

#### The shared SCM token and `scm.allowedRepositories`

Git clone and push use the Pipeline's `git.secretRef` token. The controller uses its own SCM
token (`scm.token` or `scm.secretRef`, the controller Pod's `GITHUB_TOKEN`) to open, label,
comment on and close PRs. When it closes a PR that was not merged, it also deletes the PR's head
branch, `kardinal/<namespace hash>/<bundle>/<env>`, with that token, so the closed PR cannot be merged later. It
deletes that branch too when a step that pushed it ends before it opens a PR. It deletes only
branches under `kardinal/`. So the controller token needs write access to repository contents,
not only to pull requests.

Without a limit, anyone who can create a Pipeline, in any namespace, can have PRs opened, and
`kardinal/` branches deleted, in any repository that token can write to. Set
`scm.allowedRepositories` (the controller flag `--scm-allowed-repositories`) to the repositories
the controller's token may act on:

```yaml
scm:
  allowedRepositories:
    - github.com/acme/gitops          # one repository
    - github.com/acme-platform/*      # every repository of an owner
    - gitlab.example.com/platform/**  # everything under a group, subgroups included
    - dev.azure.com/acme/platform/*   # Azure DevOps: organization/project/repository
    - bitbucket.example.com/PLAT/*    # Bitbucket Data Center: host/KEY/slug
    - bitbucket.example.com/~alice/*  # a Data Center user's personal repositories
```

Each entry is `host/repository`: the SCM host, and the repository as the SCM API names it,
`owner/repo` (GitHub, Forgejo, Gitea, Bitbucket), the full project path (GitLab), or
`organization/project/repository` on `dev.azure.com` (Azure DevOps, also for
`<org>.visualstudio.com` and SSH remotes), or `KEY/slug` on Bitbucket Data Center, the project
key and the repository slug, `~user/slug` for a personal repository. A Data Center URL is
matched in that form whichever way it names the repository (`/scm/PLAT/web-app.git`,
`/projects/PLAT/repos/web-app/browse`, `ssh://git@host:7999/plat/web-app.git`), so
`bitbucket.example.com/PLAT/*` covers them all; the host is the one of `scm.apiURL`. Matching ignores case, and a scheme, user, port or
`.git` in the entry (an IPv6 host may keep its brackets). `*` matches one path segment, and an
entry ending in `/**` matches every repository below it. A `spec.git.url` that does not parse to
a host and a repository never matches, and neither does a repository with a segment other than
letters, digits, `.`, `_` and `-` (only Azure DevOps project and repository names may hold single spaces), so
a percent escape, backslash, `?`, `#` or control character cannot smuggle in another path. The
PRStatus CRD refuses such a `spec.repo` too.

The list is enforced on every SCM API call the controller's token makes: opening, labelling,
commenting on, polling and closing PRs, reading reviews and merge commits, and deleting
branches. A call for any other repository is refused before it is sent, whatever code path
makes it (a superseded step cleaning up its branch, a PRStatus poll, a webhook confirmation). A
refused PRStatus poll is recorded in `status.pollError`. On top of that, a Pipeline that would
need the controller's token for a repository that is not allowed is `Ready=False` with reason
`RepositoryNotAllowed`, and its PromotionSteps fail before `git-clone` with the same message, so
nothing is cloned, pushed or opened. That is every Pipeline whose `spec.git.url` is not allowed,
except one that never uses the controller's token: its `git.secretRef` names a Secret that
exists in its namespace (git clone and push use that token) and no environment uses
`approval: pr-review` (whose PR the controller's token opens). A Pipeline refused because its
Secret is missing is checked again every 2 seconds at first, then every 15 seconds and every minute. `kardinal validate --allowed-repositories
<list>` reports the same before you apply the file (offline it takes the Secret to exist); add
`--scm-provider bitbucket-datacenter` for a Data Center controller, so URLs are matched as
`KEY/slug` too. When
the value is empty, every repository is allowed and the controller logs a warning at
startup.

Also:

1. Scope the controller's SCM token to the GitOps repositories kardinal manages, for example a
   fine-grained PAT limited to those repositories.
2. Give each team its own `git.secretRef` token in its namespace, scoped to that team's
   repositories, so a team's clones and pushes are limited by its own token.
3. Grant `create` on `pipelines.kardinal.io` only to people you trust with the controller
   token's reach.
4. Give a team its own SCM token with a [ScmProvider](../scm-providers.md#several-scm-providers-scmprovider-and-clusterscmprovider)
   in its namespace. Its Pipelines name it in `spec.git.providerRef`, and its
   `allowedRepositories` limits that token the way `scm.allowedRepositories` limits the
   controller's. The Secrets a ScmProvider names must be labeled `kardinal.io/referenceable: "true"`.

#### Stronger isolation: one install per team namespace

When teams must not share a controller or its SCM token, run one kardinal installation per
team namespace. This uses the `controller.watchNamespace` Helm value to limit each
controller to a single namespace, with a `Role`/`RoleBinding` scoped to that namespace
instead of a cluster-wide `ClusterRole`.

**Example: two teams, two installs**

```bash
# Team A — installs kardinal watching only the "team-a" namespace
helm install kardinal-team-a oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.11.0 \
  --namespace team-a \
  --create-namespace \
  --set controller.watchNamespace=team-a \
  --set scm.secretRef.name=github-token

# Team B — separate install watching only the "team-b" namespace
helm install kardinal-team-b oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.11.0 \
  --namespace team-b \
  --create-namespace \
  --set controller.watchNamespace=team-b \
  --set scm.secretRef.name=github-token
```

Each install creates a `Role` and `RoleBinding` scoped to its watch namespace. Team A
cannot see or modify Team B's Pipelines, Bundles, or PolicyGates.

**Cost**: one controller replica per team namespace. For 20 teams, this means 20
controller pods. Each controller is lightweight (55-70 MiB with no Pipelines), but the operational
overhead of managing multiple Helm releases is real. Use a tool like ArgoCD's
ApplicationSet or Flux's HelmRelease to manage the installs at scale.

**Central read access**: a central team can still read every namespace, for example with
`kardinal get pipelines --all-namespaces`. The CLI and kubectl read with your own kubeconfig, not
through a controller. Give that team a ClusterRole with `get` and `list` on the `kardinal.io`
resources (pipelines, bundles, promotionsteps, policygates). Do not add a cluster-scoped kardinal
install next to the per-namespace ones: it would reconcile every team's Pipelines too.

#### Additional isolation steps

1. Set `networkPolicy.enabled=true` (the default is `false`) and narrow `networkPolicy.ingressFrom.*` for each install (see [NetworkPolicy](#networkpolicy))
2. Give each team its own SCM token, scoped to that team's GitOps repositories, so a leaked token only reaches one team's repos
3. Keep each team's token `Secret` in that team's namespace — never share a token across namespace installs

---

## Secret Management

### Secrets referenced by custom resources

Some custom resources name a Secret whose values the controller sends somewhere the resource's
author chose: NotificationHook `spec.webhook.secretRef` sends its `authorization` key to the
hook URL. The controller can read every Secret of the namespace (`get` on `secrets`, above), so
without a guard anyone allowed to create such a resource could have the controller send them a
Secret they cannot read themselves.

**One rule:** the controller uses a Secret a custom resource names only when the Secret carries
the label `kardinal.io/referenceable: "true"`. The label is the Secret owner's consent; set it
on the Secrets meant for these resources only:

```bash
kubectl label secret alerting-webhook -n team-a kardinal.io/referenceable=true
```

| Reference | With an unlabeled Secret |
|-----------|--------------------------|
| NotificationHook `spec.webhook.secretRef` | Not used: `Ready=False`, reason `SecretNotReferenceable`, nothing is sent |
| Pipeline `spec.git.secretRef` | Not used: the Pipeline is `Ready=False`, reason `SecretNotReferenceable`, with the `kubectl label` command; every git step and render waits, without a git call, until the Secret is labeled |

Other Secret references adopt the rule as they are added. A resource that refuses a Secret
reports reason `SecretNotReferenceable` where it reports errors. Labeling the Secret takes
effect at the next reconcile; removing the label stops its use again. Whoever may `update` or
`patch` Secrets in the namespace may set the label, so grant that as narrowly as reading them.

Pipeline `spec.git.secretRef` has the same exposure: its token goes to the Pipeline's git URL.

### The controller's own SCM token

**Never label the controller's SCM token Secret** (`scm.secretRef.name`, or the chart's
`<fullname>-github-token` from `scm.token`) `kardinal.io/referenceable: "true"`. The label would
let any kardinal object in that namespace use it: a Pipeline `git.secretRef`, a MetricCheck
`authorizationSecretRef`, a NotificationHook, an ScmProvider or a Subscription could send the
controller's token to a URL its author chooses. The chart never labels it.

The controller also refuses a Pipeline whose `git.secretRef` names its own token Secret
(`--scm-token-secret-name` in `--scm-token-secret-namespace`), labelled or not: the Pipeline is
`Ready=False` with reason `ValidationFailed`, its steps fail before `git-clone`, its Bundles get no Graph, and a RenderRun whose git Secret it is fails before its render Job is created, so no render Pod mounts the token. Such a
Pipeline would otherwise choose where the controller's token goes, and its own existing Secret
would exempt it from `--scm-allowed-repositories`. Give each Pipeline its own Secret; the chart's
demo Pipeline gets one (`demo.secretRef`, or a labelled copy the chart creates with `scm.token`).

### Recommended: External Secrets Operator

Use [External Secrets Operator](https://external-secrets.io/) to sync tokens from Vault, AWS Secrets Manager, or GCP Secret Manager:

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: github-token
  namespace: kardinal-system
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: ClusterSecretStore
    name: vault-backend
  target:
    name: github-token
  data:
    - secretKey: token
      remoteRef:
        key: secret/kardinal/github-token
        property: value
```

---

## Pod Security

The Helm chart sets secure defaults for the controller pod:

```yaml
podSecurityContext:
  runAsNonRoot: true
  seccompProfile:
    type: RuntimeDefault
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  runAsNonRoot: true
  capabilities:
    drop: ["ALL"]
```

These defaults comply with the Kubernetes `restricted` pod security standard.

### Render Jobs (layout: branch)

A `layout: branch` environment renders kustomize overlays and Helm charts written by whoever can
push to the DRY source: untrusted input. kardinal never renders in the controller. Each render is
a RenderRun Job in the Pipeline's namespace ([Rendered Manifests](../rendered-manifests.md#the-render-job)),
which also meets the `restricted` standard:

- no Kubernetes credentials: `automountServiceAccountToken: false`, and its ServiceAccount
  (`kardinal-render`, created by the controller in the namespace) has no token and no role. A
  render that escaped the renderer still could not call the API server as anyone;
- git access only through the Pipeline's own git Secret (`spec.git.secretRef`, the `token` key
  only), which belongs to the tenant; the controller's SCM token is never in the Pod;
- non-root, read-only root filesystem, no privilege escalation, every capability dropped,
  `RuntimeDefault` seccomp, no service links;
- CPU, memory and time limits (`render.resources.limits`, `render.timeout`): a template or overlay
  that explodes is stopped by the kernel or the Job deadline, not by the controller's memory;
- no network in the render process but git to the host of `spec.git.url`, through the egress
  guard: every other HTTP request is refused;
- recommended (`render.networkPolicy`, opt-in because it needs a CNI that enforces it): egress to
  DNS and the git host only, so even a process that escaped the renderer reaches no cloud
  metadata endpoint, no Kubernetes API and no other Service.

The render Job's result (its termination message) is trusted as far as the controller checks it:
the commit is read back from the remote, and its marker must be a recorded one. Whoever can create
Pods in the Pipeline's namespace can mount the namespace's git Secret, push to the rendered branch
and create a Pod that names a render Job as its owner, so that permission is trusted as much as push access to
the rendered branch: grant it only to the namespace's owners.

Inside the Job the renderer refuses remote references in any kustomization field, symbolic links
anywhere in the DRY source, overlay diamonds past the object limit, oversized Helm template values
and nondeterministic Helm functions; see [Determinism and limits](../rendered-manifests.md#determinism-and-limits).

---

## Audit Logging

kardinal writes an immutable `AuditEvent` CRD record at every significant promotion
lifecycle transition, in the Pipeline's namespace. AuditEvents are append-only — the
spec is set at creation and never mutated. Kubernetes RBAC controls who can delete them (see
[RBAC: read-only access to audit records](#rbac-read-only-access-to-audit-records)).

### Retention

An AuditEvent has no owner, so it outlives the Bundle and the step it records, and nothing
else deletes it. Without retention a busy cluster keeps every record in etcd for ever, and a
full etcd quota stops the whole cluster: no write of any kind succeeds until an operator
compacts and defragments etcd. Each Verified promotion step writes two records of about
1.2 KB in etcd; the scale suite's soak (5 Bundles a second) wrote 350-660 a minute, which fills
etcd's default 2 GiB quota in 2 to 4 days. Retention is therefore **on by default**. The
controller's leader applies it every 10 minutes:

| Value | Flag | Default | Deletes |
|---|---|---|---|
| `audit.retention.enabled` | `--audit-retention` | `true` | `true` applies retention and grants the controller `delete` on AuditEvents; `false` keeps every record |
| `audit.retention.maxAge` | `--audit-retention-max-age` | `2160h` (90 days) | records created longer ago (`metadata.creationTimestamp`, set by the API server). `0s` keeps any age |
| `audit.retention.maxPerPipeline` | `--audit-retention-max-per-pipeline` | `1000` | per Pipeline (namespace and `kardinal.io/pipeline` label), all but the newest records, newest by `metadata.creationTimestamp` and, within one second, `kardinal.io/created-at`. A record created in the last 10 minutes (one run's interval) is kept even past the limit, so a burst, such as an [audit outbox](#audit-outbox) flushed after an outage, stays at least that long for an export to read. `0` keeps any number |

Records that name no Pipeline (no `kardinal.io/pipeline` label, such as Bundle API writes and
overrides of standalone gates) are capped too: in each namespace they share one cap of
`maxPerPipeline`. So with the defaults a namespace holds at most 1000 records per Pipeline
plus 1000 that name none, younger than 90 days.

A run lists the records metadata-only, 500 at a time, and deletes at most 2000, the oldest
first. It uses a client of its own limited to 5 API requests a second, so it never takes API
capacity from the reconcilers: clearing 2000 records takes about 7 minutes, and a larger
backlog is cleared over several runs. `kardinal_auditevents_pruned_total` counts the
deletions.

A gate whose result changes often writes a record at each change, and can reach
`maxPerPipeline` quickly. If you must keep every record, export them
([SIEM integration](#siem-integration)) more often than the effective retention (the age
limit, or the time a busy Pipeline takes to write `maxPerPipeline` records), or turn
retention off.

**Sharding.** With `controller.namespaceShard` (`--namespace-shard`), each shard's leader
prunes only the namespaces its shard owns, so shards neither repeat each other's work nor apply
their limits to another shard's records ([Sharding](../sharding.md)).

**Turning it off.** Set `audit.retention.enabled: false` (`--audit-retention=false`). The chart
then also drops `delete` on AuditEvents. An install that does not use the chart, or that
removed the `delete` grant, must set `--audit-retention=false` too: otherwise every run (every
10 minutes) fails with `Forbidden` and logs the error. Every record stays, so size etcd for them (see the
numbers above) or export and delete them yourself.

**Exporting first.** To keep every record outside the cluster, run the
[SIEM integration](#siem-integration) export on a schedule shorter than the effective
retention, and make its first run before you install or upgrade to a release with retention
on.

**Upgrading** from a release without retention deletes, in the first runs after the upgrade,
the records already past the limits: older than 90 days, or beyond each Pipeline's 1000 newest.
A run deletes at most 2000, so a large backlog is cleared over several runs (about 7 minutes
each). Export first if you must keep them, or upgrade with `audit.retention.enabled: false`
and turn it on after the export.

### Events written automatically

| Action | Trigger |
|---|---|
| `PromotionStarted` | Bundle begins promoting through an environment |
| `PromotionSucceeded` | Health check passed; PromotionStep reached Verified |
| `PromotionFailed` | PromotionStep reached Failed or AbortedByAlarm |
| `PromotionSuperseded` | A newer Bundle superseded an in-flight promotion (a step that had not started writes none) |
| `PromotionRejected` | `kardinal reject` cancelled an in-flight promotion (its Bundle was [rejected](../reject.md); a step that had not started writes none) |
| `BundleRejected` | A Bundle is rejected (`kardinal reject` or the UI), once, naming who rejected it and why; its environment is `*` (the whole Bundle) |
| `GateOverridden` | An entry in a gate instance's `spec.overrides[]` (`kardinal override` or the UI), written once per entry with its verified `createdBy`, stage, expiry and reason |
| `ApprovalRecorded` / `ApprovalRevoked` | An approval gate saw a decision (`kardinal approve`) appear, or its Approval deleted, with the approver, the decision and whether it counts |
| `HoldCreated` / `HoldReleased` / `HoldBundleMissing` | A hold (`kardinal hold`, `kardinal rollback --hold`) appeared in, or left, `spec.holds`, naming who held and who released it (`kardinal unhold --reason`); or its rollback Bundle has been missing for `--hold-bundle-grace` (the hold stays in effect: [A hold whose rollback Bundle does not exist](../rollback.md#a-hold-whose-rollback-bundle-does-not-exist)) |
| `PipelinePaused` / `PipelineResumed` | (v0.12.0, unreleased) A Pipeline, or one fleet of it (environment set), was paused or resumed (`kardinal pause` / `resume`, the UI, or a `spec.paused` / `spec.pausedFleets` edit), naming who and why (`--reason`); the environment is `*` for the whole Pipeline |
| `FreezeStarted` / `FreezeLifted` | (v0.12.0, unreleased) A freeze (`kardinal freeze`, the UI) started or was lifted (`kardinal unfreeze`, the UI, or a delete), naming who; in the freeze's namespace, or the controller's for a freeze of several or every namespace |
| `FreezeExempted` | A step went on through a freeze under an exemption (`kardinal override --freeze`), once per freeze, naming who made it |
| `GateEvaluated` | PolicyGate instance first evaluated, and every later change of readiness (blocked or unblocked); one record per change |
| `PRMerged` | (v0.12.0, unreleased) A promotion step's pull request (a `pr-review` environment's) was merged: who merged it, asked of the SCM API once the merge is confirmed (never taken from a webhook payload, which anyone holding the webhook secret could write), whether the webhook or polling saw the merge, and the merge commit. The step waits for the name for at most 5 minutes; if the SCM cannot give it, the record says the merger is unknown. The step's `status.outputs.prMergedBy` names the merger too. On GitLab the merger is the API's `merge_user`: with "merge when pipeline succeeds", the user who set it |
| `RollbackStarted` | `onHealthFailure: rollback` triggered a rollback Bundle |
| `RollbackSucceeded` | A PromotionStep of a rollback Bundle (from `kardinal rollback`, the UI, a RollbackPolicy or `onHealthFailure: rollback`) reached Verified; written besides `PromotionSucceeded`, one record per step |
| `APIWrite` | A write through the UI API or the Bundle API (promote, roll back, create a version, approve, override, pause, resume, reject, release a hold), refused ones included (outcome `Failure`), naming the caller and the route ([API writes](#api-writes)) |

Each record of a person's or an agent's action names its verified actor in `spec.actor`
([Agents and the verified actor](../actors.md)): the stamp the change carried (a hold's,
`spec.lastChange`, a rejection's, a freeze's), which the chart's actor admission policies check.
A change made without kardinal, such as `kubectl patch` of `spec.paused` or `kubectl delete` of a
freeze, is still recorded, with no actor and a message that says so: the Kubernetes audit log
names who made it. A `spec.lastChange` names only the change it was written with: the Pipeline
reconciler records the one it used (`status.observedLastChange`), so a later kubectl change is
not attributed to it. A stamp that names someone other than the user the record names is never
recorded.

Admission checks a stamp in two parts. That it names the requester (its `principal`), carries no
`via` (only the controller sets it) and has a kind that fits (`bot` or `agent` for a
ServiceAccount, `human` or `agent` for anyone else) is checked always, by the hold-writes,
bundle-rejection and freezes policies. That an agent's stamp names its registered agent and
sponsor needs the AgentIndex. For a hold, a `spec.lastChange` and a rejection it is checked by
`<release>-actor-records`, which admits while the index is missing (a fresh install, until the
controller builds it, so Pipeline writes and Bundle updates never wait for it). In that window
admission cannot tell an agent's stamp from a forged one: anyone may write a stamp of kind
`agent` naming any agent and sponsor, and an agent may stamp itself `bot` (a ServiceAccount) or
`human` (a user or a member of a registered group); the stamp still names the requester as its
principal and carries no `via`.

The controller therefore checks every such stamp against the AgentIndex again before an AuditEvent
records it, and agents fail closed: a stamp of kind `agent` is recorded only when the index says
the requester itself (a username or ServiceAccount the AgentIdentity lists) is that agent of that
sponsor, and never while the index is missing or not ready. The stamp of an agent registered by
group is never recorded on these three records: the controller cannot see the requester's groups,
so it cannot confirm membership, and admission confirms it only while the index exists. A
non-agent stamp of a user the index names as an agent is not recorded either. A stamp that is not recorded leaves the record with no actor, and its message says why
(`actor stamp not recorded: ...`). People's and bots' stamps are recorded during an index outage
as at any time. A freeze and its lift are checked by `<release>-actor`, which refuses every
ChangeWindow write while the index is missing, so they have no such window.

The CRD also accepts the actions `HealthCheckFailed` and `GateBlocked`, but kardinal never writes
them: a failed health check records `PromotionFailed` (or `RollbackStarted` with
`onHealthFailure: rollback`), and a blocked gate records `GateEvaluated` with outcome `Failure`.

### Audit outbox

A transition and its AuditEvent are two API writes. So that an etcd timeout, a lost
leader or a crash between them cannot lose the record, every writer (the
PromotionStep reconciler; the PolicyGate reconciler for gate results, approvals
and overrides; the Pipeline reconciler for holds) records the AuditEvent in its own
`status.pendingAuditEvents` in the same status patch as the transition. They then
create it and remove the entry. An entry whose create fails stays in status, and
the object is reconciled again every 5 seconds until the create succeeds. Each
entry's name is fixed when it is stored, so a create that had already succeeded
returns `AlreadyExists` and counts as written: there is one record per
transition, never two. The record carries the time of the transition, not the
time it was written.

A failed write never blocks a promotion or a gate evaluation. A finished Bundle's
Graph is not retired while one of its steps or gates still holds unwritten
records, since retiring deletes them (the `GraphRetired` condition names the
object). A record the CRD would refuse is not stored at all, so it cannot fail the
status write, and messages are cut to 1 KiB. The outbox holds at
most 32 entries. When it is full, the oldest entry is dropped and counted in
`kardinal_audit_events_dropped_total`. A record the API server rejects as invalid
is dropped too. `kardinal_audit_write_failures_total` counts the creates being
retried ([Monitoring](../reference/metrics.md#kardinal-metrics)). To see records not yet
written:

```bash
kubectl get promotionsteps,policygates -A -o json \
  | jq -r '.items[] | select(.status.pendingAuditEvents) | "\(.metadata.namespace)/\(.metadata.name): \(.status.pendingAuditEvents | length)"'
```

### Fields on every event

| Field | Description |
|---|---|
| `spec.timestamp` | RFC 3339 time when the event occurred, stored to the second |
| `metadata.annotations["kardinal.io/created-at"]` | The same time with nanoseconds: it orders events within one second, such as a gate that flips twice in a second (`kardinal get auditevents` sorts by it) |
| `spec.pipelineName` | Name of the Pipeline |
| `spec.bundleName` | Name of the Bundle being promoted |
| `spec.environment` | Environment name (e.g. `prod`) |
| `spec.action` | One of the action values in the table above |
| `spec.outcome` | `Success`, `Failure`, or `Pending` |
| `spec.message` | Human-readable description |
| `spec.actor` | Who caused the event ([Agents and the verified actor](../actors.md)) |
| `spec.request` | The route and method of an `APIWrite` event |

An event names at least one of the Pipeline, the Bundle and the environment.

### Who acted

`spec.actor` is the verified actor of the record the event reports:

| Event | Actor |
|---|---|
| Promotion and rollback events | The version's actor (its Bundle's `spec.actor`): the change belongs to whoever made the version |
| `ApprovalRecorded`, `ApprovalRevoked` | The Approval's actor |
| `GateOverridden` | The override's actor, when the identity admission policy checked it |
| `APIWrite` | The caller, as the UI API or the Bundle API authenticated them |

Its `kind`, `principal`, `agent`, `sponsor` and `via` are verified. The principal is the
username the Kubernetes API server authenticated (`request.userInfo`): the chart's admission
policies admit a version, an Approval or an override only when its actor names the requester,
and only kardinal's controller may name someone else, for the callers its TokenReview
authenticated (`via: ui` or `via: api`). With the UI's static token, or the Bundle API's, the
caller is the shared credential: a `bot` named `kardinal-ui` or `bundle-api`. `asserted.model`,
`asserted.session` and `asserted.client` are what the client said about itself: recorded as
written and never trusted.

The `<release>-audit-actor` policy admits an AuditEvent that names an actor only from the
controller, so a `spec.actor` in the log was written by kardinal. An event without one (a
record from before v0.12, or one a version without a stamp caused) names nobody; a version's
`kardinal.io/created-by` then says who created it. The Kubernetes API server audit log remains
the record of every request, kardinal's own included.

### API writes

With `controller.accessLog.auditEvents` (`--api-audit-events`, on by default), every write
through the UI API and the Bundle API is an `APIWrite` AuditEvent in the target's namespace. It
records the caller (`spec.actor`), the route and method (`spec.request`), the target and what
was done (`spec.message`), and the outcome: `Failure` when the controller or the API server
refused it, once the request named its target (requests refused before that, such as a
malformed body or the Bundle API's rate limit, are in the access log only). No token or request
body is recorded. The events are queued and written after the
response, at most 50 a second per controller replica; past that, or with the queue full, an
event is dropped and counted in `kardinal_audit_events_dropped_total{kind="APIWrite"}`. They
are retained with the other AuditEvents ([Retention](#retention)).

### Querying audit events

```bash
# List the 20 most recent audit events in the current namespace (--limit 0 for all)
kardinal get auditevents

# Filter by pipeline
kardinal get auditevents --pipeline my-app

# Filter by environment
kardinal get auditevents --pipeline my-app --env prod

# Raw kubectl (shows all fields)
kubectl get auditevents -n my-team -o wide

# Watch a specific pipeline's events in real-time
kubectl get auditevents -n my-team \
  -l kardinal.io/pipeline=my-app \
  --watch

# Filter by action, across namespaces
kubectl get auditevent -A -l kardinal.io/action=PromotionFailed
```

### SIEM integration

`kardinal audit export` writes AuditEvents as newline-delimited JSON for a SIEM, either the
AuditEvent spec (`--format json`) or OCSF 1.3.0 API Activity events (`--format ocsf`, class
6003), each with its actor:

```bash
# Every namespace, the last 24 hours, as OCSF
kardinal audit export -A --format ocsf --file audit.ocsf.ndjson

# One pipeline's last week, as JSON lines
kardinal audit export --pipeline my-app --since 7d > my-app.ndjson
```

In OCSF, `actor.user.name` is the principal, `actor.user.type` is `User`, `System` or `Agent`,
`actor.app_name` is the agent, and the sponsor and the asserted fields are under
`unmapped.kardinal.actor` ([kardinal audit export](../reference/cli/kardinal-audit-export.md)).

Log forwarders such as Fluent Bit and Vector read container logs, not custom resources. Run
the export on a schedule (for example a CronJob) and forward its output to your SIEM
(Splunk, Datadog, OpenSearch, etc.). Each run exports every record in its window, so
deduplicate by `uid` (OCSF `metadata.uid`) in the SIEM. The export streams the records in the
order the API server lists them: sort by time downstream. With retention on, run the export
more often than the effective retention (see [Retention](#retention)), or records are deleted
before they are exported.

### RBAC: read-only access to audit records

The controller's ServiceAccount can create AuditEvents but cannot update them, and cannot
delete them unless you turn on [retention](#retention).
Kubernetes RBAC only grants access; it cannot deny it. A user can delete AuditEvents only if a
role grants `delete` (or `*`) on `auditevents`, as `cluster-admin` does. Grant users read-only
access like the role below, and do not grant `delete` or `*` on `kardinal.io` resources. The API
server audit log records any deletion. Deleting a namespace deletes its AuditEvents.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kardinal-audit-readonly
rules:
  - apiGroups: ["kardinal.io"]
    resources: ["auditevents"]
    verbs: ["get", "list", "watch"]
    # Intentionally no "delete" or "update"
```

### API access log

The controller logs access to the UI API (`:8082/api/v1/ui/*`) and the Bundle API
(`POST :8083/api/v1/bundles`). Each access is one structured log line with
`component=access`, written to the controller's log next to its other lines. By default
these accesses are logged:

| `access` | When |
|----------|------|
| `login` | A token was checked with the API server (a TokenReview), or the shared static token was used for the first time in 30 seconds on that server. Repeated requests within the 30-second review cache are not logins |
| `denied` | The request was answered `401`, `403`, `429` or `503` (authentication unavailable: the review API failed and the API fails closed). `reason` holds kardinal's message, for example `forbidden: user "…" cannot update pipelines.kardinal.io in namespace team-a` |
| `write` | Any request that is not `GET`, `HEAD` or `OPTIONS`: promote, roll back, pause, resume, approve, create Bundle |
| `request` | Any other request, only with `controller.accessLog.allRequests=true` (`--access-log-all-requests`) |

```json
{"level":"warn","component":"access","access":"denied","server":"ui","method":"POST","path":"/api/v1/ui/pause","status":403,"durationMs":4,"user":"system:serviceaccount:team-a:dashboard","groups":["system:serviceaccounts","system:serviceaccounts:team-a","system:authenticated"],"auth":"tokenreview","reason":"forbidden: user \"system:serviceaccount:team-a:dashboard\" cannot update pipelines.kardinal.io in namespace team-a","message":"api access"}
```

**Fields:**

- `server`: `ui` or `bundle-api`.
- `method`, `path`, `status` and `durationMs`.
- `user` and `groups`: the authenticated caller, in TokenReview mode.
- `auth`: `tokenreview` or `static-token`. A shared static token has no user, so its line says only `static-token`.

**Source address.** With `controller.accessLog.sourceIP=true` (`--access-log-source-ip`), each
line also has `sourceIP`, the address of the connecting peer. Behind an Ingress, list the
Ingress controller's addresses in `controller.accessLog.trustedProxies`
(`--access-log-trusted-proxies`, CIDRs). kardinal then takes the client address from the
nearest `X-Forwarded-For` entry that is not a trusted proxy. It ignores `X-Forwarded-For`
from any other peer, so clients cannot forge it.

**What is never logged.** Tokens, request headers, request bodies and query strings are never
logged. The request path is logged, cut to 256 bytes. It holds only route segments and object
names, and a caller controls it, so it could carry anything put in a URL. For refusals, kardinal's own refusal
message is logged: at most 256 bytes of the response, only for `401`, `403`, `429` and `503`.

**Rate limit.** `login` and `write` lines are never dropped. `denied` and `request` lines each
have a budget of 50 a second. Lines over a budget are not written. Every 10 seconds one line
reports how many of each kind were dropped (`dropped_denied`, `dropped_request`), and the
counter `kardinal_api_access_log_dropped_total{kind}` counts them. So a flood of refused
requests cannot fill the log, and it cannot hide a login or a write.

The access log covers the HTTP APIs. Each write is also an `APIWrite` AuditEvent naming the
caller ([API writes](#api-writes)), and what the controller then does (promotions, gate
results, rollbacks) is in the AuditEvents above, with the version's actor. Ship the controller's log to your SIEM to keep
both records.

---

## NetworkPolicy

By default, no NetworkPolicy is applied. In environments with a NetworkPolicy-capable CNI (Calico, Cilium, etc.), enable the built-in policy to restrict the controller's network access:

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.11.0 \
  --set networkPolicy.enabled=true
```

The policy allows:
- **Ingress**: one rule per port: metrics (`8080`), health probes (`8081`), UI (`8082`) and
  webhook/Bundle API (`8083`). Restrict who can reach each port with
  `networkPolicy.ingressFrom.{metrics,health,ui,webhook}`.
- **Egress**: DNS (`:53`), and `:443` and `:6443` to any address (Kubernetes API server, SCM
  providers, go-git), plus the kro namespace (`graph.kroNamespace`, default `kro-system`;
  set it to `""` to drop that rule). Add more rules with `networkPolicy.extraEgress`.

Disable with `--set networkPolicy.enabled=false` if your CNI does not support NetworkPolicy.

### Secrets that kardinal may send

A MetricCheck, NotificationHook or Subscription names a Secret in its own namespace, and the
controller sends that Secret's value to a URL the object's author chose. The controller can read
every Secret, so a user who can create one of these objects but cannot read Secrets could
otherwise have any Secret of the namespace sent to a server they run (a confused deputy).

**Rule:** the controller reads a Secret for these kinds only when the Secret carries the label
`kardinal.io/referenceable: "true"`. Without the label the object reports `SecretNotReferenceable`
(a MetricCheck fails with that reason, which blocks gates on it) and no request with a credential
is sent. Label a Secret only when it is meant to leave the cluster through kardinal:

```bash
kubectl -n my-app label secret datadog kardinal.io/referenceable=true
```

Who may set the label is who may `update` (or `patch`) Secrets in the namespace. Pipeline
`spec.git.secretRef` needs the label too (see [Secrets referenced by custom resources](#secrets-referenced-by-custom-resources)); it must be in the Pipeline's namespace and is sent only to
the Pipeline's git and SCM hosts.

### Outbound requests to user URLs

NotificationHook webhooks (`spec.webhook.url`), MetricCheck queries (`spec.prometheusURL`,
`datadog.address`, `cloudWatch.endpoint`, `newRelic.address`, `web.url`),
Subscription polls (`spec.image.registry` with its token realm, `spec.git.repoURL`,
`spec.helm.repoURL`) and the API calls of ScmProviders and ClusterScmProviders
(`spec.apiURL`, `https://` only unless `scm.providersAllowInsecureHTTP` is set)
send HTTP requests from the controller to a URL a user wrote into a resource. The controller
refuses to connect when the address is one of these:

- loopback (`127.0.0.0/8`, `::1`), which includes the controller's own UI API;
- link-local (`169.254.0.0/16`, `fe80::/10`), which holds the cloud metadata and credential
  endpoints `169.254.169.254`, `169.254.170.2` and `169.254.170.23`;
- other cloud metadata addresses: `fd00:ec2::254`, `fd00:ec2::23`, `fd20:ce::254`,
  `100.100.100.200` and `168.63.129.16`;
- unspecified (`0.0.0.0/8`, `::`) and multicast addresses.

The check runs when the connection is opened, on the resolved address, for every
connection including redirects. An SSH `spec.git.repoURL` is checked the same way: the host
is resolved and every address checked before the connection, which then goes to the address
that was checked, and the host key is verified against the Secret's `known_hosts`. A host name that resolves, or later re-resolves, to one of
these addresses is refused too. The failure reads `destination address is not allowed:
127.0.0.1 is loopback` and appears where that resource reports errors: NotificationHook
`status.failureMessage`, MetricCheck `status.reason` or Subscription `status.message`.

Private ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`) and public
addresses are allowed, because in-cluster Services, Prometheus, registries and Git servers
are the normal targets.

To allow only the destinations you expect, set the controller's egress allowlist (chart value
`egress.allowlist`, flag `--egress-allowlist`, environment variable `KARDINAL_EGRESS_ALLOWLIST`):

```yaml
egress:
  allowlist:
    - hooks.slack.com                      # exactly this host
    - "*.logic.azure.com"                  # any name under it (Teams Workflows)
    - "*.monitoring.svc.cluster.local"     # in-cluster Prometheus
    - 10.20.0.0/16                         # addresses in this range
```

With an allowlist, a NotificationHook, MetricCheck or Subscription request is allowed when
its URL's host name matches a host entry, or when every address the controller connects to
is inside a CIDR entry (an address entry is a `/32` or `/128`). Anything else is refused
before connecting, with `destination address is not allowed: not in the controller egress
allowlist: <host> (<address>) matches no entry` where that resource reports errors. A name
entry matches the name in the URL, whatever it resolves to; a CIDR entry is checked on the
resolved address of every connection, so a name that re-resolves outside the range is
refused. The list above never opens the deny list: loopback, link-local, metadata,
unspecified and multicast addresses stay refused even when an entry covers them. The default
(empty) keeps the behavior above: every other destination is allowed. Through a proxy, the
target is checked against the allowlist before the request goes to the proxy; the proxy's
own address does not need an entry. The controller logs the allowlist at startup and fails
to start on an invalid entry.

The allowlist works at the HTTP level and on any CNI. To enforce egress at the network
level as well, enable the NetworkPolicy and list the allowed destinations in
`networkPolicy.extraEgress`.

NotificationHook, MetricCheck and Subscription HTTP requests honour `HTTP_PROXY`, `HTTPS_PROXY` and
`NO_PROXY` (SSH connections do not use a proxy).

Subscription credentials (`spec.*.secretRef`) and webhook tokens (`spec.webhook.secretRef`)
are read only from the Subscription's own namespace, so whoever can create a Subscription
can use only the Secrets of that namespace. The controller reads them with `get` on every
poll or delivery; it never lists Secrets. The webhook receiver
(`/webhook/subscriptions/...`, see [Subscription webhooks](../subscription.md#webhooks))
answers every authentication failure, and a Subscription that does not exist, with the
same 401, limits requests per source address and per Subscription, and writes only the
`kardinal.io/refresh` annotation. Any Secret a Subscription reads (credentials and the
webhook token) must be labelled `kardinal.io/referenceable: "true"`; an unlabelled Secret
is not read and nothing is sent.
Through a proxy, the controller connects to the proxy, so before it sends a request there it
checks the target itself: an IP address against the list above, and a host name by resolving
it and checking every address it resolves to. This applies to every request, including
redirects. A host name the controller cannot resolve is refused, because it cannot be checked.
The proxy resolves the name again, and DNS can give it a different answer, so the proxy must
also enforce its own egress policy. The proxy's own address is checked too: a proxy on a
loopback address is refused.

---

**MetricCheck `web` reads from wherever the controller can reach.** A `web` MetricCheck sends a
GET or POST to a URL its author chooses and copies one JSONPath value of the answer into
`status.lastValue` (at most 256 bytes, a string, number or boolean). The egress guard keeps it off
loopback, link-local and metadata addresses, but private addresses stay allowed, so anyone who can
create a MetricCheck can read a field from an in-cluster Service that answers JSON. Give `create`
on MetricChecks only to people who may read those Services, and restrict the targets with the
controller egress allowlist (`--egress-allowlist`) or the chart NetworkPolicy
(`networkPolicy.enabled`, `networkPolicy.extraEgress`).

## Admission Validation

The CRDs validate their fields with OpenAPI schema rules, so `kubectl apply` rejects bad
values before they are stored. Examples: enum fields such as `update.strategy`, non-empty
required strings such as `PolicyGate` `spec.expression`, the Go duration format of
`spec.recheckInterval` (`5m`, `30s`, `1h`), and the rules for environment names. This works
on every supported Kubernetes version and needs no admission webhook.

The CRDs do not check that `spec.expression` is valid CEL. Run `kardinal validate -f <file>`
before you apply: it compiles each PolicyGate expression with the controller's CEL environment.
After you apply a gate, the controller compiles it and writes the result to `status.reason`.

The chart's `ValidatingAdmissionPolicy` objects are always installed: the identity policies below, `<fullname>-scoped-writes`
([User roles](#user-roles)), `<fullname>-hold-writes`, `<fullname>-fleets` (`spec.pausedFleets`) and `<fullname>-gitops-writes` ([Controller RBAC](#controller-rbac)).

### Verified identity

Records that name a person are checked by the API server, not trusted from the client. The
chart installs a `ValidatingAdmissionPolicy` with a `Deny` binding, per release:

| Policy | Checks |
|---|---|
| `<fullname>-bundle-rejection` | A Bundle's new `spec.rejected.by` ([`kardinal reject`](../reject.md)) equals the requesting user's `request.userInfo.username`. A rejection already set is immutable (CRD rule), so it is checked only when it is first written. |
| `<fullname>-gate-overrides` | Every new or changed `spec.overrides[]` entry of a PolicyGate ([`kardinal override`](../overrides.md)) has `createdBy` equal to the requesting user; the controller's ServiceAccount is exempt, because it writes overrides for the UI. Only the namespace's Graph ServiceAccount (kro) and the controller may create a gate instance (label `kardinal.io/bundle`, checked on CREATE and UPDATE) or change anything in it but `spec.overrides`: the rest of the spec and the whole metadata (labels, annotations, owner references, finalizers) are frozen, except `managedFields`, `resourceVersion` and `generation`, which the API server writes, and the `kardinal.io/force-recheck` annotation, which forces a re-evaluation. The garbage collector and the namespace controller may update an instance (they remove owner references and finalizers). In namespace mode (`controller.watchNamespace`) it applies to the watched namespace only. |
| `<fullname>-approvals` | An `Approval` ([`kardinal approve`](../approvals.md)) is created only with `spec.user` equal to the requesting user and `spec.groups` among the requester's groups (checked at create time only), and its `kardinal.io/bundle` and `kardinal.io/environment` labels always equal `spec.bundle` and `spec.environment`. It may be owned only by the Bundle it approves (`spec.bundle`, `spec.bundleUID`), and its owner references cannot change later. Only its approver may delete (revoke) it; the garbage collector and the namespace controller are exempt (kardinal's controller is not: it never deletes Approvals). The spec is immutable (CRD rule). The UI records decisions for its TokenReview user: an Approval annotated `kardinal.io/recorded-via: ui` may be created in another name, and deleted, by this release's controller ServiceAccount (and `admission.controllerUsernames`) only. |
| `<fullname>-bundle-creator` | A new Bundle's `kardinal.io/created-by` annotation, when set, equals the requesting user, and it cannot be added, changed or removed later. Exactly this release's controller ServiceAccount is exempt, plus the usernames listed in `admission.controllerUsernames` (exact usernames, no wildcards; never a namespace or a group: in namespace mode the release namespace is the tenant's): it names the creator of the Bundles it creates (the UI user, `subscription:<name>`, `bundle-api`, `kardinal-controller`). Another controller instance that is not listed creates its Bundles without a creator, which excludeAuthor gates hold. An approval gate's `excludeAuthor` reads it. |
| `<fullname>-actor` | The actor stamp (`spec.actor`) of a new Bundle, a new Approval or Verdict, every new freeze exemption, and who froze or lifted a freeze, says what the requester is, according to the release's `AgentIndex`: the `principal` is the requesting user, the `kind` is `agent` for a registered agent (by username or group), `bot` for any other ServiceAccount and `human` otherwise, an agent's `agent` and `sponsor` are the registered ones, and `via` is left to the controller. An agent must stamp, may act only in its scope (namespaces; the Bundle's Pipeline; the Approval's or the gate's environment) and not while suspended or not ready; a person or a bot may leave the stamp out. An agent may not add a freeze exemption at all (break-glass is for people, [Freezes](../agentic/freezes.md#let-one-change-through)). The controller is exempt: it stamps for the callers it authenticated. Until the controller has built the AgentIndex (it is not Ready), the policy refuses what it checks (fail closed); the chart creates the index, so it is never missing ([Agents and the verified actor](../actors.md#how-the-actor-is-verified)). Gate override entries are checked the same way by `<fullname>-actor-overrides`, not by this policy: it matches only gates labelled `kardinal.io/override-check`, which `<fullname>-override-marker` requires on every write that adds an override. |
| `<fullname>-actor-records` | The actor stamps of who changed a Pipeline (a new `spec.holds` entry, a new `spec.lastChange`: a pause, a resume, a hold release) or rejected a Bundle (a new `spec.rejected`) say what the requester is, as `<fullname>-actor` checks a version's. It matches only Pipelines and Bundles labelled `kardinal.io/actor-check` (the object selector is matched before the API server converts the `AgentIndex` param, which costs every matched request in proportion to the index): the controller's writes and every other write that adds no stamp do not pay for the agent registry (#1936). |
| `<fullname>-actor-record-marker` | A Pipeline write that adds a hold or a `spec.lastChange`, and a Bundle update that adds a rejection, carries the label `kardinal.io/actor-check: "true"` (`kardinal pause`, `resume`, `hold`, `release-hold`, `reject`, the UI and the MCP tools set it), so `<fullname>-actor-records` checks its stamp; kubectl and scripts that write these fields add the label in the same write. No params. The controller is exempt. |
| `<fullname>-override-marker` | A PolicyGate write that adds or changes an override entry carries the label `kardinal.io/override-check: "true"` (`kardinal override` and the UI set it), so `<fullname>-actor-overrides` checks its stamp. The controller is exempt. |
| `<fullname>-agent-index` | Only this release's controller deletes the release's AgentIndex or writes its status, a single delete and a `deletecollection` alike, cluster-admins included: the actor policies decide with it. To delete it on purpose, delete this policy's binding first; the controller then recreates the index with the release's Helm ownership. `helm uninstall` keeps the index (`helm.sh/resource-policy: keep`) and removes this policy: delete it afterwards with `kubectl delete agentindex <release>`. |
| `<fullname>-audit-actor` | Only this release's controller (and `admission.controllerUsernames`) creates an AuditEvent that names an actor (`spec.actor`). |
| `<fullname>-agent-identities` | An AgentIdentity is deleted only once `spec.suspended` is true: deleting it turns its subjects into people or bots. |
| `<fullname>-graph-objects` | The objects a promotion Graph makes and nothing else should: PromotionSteps, PRStatuses, HookRuns, RenderRuns, ImageVerifications and per-promotion MetricCheck instances (label `kardinal.io/bundle`; a MetricCheck you write is not checked), spec, metadata and status. Only kro, this release's controller ServiceAccount and the exact usernames in `admission.controllerUsernames` may create or change them. kro counts only when it impersonates the namespace's Graph ServiceAccount: a request as that ServiceAccount that carries `authentication.kubernetes.io/credential-id` (a token from `kubectl create token`, a bound token) or `authentication.kubernetes.io/pod-name` (a Pod running as it) is refused, so `kubectl create token` and Pods running as the Graph ServiceAccount do not pass as kro. Admission cannot tell kro's impersonation from anyone else's: whoever may impersonate the Graph ServiceAccount, or read a legacy token Secret of it, passes as kro (see Trust below). The garbage collector and the namespace controller may update them, as they do while deleting objects (owner references, their `orphan`/`foregroundDeletion` finalizers); kardinal's own finalizers are removed by its controller. Deleting one is not checked, since the Graph recreates it (deleting a failed PromotionStep retries it), except HookRuns: only kro, the controllers, the garbage collector and the namespace controller delete them, and anyone else gets a message of its own ("HookRuns record that a hook ran; only kardinal deletes them ..."). The step's `status.hookRecords` keeps a recreated HookRun from running its hook again, but deleting the step as well (allowed: it is the documented retry) drops that record, and the Graph's new HookRun would run the hook a second time. Argo Rollouts AnalysisRuns labelled `kardinal.io/bundle` are protected on create only: the Rollouts controller writes them afterwards. No one may turn an existing MetricCheck into an instance (kro adopts by name, see below). |

Anyone who may impersonate other users or groups (`impersonate` RBAC) passes these checks as
whoever they impersonate: treat `impersonate` as full trust.

What a caller can do with overrides depends on its access. A caller allowed to update
PolicyGates (full edit) can add an override only in its own name, but can also remove any
override, or remove one and add it again in its own name (re-attribute it). Removing does not
erase the record: the controller keeps a record of every override it saw (`status.overrides`,
with its first-seen time) and its `GateOverridden` AuditEvent, and an entry added again keeps
its first-seen time, so it cannot restart the override cap. A caller that may only record
overrides (the `policygates/override` role) is append-only.

The controller records an override's `createdBy` as **verified** only when it first sees the
override while the `<fullname>-gate-overrides` policy and its `Deny` binding exist: the chart
passes their name (`--override-identity-policy`) and lets the controller `get` those two
objects. An override first seen while they are missing, or one already on a gate when the
upgrade that added the check ran, stays unverified: the gate reason, the AuditEvent and the UI
API (`createdByVerified: false`) say so.

A PolicyGate created ahead of kro under the name a gate instance will get cannot become that
instance: kro adopts an existing object of the name it applies and keeps the fields its template
does not set (an override, say), so the `<fullname>-gate-overrides` policy refuses every update
that adds `kardinal.io/bundle` to an existing PolicyGate, kro's included. The promotion then
waits until the squatter is deleted; kro creates the real instance in its place.

Each release exempts only its own controller, so each binds its policies only to the namespaces
it manages: the watched namespace in namespace mode, its shard's namespaces with
`controller.namespaceShard` (the `default` shard also the unlabelled ones), every namespace in
cluster mode. Releases still overlap in two cases, and then each must list the others' controller
ServiceAccounts, by exact username, in `admission.controllerUsernames`:

- two cluster-mode releases on one cluster (both bind every namespace);
- shards during a handoff: when a namespace changes shard (its `kardinal.io/shard` label), the
  previous shard's controller may still finish a write there, and the new shard's policies refuse
  it unless it is listed. List every shard's controller in every shard's release.

**Who is trusted as kro in a namespace.** The stock `edit` and `admin` roles grant `impersonate` on
ServiceAccounts and let their holders create legacy ServiceAccount token Secrets, so a user with
`edit` or `admin` in a namespace can act as its Graph ServiceAccount through either, and is
trusted as kro there: they can forge a gate instance, a PromotionStep or a PRStatus in that
namespace. kardinal's user roles (`<fullname>-viewer`, `-promoter` and `-approver`) grant neither `impersonate` nor `create` on Secrets. Give tenants kardinal's roles plus the stock `view` role,
not `edit`, where promotions must not be forged. A namespace's `edit`/`admin` holders are its
owners as far as kardinal can check (ledger G16 has the upstream ask that would close this).

**Impersonation is full trust.** Anyone allowed to impersonate (`impersonate` RBAC) passes every
check here as whoever they impersonate, the Graph ServiceAccount and the controller included; a
cluster admin can. In namespace mode the controller's ServiceAccount lives in the namespace the
release manages, so whoever may create Pods or tokens there can act as the controller: namespace
mode trusts the namespace's editors that far. A Pod's token carries `pod-name` just as the
controller's own does, so the rule that tells kro from a token cannot tell the controller from a
Pod running as it.

The checks exist only where the chart's policies are installed: the CRDs do not check the
names. Installing the CRDs alone (`kubectl apply -f config/crd/bases`) or deleting a policy
binding turns them off.

The CLI reads your username from the API server with a SelfSubjectReview (what
`kubectl auth whoami` shows): an OIDC user is often `oidc:alice@example.com`, a ServiceAccount
`system:serviceaccount:<namespace>:<name>`. The local OS user is not used. The policy has
`failurePolicy: Fail`, so a policy that cannot be evaluated denies the write. It needs
Kubernetes 1.35 or later, the chart's minimum.

---

## UI API Access Control

The embedded UI server runs on port `:8082` and serves two surfaces:

| Path prefix | Content | Default |
|---|---|---|
| `/ui/*` | React app (HTML/JS/CSS) | Public: no data, and it must load before the browser can send a token |
| `/api/v1/ui/*` | Pipeline state, Bundle history, gate details, and the promote, rollback, pause/resume and gate-approve actions | Local clients only, unless one of the modes below is enabled |

The UI API acts with the controller's own ServiceAccount. With no auth mode set, the
controller serves `/api/` only to clients that connect from loopback, which is how
`kubectl port-forward` arrives: the container runtime dials `localhost:<port>` inside the
pod's network namespace. Every other client, whether a pod, a node, a `NodePort`, a
`LoadBalancer` or an Ingress controller, gets `403`:

```
UI API: no UI auth mode is set, so only local clients (kubectl port-forward) are served; set Helm value ui.auth.tokenReview=true or ui.auth.tokenSecretRef.name (flags --ui-tokenreview-auth, --ui-auth-token)
```

The check is on the TCP peer address, not on the `Host` header, which any client can set.
A request that carries a header a proxy adds (`Forwarded`, `X-Forwarded-*`, `X-Real-Ip`,
`X-Envoy-*` or `l5d-*`) is not local either, even from loopback. There is no flag to turn
the check off. The controller logs a warning at startup while no auth mode is set.

!!! warning "With a sidecar mesh, set an auth mode"
    With a service-mesh sidecar in the controller pod (Istio, Linkerd), inbound traffic
    reaches the controller from the sidecar on a loopback address (`127.0.0.6` for Istio,
    `127.0.0.1` for Linkerd), so every client in the mesh looks local. The proxy-header
    check catches the usual sidecar configurations but is best effort: a sidecar can be
    set up to strip those headers. Any other container in the controller pod is local as
    well. When the controller pod runs with a sidecar, set one of the two modes below.

To reach the UI through an Ingress, a `NodePort` or a `LoadBalancer`, set one of the two
modes below. For single sign-on, where each person signs in with your identity provider and the
UI acts as that person, see [Option 3](#option-3-single-sign-on-through-an-oidc-proxy): it is
Option 2 behind an OIDC proxy. A proxy that injects Option 1's shared token makes every signed-in
user the controller, and the UI then refuses approvals.

### Option 1: shared UI token

Store a random token in a Secret and point the chart at it (`--ui-auth-token`, or the
`KARDINAL_UI_TOKEN` environment variable):

```bash
kubectl create secret generic kardinal-ui-token -n kardinal-system \
  --from-literal=token="$(openssl rand -hex 32)"

helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.11.0 \
  -n kardinal-system --reset-then-reuse-values \
  --set ui.auth.tokenSecretRef.name=kardinal-ui-token
```

Every `/api/v1/ui/*` request must then include:

```
Authorization: Bearer <token>
```

Requests without the token get `HTTP 401` with a `Www-Authenticate: Bearer realm="kardinal-ui"`
header. The comparison is constant-time. Everyone who has the token has full UI access:
there is no per-user authorization in this mode. **The shared token is a single-tenant admin
credential**: it acts as the controller, in every namespace the controller watches. Use it for
one team, or behind an authenticating proxy; for several teams, use Option 2 and the
[user roles](#user-roles).

### Option 2: Kubernetes tokens (TokenReview)

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.11.0 \
  -n kardinal-system --reset-then-reuse-values \
  --set ui.auth.tokenReview=true
```

This sets `--ui-tokenreview-auth=true`. Users sign in with a Kubernetes token minted for
kardinal's audience:

```bash
kubectl create token <service-account> -n <namespace> --audience kardinal-promoter
```

For every `/api/v1/ui/*` request the controller:

1. Validates the token with a `TokenReview` for the audiences in `tokenReview.audiences`
   (`--tokenreview-audiences`, default `kardinal-promoter`) and checks that the API server
   returns one of them in `status.audiences`. An invalid token, or one for another audience,
   gets `401`. A token for the `kardinal-promoter` audience is refused by the API server
   itself, so kardinal cannot replay it there. Kubeconfig tokens and OIDC ID tokens are for
   the API server's audience and are refused unless you opt in, separately for each API:
    - the UI API with `tokenReview.acceptAPIServerAudience=true`
      (`--tokenreview-accept-apiserver-audience`), which [single sign-on](#option-3-single-sign-on-through-an-oidc-proxy)
      needs;
    - the Bundle API with `bundleAPI.acceptAPIServerAudience=true`
      (`--bundle-api-tokenreview-accept-apiserver-audience`). CI does not need it: mint a token
      for `kardinal-promoter` instead.

   A ServiceAccount token for the API server's audience, the token every Pod mounts, is
   refused by both APIs even with the opt-in: a ServiceAccount can always mint a token for
   `kardinal-promoter`. A 401 from these rules names the rule in its body (`unauthorized: token
   audience is not kardinal-promoter; ...` or `unauthorized: a ServiceAccount token ...`), and so
   does the access log; any other 401 says only `unauthorized`. Opt in only if you trust kardinal and every hop to it with tokens that
   also work against the API server. Putting the API server's own audience in
   `tokenReview.audiences` is refused at startup (the controller reads it from its own
   ServiceAccount token), so the opt-in stays explicit.
2. Checks every object the request reads with a `SubjectAccessReview` for that user, and every
   write either on the object or, for pause, resume and gate approval, on its
   [action subresource](#user-roles). A denied check gets `403` naming the verb, resource and
   namespace. A user never sees more through the UI than `kubectl` would allow them.
3. Fails closed. If the review API cannot be reached, the request gets `503`. If the review
   clients cannot be built, the controller does not start.
4. Caches review results for 30 seconds per token and per action. A revoked token or a
   removed RoleBinding keeps working through the UI until 30 seconds after the last review
   that started before the revoke returned (a review is bounded by the API request's
   timeout): a review that started earlier never replaces a later one's result in the cache.
5. Limits the TokenReviews it sends before sending them: 60 a minute per client address
   and 600 a minute in all, shared by the UI API and the Bundle API. A cached token does not
   count, so only a client sending new tokens (guessing) reaches the limit; it gets `429`
   with `Retry-After: 60`. Behind an Ingress or a mesh sidecar every client has the proxy's
   address and shares the per-address budget.

When the chart enables this mode, it also grants the controller `create` on
`tokenreviews` and `subjectaccessreviews`. Setting a shared UI token as well is refused: the
install fails, and the controller does not start with both flags, because the shared token
would win and turn per-user RBAC off. To keep that behaviour anyway, set
`ui.auth.allowStaticTokenWithTokenReview=true` (`--ui-auth-static-overrides-tokenreview`).

The user needs these permissions:

| UI action | Permissions |
|---|---|
| View pipelines, bundles, gates and steps | `get`, `list` on `pipelines`, `bundles`, `policygates`, `promotionsteps` (`kardinal.io`) |
| View step events | `list` on `events` (core) |
| Create a Bundle | `get` on `pipelines`; `create` on `bundles` |
| Promote | `get` on `pipelines`; `list` on `promotionsteps` and `bundles`; `create` on `bundles` |
| Roll back | `get` on `pipelines` and `bundles`; `list` on `promotionsteps` and `bundles`; `create` on `bundles` |
| Pause, resume | `get` on `pipelines`; `update` on `pipelines/pause` |
| Roll back and hold, release a hold | as roll back; `update` on `pipelines/hold` |
| Approve a gate | `get` on `policygates`; `update` on `policygates/override`. Recording or revoking an Approval (`/api/v1/ui/approvals`: Approve, Decline, Revoke mine) needs `create` or `delete` on `approvals` |
| Override a gate in an environment (`/api/v1/ui/override`) | `get` on `pipelines` and `policygates`; `list` on `policygates` and `bundles`; `update` on `policygates/override` |
| Reject a Bundle | `update` on `bundles/reject` |

Each action is checked call by call, so it needs every verb in its row. Promote and roll
back read the Pipeline and the environment's history before they create the Bundle. The
reads are the view permissions, so a user who can view needs only the write verbs on top.
Pause, resume, gate approval and rejecting a Bundle are authorized on a virtual subresource
(`pipelines/pause`, `policygates/override`, `bundles/reject`) instead of `update` on the object:
the API server serves no such endpoint, but RBAC grants it and a `SubjectAccessReview` checks it.
The controller then makes the one change (`spec.paused` or `spec.pausedFleets`, one `spec.overrides` entry whose
`createdBy` is the caller, or `spec.rejected` whose `by` is the caller) with its own identity. The
bundle-rejection and gate-overrides admission policies admit a record in another user's name only
from the controller's own ServiceAccount (and `admission.controllerUsernames`). An override of a
gate in an environment checks `policygates/override` on every instance it writes, before it
writes any. A user who may pause cannot edit the
Pipeline, and one who may approve cannot change the gate's expression. The controller
manages the freeze gate itself, so the user needs no rights on `policygates` to pause.

The list views respect namespace RBAC. A user who may list a kind cluster-wide gets every
namespace; one bound only in some namespaces (a RoleBinding) gets exactly the objects of those
namespaces, and the rest are left out without an error; one who may list it nowhere gets an
empty list. A read of one object, and every write, is still checked on that object and refused
with `403`.
When the controller runs with `--watch-namespace`, lists are checked against that namespace.
The chart's [user roles](#user-roles) hold these permissions.

### Option 3: single sign-on through an OIDC proxy

People sign in with your identity provider (Keycloak, Okta, Entra ID, Google, Dex), and the UI
acts as each of them: RBAC applies per user, and an approval from the UI records the person.
This is Option 2 with three pieces in front of it, and needs no change to kardinal:

1. **The API server trusts the identity provider.** Configure an OIDC authenticator on the
   cluster, with a username and groups prefix so these users cannot collide with other
   identities:
    - on EKS: `aws eks associate-identity-provider-config --cluster-name <cluster> --oidc identityProviderConfigName=sso,issuerUrl=<issuer>,clientId=<client id>,usernameClaim=preferred_username,usernamePrefix=oidc:,groupsClaim=groups,groupsPrefix=oidc:`.
      The first requests after the association turns `ACTIVE` can still be refused for about a
      minute;
    - on a cluster you run: the API server's `--oidc-issuer-url`, `--oidc-client-id`,
      `--oidc-username-claim`, `--oidc-username-prefix`, `--oidc-groups-claim` and
      `--oidc-groups-prefix` flags, or the same in a structured `AuthenticationConfiguration`.

    A user then reaches the API server as `oidc:<username>` in the groups `oidc:<group>`.

2. **A proxy signs people in and forwards their ID token.** In front of the UI port (`8082`), the
   proxy runs the OIDC login and passes the user's ID token to kardinal as
   `Authorization: Bearer <token>`. With [oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/):

    ```text
    --provider=oidc
    --oidc-issuer-url=<issuer>
    --client-id=<client id>
    --client-secret=<client secret>
    --cookie-secret=<32 random bytes, base64>
    --email-domain=*
    --redirect-url=https://kardinal.example.com/ui/oauth2/callback
    --upstream=http://kardinal-promoter.kardinal-system.svc:8082/
    --pass-authorization-header=true
    --proxy-prefix=/ui/oauth2
    --api-route=^/api/v1/ui/
    --skip-provider-button=true
    --http-address=0.0.0.0:4180
    ```

    - `--pass-authorization-header` forwards the ID token, which is what kardinal reviews.
    - The ID token must carry the groups claim. Some providers add it only when the client asks
      for a scope: with Dex, add `--scope=openid email profile groups`.
    - `--http-address=0.0.0.0:4180` serves the proxy on the pod's address; its default,
      `127.0.0.1:4180`, is reachable only from inside the pod.
    - The UI loads from `/ui/` and calls `/api/v1/ui/`, both absolute paths, so the proxy must
      pass both. `--proxy-prefix=/ui/oauth2` keeps the proxy's own endpoints under `/ui/`.
    - `--api-route` makes the proxy answer an API call that has no session with `401` instead
      of a login redirect, which a script cannot follow. When a session ends, the UI then shows
      its own **Sign in to kardinal** dialog; reloading the page signs in through the proxy
      again.
    - The ID token must still be valid when it reaches kardinal. Have the proxy refresh it
      before it expires (oauth2-proxy `--cookie-refresh`, shorter than the provider's ID token
      lifetime).

3. **kardinal accepts the ID token.** Turn on TokenReview, accept tokens for the API server's
   own audience, and allow the host name people browse to:

    ```bash
    helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.11.0 \
      -n kardinal-system --reset-then-reuse-values \
      --set ui.auth.tokenReview=true \
      --set tokenReview.acceptAPIServerAudience=true \
      --set 'ui.allowedHosts={kardinal.example.com}'
    ```

    The OIDC authenticator is not audience-aware: it authenticates the ID token but returns no
    audience, so the review for `kardinal-promoter` refuses it and only the review for the API
    server's audience accepts it ([Option 2](#option-2-kubernetes-tokens-tokenreview), step 1).
    The opt-in applies to the UI API only. The Bundle API keeps accepting only tokens for
    `kardinal-promoter`, and neither API accepts a Pod's default ServiceAccount token.
    `ui.allowedHosts` is needed because the proxy passes the browser's `Host` header
    ([Host names](#host-names-dns-rebinding)).

Then bind people by their prefixed groups, as for any Kubernetes user, and name those groups in
approval gates:

```bash
kubectl create rolebinding release-managers-approve -n my-app \
  --clusterrole kardinal-promoter-approver --group oidc:release-managers
kubectl create rolebinding developers-view -n my-app \
  --clusterrole kardinal-promoter-viewer --group oidc:developers
```

```yaml
spec:
  approval:
    allowedGroups: [oidc:release-managers]
```

What people see: a release manager signs in through the identity provider with no kardinal
dialog, and approves from the Approvals page; the Approval holds `user: oidc:<name>`, their
groups and `kardinal.io/recorded-via: ui`, and the gate's reason says `approved by oidc:<name>`.
A developer bound only to the viewer role sees "Nothing waits on you"; a direct
`POST /api/v1/ui/approvals` is refused with `403` naming the verb, resource and namespace.

Trust: kardinal now receives tokens that also work against the API server, so treat the proxy,
kardinal and every hop between them as able to act as the signed-in users. Serve the UI over
TLS ([TLS Configuration](#tls-configuration)) and keep the proxy and kardinal in the same
cluster network.

### User roles

The chart installs four ClusterRoles (`rbac.userRoles.enabled`, default `true`) to bind people,
CI and bots to. Bind them with a **RoleBinding** in a namespace for that namespace only, or with a
ClusterRoleBinding for all. In TokenReview mode they decide what a caller can do through the UI
API and the Bundle API, and they work the same for `kubectl` and the `kardinal` CLI.

| ClusterRole | Grants | For |
|-------------|--------|-----|
| `<fullname>-viewer` | `get`, `list`, `watch` on every kardinal kind; `get`, `list`, `watch` on `events` | Dashboards, read-only users |
| `<fullname>-promoter` | viewer, `create` on `bundles`, `update` on `pipelines/pause`, `pipelines/hold` and `bundles/reject` | CI (Bundle API, `kardinal create bundle`), promote, roll back (and hold), pause and resume, and reject a Bundle in the UI |
| `<fullname>-approver` | viewer, `update` on `policygates/override` and `bundles/reject`, `create` and `delete` on `approvals` | Gate overrides and Bundle rejection in the UI, approvals |
| `<fullname>-admin` | every verb on every kardinal kind, `update` on `pipelines/pause`, `pipelines/hold`, `pipelines/edit`, `policygates/override`, `policygates/edit` and `bundles/reject`; aggregates the three above | Pipeline owners |

`<fullname>` is the release's full name: `kardinal-promoter` for the default release, so
`kardinal-promoter-viewer` and so on. Bind these roles explicitly; that is the only way to
grant kardinal access by default.

When the UI API refuses an action on a virtual subresource, its `403` names the role that
grants it, for example `forbidden: user "bob" cannot update policygates/override.kardinal.io …;
the chart's approver or admin role grants it`. The UI shows that text in the action's dialog.

To opt in to the built-in roles, set `rbac.userRoles.aggregateToDefaultRoles=true`. The roles
then also aggregate into Kubernetes' built-in roles: the viewer into `view`, the promoter and
approver into `edit`, and the admin rules into `admin`. A namespace RoleBinding to `view`,
`edit` or `admin` then grants the matching kardinal access. Opt in knowingly: everyone bound to
`edit` in a namespace can then both promote and approve gates there, so promoter and approver
are no longer kept apart.

```bash
# Team A's CI may create Bundles in team-a only; its release managers approve gates there.
kubectl create rolebinding ci-promoter -n team-a \
  --clusterrole=kardinal-promoter-promoter --serviceaccount=team-a:ci
kubectl create rolebinding release-managers -n team-a \
  --clusterrole=kardinal-promoter-approver --group=team-a-release-managers
```

Promote and roll back also read the environment's history (the viewer rules, included).

Neither the promoter nor the approver role grants `update` on Pipelines or PolicyGates. They
act through the UI API, which checks the action subresource and writes as the controller,
recording the caller (`kardinal.io/created-by` and `kardinal.io/requested-by` on Bundles,
`createdBy` on overrides and holds). The promoter also holds `pipelines/hold` (roll back and
hold, release a hold) and the approver `create` and `delete` on `approvals`. The admin role
holds `pipelines/edit` and `policygates/edit`, which mean "may change anything".

`kardinal pause`, `kardinal rollback --hold`, `kardinal unhold` (`release-hold`) and `kardinal override`
write from the user's kubeconfig, so they need `update` on the object.
`rbac.userRoles.directWrites=true` adds `update`/`patch` on `pipelines` to the promoter and
on `policygates` to the approver, and the chart's `<fullname>-scoped-writes`
ValidatingAdmissionPolicy (always installed, UPDATE only) limits such writes: a caller who
holds `pipelines/pause` or `pipelines/hold` but not `pipelines/edit` may change only
`spec.paused` and `spec.pausedFleets` (with `pipelines/pause`) and `spec.holds` (with `pipelines/hold`; the
`<fullname>-hold-writes` policy checks each entry) of a Pipeline, and one who holds
`policygates/override` but not `policygates/edit` only `spec.overrides` of a PolicyGate;
neither may change labels or annotations. The policy checks
with the API server's authorizer, so it applies to any binding, not only the chart's roles.
A caller with neither subresource is not limited by it: their RBAC decides. For these
callers the policy also keeps all of the metadata as it was, except what the API server sets
(managedFields, resourceVersion, generation): no labels, annotations, finalizers or
ownerReferences (an ownerReference to a deleted object would have the garbage collector
delete the gate). It makes `spec.overrides` append-only, requires `createdBy` to be the
caller on each new entry (as the `<fullname>-gate-overrides` policy does for everyone), and
requires an `expiresAt` at most the override cap (`controller.gateOverrideMaxMinutes`,
default 1440, the bound the UI and the PolicyGate reconciler apply) after its `createdAt`.
The admission policy cannot read the clock, so the PolicyGate reconciler enforces the cap in
time: it records when it first saw each override (`status.overrides`), ends an override at
the earlier of its `expiresAt` and that time plus the cap, and does not count an entry whose
`createdAt` is more than 5 minutes after it first saw it (condition `OverrideIgnored`, naming
the entries). Entries chained a cap apart in advance therefore give one cap, not one per
entry.

`directWrites` weakens two guarantees. First, separation of duties: the narrow roles become
direct write access, held back only by this policy. Second, the audit trail: such a write is
the user's own, and kardinal records no `requested-by` for it. Its `createdBy` is self-declared
and checked only by the policy. Prefer the UI API for pause and approval, and enable
`directWrites` only where people need the CLI.

What this separates, and what it does not:

- The approver role cannot create Bundles and the promoter role cannot override gates, so a
  CI token bound only to the promoter role cannot approve its own promotion.
- Nothing stops one person from holding both roles, or `edit`/`admin` when
  `aggregateToDefaultRoles` is on, which include them. Separation of duties holds only if your
  bindings keep the roles apart; review who is bound to both.
- An override records who made it; it is not a second person's sign-off on the change.

### Signing in from the browser

When the API answers `401`, the UI opens a **Sign in to kardinal** dialog. Paste the
shared UI token or a Kubernetes token. The token is kept in `sessionStorage` for that
browser tab only, is cleared when the tab closes, and is sent as the `Authorization`
header on every API call. If the token later stops working (it expired or was rotated),
the UI drops it and asks again.

### Cross-origin requests (CORS)

The UI calls its API on the same origin, which always works. A cross-origin request to
`/api/v1/ui/*` gets `403` unless its origin is listed in `--cors-allowed-origins`
(chart value `ui.corsAllowedOrigins`). `*` allows every origin and is for development
only.

### Host names (DNS rebinding)

A request counts as same-origin only when its `Host` header is one of the UI server's
own names:

- `localhost`, `127.0.0.1` and `::1`, which are always allowed (for `kubectl port-forward`);
- the names in `--ui-allowed-hosts` (env `KARDINAL_UI_ALLOWED_HOSTS`, chart value
  `ui.allowedHosts`). This is a comma-separated list of host names or IP addresses with
  no scheme, path or wildcard; a port is ignored.

The chart always passes the controller Service's DNS names: `<fullname>`,
`<fullname>.<namespace>`, `<fullname>.<namespace>.svc` and
`<fullname>.<namespace>.svc.cluster.local`. Add your Ingress host name, a
non-default cluster domain, or the node IP you browse to. Clients that come in that
way are not local, so they also need a UI auth mode (see above):

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.11.0 \
  -n kardinal-system --reset-then-reuse-values \
  --set 'ui.allowedHosts={kardinal.example.com}'
```

Checking `Origin` against `Host` alone would not be enough. Under DNS rebinding, a page
on `evil.example` gets its name re-resolved to the controller's address. The browser
then treats the page as same-origin with the controller, so the `Origin` and `Host`
headers match. To stop this:

- A request to any other `Host` never gets a same-origin grant. It is handled as
  cross-origin and needs `--cors-allowed-origins`.
- While UI auth is off, the controller answers every `/api/` request to any other
  `Host` with `403`, reads included and with or without an `Origin` header. A rebound
  page cannot read pipeline state or send a plain form post. Browsers send no `Origin`
  on a same-origin `GET`, so only the `Host` check stops those.
- Static assets under `/ui/` are not checked. They are the same for everyone and hold
  no pipeline data.
- While UI auth is on, a request to another `Host` without an `Origin` is served,
  because the token protects it.

This is the same defense Jupyter and webpack-dev-server use.

If the UI page loads but shows no data and its API calls fail with `403` and
`host not allowed; add it to --ui-allowed-hosts (Helm value ui.allowedHosts)`, add the
host name you browse to to `ui.allowedHosts`.

### Accessing the UI securely (before TLS is configured)

Until TLS is configured, the recommended access method is:

```bash
kubectl port-forward svc/kardinal-promoter -n kardinal-system 8082:8082
```

The Service is named after the Helm release (`<fullname>`, `kardinal-promoter` for a
release named `kardinal-promoter`). Then open the UI at `http://localhost:8082/ui/`. The
browser may display a warning when accessed over plain HTTP.

> **Production note**: A LoadBalancer or Ingress in front of port 8082 needs a UI auth mode (without one, its clients get `403`) and TLS. Use port-forward for operator access or configure TLS as described below.

### Browser security headers

Every response from the UI server (the app at `/ui/` and the API at `/api/v1/ui/*`) carries these headers:

| Header | Value | Why |
|---|---|---|
| `Content-Security-Policy` | `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'` | Scripts and API calls come from the UI's own origin only, and no other site can frame the UI. |
| `X-Frame-Options` | `DENY` | The same framing block, for older browsers. |
| `X-Content-Type-Options` | `nosniff` | The browser uses the declared content type and does not guess. |
| `Referrer-Policy` | `no-referrer` | Links to pull requests do not send the UI's address. |

Because of `frame-ancestors 'none'`, the UI cannot be embedded in another dashboard with an `<iframe>`. Open it in its own tab. If an Ingress or proxy in front of the controller adds its own `Content-Security-Policy`, the browser applies both, so the stricter one wins.

---

## TLS Configuration

Both the UI server (`:8082`) and the webhook/bundle-API server (`:8083`) support TLS via the `--tls-cert-file` and `--tls-key-file` flags (environment variables `KARDINAL_TLS_CERT_FILE` / `KARDINAL_TLS_KEY_FILE`).

When both flags are set, both servers serve HTTPS. When neither is set, both serve plain HTTP. Setting only one of the two flags is a startup error: the controller exits instead of silently serving plain HTTP. The Helm chart refuses `controller.tlsCertFile` without `controller.tlsKeyFile` (or the reverse) at install time.

### Helm: cert-manager integration (recommended)

Use cert-manager to provision a certificate and mount it as a Kubernetes Secret:

```yaml
# 1. Create a Certificate using cert-manager
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: kardinal-tls
  namespace: kardinal-system
spec:
  secretName: kardinal-tls
  duration: 2160h  # 90 days
  renewBefore: 360h
  dnsNames:
    - kardinal-promoter.kardinal-system.svc
    - kardinal-promoter.kardinal-system.svc.cluster.local
  issuerRef:
    name: internal-ca  # your private CA ClusterIssuer
    kind: ClusterIssuer
```

The issuer must be one that signs in-cluster names, such as a cert-manager
[CA issuer](https://cert-manager.io/docs/configuration/ca/) backed by your private CA.
Public ACME issuers such as Let's Encrypt cannot issue certificates for `.svc` names.
Clients then trust that CA (for example `curl --cacert ca.crt`).

```yaml
# 2. tls-values.yaml: mount the cert-manager Secret and point the controller at it
controller:
  tlsCertFile: /etc/kardinal-tls/tls.crt
  tlsKeyFile: /etc/kardinal-tls/tls.key
  extraVolumes:
    - name: kardinal-tls
      secret:
        secretName: kardinal-tls
  extraVolumeMounts:
    - name: kardinal-tls
      mountPath: /etc/kardinal-tls
      readOnly: true
```

Apply it with `helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.11.0 -f tls-values.yaml`.
The paths must point at a mounted certificate. The chart refuses a path that is not in a
`secret`, `projected` or `csi` volume mounted with `controller.extraVolumes` and
`controller.extraVolumeMounts` (in the mounted directory, or the file a `subPath` mount puts
there). If the files still cannot be read, for example a wrong file name in the Secret, the
controller exits at startup instead of serving plain HTTP.

Certificates that reach the container another way (a `hostPath` volume, files in a custom
image, or a volume a mutating webhook injects, such as the Vault Agent Injector's
`/vault/secrets`) do not pass that check. Set the paths with `controller.extraEnv` instead:

```yaml
controller:
  extraEnv:
    - name: KARDINAL_TLS_CERT_FILE
      value: /vault/secrets/tls.crt
    - name: KARDINAL_TLS_KEY_FILE
      value: /vault/secrets/tls.key
```

The chart does not check those paths; the controller exits at startup if it cannot open them.

### Self-signed certificates (development only)

Generate a self-signed cert for local testing:

```bash
openssl req -x509 -newkey rsa:4096 -keyout tls.key -out tls.crt \
  -days 365 -nodes -subj '/CN=localhost'

kubectl create secret tls kardinal-tls \
  --cert=tls.crt --key=tls.key \
  -n kardinal-system
```

> **Do not use self-signed certificates in production.** Use cert-manager or a trusted CA.

---

## Further Reading

- [Installation](../installation.md) — Helm values reference
- [Monitoring](monitoring.md) — Prometheus metrics
- [Every permission the chart grants](#every-permission-the-chart-grants)
