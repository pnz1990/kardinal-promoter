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
| `auditevents` | get, list, watch, create | Audit records are append-only |
| `graphs.kro.run` | full CRUD; get on `graphs/status` | One Graph per Bundle |
| `serviceaccounts`; `rolebindings`; `clusterroles` (bind, limited to the two Graph ClusterRoles) | get, create; get, list, create, update, delete; bind | The Graph identity. `list` is for the sweep that deletes reader bindings no Graph reads through; it runs in cluster mode only and touches only RoleBindings labeled `app.kubernetes.io/managed-by=kardinal-promoter` |
| `deployments`, `argoproj.io` `applications` and `rollouts`, Flux `kustomizations`, Flagger `canaries` | get, list, watch | Health adapters. `rbac.argocdApplicationsWrite=true` adds `patch` on Applications for `update.strategy: argocd` |
| `replicasets` | get | The `resource` and `flux` health adapters read, by name, the ReplicaSet a Deployment's `ProgressDeadlineExceeded` names, to tell whether the rollout of the current pod template stalled |
| `pods` | list | While a Deployment's replicas are unavailable, the `resource` health adapter lists, uncached, the pods of its new ReplicaSet (by label selector) to name why one is not ready (`ErrImagePull`, `CrashLoopBackOff`). In cluster mode `list` reaches **every pod in the cluster**, which includes pod specs and their literal `env` values (not Secret contents). Use `controller.watchNamespace` to limit it, or remove the rule in RBAC of your own: the health messages then leave the pod out and nothing else changes |

The cluster-scoped rules cover `changewindows` (read, and status writes), `namespaces` (get,
limited to `controller.watchNamespace` in namespace mode: the controller checks whether a
namespace is being deleted before it translates a Bundle, removes kro's finalizer from a Graph, or
cleans up a deleted step's PR) and, with
`ui.auth.tokenReview=true`, `tokenreviews` and `subjectaccessreviews` (create).

To see the exact rules for your values:

```bash
helm template kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --namespace kardinal-system -f my-values.yaml \
  --show-only templates/clusterrole.yaml
```

To keep Secret access out of other namespaces, run in namespace mode
(`controller.watchNamespace`), which turns the namespaced rules into a Role in that namespace.
The release-namespace Role also grants `get` on the SCM token Secret, limited to its name
(`github.secretRef.name`, or the Secret the chart creates from `github.token`). The rule is
left out when neither is set.

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
| `Metadata` | Read (required by GitHub for all fine-grained PATs) |

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

### Using OIDC instead of a PAT

The controller has no OIDC or GitHub App token exchange, and the chart has no
`github.auth` value. The controller only reads a token from the Secret in
`github.secretRef`. It polls that Secret every 30 seconds and reloads the token without a
restart. So a short-lived GitHub App installation token works if something outside kardinal
refreshes the Secret before the token expires. The GitHub App needs `Pull requests: Read and write`
and `Contents: Read and write`. That refresher can be an External Secrets generator or a
CronJob.

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

The namespace is kardinal's tenancy unit. There is no Project CRD, and none is planned
([#1187](https://github.com/pnz1990/kardinal-promoter/issues/1187)).

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
token (`github.token` or `github.secretRef`, the controller Pod's `GITHUB_TOKEN`) to open, label,
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
```

Each entry is `host/repository`: the SCM host, and the repository as the SCM API names it,
`owner/repo` (GitHub, Forgejo, Gitea, Bitbucket), the full project path (GitLab), or
`organization/project/repository` on `dev.azure.com` (Azure DevOps, also for
`<org>.visualstudio.com` and SSH remotes). Matching ignores case, and a scheme, user, port or
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
Secret is missing is checked again every minute. `kardinal validate --allowed-repositories
<list>` reports the same before you apply the file (offline it takes the Secret to exist). When
the value is empty, every repository is allowed, as before, and the controller logs a warning at
startup.

Also:

1. Scope the controller's SCM token to the GitOps repositories kardinal manages, for example a
   fine-grained PAT limited to those repositories.
2. Give each team its own `git.secretRef` token in its namespace, scoped to that team's
   repositories, so a team's clones and pushes are limited by its own token.
3. Grant `create` on `pipelines.kardinal.io` only to people you trust with the controller
   token's reach.

#### Stronger isolation: one install per team namespace

When teams must not share a controller or its SCM token, run one kardinal installation per
team namespace. This uses the `controller.watchNamespace` Helm value (added in v0.6.0) to limit each
controller to a single namespace, with a `Role`/`RoleBinding` scoped to that namespace
instead of a cluster-wide `ClusterRole`.

**Example: two teams, two installs**

```bash
# Team A — installs kardinal watching only the "team-a" namespace
helm install kardinal-team-a oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --namespace team-a \
  --create-namespace \
  --set controller.watchNamespace=team-a \
  --set github.secretRef.name=github-token

# Team B — separate install watching only the "team-b" namespace
helm install kardinal-team-b oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  --namespace team-b \
  --create-namespace \
  --set controller.watchNamespace=team-b \
  --set github.secretRef.name=github-token
```

Each install creates a `Role` and `RoleBinding` scoped to its watch namespace. Team A
cannot see or modify Team B's Pipelines, Bundles, or PolicyGates.

**Cost**: one controller replica per team namespace. For 20 teams, this means 20
controller pods. Each controller is lightweight (~50 MB RAM), but the operational
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

Other Secret references adopt the rule as they are added. A resource that refuses a Secret
reports reason `SecretNotReferenceable` where it reports errors. Labeling the Secret takes
effect at the next reconcile; removing the label stops its use again. Whoever may `update` or
`patch` Secrets in the namespace may set the label, so grant that as narrowly as reading them.

Pipeline `spec.git.secretRef` has the same exposure (its token goes to the Pipeline's git URL)
and will follow the rule in v0.11 ([#1506](https://github.com/pnz1990/kardinal-promoter/issues/1506));
label your git Secrets now. The controller's own SCM token Secret is set by the operator and
is not covered.

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

---

## Audit Logging

kardinal writes an immutable `AuditEvent` CRD record at every significant promotion
lifecycle transition, in the Pipeline's namespace. AuditEvents are append-only — the
spec is set at creation and never mutated. Kubernetes RBAC controls who can delete them (see
[RBAC: read-only access to audit records](#rbac-read-only-access-to-audit-records)).

### Events written automatically

| Action | Trigger |
|---|---|
| `PromotionStarted` | Bundle begins promoting through an environment |
| `PromotionSucceeded` | Health check passed; PromotionStep reached Verified |
| `PromotionFailed` | PromotionStep reached Failed or AbortedByAlarm |
| `PromotionSuperseded` | A newer Bundle superseded an in-flight promotion |
| `PromotionRejected` | `kardinal reject` cancelled an in-flight promotion (its Bundle was [rejected](../rollback.md#reject-a-bundle)) |
| `GateOverridden` | An entry in a gate instance's `spec.overrides[]` (`kardinal override` or the UI), written once per entry with its verified `createdBy`, stage, expiry and reason |
| `ApprovalRecorded` / `ApprovalRevoked` | An approval gate saw a decision (`kardinal approve`) appear, or its Approval deleted, with the approver, the decision and whether it counts |
| `GateEvaluated` | PolicyGate instance first evaluated, and every later change of readiness (blocked or unblocked); one record per change |
| `RollbackStarted` | `onHealthFailure: rollback` triggered a rollback Bundle |
| `RollbackSucceeded` | A PromotionStep of a rollback Bundle (from `kardinal rollback`, the UI, a RollbackPolicy or `onHealthFailure: rollback`) reached Verified; written besides `PromotionSucceeded`, one record per step |

### Fields on every event

| Field | Description |
|---|---|
| `spec.timestamp` | RFC 3339 time when the event occurred, stored to the second |
| `metadata.annotations["kardinal.io/created-at"]` | The same time with nanoseconds: it orders events within one second, such as a gate that flips twice in a second (`kardinal get auditevents` sorts by it). Events written before v0.10.0 do not have it |
| `spec.pipelineName` | Name of the Pipeline |
| `spec.bundleName` | Name of the Bundle being promoted |
| `spec.environment` | Environment name (e.g. `prod`) |
| `spec.action` | One of the action values in the table above |
| `spec.outcome` | `Success`, `Failure`, or `Pending` |
| `spec.message` | Human-readable description |

An AuditEvent does not record who acted. A Bundle made by a promote or a
rollback, or created from the UI, names who asked for it in its
`kardinal.io/requested-by` annotation. A gate approval records the same value
as the override's `createdBy`, which the gate's reason shows as
`OVERRIDDEN by <createdBy>: <reason> (expires <time>)`. The value is:

- **UI with TokenReview auth** (`ui.auth.tokenReview`): the caller's Kubernetes
  username, as the API server returned it for their token, for example
  `system:serviceaccount:team-a:deployer`.
- **UI with the static token, or with no UI auth**: `kardinal-ui`. The UI
  cannot tell its callers apart in these modes.
- **CLI** (`kardinal promote`, `kardinal rollback`, `kardinal override`): the
  local username on the machine that ran it. Nothing verifies it.
- **Automatic rollback**: `kardinal-controller (...)`, naming what triggered it.

A rollback Bundle keeps the restored build's provenance, so its
`spec.provenance.author` is that build's author, not who rolled back.

Anyone who may create Bundles can also set the annotation, and anyone who may
update PolicyGates can set `createdBy`, so for a verified identity read the
Kubernetes API server audit log. It names the kubeconfig user who created a
CLI Bundle. The UI creates Bundles and approves gates with the controller's
ServiceAccount, so for a UI action the audit log names the controller, and in
TokenReview mode the annotation or `createdBy` names the user.

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
```

### SIEM integration

Export AuditEvents as structured JSON for forwarding to your SIEM:

```bash
# JSON dump of all events in every namespace (pipe to your log forwarder)
kubectl get auditevents -A -o json \
  | jq -c '.items[] | {
      ts: .spec.timestamp,
      pipeline: .spec.pipelineName,
      bundle: .spec.bundleName,
      env: .spec.environment,
      action: .spec.action,
      outcome: .spec.outcome,
      message: .spec.message
    }'
```

Log forwarders such as Fluent Bit and Vector read container logs, not custom resources. Run
the command above on a schedule (for example a CronJob) and forward its output to your SIEM
(Splunk, Datadog, OpenSearch, etc.).

### RBAC: read-only access to audit records

The controller's ServiceAccount can create AuditEvents but cannot update or delete them.
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

The access log covers the HTTP APIs. What the controller then does (promotions, gate
results, rollbacks) is in the AuditEvents above, with the caller in `kardinal.io/requested-by`
where the UI or Bundle API made the change. Ship the controller's log to your SIEM to keep
both records.

---

## NetworkPolicy

By default, no NetworkPolicy is applied. In environments with a NetworkPolicy-capable CNI (Calico, Cilium, etc.), enable the built-in policy to restrict the controller's network access:

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
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
`spec.git.secretRef` is not covered: it must be in the Pipeline's namespace and is sent only to
the Pipeline's git and SCM hosts.

### Outbound requests to user URLs

NotificationHook webhooks (`spec.webhook.url`), MetricCheck queries (`spec.prometheusURL`,
`datadog.address`, `cloudWatch.endpoint`, `newRelic.address`, `web.url`)
and Subscription polls (`spec.image.registry` with its token realm, `spec.git.repoURL`,
`spec.helm.repoURL`) send HTTP requests from the controller to a URL a user wrote into a resource. The controller
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
(`/webhook/subscriptions/...`, see [Subscription webhooks](../subscription-webhooks.md))
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
controller egress allowlist (`--egress-allowlist`, #1474) or the chart NetworkPolicy
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

The chart's only `ValidatingAdmissionPolicy` objects are the identity policies below. The
`validatingAdmissionPolicy.enabled` value is deprecated and has no effect: the identity
policies are always installed.

### Verified identity

Records that name a person are checked by the API server, not trusted from the client. The
chart installs a `ValidatingAdmissionPolicy` with a `Deny` binding, per release:

| Policy | Checks |
|---|---|
| `<release>-bundle-rejection` | A Bundle's new `spec.rejected.by` ([`kardinal reject`](../rollback.md#reject-a-bundle)) equals the requesting user's `request.userInfo.username`. A rejection already set is immutable (CRD rule), so it is checked only when it is first written. |
| `<release>-gate-overrides` | Every new or changed `spec.overrides[]` entry of a PolicyGate ([`kardinal override`](../policy-gates.md#emergency-overrides-k-09)) has `createdBy` equal to the requesting user; the controller's ServiceAccount is exempt, because it writes overrides for the UI. Only the namespace's Graph ServiceAccount (kro) and the controller may create a gate instance (label `kardinal.io/bundle`, checked on CREATE and UPDATE) or change anything in it but `spec.overrides`: the rest of the spec and the whole metadata (labels, annotations, owner references, finalizers) are frozen, except `managedFields`, `resourceVersion` and `generation`, which the API server writes, and the `kardinal.io/force-recheck` annotation, which forces a re-evaluation. The garbage collector and the namespace controller may update an instance (they remove owner references and finalizers). In namespace mode (`controller.watchNamespace`) it applies to the watched namespace only. |
| `<release>-approvals` | An `Approval` ([`kardinal approve`](../policy-gates.md#approval-gates)) is created only with `spec.user` equal to the requesting user and `spec.groups` among the requester's groups (checked at create time only), and its `kardinal.io/bundle` and `kardinal.io/environment` labels always equal `spec.bundle` and `spec.environment`. It may be owned only by the Bundle it approves (`spec.bundle`, `spec.bundleUID`), and its owner references cannot change later. Only its approver may delete (revoke) it; the garbage collector and the namespace controller are exempt (kardinal's controller is not: it never deletes Approvals). The spec is immutable (CRD rule). |
| `<release>-bundle-creator` | A new Bundle's `kardinal.io/created-by` annotation, when set, equals the requesting user, and it cannot be added, changed or removed later. Exactly this release's controller ServiceAccount is exempt, plus the usernames listed in `admission.controllerUsernames` (exact names; an entry ending in `*` is a prefix, meant for test rigs; never list a tenant's namespace: in namespace mode the release namespace is the tenant's): it names the creator of the Bundles it creates (the UI user, `subscription:<name>`, `bundle-api`, `kardinal-controller`). Another controller instance that is not listed creates its Bundles without a creator, which excludeAuthor gates hold. An approval gate's `excludeAuthor` reads it. |

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
override while the `<release>-gate-overrides` policy and its `Deny` binding exist: the chart
passes their name (`--override-identity-policy`) and lets the controller `get` those two
objects. An override first seen while they are missing, or one already on a gate when the
upgrade that added the check ran, stays unverified: the gate reason, the AuditEvent and the UI
API (`createdByVerified: false`) say so.

The checks exist only where the chart's policies are installed: the CRDs do not check the
names. Installing the CRDs alone (`kubectl apply -f config/crd/bases`) or deleting a policy
binding turns them off.

The CLI reads your username from the API server with a SelfSubjectReview (what
`kubectl auth whoami` shows): an OIDC user is often `oidc:alice@example.com`, a ServiceAccount
`system:serviceaccount:<namespace>:<name>`. The local OS user is not used. The policy has
`failurePolicy: Fail`, so a policy that cannot be evaluated denies the write. It needs
Kubernetes 1.30 or later, the chart's minimum.

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
modes below. An authenticating reverse proxy in front of the UI (for example
oauth2-proxy) can use Option 1 and inject the shared token as the `Authorization` header.

### Option 1: shared UI token

Store a random token in a Secret and point the chart at it (`--ui-auth-token`, or the
`KARDINAL_UI_TOKEN` environment variable):

```bash
kubectl create secret generic kardinal-ui-token -n kardinal-system \
  --from-literal=token="$(openssl rand -hex 32)"

helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  -n kardinal-system --reset-then-reuse-values \
  --set ui.auth.tokenSecretRef.name=kardinal-ui-token
```

Every `/api/v1/ui/*` request must then include:

```
Authorization: Bearer <token>
```

Requests without the token get `HTTP 401` with a `Www-Authenticate: Bearer realm="kardinal-ui"`
header. The comparison is constant-time. Everyone who has the token has full UI access:
there is no per-user authorization in this mode.

### Option 2: Kubernetes tokens (TokenReview)

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
  -n kardinal-system --reset-then-reuse-values \
  --set ui.auth.tokenReview=true
```

This sets `--ui-tokenreview-auth=true`. Users sign in with a Kubernetes token, for example
`kubectl create token <service-account> -n <namespace>`. For every `/api/v1/ui/*` request
the controller:

1. Validates the token with a `TokenReview`. An invalid token gets `401`.
2. Checks every object the request reads or writes with a `SubjectAccessReview` for that
   user. A denied check gets `403` naming the verb, resource and namespace. A user never
   sees or changes more through the UI than `kubectl` would allow them.
3. Fails closed. If the review API cannot be reached, the request gets `503`. If the review
   clients cannot be built, the controller does not start.
4. Caches review results for 30 seconds per token and per action. A revoked token or a
   removed RoleBinding keeps working through the UI for up to 30 seconds.

When the chart enables this mode, it also grants the controller `create` on
`tokenreviews` and `subjectaccessreviews`. If a shared UI token is set as well, the shared
token wins and TokenReview is not used.

The user needs these permissions:

| UI action | Permissions |
|---|---|
| View pipelines, bundles, gates and steps | `get`, `list` on `pipelines`, `bundles`, `policygates`, `promotionsteps` (`kardinal.io`) |
| View step events | `list` on `events` (core) |
| Create a Bundle | `get` on `pipelines`; `create` on `bundles` |
| Promote | `get` on `pipelines`; `list` on `promotionsteps` and `bundles`; `create` on `bundles` |
| Roll back | `get` on `pipelines` and `bundles`; `list` on `promotionsteps` and `bundles`; `create` on `bundles` |
| Pause, resume | `get`, `update` on `pipelines` |
| Approve a gate | `get`, `update` on `policygates` |

Each action is checked call by call, so it needs every verb in its row. Promote and roll
back read the Pipeline and the environment's history before they create the Bundle. The
reads are the view permissions, so a user who can view needs only the write verbs on top.
Pause and resume only set `spec.paused` on the Pipeline. The controller manages the freeze
gate itself, so the user needs no rights on `policygates` to pause.

The list views read all namespaces, so the user needs a ClusterRole bound with a
ClusterRoleBinding. When the controller runs with `--watch-namespace`, lists are checked
against that namespace only, and a Role and RoleBinding there are enough.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kardinal-ui-viewer
rules:
  - apiGroups: ["kardinal.io"]
    resources: ["pipelines", "bundles", "policygates", "promotionsteps"]
    verbs: ["get", "list"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["list"]
---
# Bind this as well to let the user act: create Bundles, promote, roll back,
# pause and resume Pipelines, and approve gates.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kardinal-ui-operator
rules:
  - apiGroups: ["kardinal.io"]
    resources: ["bundles"]
    verbs: ["create"]
  - apiGroups: ["kardinal.io"]
    resources: ["pipelines", "policygates"]
    verbs: ["update"]
```

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
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 \
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

Apply it with `helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0 -f tls-values.yaml`.
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
- [FAQ](../faq.md#what-permissions-does-the-controller-need) — RBAC questions
