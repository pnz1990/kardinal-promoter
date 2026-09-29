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

This installs the **kardinal-promoter controller** in the `kardinal-system` namespace and the
ClusterRoles bound to the Graph identity (`kardinal-graph-applier`, `kardinal-graph-reader`).

Verify both controllers are running:

```bash
kubectl get pods -n kardinal-system
# NAME                                              READY   STATUS    RESTARTS   AGE
# kardinal-promoter-controller-6b6c8c8446-jc79g     1/1     Running   0          30s

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
| `logLevel` | `info` | Log verbosity (`debug`, `info`, `warn`, `error`) |
| `leaderElect` | `true` | Enable leader election (required for HA) |
| `github.token` | `""` | Direct token value (use `secretRef` in production) |
| `github.secretRef.name` | `""` | Name of existing Secret containing the token |
| `github.secretRef.key` | `token` | Key in the Secret |
| `resources.limits.cpu` | `500m` | CPU limit |
| `resources.limits.memory` | `128Mi` | Memory limit |
| `resources.requests.cpu` | `10m` | CPU request |
| `resources.requests.memory` | `64Mi` | Memory request |
| `nodeSelector` | `{}` | Node selector |
| `tolerations` | `[]` | Pod tolerations |
| `affinity` | `{}` | Pod affinity |
| `validatingAdmissionPolicy.enabled` | `true` | Deploy `ValidatingAdmissionPolicy` (requires Kubernetes ≥ 1.28) |

### kro Graph integration

| Key | Default | Description |
|---|---|---|
| `graph.serviceAccountName` | `kardinal-graph` | ServiceAccount kro impersonates to apply each Graph's children (`spec.serviceAccountName`) |
| `graph.kroNamespace` | `kro-system` | Namespace kro runs in (NetworkPolicy egress) |
| `graph.aggregateToKro` | `true` | Ship a ClusterRole aggregated into kro's controller role (kro with `rbac.mode=aggregation`) |

---

## Accessing the UI

The kardinal controller serves an embedded web UI at port `8082` (configurable via `--ui-listen-address`).

### In-cluster access (recommended): kubectl port-forward

The supported access method for in-cluster deployments without Ingress is `kubectl port-forward`:

```bash
kubectl port-forward svc/kardinal-controller -n kardinal-system 8082:8082
```

Then open <http://localhost:8082/ui/> in your browser.

!!! tip "Why port-forward?"
    Port-forwarding routes traffic through the Kubernetes API server over a secure
    tunnel — no Ingress or LoadBalancer needed. It is the recommended approach for
    platform engineers accessing the UI from their workstation.

!!! warning "Avoid accessing the UI over plain HTTP from a remote address"
    If you expose port 8082 directly (e.g. via `NodePort`) without TLS, the UI will
    display a security warning. Use port-forward from localhost instead, or configure
    TLS with `--tls-cert-file` / `--tls-key-file`.

!!! note "Browsing to a name other than localhost"
    The UI API only accepts its own host names: localhost and the controller Service's
    DNS names. If you browse to an Ingress host or a node IP, add it to
    `ui.allowedHosts` (`--ui-allowed-hosts`). Otherwise UI actions fail with
    `host not allowed`. See
    [Host names (DNS rebinding)](guides/security.md#host-names-dns-rebinding).

### With TLS (production)

If you configure TLS via `--tls-cert-file` / `--tls-key-file` (or the Helm values
`controller.tlsCertFile` / `controller.tlsKeyFile`), the UI is served over HTTPS
and no browser warning is shown. See the [Security guide](guides/security.md) for
a cert-manager example.

---

## Upgrade

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
  rollbackpolicies.kardinal.io

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

The kardinal-promoter controller's `ServiceAccount` requires:

| Resource | Verbs |
|---|---|
| `pipelines`, `bundles`, `promotionsteps`, `policygates`, `prstatuses`, `rollbackpolicies` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `graphs.kro.run` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `serviceaccounts` | `get`, `create` (Graph identity) |
| `rolebindings` | `get`, `create`, `update` (Graph identity) |
| `clusterroles` | `bind`, limited to `kardinal-graph-applier` and `kardinal-graph-reader` |
| `deployments`, `services`, `pods` | `get`, `list`, `watch` |
| `secrets` | `get` (GitHub token secret only) |
| `events` | `create`, `patch` |
| `configmaps` | `get`, `create`, `update` (leader election + version ConfigMap) |

kro does not apply a Graph's children with its own identity. It impersonates the Graph's
`spec.serviceAccountName` (default `kardinal-graph`) in the Graph's namespace. The kardinal-promoter
controller creates that ServiceAccount and binds it with RoleBindings to `kardinal-graph-applier`
(in the Graph namespace) and `kardinal-graph-reader` (in each namespace a health `ref` node reads).
See G5 in the [Graph capability ledger](design/16-graph-capability-ledger.md).

Both `ClusterRole` and `ClusterRoleBinding` resources are created automatically by the Helm chart.

---

## Next steps

- [Quickstart](quickstart.md) — apply your first Pipeline and promote a Bundle
- [Concepts](concepts.md) — understand Pipelines, Bundles, PolicyGates
- [Policy Gates](policy-gates.md) — write CEL expressions for promotion policies
- [Troubleshooting](troubleshooting.md) — diagnose installation issues with `kardinal doctor`
