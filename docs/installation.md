# Installation

This guide covers installing kardinal-promoter in a Kubernetes cluster using Helm.

---

## Prerequisites

| Requirement | Version | Notes |
|---|---|---|
| Kubernetes | ≥ 1.28 | kind, EKS, GKE, or any conformant cluster |
| kubectl | ≥ 1.28 | Matches your cluster version |
| Helm | ≥ 3.12 | `brew install helm` |
| GitHub token | — | Personal access token with `repo` scope |
| kro | ≥ 0.10.0-rc.0 | Graph controller with the `GraphKind` feature gate — see [Install kro](#install-kro) |

!!! tip "Trying it out locally?"
    Use [kind](https://kind.sigs.k8s.io/) for a single-node local cluster.
    See the [Quickstart](quickstart.md) for the fast path.

---

## Install kro

kardinal-promoter renders one kro [Graph](https://kro.run/next/docs/concepts/graph/overview/)
(`kro.run/v1alpha1`) per Bundle. [kro](https://github.com/kubernetes-sigs/kro) is a separate
prerequisite; the kardinal-promoter chart does not bundle it.

```bash
# From a kardinal-promoter checkout
bash hack/install-kro.sh
```

The script installs the kro Helm chart (`oci://registry.k8s.io/kro/charts/kro`) into `kro-system`
with `config.featureGates.GraphKind=true` and `rbac.mode=aggregation`, then server-side applies the
kro CRDs. Override the version with `KRO_VERSION=<version>`.

!!! warning "Version compatibility"
    The kro version kardinal-promoter is tested against is pinned in `hack/install-kro.sh`.

---

## Install kardinal-promoter

### 1. Create the GitHub token secret

kardinal-promoter needs a GitHub personal access token to open and monitor pull requests.
The token requires the `repo` scope (contents read/write, pull requests read/write).

```bash
kubectl create namespace kardinal-system

kubectl create secret generic github-token \
  --namespace kardinal-system \
  --from-literal=token=<your-github-token>
```

### 2. Install with Helm

```bash
helm install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter \
  --namespace kardinal-system \
  --create-namespace \
  --set github.secretRef.name=github-token
```

This installs the kardinal CRDs (from the chart's `crds/` directory), the **kardinal-promoter
controller** in the `kardinal-system` namespace, and the ClusterRoles bound to the Graph identity
(`<release>-graph-applier`, `<release>-graph-reader`).

Helm installs `crds/` only on the first install and never updates or deletes it. See
[Upgrade](#upgrade) for how to update the CRDs.

The chart ships `values.schema.json`, which rejects unknown keys: a misspelled `--set` fails the
install instead of being ignored.

Verify both controllers are running:

```bash
kubectl get pods -n kardinal-system
# NAME                                              READY   STATUS    RESTARTS   AGE
# kardinal-promoter-6b6c8c8446-jc79g               1/1     Running   0          30s

kubectl get pods -n kro-system
# NAME                              READY   STATUS    RESTARTS   AGE
# kro-7d4b8f9f5-xk2pq               1/1     Running   0          30s
```

---

## Helm values reference

### kardinal-promoter controller

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | Number of controller replicas |
| `image.repository` | `ghcr.io/pnz1990/kardinal-promoter/controller` | Controller image |
| `image.tag` | Chart `appVersion` | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `logLevel` | `info` | Log verbosity (`debug`, `info`, `warn`, `error`). Sets both `--log-level` and `--zap-log-level` (`warn` maps to `error` there) |
| `leaderElect` | `true` | Enable leader election (required for HA) |
| `github.secretRef.name` | `""` | Existing Secret (release namespace) holding the SCM token. Recommended |
| `github.secretRef.key` | `token` | Key in the Secret |
| `github.token` | `""` | Token value. The chart stores it in Secret `<release>-github-token`; the value stays in the Helm release history. Setting both this and `secretRef.name` fails |
| `scm.provider` | `""` | `--scm-provider`: `github` (default), `gitlab`, `forgejo`, `gitea`, `bitbucket`, `azuredevops` |
| `scm.apiURL` | `""` | `--scm-api-url` for self-hosted SCM instances |
| `webhook.secretRef.name` / `.key` | `""` / `secret` | Secret with the SCM webhook HMAC secret (`KARDINAL_WEBHOOK_SECRET`) |
| `bundleAPI.tokenSecretRef.name` / `.key` | `""` / `token` | Secret with the Bundle API bearer token (`KARDINAL_BUNDLE_TOKEN`). `POST /api/v1/bundles` is off until this is set |
| `ui.auth.tokenSecretRef.name` / `.key` | `""` / `token` | Secret with a static UI API bearer token (`KARDINAL_UI_TOKEN`). With neither this nor `ui.auth.tokenReview` set, the UI API serves only local clients (`kubectl port-forward`) |
| `ui.auth.tokenReview` | `false` | `--ui-tokenreview-auth`: validate UI tokens with TokenReview; adds the RBAC it needs |
| `ui.corsAllowedOrigins` | `[]` | `--cors-allowed-origins` |
| `ui.allowedHosts` | `[]` | Extra host names for `--ui-allowed-hosts` (Ingress host, node IP). localhost and the Service DNS names are always allowed |
| `service.uiPort` | `8082` | UI and UI API port (container and Service) |
| `service.webhookPort` | `8083` | Webhook (`/webhook/scm`) and Bundle API port (container and Service) |
| `controller.watchNamespace` | `""` | Namespace-scoped mode (`--watch-namespace`). Must equal the release namespace |
| `controller.policyNamespaces` | `[]` | Namespaces with org-level PolicyGates (`--policy-namespaces`; default `platform-policies`) |
| `controller.shard` | `""` | `--shard`: this controller reconciles only PromotionSteps labelled with the shard, but still runs every other reconciler. Not a way to deploy a shard agent (see [Distributed mode](distributed-mode.md)) |
| `controller.tlsCertFile` / `tlsKeyFile` | `""` | TLS for the UI and webhook servers. Paths inside the container: mount the certificate Secret with `controller.extraVolumes` / `extraVolumeMounts` |
| `controller.extraArgs` / `extraEnv` / `extraVolumes` / `extraVolumeMounts` | `[]` | Extra controller args, env vars, volumes and mounts |
| `rbac.argocdApplicationsWrite` | `false` | Grant `patch` on Argo CD Applications (the `argocd` update strategy) |
| `rbac.integrationTestJobs` | `false` | Deprecated, no effect, removed in v0.10. The `integration-test` step was removed, so the chart grants no Job access |
| `resources.limits.cpu` | `500m` | CPU limit |
| `resources.limits.memory` | `128Mi` | Memory limit |
| `resources.requests.cpu` | `10m` | CPU request |
| `resources.requests.memory` | `64Mi` | Memory request |
| `nodeSelector` | `{}` | Node selector |
| `tolerations` | `[]` | Pod tolerations |
| `affinity` | `{}` | Pod affinity |
| `networkPolicy.enabled` | `false` | NetworkPolicy for the controller Pod |
| `networkPolicy.ingressFrom.{metrics,health,ui,webhook}` | `[]` | Allowed peers per ingress port (empty admits any source) |
| `networkPolicy.extraEgress` | `[]` | Extra egress rules (e.g. Prometheus for MetricChecks) |
| `validatingAdmissionPolicy.enabled` | `true` | Deprecated, no effect. The CRD schemas validate these fields |

### kro Graph integration

| Key | Default | Description |
|---|---|---|
| `graph.serviceAccountName` | `kardinal-graph` | ServiceAccount kro impersonates to apply each Graph's children (`spec.serviceAccountName`) |
| `graph.kroNamespace` | `kro-system` | Namespace kro runs in (NetworkPolicy egress; `""` drops the rule) |
| `graph.aggregateToKro` | `true` | Ship a ClusterRole aggregated into kro's controller role (kro with `rbac.mode=aggregation`) |
| `graph.readerNamespaces` | `[argocd, flux-system]` | `--graph-reader-namespaces`: namespaces, besides a Graph's own, where the Graph identity may be bound to the reader role for health `ref` nodes. A health ref into any other namespace is dropped from the Graph with a warning (health refs are observational; the PromotionStep reconciler still checks health). Add the namespaces your `health.resource` targets live in. `["*"]` allows every namespace; use it only when every Pipeline author may read every namespace. `kube-system`, `kube-public` and `kube-node-lease` are never allowed |

---

## Accessing the UI

The kardinal controller serves an embedded web UI at port `8082` (configurable via `--ui-listen-address`).

### In-cluster access (recommended): kubectl port-forward

The supported access method for in-cluster deployments without Ingress is `kubectl port-forward`:

```bash
kubectl port-forward svc/kardinal-promoter -n kardinal-system 8082:8082
```

Then open <http://localhost:8082/ui/> in your browser. If you installed with a
release name other than `kardinal-promoter`, the Service is named
`<release>-kardinal-promoter`.

!!! tip "Why port-forward?"
    Port-forwarding routes traffic through the Kubernetes API server over a secure
    tunnel — no Ingress or LoadBalancer needed. It is the recommended approach for
    platform engineers accessing the UI from their workstation.

!!! warning "Ingress, NodePort and LoadBalancer need a UI auth mode"
    With no UI auth mode set, the UI API answers only local clients, which is how
    `kubectl port-forward` connects. A client that comes through an Ingress, a `NodePort`,
    a `LoadBalancer` or another pod gets `403`. To serve those clients, set
    `ui.auth.tokenReview=true` or `ui.auth.tokenSecretRef.name`. An authenticating proxy
    in front of the UI can inject the shared token. With a service-mesh sidecar in the
    controller pod, set an auth mode as well: the sidecar makes mesh clients look local.
    See [UI API Access Control](guides/security.md#ui-api-access-control).

!!! warning "Avoid accessing the UI over plain HTTP from a remote address"
    If you expose port 8082 directly (e.g. via `NodePort`) without TLS, the UI will
    display a security warning. Use port-forward from localhost instead, or configure
    TLS with `--tls-cert-file` / `--tls-key-file`.

!!! note "Browsing to a name other than localhost"
    The UI API only accepts its own host names: localhost and the controller Service's
    DNS names. If you browse to an Ingress host or a node IP, add it to
    `ui.allowedHosts` (`--ui-allowed-hosts`) as well as setting an auth mode. See
    [Host names (DNS rebinding)](guides/security.md#host-names-dns-rebinding).

### With TLS (production)

If you configure TLS via `--tls-cert-file` / `--tls-key-file` (or the Helm values
`controller.tlsCertFile` / `controller.tlsKeyFile`), the UI is served over HTTPS
and no browser warning is shown. See the [Security guide](guides/security.md) for
a cert-manager example.

---

## Upgrade

Helm does not update CRDs on upgrade. Apply the new chart's CRDs first:

```bash
helm show crds oci://ghcr.io/pnz1990/charts/kardinal-promoter --version <version> \
  | kubectl apply --server-side -f -
```

Then upgrade the release:

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter \
  --namespace kardinal-system \
  --reuse-values
```

## Graceful shutdown

The controller handles `SIGTERM` gracefully: it stops accepting new reconcile requests
and allows in-flight reconcile loops up to **30 seconds** to complete before exiting.
This prevents mid-step interruptions during rolling updates or node evictions — for
example, a git-push that was 5 seconds from finishing will complete rather than leaving
the PromotionStep in an inconsistent state.

The Helm chart sets `terminationGracePeriodSeconds: 60` (double the shutdown timeout)
so Kubernetes sends `SIGKILL` only after the controller has had a full 30 seconds to drain.

To adjust the timeout:

```yaml
# values.yaml
terminationGracePeriodSeconds: 120  # increase if reconcile loops routinely take >30s
```

`helm upgrade` upgrades the kardinal-promoter controller only. Upgrade kro separately by
re-running `hack/install-kro.sh` from the matching kardinal-promoter release.

!!! note
    kardinal-promoter is backwards-compatible across patch versions.
    Minor version upgrades may introduce new CRD fields — apply updated CRDs
    with `kubectl apply -f config/crd/bases/` before upgrading the controller.

---

## Uninstall

```bash
helm uninstall kardinal-promoter -n kardinal-system

# Optional: remove kardinal CRDs (deletes all Pipelines, Bundles, PolicyGates, etc.)
kubectl delete crd \
  pipelines.kardinal.io \
  bundles.kardinal.io \
  promotionsteps.kardinal.io \
  policygates.kardinal.io \
  prstatuses.kardinal.io \
  rollbackpolicies.kardinal.io \
  metricchecks.kardinal.io \
  scheduleclocks.kardinal.io \
  changewindows.kardinal.io \
  subscriptions.kardinal.io \
  notificationhooks.kardinal.io \
  promotiontemplates.kardinal.io \
  auditevents.kardinal.io

# Optional: remove kro and its CRDs (only if nothing else uses kro)
helm uninstall kro -n kro-system
kubectl delete crd \
  graphs.kro.run \
  graphrevisions.internal.kro.run \
  resourcegraphdefinitions.kro.run
kubectl delete namespace kro-system
```

---

## RBAC requirements

The chart creates the controller's ServiceAccount (`kardinal-promoter`) and its RBAC. In summary:

| Resources | Verbs |
|---|---|
| All `kardinal.io` kinds and their `/status` | Full CRUD, except `auditevents` (get, list, watch, create) and `changewindows` (get, list, watch; get, update, patch on `/status`) |
| `graphs.kro.run` | Full CRUD; get on `graphs/status` |
| `serviceaccounts`, `rolebindings` | get, create; get, create, update, delete (Graph identity; `delete` removes reader bindings no Graph needs) |
| `clusterroles` | `bind`, limited to `kardinal-promoter-graph-applier` and `kardinal-promoter-graph-reader` |
| `deployments`, Argo CD `applications` and `rollouts`, Flux `kustomizations`, Flagger `canaries` | get, list, watch (health adapters) |
| `secrets` | get only: the controller reads each Secret by name and never lists or watches them. In the default cluster mode `get` covers **every Secret in the cluster**. The release-namespace Role adds `get` on the SCM token Secret by name |
| `configmaps` | get, list, watch; the `kardinal-version` ConfigMap is written through the leader-election Role |
| `leases` | Leader election, through a Role in the release namespace |
| `events` | get, list, watch, create, patch |

Optional rules: `rbac.argocdApplicationsWrite` (patch Applications) and `ui.auth.tokenReview`
(create TokenReviews and SubjectAccessReviews). The full list of objects and rules is in
[Security: Controller RBAC](guides/security.md#controller-rbac). Set `controller.watchNamespace`
to turn the namespaced rules into a Role in one namespace.

kro does not apply a Graph's children with its own identity. It impersonates the Graph's
`spec.serviceAccountName` (default `kardinal-graph`) in the Graph's namespace. The kardinal-promoter
controller creates that ServiceAccount and binds it with RoleBindings to `kardinal-promoter-graph-applier`
(in the Graph namespace) and `kardinal-promoter-graph-reader` (in each namespace a health `ref` node reads,
limited to the Graph's own namespace and `graph.readerNamespaces`). Reader bindings that no Graph
in the namespace needs any more are deleted.
See G5 in the [Graph capability ledger](design/16-graph-capability-ledger.md).

**Upgrading:** earlier versions bound the reader role in every namespace a health `ref` named,
and did not record those bindings, so the controller does not delete them. After upgrading,
list them and delete any in a namespace that is not the Graph's own and not in
`graph.readerNamespaces`:

```bash
kubectl get rolebindings -A -l app.kubernetes.io/managed-by=kardinal-promoter \
  -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name | grep graph-reader-
```

The Helm chart creates all of these ClusterRoles, Roles and bindings.

---

## Next steps

- [Quickstart](quickstart.md) — apply your first Pipeline and promote a Bundle
- [Concepts](concepts.md) — understand Pipelines, Bundles, PolicyGates
- [Policy Gates](policy-gates.md) — write CEL expressions for promotion policies
- [Troubleshooting](troubleshooting.md) — diagnose installation issues with `kardinal doctor`
