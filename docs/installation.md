# Installation

This guide covers installing kardinal-promoter in a Kubernetes cluster using Helm.

---

## Prerequisites

| Requirement | Version | Notes |
|---|---|---|
| Kubernetes | ≥ 1.28 | kind, EKS, GKE, or any conformant cluster |
| kubectl | ≥ 1.28 | Matches your cluster version |
| Helm | ≥ 3.12 | `brew install helm` |
| krocodile | pinned commit | See [krocodile install](#install-krocodile) below |
| GitHub token | — | Personal access token with `repo` scope |

!!! tip "Trying it out locally?"
    Use [kind](https://kind.sigs.k8s.io/) for a single-node local cluster.
    See the [Quickstart](quickstart.md) for the fast path.

---

## Install krocodile

kardinal-promoter uses the krocodile Graph controller (an experimental fork of
[kro](https://github.com/kubernetes-sigs/kro)) to manage its internal DAG state machine.
Install it before the controller:

```bash
make install-krocodile
# or directly:
bash hack/install-krocodile.sh
```

This installs the Graph CRD (`experimental.kro.run/v1alpha1`) and its controller
into the `kro-system` namespace.

---

## Install kardinal-promoter

### 1. Create the GitHub token secret

kardinal-promoter needs a GitHub personal access token to open and monitor pull requests.
The token requires the `repo` scope.

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

Verify the controller is running:

```bash
kubectl get pods -n kardinal-system
# NAME                                         READY   STATUS    RESTARTS   AGE
# kardinal-kardinal-promoter-6b6c8c8446-jc79g  1/1     Running   0          30s
```

---

## Helm values reference

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

---

## Upgrade

```bash
helm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter \
  --namespace kardinal-system \
  --reuse-values
```

!!! note
    kardinal-promoter is backwards-compatible across patch versions.
    Minor version upgrades may introduce new CRD fields — apply updated CRDs
    with `kubectl apply -f config/crd/bases/` before upgrading the controller.

---

## Uninstall

```bash
helm uninstall kardinal-promoter -n kardinal-system

# Optional: remove CRDs (this deletes all Pipelines, Bundles, PolicyGates, etc.)
kubectl delete crd \
  pipelines.kardinal.io \
  bundles.kardinal.io \
  promotionsteps.kardinal.io \
  policygates.kardinal.io \
  prstatuses.kardinal.io \
  rollbackpolicies.kardinal.io
```

---

## RBAC requirements

The controller's `ServiceAccount` requires the following cluster-level permissions:

| Resource | Verbs |
|---|---|
| `pipelines`, `bundles`, `promotionsteps`, `policygates`, `prstatuses`, `rollbackpolicies` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `graphs.experimental.kro.run` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `deployments`, `services`, `pods` | `get`, `list`, `watch` |
| `secrets` | `get` (GitHub token secret only) |
| `events` | `create`, `patch` |
| `configmaps` | `get`, `create`, `update` (leader election + version ConfigMap) |

The Helm chart creates the necessary `ClusterRole` and `ClusterRoleBinding` automatically.

---

## Next steps

- [Quickstart](quickstart.md) — apply your first Pipeline and promote a Bundle
- [Concepts](concepts.md) — understand Pipelines, Bundles, PolicyGates
- [Policy Gates](policy-gates.md) — write CEL expressions for promotion policies
