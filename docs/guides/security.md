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
| ClusterRole `kardinal-promoter-kro-watch` | Cluster | Lets kro watch the kinds a Graph renders; aggregated into kro's role when kro uses `rbac.mode=aggregation` |

What the namespaced rules grant:

| Resources | Verbs | Why |
|---|---|---|
| `secrets` | get | Pipeline `spec.git.secretRef` and the SCM token Secret. The controller reads Secrets straight from the API server, one by name, and never lists or watches them. In cluster mode `get` still reaches **every Secret in the cluster** by name, because the rule is in a ClusterRole |
| `events.k8s.io` `events` | create, patch | Events from every reconciler (the events.k8s.io/v1 API) |
| `events` (core) | get, list, watch, create, patch | The UI step event list reads Events through core/v1; leader election writes core Events |
| All kardinal.io kinds and their `/status` | full CRUD; get, update, patch on status | Reconcilers |
| `auditevents` | get, list, watch, create | Audit records are append-only |
| `graphs.kro.run` | full CRUD; get on `graphs/status` | One Graph per Bundle |
| `serviceaccounts`; `rolebindings`; `clusterroles` (bind, limited to the two Graph ClusterRoles) | get, create; get, list, create, update, delete; bind | The Graph identity. `list` is for the sweep that deletes reader bindings no Graph reads through; it runs in cluster mode only and touches only RoleBindings labeled `app.kubernetes.io/managed-by=kardinal-promoter` |
| `deployments`, `argoproj.io` `applications` and `rollouts`, Flux `kustomizations`, Flagger `canaries` | get, list, watch | Health adapters. `rbac.argocdApplicationsWrite=true` adds `patch` on Applications for `update.strategy: argocd` |

The cluster-scoped rules cover `changewindows` (read, and status writes), `namespaces` (get,
limited to `controller.watchNamespace` in namespace mode: the controller checks whether a Graph's
namespace is being deleted before it removes kro's finalizer) and, with
`ui.auth.tokenReview=true`, `tokenreviews` and `subjectaccessreviews` (create).

To see the exact rules for your values:

```bash
helm template kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0-rc.1 \
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

kardinal-promoter uses a GitHub Personal Access Token (PAT) to:

1. Open pull requests (one per environment promotion)
2. Read PR status (merged, closed, open)
3. Post comments on PRs (soak time, gate results, rollback evidence)

### Minimum required scopes (classic PAT)

| Scope | Why |
|---|---|
| `repo` | Read/write access to repositories (open PRs, push branches) |

No admin scopes are required. The token does **not** need:
- `admin:org`
- `admin:repo_hook`
- `delete_repo`
- `workflow`

### Fine-grained PAT (recommended)

GitHub fine-grained PATs give per-repository permissions:

| Permission | Level |
|---|---|
| `Contents` | Read and write (push branches) |
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

The controller reads the secret on every SCM operation — no restart required.

### Using OIDC instead of a PAT

The controller has no OIDC or GitHub App token exchange, and the chart has no
`github.auth` value. The controller only reads a token from the Secret in
`github.secretRef`. It watches that Secret and reloads the token without a restart. So a
short-lived GitHub App installation token works if something outside kardinal refreshes
the Secret before the token expires. The GitHub App needs `Pull requests: Read and write`
and `Contents: Read and write`. That refresher can be an External Secrets generator or a
CronJob.

---

## Namespace Isolation

### Controller namespace

The controller runs in `kardinal-system` by default. It watches CRDs across all namespaces but writes only to the namespaces where Pipelines are deployed.

### Policy gate scoping

PolicyGates are namespace-scoped:

- **Org-level gates** (`namespace: platform-policies`): mandatory for all pipelines targeting the matching environment. Teams cannot override them. An org gate reads `metrics.*` from the MetricChecks of its own org policy namespace, so a team MetricCheck of the same name cannot decide it.
- **Team-level gates** (team namespace): additive — injected alongside org gates. Teams add restrictions, not bypasses.

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: platform-policies
  labels:
    kardinal.io/policy-namespace: "true"
```

Configure the org policy namespace via the controller flag `--policy-namespaces platform-policies`.

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

#### Known limit: the shared SCM token

Git clone and push use the Pipeline's `git.secretRef` token, but the controller opens, labels,
comments on and closes PRs with its own SCM token (`github.token` or `github.secretRef`, the
controller Pod's `GITHUB_TOKEN`). So anyone who can create a Pipeline, in any namespace, can have
PRs opened in any repository that token can write to. Restricting the repositories is tracked in
[#1332](https://github.com/pnz1990/kardinal-promoter/issues/1332) (`scm.allowedRepositories`).
Until then:

1. Scope the controller's SCM token to the GitOps repositories kardinal manages, for example a
   fine-grained PAT limited to those repositories.
2. Give each team its own `git.secretRef` token in its namespace, scoped to that team's
   repositories.
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
helm install kardinal-team-a oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0-rc.1 \
  --namespace team-a \
  --create-namespace \
  --set controller.watchNamespace=team-a \
  --set github.secretRef.name=github-token

# Team B — separate install watching only the "team-b" namespace
helm install kardinal-team-b oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0-rc.1 \
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

**When this is not appropriate**: if you have a central platform team that needs
read access across all team namespaces for observability (e.g. `kardinal get pipelines
--all-namespaces`), the per-namespace model will not provide that. In this case,
consider running a read-only cluster-scoped installation alongside the namespace-scoped
installs, using RBAC to restrict writes.

#### Additional isolation steps

1. Set `networkPolicy.enabled=true` (the default is `false`) and narrow `networkPolicy.ingressFrom.*` for each install (see [NetworkPolicy](#networkpolicy))
2. Give each team its own SCM token, scoped to that team's GitOps repositories, so a leaked token only reaches one team's repos
3. Keep each team's token `Secret` in that team's namespace — never share a token across namespace installs

---

## Secret Management

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
spec is set at creation and never mutated. Kubernetes RBAC can be used to prevent deletion, satisfying SOC 2,
ISO 27001, and FedRAMP audit trail requirements.

### Events written automatically

| Action | Trigger |
|---|---|
| `PromotionStarted` | Bundle begins promoting through an environment |
| `PromotionSucceeded` | Health check passed; PromotionStep reached Verified |
| `PromotionFailed` | PromotionStep reached Failed or AbortedByAlarm |
| `PromotionSuperseded` | A newer Bundle superseded an in-flight promotion |
| `GateEvaluated` | PolicyGate instance first evaluated, and every later change of readiness (blocked or unblocked); one record per change |
| `RollbackStarted` | `onHealthFailure: rollback` triggered a rollback Bundle |
| `RollbackSucceeded` | A PromotionStep of a rollback Bundle (from `kardinal rollback`, the UI, a RollbackPolicy or `onHealthFailure: rollback`) reached Verified; written besides `PromotionSucceeded`, one record per step |

### Fields on every event

| Field | Description |
|---|---|
| `spec.timestamp` | RFC 3339 time when the event occurred |
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
`OVERRIDDEN by <createdBy>: <reason>`. The value is:

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
# List the audit events in the current namespace (most recent first)
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

With **Fluentd / Vector / Fluent Bit**: configure a Kubernetes input that tails the
`auditevents` resource and forwards to your SIEM sink (Splunk, Datadog, OpenSearch,
etc.). The structured JSON output above is the recommended log format.

### RBAC: preventing deletion

By default the controller's service account creates AuditEvents but cannot delete
them. To prevent all users from deleting audit records, apply:

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

---

## NetworkPolicy

By default, no NetworkPolicy is applied. In environments with a NetworkPolicy-capable CNI (Calico, Cilium, etc.), enable the built-in policy to restrict the controller's network access:

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0-rc.1 \
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

### Outbound requests to user URLs

NotificationHook webhooks (`spec.webhook.url`), MetricCheck queries (`spec.prometheusURL`)
and Subscription polls (`spec.image.registry` with its token realm, `spec.git.repoURL`)
send HTTP requests from the controller to a URL a user wrote into a resource. The controller
refuses to connect when the address is one of these:

- loopback (`127.0.0.0/8`, `::1`), which includes the controller's own UI API;
- link-local (`169.254.0.0/16`, `fe80::/10`), which holds the cloud metadata and credential
  endpoints `169.254.169.254`, `169.254.170.2` and `169.254.170.23`;
- other cloud metadata addresses: `fd00:ec2::254`, `fd00:ec2::23`, `fd20:ce::254`,
  `100.100.100.200` and `168.63.129.16`;
- unspecified (`0.0.0.0/8`, `::`) and multicast addresses.

The check runs when the connection is opened, on the resolved address, for every
connection including redirects. A host name that resolves, or later re-resolves, to one of
these addresses is refused too. The failure reads `destination address is not allowed:
127.0.0.1 is loopback` and appears where that resource reports errors: NotificationHook
`status.failureMessage`, MetricCheck `status.reason` or Subscription `status.message`.

Private ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`) and public
addresses are allowed, because in-cluster Services, Prometheus, registries and Git servers
are the normal targets.
To narrow egress further, enable the NetworkPolicy and list the allowed destinations in
`networkPolicy.extraEgress`.

NotificationHook, MetricCheck and Subscription requests honour `HTTP_PROXY`, `HTTPS_PROXY` and
`NO_PROXY`.
Through a proxy, the controller connects to the proxy, so before it sends a request there it
checks the target itself: an IP address against the list above, and a host name by resolving
it and checking every address it resolves to. This applies to every request, including
redirects. A host name the controller cannot resolve is refused, because it cannot be checked.
The proxy resolves the name again, and DNS can give it a different answer, so the proxy must
also enforce its own egress policy. The proxy's own address is checked too: a proxy on a
loopback address is refused.

---

## Admission Validation

The CRDs validate their fields with OpenAPI schema rules, so `kubectl apply` rejects bad
values before they are stored. Examples: enum fields such as `update.strategy`, non-empty
required strings such as `PolicyGate` `spec.expression`, the Go duration format of
`spec.recheckInterval` (`5m`, `30s`, `1h`), and the rules for environment names. This works
on every supported Kubernetes version and needs no admission webhook.

Full CEL syntax validation (catching invalid CEL expressions) requires a validating webhook — see issue #317.

The chart no longer installs a `ValidatingAdmissionPolicy`. The
`validatingAdmissionPolicy.enabled` value is deprecated and has no effect.

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

helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0-rc.1 \
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
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0-rc.1 \
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
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0-rc.1 \
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

When both flags are set, both servers serve HTTPS. When neither is set, both serve plain HTTP. Setting only one of the two flags is a startup error: the controller exits instead of silently serving plain HTTP.

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
    - kardinal-promoter.kardinal-system.svc.cluster.local
  issuerRef:
    name: letsencrypt-prod  # your ClusterIssuer
    kind: ClusterIssuer
```

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

Apply it with `helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0-rc.1 -f tls-values.yaml`.
The paths must point at a mounted certificate: if the files cannot be read, the controller
exits at startup instead of serving plain HTTP.

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
