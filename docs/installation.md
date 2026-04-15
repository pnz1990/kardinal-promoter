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

!!! tip "Trying it out locally?"
    Use [kind](https://kind.sigs.k8s.io/) for a single-node local cluster.
    See the [Quickstart](quickstart.md) for the fast path.

!!! info "krocodile is bundled"
    kardinal-promoter bundles the krocodile Graph controller as part of its Helm chart.
    **No separate krocodile install is required.** A single `helm install` installs both controllers.

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

This single command installs:

- The **kardinal-promoter controller** in the `kardinal-system` namespace
- The **krocodile Graph controller** in the `kro-system` namespace (unless `--set krocodile.enabled=false`)

Verify both controllers are running:

```bash
kubectl get pods -n kardinal-system
# NAME                                              READY   STATUS    RESTARTS   AGE
# kardinal-promoter-controller-6b6c8c8446-jc79g     1/1     Running   0          30s

kubectl get pods -n kro-system
# NAME                              READY   STATUS    RESTARTS   AGE
# graph-controller-7d4b8f9f5-xk2pq  1/1     Running   0          30s
```

---

## Bring your own krocodile

If you already run krocodile independently in your cluster, disable the bundled installation:

```bash
helm install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter \
  --namespace kardinal-system \
  --create-namespace \
  --set github.secretRef.name=github-token \
  --set krocodile.enabled=false
```

!!! warning "Version compatibility"
    When running your own krocodile, ensure it is at a compatible commit.
    The required minimum is documented in `hack/install-krocodile.sh`.
    The bundled version is pinned per release and tested together.

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

### krocodile Graph controller

| Key | Default | Description |
|---|---|---|
| `krocodile.enabled` | `true` | Install bundled krocodile controller |
| `krocodile.image.repository` | `ghcr.io/pnz1990/kardinal-promoter/krocodile` | krocodile image |
| `krocodile.image.tag` | `krocodile.pinnedCommit` | Image tag (defaults to pinned commit SHA) |
| `krocodile.pinnedCommit` | See `Chart.yaml` annotations | Source commit bundled with this release |
| `krocodile.namespace` | `kro-system` | Namespace for krocodile controller |
| `krocodile.replicaCount` | `1` | Number of krocodile replicas |
| `krocodile.resources.limits.memory` | `512Mi` | Memory limit |

---

## Upgrade

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter \
  --namespace kardinal-system \
  --reuse-values
```

This upgrades both the kardinal-promoter controller and the bundled krocodile controller
to the versions pinned in the new chart version.

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

# Optional: remove krocodile CRDs (only if you don't use krocodile elsewhere)
kubectl delete crd \
  graphs.experimental.kro.run \
  graphrevisions.experimental.kro.run

# Optional: remove krocodile namespace
kubectl delete namespace kro-system
```

---

## RBAC requirements

The kardinal-promoter controller's `ServiceAccount` requires:

| Resource | Verbs |
|---|---|
| `pipelines`, `bundles`, `promotionsteps`, `policygates`, `prstatuses`, `rollbackpolicies` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `graphs.experimental.kro.run` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `deployments`, `services`, `pods` | `get`, `list`, `watch` |
| `secrets` | `get` (GitHub token secret only) |
| `events` | `create`, `patch` |
| `configmaps` | `get`, `create`, `update` (leader election + version ConfigMap) |

The krocodile controller's `ServiceAccount` requires full cluster-level access to manage
arbitrary resources as directed by Graph specs. This is inherent to its design as a general-purpose
DAG engine — it applies resources of any type that appear in Graph node templates.

Both `ClusterRole` and `ClusterRoleBinding` resources are created automatically by the Helm chart.

---

## Next steps

- [Quickstart](quickstart.md) — apply your first Pipeline and promote a Bundle
- [Concepts](concepts.md) — understand Pipelines, Bundles, PolicyGates
- [Policy Gates](policy-gates.md) — write CEL expressions for promotion policies
- [Troubleshooting](troubleshooting.md) — diagnose installation issues with `kardinal doctor`
